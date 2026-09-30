package egress

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// TestHostPortsAndAddresses: entries the control plane derives from custom base URLs — a host with its own port,
// an IP address listed exactly — and what they do not open.
func TestHostPortsAndAddresses(t *testing.T) {
	a, err := ParseHosts([]string{"vllm.lan:8000", "10.0.0.5:8000", "[fd00::5]:9000", "api.minimax.io", "10.0.0.9"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		host string
		port int
		want bool
	}{
		{"vllm.lan", 8000, true},
		{"VLLM.lan.", 8000, true},
		{"vllm.lan", 443, false}, // a host:port entry opens that port only
		{"vllm.lan", 8001, false},
		{"10.0.0.5", 8000, true},
		{"10.0.0.5", 443, false},
		{"fd00::5", 9000, true},
		{"[fd00::5]", 9000, true},
		{"10.0.0.9", 443, true}, // an address listed exactly, on the allowed ports
		{"10.0.0.9", 8000, false},
		{"10.0.0.10", 443, false},
		{"api.minimax.io", 443, true},
		{"api.minimax.io", 8000, false},
	} {
		if got := a.Allows(tc.host, tc.port); got != tc.want {
			t.Errorf("Allows(%s, %d) = %v, want %v", tc.host, tc.port, got, tc.want)
		}
	}
	for _, bad := range []string{"vllm.lan:http", "vllm.lan:0", "vllm.lan:70000", ":8000", "*.lan:8000", "http://x:1"} {
		if _, err := ParseHosts([]string{bad}, nil); err == nil {
			t.Errorf("ParseHosts(%q) accepted", bad)
		}
	}
	empty, err := ParseHosts(nil, nil)
	if err != nil || !empty.Empty() || empty.Allows("api.minimax.io", 443) {
		t.Errorf("an empty dynamic list: %v, %v", empty, err)
	}
}

// TestRefresher polls a fake control plane: the token from the file goes in the Authorization header, the list it
// answers opens hosts in the proxy, a failure keeps the last list, and a new list replaces it.
func TestRefresher(t *testing.T) {
	hosts := `{"hosts":["api.minimax.io","vllm.lan:8000"]}`
	status := http.StatusOK
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/egress-hosts" {
			http.NotFound(w, r)
			return
		}
		auth = r.Header.Get("Authorization")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(hosts))
	}))
	defer srv.Close()
	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte("cep_test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	static, err := ParseAllowlist("pypi.org", nil)
	if err != nil {
		t.Fatal(err)
	}
	p := &Proxy{Allow: static, Extra: &Dynamic{}}
	r := &Refresher{BaseURL: srv.URL, TokenFile: tokenFile, Into: p.Extra}
	if p.allows("api.minimax.io", 443) {
		t.Fatal("allowed before the first refresh")
	}
	got, err := r.Once(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if auth != "Bearer cep_test" {
		t.Errorf("Authorization = %q", auth)
	}
	if !slices.Equal(got, []string{"api.minimax.io", "vllm.lan:8000"}) {
		t.Errorf("hosts = %v", got)
	}
	for _, tc := range []struct {
		host string
		port int
		want bool
	}{
		{"api.minimax.io", 443, true}, {"vllm.lan", 8000, true}, {"pypi.org", 443, true}, {"api.openai.com", 443, false},
	} {
		if p.allows(tc.host, tc.port) != tc.want {
			t.Errorf("allows(%s, %d) != %v", tc.host, tc.port, tc.want)
		}
	}
	status = http.StatusServiceUnavailable
	if _, err := r.Once(context.Background()); err == nil {
		t.Error("a failed refresh reported no error")
	}
	if !p.allows("api.minimax.io", 443) {
		t.Error("a failed refresh dropped the last list")
	}
	status, hosts = http.StatusOK, `{"hosts":[]}`
	if _, err := r.Once(context.Background()); err != nil {
		t.Fatal(err)
	}
	if p.allows("api.minimax.io", 443) || !p.allows("pypi.org", 443) {
		t.Error("a new list did not replace the old one, or touched the static list")
	}
}
