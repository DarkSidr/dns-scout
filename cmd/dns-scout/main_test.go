package main

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func goodAnswer(q []byte) []byte {
	b := append([]byte(nil), q...)
	b[2] = 0x81
	b[3] = 0x80
	b[7] = 1
	return append(b, 0xc0, 0x0c, 0, 1, 0, 1, 0, 0, 0, 60, 0, 4, 93, 184, 216, 34)
}
func TestValidateDNS(t *testing.T) {
	q := query("example.com")
	good := goodAnswer(q)
	if e := validateDNS(q, good); e != nil {
		t.Fatal(e)
	}
	cases := map[string]func([]byte) []byte{
		"wrong ID": func(b []byte) []byte { b[0] ^= 1; return b }, "not response": func(b []byte) []byte { b[2] &= 127; return b }, "truncated": func(b []byte) []byte { return b[:len(b)-2] }, "NXDOMAIN": func(b []byte) []byte { b[3] |= 3; return b }, "SERVFAIL": func(b []byte) []byte { b[3] |= 2; return b }, "TC": func(b []byte) []byte { b[2] |= 2; return b }, "wrong question": func(b []byte) []byte { b[13] = 'z'; return b }, "no answers": func(b []byte) []byte { b[7] = 0; return b }, "sinkhole": func(b []byte) []byte {
			for i := len(b) - 4; i < len(b); i++ {
				b[i] = 0
			}
			return b
		}, "compression loop": func(b []byte) []byte { b[12] = 0xc0; b[13] = 12; return b }, "oversized RDATA": func(b []byte) []byte { binary.BigEndian.PutUint16(b[len(b)-6:len(b)-4], 65535); return b }}
	for name, f := range cases {
		t.Run(name, func(t *testing.T) {
			if validateDNS(q, f(append([]byte(nil), good...))) == nil {
				t.Fatal("accepted invalid DNS")
			}
		})
	}
}
func TestUntrustedTLSRejected(t *testing.T) {
	s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/dns-message")
		w.Write(goodAnswer(b))
	}))
	defer s.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, e := exchange(ctx, Server{Protocol: "doh", URL: s.URL}, nil, query("example.com"))
	if e == nil {
		t.Fatal("untrusted certificate accepted")
	}
}
func fixture(t *testing.T) Config {
	t.Helper()
	var c Config
	b, e := os.ReadFile("../../files/etc/dns-scout/config.json")
	if e != nil {
		t.Fatal(e)
	}
	if e = json.Unmarshal(b, &c); e != nil {
		t.Fatal(e)
	}
	return c
}
func TestConfiguration(t *testing.T) {
	if e := fixture(t).validate(); e != nil {
		t.Fatal(e)
	}
	cases := map[string]func(*Config){"shell URL": func(c *Config) { c.Servers[0].URL = "https://example.com/';reboot" }, "http": func(c *Config) { c.Servers[0].URL = "http://example.com/dns-query" }, "dot": func(c *Config) { c.Servers[0].Protocol = "dot" }, "duplicate ID": func(c *Config) { c.Servers[1].ID = c.Servers[0].ID }, "cron injection": func(c *Config) { c.Daily = "00:00\n* * * * * reboot" }, "parallel": func(c *Config) { c.Parallel = 100 }, "private bootstrap": func(c *Config) { c.Bootstrap = []string{"127.0.0.1"} }, "domain length": func(c *Config) { c.Domains[0] = strings.Repeat("x", 64) + ".com" }, "private URL": func(c *Config) { c.Servers[0].URL = "https://192.168.1.1/" }, "credentials": func(c *Config) { c.Servers[0].URL = "https://root:password@example.com/dns-query" }}
	for name, f := range cases {
		t.Run(name, func(t *testing.T) {
			c := fixture(t)
			f(&c)
			if c.validate() == nil {
				t.Fatal("invalid config accepted")
			}
		})
	}
}
func TestRankingAndCandidates(t *testing.T) {
	c := fixture(t)
	a, b := c.Servers[0], c.Servers[1]
	r := Report{Finished: time.Now().Unix(), Results: []Result{{Server: a, Success: 2, Total: 3, P95: 10}, {Server: b, Success: 3, Total: 3, P95: 100}}}
	rank(r.Results)
	if r.Results[0].Server.ID != b.ID {
		t.Fatal("speed took precedence over reliability")
	}
	ss, e := candidates(r, c, "")
	if e != nil || len(ss) != 1 || ss[0].ID != b.ID {
		t.Fatal(ss, e)
	}
	if _, e = candidates(r, c, a.ID); e == nil {
		t.Fatal("partial success allowed")
	}
	r.Finished -= 3601
	if _, e = candidates(r, c, ""); e == nil {
		t.Fatal("stale results accepted")
	}
	r.Finished = time.Now().Unix()
	c.Servers[1].URL += "/changed"
	if _, e = candidates(r, c, ""); e == nil {
		t.Fatal("changed endpoint accepted")
	}
}
func TestVerifyActiveRequiresRuntimeAndPortOrder(t *testing.T) {
	old := command
	defer func() { command = old }()
	s := fixture(t).Servers[0]
	broken := false
	command = func(name string, args ...string) (string, error) {
		if name == "/bin/ubus" {
			return fmt.Sprintf(`{"https-dns-proxy":{"instances":{"one":{"running":%t,"command":["https-dns-proxy","-r",%q,"-p","5053"]}}}}`, !broken, s.URL), nil
		}
		key := args[len(args)-1]
		switch key {
		case "dhcp.x.server":
			return "127.0.0.1#9999", nil
		case "dhcp.x.strictorder", "dhcp.x.noresolv":
			return "1", nil
		}
		return "", fmt.Errorf("unexpected command")
	}
	if e := verifyActive([]Server{s}, "dhcp.x"); e == nil || !strings.Contains(e.Error(), "порядок") {
		t.Fatal(e)
	}
}
func TestAtomicFileAndStats(t *testing.T) {
	p := t.TempDir() + "/config.json"
	if e := atomicJSON(p, map[string]int{"v": 1}); e != nil {
		t.Fatal(e)
	}
	var v map[string]int
	if e := readJSON(p, &v); e != nil || v["v"] != 1 {
		t.Fatal(v, e)
	}
	r := Result{Success: 3, Samples: []float64{9, 1, 5}}
	stats(&r)
	if r.Median != 5 || r.P95 != 9 {
		t.Fatal(r)
	}
}
func FuzzDNSParser(f *testing.F) {
	q := query("example.com")
	q[0], q[1] = 0x12, 0x34
	f.Add(goodAnswer(q))
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, b []byte) { _ = validateDNS(q, b) })
}

