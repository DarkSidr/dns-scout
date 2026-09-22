package main

import (
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var configPath = "/etc/dns-scout/config.json"

const stateDir = "/tmp/dns-scout"

var backupDir = "/etc/dns-scout/transaction"

type Server struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Protocol string `json:"protocol"`
	URL      string `json:"url"`
	Enabled  bool   `json:"enabled"`
	Eligible bool   `json:"eligible"`
}
type Config struct {
	Servers      []Server `json:"servers"`
	Bootstrap    []string `json:"bootstrap"`
	Domains      []string `json:"domains"`
	Samples      int      `json:"samples"`
	Timeout      int      `json:"timeout"`
	Parallel     int      `json:"parallel"`
	Daily        string   `json:"daily"`
	Auto         bool     `json:"auto"`
	Fallbacks    int      `json:"fallbacks"`
	FallbackMode string   `json:"fallback_mode"`
	FallbackIDs  []string `json:"fallback_ids"`
}

func readJSON(path string, out any) error {
	b, e := os.ReadFile(path)
	if e != nil {
		return e
	}
	return json.Unmarshal(b, out)
}
func atomicJSON(path string, v any) error {
	b, e := json.MarshalIndent(v, "", "  ")
	if e != nil {
		return e
	}
	return atomicFile(path, append(b, '\n'), 0600)
}
func atomicFile(path string, b []byte, mode os.FileMode) error {
	if e := os.MkdirAll(filepath.Dir(path), 0700); e != nil {
		return e
	}
	f, e := os.CreateTemp(filepath.Dir(path), ".new-")
	if e != nil {
		return e
	}
	defer os.Remove(f.Name())
	if e = f.Chmod(mode); e == nil {
		_, e = f.Write(b)
	}
	if e == nil {
		e = f.Sync()
	}
	ce := f.Close()
	if e == nil {
		e = ce
	}
	if e != nil {
		return e
	}
	if e = os.Rename(f.Name(), path); e != nil {
		return e
	}
	d, e := os.Open(filepath.Dir(path))
	if e != nil {
		return e
	}
	defer d.Close()
	return d.Sync()
}
func loadConfig() (Config, error) {
	var c Config
	e := readJSON(configPath, &c)
	if e == nil {
		e = c.validate()
	}
	return c, e
}

var idRE = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)
var hostRE = regexp.MustCompile(`^[a-zA-Z0-9.-]+$`)

func (c Config) validate() error {
	if len(c.Servers) == 0 || len(c.Servers) > 300 {
		return fmt.Errorf("нужно от 1 до 300 серверов")
	}
	ids := map[string]bool{}
	for _, s := range c.Servers {
		if !idRE.MatchString(s.ID) || ids[s.ID] || len(s.Name) == 0 || len(s.Name) > 120 {
			return fmt.Errorf("некорректное или повторное имя/ID")
		}
		ids[s.ID] = true
		u, e := url.Parse(s.URL)
		if e != nil || u.Hostname() == "" || u.User != nil || u.Fragment != "" || len(s.URL) > 512 || strings.ContainsAny(s.URL, "\r\n\t '") {
			return fmt.Errorf("некорректный URL %s", s.ID)
		}
		if s.Protocol != "doh" || u.Scheme != "https" {
			return fmt.Errorf("требуется DoH https://")
		}
		if ip := net.ParseIP(u.Hostname()); ip != nil {
			if !ip.IsGlobalUnicast() || ip.IsPrivate() {
				return fmt.Errorf("нужен публичный адрес")
			}
		} else if !hostRE.MatchString(u.Hostname()) || !strings.Contains(u.Hostname(), ".") {
			return fmt.Errorf("некорректное имя хоста")
		}
		if p := u.Port(); p != "" {
			var n int
			if _, e := fmt.Sscanf(p, "%d", &n); e != nil || n < 1 || n > 65535 {
				return fmt.Errorf("некорректный порт")
			}
		}
	}
	if len(c.Bootstrap) < 1 || len(c.Bootstrap) > 8 {
		return fmt.Errorf("укажите 1–8 bootstrap IP")
	}
	for _, s := range c.Bootstrap {
		ip := net.ParseIP(s)
		if ip == nil || ip.To4() == nil || !ip.IsGlobalUnicast() || ip.IsPrivate() {
			return fmt.Errorf("bootstrap: нужен публичный IPv4")
		}
	}
	if c.Samples < 2 || c.Samples > 10 || c.Timeout < 2 || c.Timeout > 15 || c.Parallel < 1 || c.Parallel > 8 || c.Fallbacks < 1 || c.Fallbacks > 3 {
		return fmt.Errorf("выход за пределы параметров теста")
	}
	if c.FallbackMode != "" && c.FallbackMode != "auto" && c.FallbackMode != "manual" {
		return fmt.Errorf("неизвестный режим резервирования")
	}
	if len(c.FallbackIDs) > 2 {
		return fmt.Errorf("можно выбрать не более двух резервных DNS")
	}
	if c.FallbackMode == "manual" {
		seen := map[string]bool{}
		for _, id := range c.FallbackIDs {
			found := false
			for _, server := range c.Servers {
				if server.ID == id && server.Enabled && server.Eligible {
					if seen[server.URL] {
						return fmt.Errorf("резервные DNS должны иметь разные адреса")
					}
					seen[server.URL] = true
					found = true
					break
				}
			}
			if !found {
				return fmt.Errorf("резерв %s: включите тестирование и разрешите выбор сервера", id)
			}
		}
	}
	if len(c.Domains) < 2 || len(c.Domains) > 8 {
		return fmt.Errorf("укажите 2–8 тестовых доменов")
	}
	for _, d := range c.Domains {
		if len(d) > 253 || !hostRE.MatchString(d) || !strings.Contains(d, ".") {
			return fmt.Errorf("некорректный домен")
		}
		for _, p := range strings.Split(d, ".") {
			if len(p) < 1 || len(p) > 63 {
				return fmt.Errorf("некорректная DNS метка")
			}
		}
	}
	if c.Daily != "" && !regexp.MustCompile(`^([01][0-9]|2[0-3]):[0-5][0-9]$`).MatchString(c.Daily) {
		return fmt.Errorf("время: ЧЧ:ММ или пусто")
	}
	return nil
}
