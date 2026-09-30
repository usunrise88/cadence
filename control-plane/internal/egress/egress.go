// Package egress is the allowlisting forward proxy of the agent sandbox (docs/spec/08-resolutions.md R4): the agent
// host sits on an internal compose network whose only way out is this proxy, which tunnels (HTTP CONNECT) or
// forwards (plain HTTP) to the allowed hosts only — the model providers, Hugging Face, PyPI and NGC — and refuses
// everything else with 403. It sees host names, never the traffic inside a TLS tunnel.
package egress

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Allowlist is the set of hosts the proxy lets through: an exact name ("api.anthropic.com") or a domain with its
// subdomains ("*.huggingface.co" or ".huggingface.co", which do not match the bare domain).
type Allowlist struct {
	exact    map[string]bool
	suffixes []string
	ports    []int
}

// ParseAllowlist reads a list separated by commas or white space; ports are the destination ports allowed (443 and
// 80 when empty).
func ParseAllowlist(list string, ports []int) (Allowlist, error) {
	a := Allowlist{exact: map[string]bool{}, ports: ports}
	if len(a.ports) == 0 {
		a.ports = []int{443, 80}
	}
	for _, f := range strings.FieldsFunc(list, func(r rune) bool { return r == ',' || r == ' ' || r == '\n' || r == '\t' }) {
		h := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(f), "."))
		switch {
		case h == "":
		case strings.HasPrefix(h, "*."):
			a.suffixes = append(a.suffixes, h[1:])
		case strings.HasPrefix(h, "."):
			a.suffixes = append(a.suffixes, h)
		case strings.ContainsAny(h, "*/:"):
			return Allowlist{}, fmt.Errorf("%q is not a host name or *.domain", f)
		default:
			a.exact[h] = true
		}
	}
	if len(a.exact) == 0 && len(a.suffixes) == 0 {
		return Allowlist{}, errors.New("the allowlist is empty: name the hosts agents may reach")
	}
	return a, nil
}

// Allows reports whether host:port may be reached.
func (a Allowlist) Allows(host string, port int) bool {
	if !slices.Contains(a.ports, port) {
		return false
	}
	h := strings.ToLower(strings.TrimSuffix(host, "."))
	if net.ParseIP(strings.Trim(h, "[]")) != nil {
		return false // addresses bypass name rules: never
	}
	if a.exact[h] {
		return true
	}
	for _, s := range a.suffixes {
		if strings.HasSuffix(h, s) {
			return true
		}
	}
	return false
}

// Proxy is the HTTP handler of the proxy.
type Proxy struct {
	Allow Allowlist
	Log   *slog.Logger
	// Dial connects to the destination (tests replace it).
	Dial func(ctx context.Context, network, addr string) (net.Conn, error)
	// Transport forwards plain HTTP requests.
	Transport http.RoundTripper

	once sync.Once
}

func (p *Proxy) init() {
	p.once.Do(func() {
		if p.Log == nil {
			p.Log = slog.New(slog.DiscardHandler)
		}
		if p.Dial == nil {
			d := &net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}
			p.Dial = d.DialContext
		}
		if p.Transport == nil {
			p.Transport = &http.Transport{DialContext: p.Dial, Proxy: nil, ResponseHeaderTimeout: 60 * time.Second}
		}
	})
}

func hostPort(hostport string, def int) (string, int, error) {
	host, ps, err := net.SplitHostPort(hostport)
	if err != nil {
		return hostport, def, nil //nolint:nilerr // no port given
	}
	port, err := strconv.Atoi(ps)
	if err != nil {
		return "", 0, fmt.Errorf("bad port in %q", hostport)
	}
	return host, port, nil
}

// ServeHTTP tunnels CONNECT requests and forwards absolute-URI requests to allowed hosts.
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p.init()
	if r.Method == http.MethodConnect {
		p.connect(w, r)
		return
	}
	if r.URL.Host == "" || !r.URL.IsAbs() {
		http.Error(w, "this is a forward proxy: send absolute URLs or CONNECT", http.StatusBadRequest)
		return
	}
	host, port, err := hostPort(r.URL.Host, 80)
	if err != nil || r.URL.Scheme != "http" || !p.Allow.Allows(host, port) {
		p.refuse(w, r, r.URL.Host)
		return
	}
	out := r.Clone(r.Context())
	out.RequestURI = ""
	out.Header.Del("Proxy-Connection")
	out.Header.Del("Proxy-Authorization")
	resp, err := p.Transport.RoundTrip(out)
	if err != nil {
		http.Error(w, "upstream: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer func() { _ = resp.Body.Close() }()
	for k, vs := range resp.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

func (p *Proxy) refuse(w http.ResponseWriter, r *http.Request, target string) {
	p.Log.Warn("egress refused", "target", target, "method", r.Method, "client", r.RemoteAddr)
	http.Error(w, "Cadence egress proxy: "+target+" is not on the allowlist (docs/spec/08-resolutions.md R4)", http.StatusForbidden)
}

func (p *Proxy) connect(w http.ResponseWriter, r *http.Request) {
	host, port, err := hostPort(r.Host, 443)
	if err != nil || !p.Allow.Allows(host, port) {
		p.refuse(w, r, r.Host)
		return
	}
	upstream, err := p.Dial(r.Context(), "tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		http.Error(w, "upstream: "+err.Error(), http.StatusBadGateway)
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		_ = upstream.Close()
		http.Error(w, "cannot tunnel on this connection", http.StatusInternalServerError)
		return
	}
	client, buf, err := hj.Hijack()
	if err != nil {
		_ = upstream.Close()
		return
	}
	if _, err := client.Write([]byte("HTTP/1.1 200 Connection established\r\n\r\n")); err != nil {
		_ = client.Close()
		_ = upstream.Close()
		return
	}
	p.Log.Info("egress tunnel", "target", r.Host, "client", r.RemoteAddr)
	go func() {
		if buf != nil && buf.Reader.Buffered() > 0 {
			b := make([]byte, buf.Reader.Buffered())
			n, _ := buf.Read(b)
			_, _ = upstream.Write(b[:n])
		}
		_, _ = io.Copy(upstream, client)
		closeWrite(upstream)
	}()
	_, _ = io.Copy(client, upstream)
	closeWrite(client)
	_ = client.Close()
	_ = upstream.Close()
}

func closeWrite(c net.Conn) {
	if cw, ok := c.(interface{ CloseWrite() error }); ok {
		_ = cw.CloseWrite()
	}
}
