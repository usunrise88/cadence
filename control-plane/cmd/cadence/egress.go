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
// hosts, required: names or *.domain, comma or space separated), CADENCE_EGRESS_PORTS (443,80), CADENCE_EGRESS_ADDR
// (0.0.0.0:3128). One JSON log line per tunnel and per refusal.
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
	srv := &http.Server{Addr: addr, Handler: &egress.Proxy{Allow: allow, Log: log}, ReadHeaderTimeout: 10 * time.Second}
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
