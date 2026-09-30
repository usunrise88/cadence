package egress

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"
)

// Refresher keeps a Dynamic list current: it polls the control plane's egressHosts.list (GET /api/egress-hosts)
// with the proxy's own credential (a cep_ token the control plane writes to TokenFile) every Every. When the control
// plane cannot be reached the last good list stays; the static list is never touched.
type Refresher struct {
	BaseURL   string // the control plane, e.g. http://control-plane:8080
	TokenFile string // re-read on every poll, so a re-issued token is picked up
	Every     time.Duration
	Ports     []int
	Into      *Dynamic
	Log       *slog.Logger
	Client    *http.Client

	last []string
}

func (r *Refresher) client() *http.Client {
	if r.Client != nil {
		return r.Client
	}
	// Never through a proxy: the control plane is on the proxy's own networks.
	return &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{Proxy: nil}}
}

// Once fetches the list and installs it; it returns the hosts it installed.
func (r *Refresher) Once(ctx context.Context) ([]string, error) {
	b, err := os.ReadFile(r.TokenFile)
	if err != nil {
		return nil, fmt.Errorf("read the egress token: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(r.BaseURL, "/")+"/api/egress-hosts", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(b)))
	req.Header.Set("Accept", "application/json")
	resp, err := r.client().Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch the egress allowlist: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("read the egress allowlist: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("the control plane answered %d: %s", resp.StatusCode, strings.TrimSpace(string(body[:min(len(body), 300)])))
	}
	var list struct {
		Hosts []string `json:"hosts"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		return nil, fmt.Errorf("decode the egress allowlist: %w", err)
	}
	a, err := ParseHosts(list.Hosts, r.Ports)
	if err != nil {
		return nil, fmt.Errorf("the control plane's allowlist: %w", err)
	}
	r.Into.Set(a)
	return list.Hosts, nil
}

// Run polls until ctx ends, logging changes and failures.
func (r *Refresher) Run(ctx context.Context) {
	log := r.Log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	every := r.Every
	if every <= 0 {
		every = 15 * time.Second
	}
	failing := false
	for {
		hosts, err := r.Once(ctx)
		switch {
		case err != nil && ctx.Err() == nil:
			if !failing {
				log.Warn("egress allowlist refresh failed; keeping the last list", "err", err)
			}
			failing = true
		case err == nil:
			if failing || !slices.Equal(hosts, r.last) {
				log.Info("egress allowlist from the control plane", "hosts", strings.Join(hosts, " "))
			}
			failing, r.last = false, hosts
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(every):
		}
	}
}
