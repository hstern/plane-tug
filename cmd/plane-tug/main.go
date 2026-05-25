// Copyright 2026 The plane-tug Authors
// SPDX-License-Identifier: Apache-2.0

// Command plane-tug is a webhook→SSE bridge for Plane workspaces.
//
// It accepts HMAC-signed webhook deliveries from a Plane instance on
// POST /webhook, fans them out to long-lived browser SSE connections on
// GET /events?project=<uuid>, and exposes a liveness probe at
// GET /healthz. The bridge is stateless and is intended to run behind a
// reverse proxy that terminates TLS.
//
// Configuration is by environment variable; flags are reserved for
// process-level controls (--version, --help). The bridge fails fast on
// missing or malformed config rather than running in a half-working state.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/hstern/plane-tug/internal/bridge"
	"github.com/hstern/plane-tug/internal/planeauth"
)

// version is the build version, injected via -ldflags="-X main.version=...".
var version = "dev"

const (
	envWebhookSecret = "PLANE_TUG_WEBHOOK_SECRET"
	envPlaneBaseURL  = "PLANE_TUG_PLANE_BASE_URL"
	envListen        = "PLANE_TUG_LISTEN"
	envLogLevel      = "PLANE_TUG_LOG_LEVEL"

	defaultListen = ":8080"
)

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "fatal:", err)
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr *os.File) error {
	fs := flag.NewFlagSet("plane-tug", flag.ContinueOnError)
	fs.SetOutput(stderr)
	printVersion := fs.Bool("version", false, "print version and exit")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *printVersion {
		_, _ = fmt.Fprintln(stdout, version)
		return nil
	}

	secret := os.Getenv(envWebhookSecret)
	if secret == "" {
		return fmt.Errorf("%s is required", envWebhookSecret)
	}
	planeBase := os.Getenv(envPlaneBaseURL)
	if planeBase == "" {
		return fmt.Errorf("%s is required", envPlaneBaseURL)
	}
	listen := os.Getenv(envListen)
	if listen == "" {
		listen = defaultListen
	}

	logger := newLogger(os.Getenv(envLogLevel), stdout)

	verifier, err := planeauth.New(planeBase)
	if err != nil {
		return fmt.Errorf("configure session verifier: %w", err)
	}

	hub := bridge.NewHub()
	srv := bridge.NewServer(secret, hub, verifier, bridge.ServerOptions{Logger: logger})

	httpSrv := &http.Server{
		Addr:              listen,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		// WriteTimeout is intentionally zero: SSE connections are
		// long-lived and writing per-event must not race a timeout.
		IdleTimeout: 120 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	logger.LogAttrs(ctx, slog.LevelInfo, "starting plane-tug",
		slog.String("version", version),
		slog.String("listen", listen),
		slog.String("plane_base_url", planeBase),
	)

	errCh := make(chan error, 1)
	go func() {
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := httpSrv.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("shutdown: %w", err)
		}
		logger.LogAttrs(context.Background(), slog.LevelInfo, "shutdown complete")
		return <-errCh
	case err := <-errCh:
		return err
	}
}

func newLogger(level string, w *os.File) *slog.Logger {
	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(strings.ToLower(level))); err != nil {
		lvl = slog.LevelInfo
	}
	return slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{Level: lvl}))
}
