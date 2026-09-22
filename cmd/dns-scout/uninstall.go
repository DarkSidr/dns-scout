package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// The caller holds the same lock used by workers until package removal and cleanup finish.
func uninstall(manager, settingsDir, runtimeDir, cronFile string) error {
	if _, e := os.Stat(filepath.Join(settingsDir, "transaction", "pending")); e == nil {
		return fmt.Errorf("есть незавершённое применение; сначала выполните dns-scout recover")
	} else if !os.IsNotExist(e) {
		return e
	}
	// APK removes unused dependencies. Keep the existing DNS service and LuCI stack.
	if filepath.Base(manager) == "apk" {
		if _, e := command(manager, "add", "--no-network", "https-dns-proxy", "luci-base", "rpcd", "ca-bundle"); e != nil {
			return fmt.Errorf("не удалось сохранить зависимости; удаление отменено: %w", e)
		}
	}
	b, e := os.ReadFile(cronFile)
	if e != nil && !os.IsNotExist(e) {
		return e
	}
	if e == nil {
		lines := []string{}
		for _, line := range strings.Split(strings.TrimRight(string(b), "\n"), "\n") {
			if !strings.HasSuffix(line, " # dns-scout") && line != "" {
				lines = append(lines, line)
			}
		}
		if e = atomicFile(cronFile, []byte(strings.Join(lines, "\n")+"\n"), 0600); e != nil {
			return e
		}
		if _, e = command("/etc/init.d/cron", "restart"); e != nil {
			return e
		}
	}
	action := "remove"
	if filepath.Base(manager) == "apk" {
		action = "del"
	}
	if _, e = command(manager, action, "luci-app-dns-scout"); e != nil {
		return fmt.Errorf("расписание отключено, но пакет не удалён; настройки сохранены: %w", e)
	}
	// Delete only this application's data, after the package manager succeeds.
	for _, dir := range []string{settingsDir, runtimeDir} {
		if e = os.RemoveAll(dir); e != nil {
			return fmt.Errorf("пакет удалён, не удалось очистить %s: %w", dir, e)
		}
	}
	if _, e = command("/etc/init.d/rpcd", "restart"); e != nil {
		return fmt.Errorf("пакет удалён, требуется перезапуск rpcd: %w", e)
	}
	return nil
}
