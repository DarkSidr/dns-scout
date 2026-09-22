package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUninstall(t *testing.T) {
	for _, scenario := range []string{"apk", "opkg", "pending", "keep-dependencies-fails", "remove-fails"} {
		t.Run(scenario, func(t *testing.T) {
			oldCommand := command
			t.Cleanup(func() { command = oldCommand })
			root := t.TempDir()
			settings, runtimeDir := filepath.Join(root, "settings"), filepath.Join(root, "runtime")
			cronFile := filepath.Join(root, "crontab")
			files := map[string]string{
				filepath.Join(settings, "config.json"):   "saved settings",
				filepath.Join(settings, "active.json"):   "active DNS",
				filepath.Join(runtimeDir, "report.json"): "report",
				filepath.Join(root, "https-dns-proxy"):   "working DNS",
				cronFile:                                 "# user schedule\n0 1 * * * /usr/bin/backup\n30 17 * * * /usr/sbin/dns-scout scheduled # dns-scout\n",
			}
			for path, content := range files {
				if e := atomicFile(path, []byte(content), 0600); e != nil {
					t.Fatal(e)
				}
			}
			if scenario == "pending" {
				if e := atomicFile(filepath.Join(settings, "transaction", "pending"), []byte("pending"), 0600); e != nil {
					t.Fatal(e)
				}
			}
			calls := []string{}
			command = func(name string, args ...string) (string, error) {
				calls = append(calls, name+" "+strings.Join(args, " "))
				if scenario == "keep-dependencies-fails" && args[0] == "add" || scenario == "remove-fails" && args[0] == "del" {
					return "", fmt.Errorf("simulated package manager failure")
				}
				return "", nil
			}
			manager := "/sbin/apk"
			if scenario == "opkg" {
				manager = "/bin/opkg"
			}
			e := uninstall(manager, settings, runtimeDir, cronFile)
			success := scenario == "apk" || scenario == "opkg"
			if (e == nil) != success {
				t.Fatalf("unexpected result: %v", e)
			}
			for _, dir := range []string{settings, runtimeDir} {
				_, statErr := os.Stat(dir)
				if os.IsNotExist(statErr) != success {
					t.Fatalf("data cleanup incorrect for %s: %v", dir, statErr)
				}
			}
			b, e := os.ReadFile(filepath.Join(root, "https-dns-proxy"))
			if e != nil || string(b) != "working DNS" {
				t.Fatal("working DNS modified")
			}
			b, e = os.ReadFile(cronFile)
			if e != nil || !strings.Contains(string(b), "# user schedule\n0 1 * * * /usr/bin/backup\n") {
				t.Fatal("unrelated cron job modified")
			}
			if success && strings.Contains(string(b), "# dns-scout") {
				t.Fatal("DNS Scout cron job remains")
			}
			if scenario == "pending" && len(calls) != 0 {
				t.Fatal("pending recovery did not block uninstall")
			}
			if scenario == "apk" && (len(calls) != 4 || calls[0] != "/sbin/apk add --no-network https-dns-proxy luci-base rpcd ca-bundle" || calls[2] != "/sbin/apk del luci-app-dns-scout") {
				t.Fatalf("unexpected APK operations: %v", calls)
			}
			if scenario == "opkg" && (len(calls) != 3 || calls[1] != "/bin/opkg remove luci-app-dns-scout") {
				t.Fatalf("unexpected OPKG operations: %v", calls)
			}
		})
	}
}