func TestApplyRollbackRestoresFiles(t *testing.T) {
	for _, rollbackFails := range []bool{false, true} {
		t.Run(fmt.Sprint(rollbackFails), func(t *testing.T) {
			oldCommand, oldProbe, oldPre, oldWatch := command, probeLocal, proxyPreflight, launchWatch
			oldDir, oldBackup, oldConfig := configDir, backupDir, configPath
			defer func() {
				command, probeLocal, proxyPreflight, launchWatch = oldCommand, oldProbe, oldPre, oldWatch
				configDir, backupDir, configPath = oldDir, oldBackup, oldConfig
			}()
			dir := t.TempDir()
			configDir = dir + "/config/"
			backupDir = dir + "/backup"
			configPath = dir + "/scout/config.json"
			for _, p := range []string{"dhcp", "https-dns-proxy"} {
				if e := atomicFile(configDir+p, []byte("original "+p), 0600); e != nil {
					t.Fatal(e)
				}
			}
			proxyPreflight = func(Config, []Server) error { return nil }
			launchWatch = func() error { return nil }
			probeLocal = func(int) error {
				if rollbackFails {
					return fmt.Errorf("simulated DNS outage")
				}
				return nil
			}
			stops := 0
			command = func(name string, args ...string) (string, error) {
				if name == "/sbin/uci" {
					switch args[1] {
					case "show":
						return "dhcp.main=dnsmasq", nil
					case "get":
						if args[2] == "https-dns-proxy.config.dnsmasq_config_update" {
							return "*", nil
						}
						return "", nil
					default:
						return "", nil
					}
				}
				if name == "/etc/init.d/https-dns-proxy" && args[0] == "stop" {
					stops++
					if stops == 1 {
						atomicFile(configDir+"dhcp", []byte("partially changed"), 0600)
						return "", fmt.Errorf("simulated restart failure")
					}
				}
				return "", nil
			}
			c := fixture(t)
			e := apply(c, c.Servers[:1])
			if e == nil {
				t.Fatal("failed apply reported success")
			}
			for _, p := range []string{"dhcp", "https-dns-proxy"} {
				b, re := os.ReadFile(configDir + p)
				if re != nil || string(b) != "original "+p {
					t.Fatalf("backup not restored: %s %q %v", p, b, re)
				}
			}
			_, pending := os.Stat(backupDir + "/pending")
			if rollbackFails && pending != nil {
				t.Fatal("failed recovery lost pending marker")
			}
			if !rollbackFails && !os.IsNotExist(pending) {
				t.Fatal("completed recovery left marker")
			}
			if _, e := os.Stat(dir + "/scout/active.json"); !os.IsNotExist(e) {
				t.Fatal("failed apply recorded as verified")
			}
		})
	}
}

