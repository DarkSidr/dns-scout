package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

var command = realCommand
var configDir = "/etc/config/"
var probeLocal = localTest
var proxyPreflight = preflight
var launchWatch = func() error {
	self, e := os.Executable()
	if e != nil {
		return e
	}
	cmd := exec.Command(self, "watch")
	if e = cmd.Start(); e != nil {
		return e
	}
	return cmd.Process.Release()
}

func realCommand(name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	b, e := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if e != nil {
		return "", fmt.Errorf("%s: %v: %s", name, e, strings.TrimSpace(string(b)))
	}
	return strings.TrimSpace(string(b)), nil
}
func uci(args ...string) (string, error) {
	return command("/sbin/uci", append([]string{"-q"}, args...)...)
}
func set(k, v string) error { _, e := uci("set", k+"="+v); return e }
func get(k string) string   { v, _ := uci("get", k); return v }
func localTest(port int) error {
	for _, d := range []string{"example.com", "openwrt.org"} {
		q := query(d)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		b, e := plainQuery(ctx, "127.0.0.1:"+strconv.Itoa(port), q)
		cancel()
		if e == nil {
			e = validateDNS(q, b)
		}
		if e != nil {
			return e
		}
	}
	return nil
}
func sections(pkg, typ string) ([]string, error) {
	out, e := uci("show", pkg)
	if e != nil {
		return nil, e
	}
	r := []string{}
	for _, l := range strings.Split(out, "\n") {
		if strings.HasSuffix(l, "="+typ) {
			r = append(r, strings.TrimSuffix(l, "="+typ))
		}
	}
	return r, nil
}
func preflight(c Config, ss []Server) error {
	// Exercise the actual installed proxy, not only the Go HTTPS implementation.
	for _, s := range ss {
		sock, e := freePort()
		if e != nil {
			return e
		}
		port := sock
		cmd := exec.Command("/usr/sbin/https-dns-proxy", "-a", "127.0.0.1", "-p", strconv.Itoa(port), "-r", s.URL, "-b", strings.Join(c.Bootstrap, ","), "-4", "-u", "nobody", "-g", "nogroup")
		var logs bytes.Buffer
		cmd.Stdout = &logs
		cmd.Stderr = &logs
		if e = cmd.Start(); e != nil {
			return e
		}
		// Older/slow devices may need more than one scheduling tick to bind.
		for attempt := 0; attempt < 6; attempt++ {
			time.Sleep(250 * time.Millisecond)
			e = probeLocal(port)
			if e == nil || !strings.Contains(e.Error(), "connection refused") {
				break
			}
		}
		cmd.Process.Kill()
		cmd.Wait()
		if e != nil {
			return fmt.Errorf("проверка https-dns-proxy для %s: %w; %s", s.Name, e, strings.TrimSpace(logs.String()))
		}
	}
	return nil
}
func rollback() error {
	if _, e := os.Stat(backupDir + "/pending"); os.IsNotExist(e) {
		return nil
	}
	_, stopErr := command("/etc/init.d/https-dns-proxy", "stop")
	for _, p := range []string{"https-dns-proxy", "dhcp"} {
		b, e := os.ReadFile(backupDir + "/" + p)
		if e != nil {
			return e
		}
		uci("revert", p)
		if e = atomicFile(configDir+p, b, 0600); e != nil {
			return e
		}
	}
	if _, e := command("/etc/init.d/https-dns-proxy", "start"); e != nil {
		return e
	}
	if _, e := command("/etc/init.d/dnsmasq", "restart"); e != nil {
		return e
	}
	if e := probeLocal(53); e != nil {
		return fmt.Errorf("конфигурация восстановлена, но DNS не отвечает: %w (stop: %v)", e, stopErr)
	}
	if e := os.Remove(filepath.Join(filepath.Dir(configPath), "active.json")); e != nil && !os.IsNotExist(e) {
		return e
	}
	return os.Remove(backupDir + "/pending")
}
func apply(c Config, ss []Server) (err error) {
	if _, e := os.Stat(backupDir + "/pending"); e == nil {
		return fmt.Errorf("есть незавершённый откат; выполните восстановление")
	}
	for _, p := range []string{"dhcp", "https-dns-proxy"} {
		out, e := uci("changes", p)
		if e != nil {
			return e
		}
		if out != "" {
			return fmt.Errorf("сначала сохраните изменения LuCI: %s", p)
		}
	}
	ds, e := sections("dhcp", "dnsmasq")
	if e != nil {
		return e
	}
	if len(ds) != 1 {
		return fmt.Errorf("автоприменение поддерживает ровно один dnsmasq")
	}
	d := ds[0]
	if v := get(d + ".port"); v != "" && v != "53" {
		return fmt.Errorf("dnsmasq должен слушать порт 53")
	}
	if v := get("https-dns-proxy.config.dnsmasq_config_update"); v != "*" {
		return fmt.Errorf("в https-dns-proxy требуется dnsmasq_config_update='*'")
	}
	if v := get("https-dns-proxy.config.proxy_server"); v != "" {
		return fmt.Errorf("внешний proxy_server: автоматическое применение не поддерживается")
	}
	if alreadyConfigured(c, ss) && verifyActive(ss, d) == nil {
		return nil
	}
	if e = proxyPreflight(c, ss); e != nil {
		return e
	}
	// Snapshots are persistent: boot recovery handles loss of power in the transaction.
	for _, p := range []string{"https-dns-proxy", "dhcp"} {
		b, e := os.ReadFile(configDir + p)
		if e != nil {
			return e
		}
		if e = atomicFile(backupDir+"/"+p, b, 0600); e != nil {
			return e
		}
	}
	if e = atomicFile(backupDir+"/pending", []byte("pending\n"), 0600); e != nil {
		return e
	}
	defer func() {
		if err != nil {
			if e := rollback(); e != nil {
				err = fmt.Errorf("%v; ОШИБКА ОТКАТА: %v", err, e)
			} else {
				err = fmt.Errorf("%v; исходные настройки восстановлены", err)
			}
		}
	}()
	if e = launchWatch(); e != nil {
		return e
	}

	if _, e = command("/etc/init.d/https-dns-proxy", "stop"); e != nil {
		return e
	}
	// Delete all old proxy instances, keeping the package-wide firewall and canary settings.
	old, e := sections("https-dns-proxy", "https-dns-proxy")
	if e != nil {
		return e
	}
	for i := len(old) - 1; i >= 0; i-- {
		if _, e = uci("delete", old[i]); e != nil {
			return e
		}
	}
	// Preserve split-DNS rules. Replace only unconditional upstreams.
	existing := strings.Fields(get(d + ".server"))
	uci("delete", d+".server")
	for _, v := range existing {
		if strings.HasPrefix(v, "/") {
			if _, e = uci("add_list", d+".server="+v); e != nil {
				return e
			}
		}
	}
	for i, s := range ss {
		sec := "https-dns-proxy.scout" + strconv.Itoa(i)
		for _, kv := range [][2]string{{sec, "https-dns-proxy"}, {sec + ".resolver_url", s.URL}, {sec + ".bootstrap_dns", strings.Join(c.Bootstrap, ",")}, {sec + ".listen_addr", "127.0.0.1"}, {sec + ".listen_port", strconv.Itoa(5053 + i)}, {sec + ".force_ipv6_resolvers", "0"}, {sec + ".force_http1", "0"}, {sec + ".force_http3", "0"}} {
			if e = set(kv[0], kv[1]); e != nil {
				return e
			}
		}
	}
	if e = set(d+".strictorder", "1"); e != nil {
		return e
	}
	if e = set(d+".noresolv", "1"); e != nil {
		return e
	}
	uci("delete", d+".allservers")
	for _, p := range []string{"dhcp", "https-dns-proxy"} {
		if _, e = uci("commit", p); e != nil {
			return e
		}
	}
	if _, e = command("/etc/init.d/https-dns-proxy", "start"); e != nil {
		return e
	}
	if _, e = command("/etc/init.d/dnsmasq", "restart"); e != nil {
		return e
	}
	time.Sleep(time.Second)
	if e = verifyActive(ss, d); e != nil {
		return e
	}
	active := map[string]any{"verified_at": time.Now().Unix(), "servers": ss, "message": "Конфигурация, процессы, каждый прокси и dnsmasq проверены"}
	if e = atomicJSON(filepath.Join(filepath.Dir(configPath), "active.json"), active); e != nil {
		return e
	}
	return os.Remove(backupDir + "/pending")
}
func verifyActive(ss []Server, d string) error {
	out, e := command("/bin/ubus", "call", "service", "list", `{"name":"https-dns-proxy"}`)
	if e != nil {
		return e
	}
	var data map[string]struct {
		Instances map[string]struct {
			Running bool     `json:"running"`
			Command []string `json:"command"`
		} `json:"instances"`
	}
	if e = json.Unmarshal([]byte(out), &data); e != nil {
		return e
	}
	upstreams := []string{}
	for _, v := range strings.Fields(get(d + ".server")) {
		if !strings.HasPrefix(v, "/") {
			upstreams = append(upstreams, v)
		}
	}
	if len(upstreams) != len(ss) || get(d+".strictorder") != "1" || get(d+".noresolv") != "1" {
		return fmt.Errorf("dnsmasq не использует ожидаемую цепочку")
	}
	for i, s := range ss {
		port := strconv.Itoa(5053 + i)
		if upstreams[i] != "127.0.0.1#"+port {
			return fmt.Errorf("неверный порядок DNS")
		}
		found := false
		for _, in := range data["https-dns-proxy"].Instances {
			urlOK, portOK := false, false
			for j := 0; j+1 < len(in.Command); j++ {
				if in.Command[j] == "-r" && in.Command[j+1] == s.URL {
					urlOK = true
				}
				if in.Command[j] == "-p" && in.Command[j+1] == port {
					portOK = true
				}
			}
			if in.Running && urlOK && portOK {
				found = true
			}
		}
		if !found {
			return fmt.Errorf("новый процесс %s не запущен", s.Name)
		}
		if e = probeLocal(5053 + i); e != nil {
			return fmt.Errorf("порт %s: %w", port, e)
		}
	}
	return probeLocal(53)
}
func recoverPending() error {
	_, e := os.Stat(filepath.Join(backupDir, "pending"))
	if os.IsNotExist(e) {
		return nil
	}
	return rollback()
}

func alreadyConfigured(c Config, ss []Server) bool {
	sections, e := sections("https-dns-proxy", "https-dns-proxy")
	if e != nil || len(sections) != len(ss) {
		return false
	}
	for i, s := range ss {
		k := "https-dns-proxy.scout" + strconv.Itoa(i)
		if get(k+".resolver_url") != s.URL || get(k+".bootstrap_dns") != strings.Join(c.Bootstrap, ",") {
			return false
		}
	}
	return true
}
