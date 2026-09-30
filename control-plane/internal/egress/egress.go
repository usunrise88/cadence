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
	"sync/atomic"
	"time"
)

// Allowlist is the set of hosts the proxy lets through: an exact name ("api.anthropic.com") or a domain with its
// subdomains ("*.huggingface.co" or ".huggingface.co", which do not match the bare domain), on the allowed ports; or
// an exact host with its own port ("vllm.lan:8000", "10.0.0.5:8000": a custom provider's base URL). An IP address is
// reached only when it is listed exactly, never through a name rule.
type Allowlist struct {
	exact     map[string]bool
	suffixes  []string
	hostPorts map[string]bool // net.JoinHostPort(host, port)
	ports     []int
}

// ParseAllowlist reads a list separated by commas or white space; ports are the destination ports allowed (443 and
// 80 when empty). An empty list is an error: the static list is the proxy's whole policy when the control plane
// cannot be reached.
func ParseAllowlist(list string, ports []int) (Allowlist, error) {
	a, err := ParseHosts(strings.FieldsFunc(list, func(r rune) bool { return r == ',' || r == ' ' || r == '\n' || r == '\t' }), ports)
	if err != nil {
		return Allowlist{}, err
	}
	if a.Empty() {
		return Allowlist{}, errors.New("the allowlist is empty: name the hosts agents may reach")
	}
	return a, nil
}

// ParseHosts reads a list of entries (host, *.domain, .domain, host:port); an empty list is allowed (the hosts the
// control plane adds).
func ParseHosts(list []string, ports []int) (Allowlist, error) {
	a := Allowlist{exact: map[string]bool{}, hostPorts: map[string]bool{}, ports: ports}
	if len(a.ports) == 0 {
		a.ports = []int{443, 80}
	}
	for _, f := range list {
		h := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(f), "."))
		switch {
		case h == "":
		case strings.HasPrefix(h, "*."):
			if strings.ContainsAny(h[2:], "*/:[]") {
				return Allowlist{}, fmt.Errorf("%q is not a host name or *.domain", f)
			}
			a.suffixes = append(a.suffixes, h[1:])
		case strings.HasPrefix(h, "."):
			if strings.ContainsAny(h, "*/:[]") {
				return Allowlist{}, fmt.Errorf("%q is not a host name or *.domain", f)
			}
			a.suffixes = append(a.suffixes, h)
		case strings.ContainsAny(h, "*/"):
			return Allowlist{}, fmt.Errorf("%q is not a host name, *.domain or host:port", f)
		case strings.Contains(h, ":") && net.ParseIP(strings.Trim(h, "[]")) == nil:
			host, ps, err := net.SplitHostPort(h)
			port, perr := strconv.Atoi(ps)
			if err != nil || perr != nil || host == "" || port < 1 || port > 65535 || strings.ContainsAny(host, "*/") {
				return Allowlist{}, fmt.Errorf("%q is not a host name, *.domain or host:port", f)
			}
			a.hostPorts[net.JoinHostPort(strings.Trim(host, "[]"), strconv.Itoa(port))] = true
		default:
			a.exact[strings.Trim(h, "[]")] = true
		}
	}
	return a, nil
}

// Empty reports whether the list allows nothing.
func (a Allowlist) Empty() bool {
	return len(a.exact) == 0 && len(a.suffixes) == 0 && len(a.hostPorts) == 0
}

// Allows reports whether host:port may be reached.
func (a Allowlist) Allows(host string, port int) bool {
	h := strings.Trim(strings.ToLower(strings.TrimSuffix(host, ".")), "[]")
	if a.hostPorts[net.JoinHostPort(h, strconv.Itoa(port))] {
		return true
	}
	if !slices.Contains(a.ports, port) {
		return false
	}
	if net.ParseIP(h) != nil {
		return a.exact[h] // addresses bypass name rules: only an exact entry
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

// Dynamic is the part of the allowlist the control plane adds (egressHosts.list: the configured providers' API
// hosts and custom base URLs); a Refresher keeps it current. The zero value allows nothing.
type Dynamic struct {
	list atomic.Pointer[Allowlist]
}

// Set replaces the dynamic list.
func (d *Dynamic) Set(a Allowlist) { d.list.Store(&a) }

// Allows reports whether the dynamic list allows host:port.
func (d *Dynamic) Allows(host string, port int) bool {
	if d == nil {
		return false
	}
	a := d.list.Load()
	return a != nil && a.Allows(host, port)
}

// Proxy is the HTTP handler of the proxy.
type Proxy struct {
	// Allow is the static list (CADENCE_EGRESS_ALLOW); Extra, when set, what the control plane adds.
	Allow Allowlist
	Extra *Dynamic
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
	if err != nil || r.URL.Scheme != "http" || !p.allows(host, port) {
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

func (p *Proxy) allows(host string, port int) bool {
	return p.Allow.Allows(host, port) || p.Extra.Allows(host, port)
}

func (p *Proxy) refuse(w http.ResponseWriter, r *http.Request, target string) {
	p.Log.Warn("egress refused", "target", target, "method", r.Method, "client", r.RemoteAddr)
	http.Error(w, "Cadence egress proxy: "+target+" is not on the allowlist (docs/spec/08-resolutions.md R4)", http.StatusForbidden)
}

func (p *Proxy) connect(w http.ResponseWriter, r *http.Request) {
	host, port, err := hostPort(r.Host, 443)
	if err != nil || !p.allows(host, port) {
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
