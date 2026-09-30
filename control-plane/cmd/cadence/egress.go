package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/usunrise88/cadence/control-plane/internal/egress"
)

// egressProxy runs the agent sandbox's allowlisting forward proxy (R4). Configuration: CADENCE_EGRESS_ALLOW (the
// static hosts, required: names or *.domain, comma or space separated), CADENCE_EGRESS_PORTS (443,80),
// CADENCE_EGRESS_ADDR (0.0.0.0:3128). With CADENCE_URL and CADENCE_EGRESS_TOKEN_FILE (the proxy's own cep_ token,
// written by the control plane) it also polls the control plane's egressHosts.list every CADENCE_EGRESS_REFRESH (15s)
// for the hosts of the configured agent providers (Settings → Agents). One JSON log line per tunnel and per refusal.
func egressProxy(ctx context.Context, getenv func(string) string) error {
	var ports []int
	for _, p := range strings.Split(getenv("CADENCE_EGRESS_PORTS"), ",") {
		if p = strings.TrimSpace(p); p == "" {
			continue
		}
		n, err := strconv.Atoi(p)
		if err != nil {
			return fmt.Errorf("CADENCE_EGRESS_PORTS: %q is not a port", p)
		}
		ports = append(ports, n)
	}
	allow, err := egress.ParseAllowlist(getenv("CADENCE_EGRESS_ALLOW"), ports)
	if err != nil {
		return fmt.Errorf("CADENCE_EGRESS_ALLOW: %w", err)
	}
	addr := getenv("CADENCE_EGRESS_ADDR")
	if addr == "" {
		addr = "0.0.0.0:3128"
	}
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	proxy := &egress.Proxy{Allow: allow, Log: log}
	if base, tokenFile := getenv("CADENCE_URL"), getenv("CADENCE_EGRESS_TOKEN_FILE"); base != "" && tokenFile != "" {
		every := 15 * time.Second
		if v := getenv("CADENCE_EGRESS_REFRESH"); v != "" {
			d, err := time.ParseDuration(v)
			if err != nil || d < time.Second {
				return fmt.Errorf("CADENCE_EGRESS_REFRESH: %q is not a duration of at least 1s", v)
			}
			every = d
		}
		proxy.Extra = &egress.Dynamic{}
		r := &egress.Refresher{BaseURL: base, TokenFile: tokenFile, Every: every, Ports: ports, Into: proxy.Extra, Log: log}
		go r.Run(ctx)
	} else {
		log.Warn("CADENCE_URL or CADENCE_EGRESS_TOKEN_FILE is not set: only the static allowlist applies")
	}
	srv := &http.Server{Addr: addr, Handler: proxy, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()
	log.Info("cadence egress proxy started", "addr", addr, "allow", getenv("CADENCE_EGRESS_ALLOW"))
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
