package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

func query(name string) []byte {
	b := make([]byte, 12)
	rand.Read(b[:2])
	b[2] = 1
	b[5] = 1
	for _, p := range strings.Split(strings.TrimSuffix(name, "."), ".") {
		b = append(b, byte(len(p)))
		b = append(b, p...)
	}
	return append(b, 0, 0, 1, 0, 1)
}

// Bounded compression traversal: reject loops, truncated labels and out-of-range pointers.
func nameAt(b []byte, pos int) (string, int, error) {
	parts := []string{}
	end := -1
	for hops := 0; hops < 128; hops++ {
		if pos >= len(b) {
			break
		}
		n := int(b[pos])
		pos++
		if n == 0 {
			if end < 0 {
				end = pos
			}
			return strings.ToLower(strings.Join(parts, ".")), end, nil
		}
		if n&192 == 192 {
			if pos >= len(b) {
				break
			}
			if end < 0 {
				end = pos + 1
			}
			pos = (n&63)*256 + int(b[pos])
			continue
		}
		if n > 63 || pos+n > len(b) {
			break
		}
		parts = append(parts, string(b[pos:pos+n]))
		pos += n
	}
	return "", 0, fmt.Errorf("invalid DNS name")
}
func validateDNS(q, b []byte) error {
	if len(b) < 12 || len(q) < 12 {
		return fmt.Errorf("truncated DNS packet")
	}
	if !bytes.Equal(b[:2], q[:2]) || b[2]&128 == 0 || b[2]&120 != 0 || b[2]&2 != 0 {
		return fmt.Errorf("DNS ID/flags mismatch or truncated reply")
	}
	if b[3]&15 != 0 {
		return fmt.Errorf("DNS RCODE %d", b[3]&15)
	}
	if binary.BigEndian.Uint16(b[4:6]) != 1 {
		return fmt.Errorf("invalid question count")
	}
	qn, qe, e := nameAt(q, 12)
	if e != nil {
		return e
	}
	bn, be, e := nameAt(b, 12)
	if e != nil || be+4 > len(b) || bn != qn || !bytes.Equal(q[qe:qe+4], b[be:be+4]) {
		return fmt.Errorf("DNS question mismatch")
	}
	n := int(binary.BigEndian.Uint16(b[6:8]))
	pos := be + 4
	valid := false
	for i := 0; i < n; i++ {
		_, p, e := nameAt(b, pos)
		if e != nil || p+10 > len(b) {
			return fmt.Errorf("invalid answer")
		}
		typ := binary.BigEndian.Uint16(b[p : p+2])
		class := binary.BigEndian.Uint16(b[p+2 : p+4])
		size := int(binary.BigEndian.Uint16(b[p+8 : p+10]))
		pos = p + 10 + size
		if pos > len(b) {
			return fmt.Errorf("truncated answer")
		}
		if typ == 1 && class == 1 && size == 4 {
			ip := net.IP(b[p+10 : pos])
			if !ip.IsGlobalUnicast() || ip.IsPrivate() {
				return fmt.Errorf("unexpected private/sinkhole answer: %s", ip)
			}
			valid = true
		}
	}
	if !valid {
		return fmt.Errorf("no public IPv4 answer")
	}
	return nil
}
func plainQuery(ctx context.Context, addr string, q []byte) ([]byte, error) {
	c, e := (&net.Dialer{}).DialContext(ctx, "udp", addr)
	if e != nil {
		return nil, e
	}
	defer c.Close()
	deadline, _ := ctx.Deadline()
	c.SetDeadline(deadline)
	if _, e = c.Write(q); e != nil {
		return nil, e
	}
	b := make([]byte, 65535)
	n, e := c.Read(b)
	if e != nil {
		return nil, e
	}
	return b[:n], nil
}
func lookup(ctx context.Context, host string, bootstrap []string) ([]string, error) {
	if ip := net.ParseIP(host); ip != nil {
		return []string{ip.String()}, nil
	}
	var last error
	for _, ip := range bootstrap {
		sub, cancel := context.WithTimeout(ctx, 2*time.Second)
		r := &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, net.JoinHostPort(ip, "53"))
		}}
		ips, e := r.LookupIP(sub, "ip4", host)
		cancel()
		if e != nil {
			last = e
			continue
		}
		out := []string{}
		for _, a := range ips {
			if a.IsGlobalUnicast() && !a.IsPrivate() {
				out = append(out, a.String())
			}
		}
		if len(out) > 0 {
			return out, nil
		}
	}
	return nil, fmt.Errorf("bootstrap: %v", last)
}
func exchange(ctx context.Context, s Server, bootstrap []string, q []byte) ([]byte, error) {
	u, e := url.Parse(s.URL)
	if e != nil {
		return nil, e
	}
	host := u.Hostname()
	port := u.Port()
	if port == "" {
		port = "443"
	}
	ips, e := lookup(ctx, host, bootstrap)
	if e != nil {
		return nil, e
	}
	dial := func(ctx context.Context, network, address string) (net.Conn, error) {
		var last error
		for _, ip := range ips {
			c, e := (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, "tcp", net.JoinHostPort(ip, port))
			if e == nil {
				return c, nil
			}
			last = e
		}
		return nil, last
	}
	tr := &http.Transport{DialContext: dial, ForceAttemptHTTP2: true, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}, DisableKeepAlives: true}
	defer tr.CloseIdleConnections()
	client := &http.Client{Transport: tr, CheckRedirect: func(r *http.Request, via []*http.Request) error { return fmt.Errorf("DoH redirect refused") }}
	r, e := http.NewRequestWithContext(ctx, "POST", s.URL, bytes.NewReader(q))
	if e != nil {
		return nil, e
	}
	r.Header.Set("Content-Type", "application/dns-message")
	r.Header.Set("Accept", "application/dns-message")
	resp, e := client.Do(r)
	if e != nil {
		return nil, e
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	if strings.Split(resp.Header.Get("Content-Type"), ";")[0] != "application/dns-message" {
		return nil, fmt.Errorf("not a DNS response")
	}
	return io.ReadAll(io.LimitReader(resp.Body, 65536))
}
