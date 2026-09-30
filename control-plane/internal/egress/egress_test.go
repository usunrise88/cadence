package egress

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestAllowlist(t *testing.T) {
	a, err := ParseAllowlist("api.anthropic.com, *.huggingface.co .pypi.org\nfiles.pythonhosted.org", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		host string
		port int
		want bool
	}{
		{"api.anthropic.com", 443, true},
		{"API.anthropic.com.", 443, true},
		{"evil-anthropic.com", 443, false},
		{"anthropic.com", 443, false},
		{"cdn-lfs.huggingface.co", 443, true},
		{"huggingface.co", 443, false}, // *.domain does not cover the bare domain
		{"x.pypi.org", 80, true},
		{"api.anthropic.com", 22, false},
		{"1.2.3.4", 443, false},
		{"[::1]", 443, false},
		{"files.pythonhosted.org", 443, true},
	} {
		if got := a.Allows(tc.host, tc.port); got != tc.want {
			t.Errorf("Allows(%s, %d) = %v, want %v", tc.host, tc.port, got, tc.want)
		}
	}
	for _, bad := range []string{"", " , ", "http://x.org", "a*b.org"} {
		if _, err := ParseAllowlist(bad, nil); err == nil {
			t.Errorf("ParseAllowlist(%q) accepted", bad)
		}
	}
}

// TestProxyTunnelsAllowedHostsOnly runs the proxy in front of a fake upstream: CONNECT to an allowed name tunnels
// bytes both ways; any other name is 403 and never dialled.
func TestProxyTunnelsAllowedHostsOnly(t *testing.T) {
	echo, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = echo.Close() }()
	go func() {
		for {
			c, err := echo.Accept()
			if err != nil {
				return
			}
			go func() { _, _ = io.Copy(c, c); _ = c.Close() }()
		}
	}()
	allow, _ := ParseAllowlist("api.anthropic.com", []int{443})
	var dialled []string
	p := &Proxy{Allow: allow, Dial: func(ctx context.Context, network, addr string) (net.Conn, error) {
		dialled = append(dialled, addr)
		return (&net.Dialer{}).DialContext(ctx, network, echo.Addr().String())
	}}
	srv := httptest.NewServer(p)
	defer srv.Close()
	addr := strings.TrimPrefix(srv.URL, "http://")

	connect := func(target string) (string, net.Conn) {
		c, err := net.Dial("tcp", addr)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = fmt.Fprintf(c, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", target, target)
		r := bufio.NewReader(c)
		line, _ := r.ReadString('\n')
		for {
			l, err := r.ReadString('\n')
			if err != nil || l == "\r\n" {
				break
			}
		}
		return line, c
	}
	line, c := connect("api.anthropic.com:443")
	if !strings.Contains(line, "200") {
		t.Fatalf("allowed CONNECT answered %q", line)
	}
	_, _ = c.Write([]byte("ping\n"))
	got, _ := bufio.NewReader(c).ReadString('\n')
	_ = c.Close()
	if got != "ping\n" {
		t.Errorf("tunnel echoed %q", got)
	}
	line, c = connect("example.com:443")
	_ = c.Close()
	if !strings.Contains(line, "403") {
		t.Errorf("CONNECT to a host off the list answered %q", line)
	}
	line, c = connect("api.anthropic.com:8443")
	_ = c.Close()
	if !strings.Contains(line, "403") {
		t.Errorf("CONNECT to another port answered %q", line)
	}
	if len(dialled) != 1 || dialled[0] != "api.anthropic.com:443" {
		t.Errorf("dialled %v", dialled)
	}

	// Plain HTTP through the proxy: refused off the list.
	proxyURL, _ := url.Parse(srv.URL)
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)}}
	resp, err := client.Get("http://example.org/") //nolint:noctx // test
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("plain HTTP off the list answered %d", resp.StatusCode)
	}
}
