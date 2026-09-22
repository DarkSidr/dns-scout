package main

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

type Result struct {
	Server  Server    `json:"server"`
	Success int       `json:"success"`
	Total   int       `json:"total"`
	Median  float64   `json:"median_ms"`
	P95     float64   `json:"p95_ms"`
	Error   string    `json:"error"`
	Samples []float64 `json:"samples_ms"`
}
type Report struct {
	Started   int64    `json:"started"`
	Finished  int64    `json:"finished"`
	Results   []Result `json:"results"`
	Bootstrap []Result `json:"bootstrap"`
	Config    Config   `json:"config"`
}
type Job struct {
	Running bool   `json:"running"`
	Kind    string `json:"kind"`
	Message string `json:"message"`
	Done    int    `json:"done"`
	Total   int    `json:"total"`
	Updated int64  `json:"updated"`
}

func job(j Job) { j.Updated = time.Now().Unix(); atomicJSON(stateDir+"/job.json", j) }
func lock() (*os.File, error) {
	os.MkdirAll(stateDir, 0700)
	f, e := os.OpenFile(stateDir+"/lock", os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return nil, e
	}
	if e = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); e != nil {
		f.Close()
		return nil, fmt.Errorf("уже выполняется другая операция")
	}
	return f, nil
}
func unlock(f *os.File) { syscall.Flock(int(f.Fd()), syscall.LOCK_UN); f.Close() }
func stats(r *Result) {
	if r.Success == 0 {
		return
	}
	a := append([]float64(nil), r.Samples...)
	sort.Float64s(a)
	r.Median = a[len(a)/2]
	r.P95 = a[(len(a)*95+99)/100-1]
}
func testServer(c Config, s Server) Result {
	r := Result{Server: s, Total: c.Samples, Samples: []float64{}}
	for i := 0; i < c.Samples; i++ {
		q := query(c.Domains[i%len(c.Domains)])
		ctx, cancel := context.WithTimeout(context.Background(), time.Duration(c.Timeout)*time.Second)
		start := time.Now()
		b, e := exchange(ctx, s, c.Bootstrap, q)
		if e == nil {
			e = validateDNS(q, b)
		}
		ms := float64(time.Since(start).Microseconds()) / 1000
		cancel()
		if e != nil {
			r.Error = e.Error()
		} else {
			r.Success++
			r.Samples = append(r.Samples, ms)
		}
	}
	stats(&r)
	return r
}
func rank(rs []Result) {
	sort.SliceStable(rs, func(i, j int) bool {
		a, b := rs[i], rs[j]
		if a.Success*b.Total != b.Success*a.Total {
			return a.Success*b.Total > b.Success*a.Total
		}
		return a.P95 < b.P95
	})
}
func scan(c Config) (Report, error) {
	report := Report{Started: time.Now().Unix(), Config: c, Results: []Result{}, Bootstrap: []Result{}}
	for _, ip := range c.Bootstrap {
		r := Result{Server: Server{ID: ip, Name: ip}, Total: len(c.Domains), Samples: []float64{}}
		for _, d := range c.Domains {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			q := query(d)
			start := time.Now()
			b, e := plainQuery(ctx, ip+":53", q)
			if e == nil {
				e = validateDNS(q, b)
			}
			cancel()
			if e == nil {
				r.Success++
				r.Samples = append(r.Samples, float64(time.Since(start).Microseconds())/1000)
			} else {
				r.Error = e.Error()
			}
		}
		stats(&r)
		report.Bootstrap = append(report.Bootstrap, r)
	}
	rank(report.Bootstrap)
	total := 0
	for _, s := range c.Servers {
		if s.Enabled {
			total++
		}
	}
	if total == 0 {
		return report, fmt.Errorf("нет включённых серверов")
	}
	job(Job{Running: true, Kind: "scan", Message: "Проверка DoH", Total: total})
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, c.Parallel)
	for _, s := range c.Servers {
		if !s.Enabled {
			continue
		}
		sem <- struct{}{}
		wg.Add(1)
		go func(s Server) {
			defer wg.Done()
			defer func() { <-sem }()
			r := testServer(c, s)
			mu.Lock()
			report.Results = append(report.Results, r)
			atomicJSON(stateDir+"/report.json", report)
			job(Job{Running: true, Kind: "scan", Message: "Проверка DoH", Done: len(report.Results), Total: total})
			mu.Unlock()
		}(s)
	}
	wg.Wait()
	rank(report.Results)
	report.Finished = time.Now().Unix()
	return report, atomicJSON(stateDir+"/report.json", report)
}
func candidates(r Report, c Config, id string) ([]Server, error) {
	if r.Finished == 0 || time.Now().Unix()-r.Finished > 3600 {
		return nil, fmt.Errorf("результаты устарели: запустите тест")
	}
	eligible := []Server{}
	for _, v := range r.Results {
		if v.Success != v.Total || v.Total < 2 {
			continue
		}
		for _, s := range c.Servers {
			if s.ID == v.Server.ID && s.URL == v.Server.URL && s.Enabled && s.Eligible {
				eligible = append(eligible, s)
			}
		}
	}
	if id != "" {
		found := -1
		for i, s := range eligible {
			if s.ID == id {
				found = i
			}
		}
		if found < 0 {
			return nil, fmt.Errorf("сервер не прошёл все тесты или исключён из выбора")
		}
		s := eligible[found]
		eligible = append([]Server{s}, append(eligible[:found], eligible[found+1:]...)...)
	}
	// Prefer independent operators/hosts; no duplicate URLs in the fallback chain.
	out := []Server{}
	seen := map[string]bool{}
	for _, s := range eligible {
		if key := providerKey(s); !seen[key] {
			out = append(out, s)
			seen[key] = true
		}
		if len(out) == c.Fallbacks {
			break
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("нет серверов со 100%% успешных ответов")
	}
	return out, nil
}

// Known aliases and IP endpoints of one operator do not make independent fallbacks.
func providerKey(s Server) string {
	u, e := url.Parse(s.URL)
	if e != nil {
		return s.URL
	}
	h := strings.ToLower(u.Hostname())
	for _, group := range [][]string{{"quad9", "quad9.net", "9.9.9.9", "9.9.9.10", "149.112.112.112"}, {"cloudflare", "cloudflare-dns.com", "1.1.1.1", "1.0.0.1"}, {"google", "dns.google", "8.8.8.8", "8.8.4.4"}, {"adguard", "adguard-dns.com", "adguard.com"}, {"controld", "controld.com"}} {
		for _, suffix := range group[1:] {
			if h == suffix || strings.HasSuffix(h, "."+suffix) {
				return group[0]
			}
		}
	}
	return h
}
