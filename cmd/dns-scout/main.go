package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"time"
)

func freePort() (int, error) {
	c, e := net.ListenPacket("udp4", "127.0.0.1:0")
	if e != nil {
		return 0, e
	}
	defer c.Close()
	return c.LocalAddr().(*net.UDPAddr).Port, nil
}
func emit(v any)   { json.NewEncoder(os.Stdout).Encode(v) }
func fail(e error) { emit(map[string]any{"error": e.Error()}) }
func start(kind, id string) error {
	f, e := lock()
	if e != nil {
		return e
	}
	defer f.Close() // child inherits the flock; do not explicitly unlock here.
	job(Job{Running: true, Kind: kind, Message: "Запуск"})
	self, e := os.Executable()
	if e != nil {
		return e
	}
	cmd := exec.Command(self, "worker", kind, id)
	cmd.ExtraFiles = []*os.File{f}
	if e = cmd.Start(); e != nil {
		job(Job{Message: e.Error()})
		return e
	}
	return cmd.Process.Release()
}
func run(kind, id string) error {
	if kind == "recover" {
		return recoverPending()
	}
	c, e := loadConfig()
	if e != nil {
		return e
	}
	switch kind {
	case "scan", "scheduled":
		r, e := scan(c)
		if e != nil {
			return e
		}
		if kind == "scheduled" && c.Auto {
			ss, e := candidates(r, c, "")
			if e != nil {
				return e
			}
			job(Job{Running: true, Kind: "apply", Message: "Проверка и применение"})
			return apply(c, ss)
		}
		return nil
	case "apply":
		var r Report
		if e = readJSON(stateDir+"/report.json", &r); e != nil {
			return e
		}
		ss, e := candidates(r, c, id)
		if e != nil {
			return e
		}
		return apply(c, ss)
	case "recover":
		return recoverPending()
	}
	return fmt.Errorf("неизвестная операция")
}
func syncCron(c Config) error {
	const path = "/etc/crontabs/root"
	b, e := os.ReadFile(path)
	if e != nil && !os.IsNotExist(e) {
		return e
	}
	lines := []string{}
	for _, l := range strings.Split(strings.TrimRight(string(b), "\n"), "\n") {
		if strings.HasSuffix(l, " # dns-scout") {
			continue
		}
		if l != "" {
			lines = append(lines, l)
		}
	}
	if c.Daily != "" {
		p := strings.Split(c.Daily, ":")
		h, _ := strconv.Atoi(p[0])
		m, _ := strconv.Atoi(p[1])
		lines = append(lines, fmt.Sprintf("%d %d * * * /usr/sbin/dns-scout scheduled # dns-scout", m, h))
	}
	if e = atomicFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0600); e != nil {
		return e
	}
	if c.Daily != "" {
		if _, e = command("/etc/init.d/cron", "enable"); e != nil {
			return e
		}
	}
	_, e = command("/etc/init.d/cron", "restart")
	return e
}
func rpc(method string) {
	var in struct {
		ID     string `json:"id"`
		Config string `json:"config"`
	}
	if e := json.NewDecoder(io.LimitReader(os.Stdin, 262145)).Decode(&in); e != nil {
		fail(e)
		return
	}
	switch method {
	case "status":
		c, e := loadConfig()
		if e != nil {
			fail(e)
			return
		}
		var r any
		readJSON(stateDir+"/report.json", &r)
		var j Job
		readJSON(stateDir+"/job.json", &j)
		f, e := lock()
		if e == nil {
			unlock(f)
			if j.Running {
				j.Running = false
				j.Message = "Предыдущая операция прервана"
			}
		}
		var active any
		readJSON("/etc/dns-scout/active.json", &active)
		_, pending := os.Stat(backupDir + "/pending")
		emit(map[string]any{"config": c, "report": r, "job": j, "active": active, "pending_recovery": pending == nil})
	case "save":
		f, e := lock()
		if e != nil {
			fail(e)
			return
		}
		defer unlock(f)
		var c Config
		if e = json.Unmarshal([]byte(in.Config), &c); e == nil {
			e = c.validate()
		}
		if e == nil {
			old, re := os.ReadFile(configPath)
			if re != nil {
				e = re
			} else if e = atomicJSON(configPath, c); e == nil {
				if e = syncCron(c); e != nil {
					atomicFile(configPath, old, 0600)
					var prev Config
					if json.Unmarshal(old, &prev) == nil {
						syncCron(prev)
					}
				}
			}
		}
		if e != nil {
			fail(e)
		} else {
			emit(map[string]bool{"ok": true})
		}
	case "scan", "apply", "recover":
		if in.ID != "" && !idRE.MatchString(in.ID) {
			fail(fmt.Errorf("некорректный ID"))
			return
		}
		if e := start(method, in.ID); e != nil {
			fail(e)
		} else {
			emit(map[string]bool{"ok": true})
		}
	default:
		fail(fmt.Errorf("неизвестный метод"))
	}
}
func main() {
	runtime.GOMAXPROCS(2)
	debug.SetGCPercent(50)
	debug.SetMemoryLimit(20 << 20)
	if len(os.Args) < 2 {
		fmt.Println("dns-scout 0.1.0: scan | scheduled | preflight | recover | sync-cron | rpc list/call")
		return
	}
	switch os.Args[1] {
	case "rpc":
		if len(os.Args) == 3 && os.Args[2] == "list" {
			fmt.Println(`{"status":{},"scan":{},"apply":{"id":""},"save":{"config":""},"recover":{}}`)
			return
		}
		if len(os.Args) == 4 && os.Args[2] == "call" {
			rpc(os.Args[3])
			return
		}
	case "watch":
		for i := 0; i < 600; i++ {
			time.Sleep(time.Second)
			if _, e := os.Stat(backupDir + "/pending"); os.IsNotExist(e) {
				return
			}
			f, e := lock()
			if e != nil {
				continue
			}
			e = recoverPending()
			unlock(f)
			if e != nil {
				job(Job{Message: "Ошибка аварийного отката: " + e.Error()})
			} else {
				job(Job{Message: "Прерванное применение: настройки восстановлены"})
			}
			return
		}
		return
	case "worker":
		if len(os.Args) != 4 {
			os.Exit(2)
		}
		f := os.NewFile(3, "lock")
		defer f.Close()
		e := run(os.Args[2], os.Args[3])
		if e != nil {
			job(Job{Kind: os.Args[2], Message: e.Error()})
			os.Exit(1)
		}
		job(Job{Kind: os.Args[2], Message: "Готово"})
		return
	case "scan", "scheduled", "recover":
		f, e := lock()
		if e != nil {
			fail(e)
			os.Exit(1)
		}
		defer unlock(f)
		e = run(os.Args[1], "")
		if e != nil {
			job(Job{Message: e.Error()})
			fail(e)
			os.Exit(1)
		}
		job(Job{Kind: os.Args[1], Message: "Готово"})
		emit(map[string]any{"ok": true, "finished": time.Now().Unix()})
		return
	case "preflight":
		c, e := loadConfig()
		if e == nil {
			var r Report
			e = readJSON(stateDir+"/report.json", &r)
			if e == nil {
				var ss []Server
				ss, e = candidates(r, c, "")
				if e == nil {
					e = preflight(c, ss)
				}
			}
		}
		if e != nil {
			fail(e)
			os.Exit(1)
		}
		emit(map[string]bool{"ok": true})
		return
	case "sync-cron":
		c, e := loadConfig()
		if e == nil {
			e = syncCron(c)
		}
		if e != nil {
			fail(e)
			os.Exit(1)
		}
		return
	}
	os.Exit(2)
}