func TestFallbackAvoidsKnownProviderAliases(t *testing.T) {
	c := fixture(t)
	c.Fallbacks = 3
	var a, b, g Server
	for _, s := range c.Servers {
		switch s.ID {
		case "quad9":
			a = s
		case "quad9_ip":
			b = s
		case "google":
			g = s
		}
	}
	r := Report{Finished: time.Now().Unix(), Results: []Result{{Server: a, Success: 3, Total: 3}, {Server: b, Success: 3, Total: 3}, {Server: g, Success: 3, Total: 3}}}
	ss, e := candidates(r, c, "")
	if e != nil || len(ss) != 2 || ss[1].ID != "google" {
		t.Fatal(ss, e)
	}
}

func TestManualFallbackSelection(t *testing.T) {
	c := fixture(t)
	c.FallbackMode = "manual"
	c.FallbackIDs = []string{"google", "controld"}
	r := Report{Finished: time.Now().Unix()}
	for _, id := range []string{"google", "cloudflare", "controld", "quad9"} {
		for _, s := range c.Servers {
			if s.ID == id {
				r.Results = append(r.Results, Result{Server: s, Success: 3, Total: 3})
			}
		}
	}
	if e := c.validate(); e != nil {
		t.Fatal(e)
	}
	ss, e := candidates(r, c, "quad9")
	if e != nil || len(ss) != 3 || ss[0].ID != "quad9" || ss[1].ID != "google" || ss[2].ID != "controld" {
		t.Fatal(ss, e)
	}
	ss, e = candidates(r, c, "")
	if e != nil || ss[0].ID != "cloudflare" {
		t.Fatal("automatic primary reused reserved DNS", ss, e)
	}
	if _, e = candidates(r, c, "google"); e == nil {
		t.Fatal("primary/reserve collision accepted")
	}
	r.Results[0].Success = 2
	if _, e = candidates(r, c, "quad9"); e == nil {
		t.Fatal("failed pinned reserve silently replaced")
	}
	c.FallbackIDs = nil
	ss, e = candidates(r, c, "quad9")
	if e != nil || len(ss) != 1 {
		t.Fatal("manual no-reserve mode", ss, e)
	}
}
func TestManualFallbackConfigValidation(t *testing.T) {
	for _, ids := range [][]string{{"google", "google"}, {"missing"}, {"google", "controld", "quad9"}, {"adguard_filter"}} {
		c := fixture(t)
		c.FallbackMode = "manual"
		c.FallbackIDs = ids
		if c.validate() == nil {
			t.Fatalf("accepted invalid backups %v", ids)
		}
	}
	c := fixture(t)
	c.FallbackMode = "unknown"
	if c.validate() == nil {
		t.Fatal("unknown mode")
	}
	// Old saved configurations have no new fields and must retain automatic mode.
	c = fixture(t)
	if e := c.validate(); e != nil {
		t.Fatal(e)
	}
}
