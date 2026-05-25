// Copyright 2026 The plane-tug Authors
// SPDX-License-Identifier: Apache-2.0

package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"
)

// maxWebhookBody caps inbound webhook bodies. Plane's payloads are well under
// this in practice — the cap is defence-in-depth against a misconfigured
// proxy fanning a giant body at the handler.
const maxWebhookBody = 1 << 20 // 1 MiB

// DefaultKeepalive is the interval between SSE comment frames sent on idle
// streams to keep intermediate proxies from closing the connection.
const DefaultKeepalive = 15 * time.Second

// SessionVerifier confirms an inbound request is from a logged-in browser.
// The bridge does not parse or store the session; it delegates entirely to
// the implementation (typically calling Plane's /api/users/me/).
type SessionVerifier interface {
	Verify(ctx context.Context, r *http.Request) error
}

// SessionVerifier implementations signal failures via these sentinels so
// the server can map them to HTTP status codes. Implementations that
// return some other error are mapped to 502 by default.
var (
	// ErrSessionUnauthorized → HTTP 401. The session is missing or rejected
	// by the upstream.
	ErrSessionUnauthorized = errors.New("session unauthorized")
	// ErrSessionUpstream → HTTP 502. The upstream session check could not
	// complete (network error, 5xx, malformed response).
	ErrSessionUpstream = errors.New("session upstream unavailable")
)

// ServerOptions configures a Server. The zero value uses sensible defaults.
type ServerOptions struct {
	// Keepalive is the SSE idle-comment interval. Defaults to DefaultKeepalive.
	Keepalive time.Duration
	// Logger receives structured operational logs. Defaults to slog.Default().
	Logger *slog.Logger
}

// Server is the HTTP front-end of the bridge. It owns the Hub and a
// SessionVerifier and exposes the two operational endpoints plus a
// liveness probe.
type Server struct {
	secret    string
	hub       *Hub
	verifier  SessionVerifier
	log       *slog.Logger
	keepalive time.Duration
}

// NewServer constructs a Server. secret is the webhook HMAC secret; verifier
// is consulted on every /events connect; opts may be the zero value.
func NewServer(secret string, hub *Hub, verifier SessionVerifier, opts ServerOptions) *Server {
	if opts.Keepalive <= 0 {
		opts.Keepalive = DefaultKeepalive
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	return &Server{
		secret:    secret,
		hub:       hub,
		verifier:  verifier,
		log:       opts.Logger,
		keepalive: opts.Keepalive,
	}
}

// Handler returns the *http.ServeMux wiring /webhook, /events and /healthz.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /webhook", s.handleWebhook)
	mux.HandleFunc("GET /events", s.handleEvents)
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	return mux
}

func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok\n"))
}

func (s *Server) handleWebhook(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxWebhookBody)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
			http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "failed to read request body", http.StatusBadRequest)
		return
	}
	if err := VerifySignature(s.secret, r.Header, body); err != nil {
		switch {
		case errors.Is(err, ErrMissingSignature), errors.Is(err, ErrInvalidSignature):
			s.log.LogAttrs(r.Context(), slog.LevelWarn, "webhook signature rejected",
				slog.String("err", err.Error()))
			http.Error(w, "invalid signature", http.StatusUnauthorized)
		case errors.Is(err, ErrEmptySecret):
			s.log.LogAttrs(r.Context(), slog.LevelError, "webhook secret not configured")
			http.Error(w, "bridge misconfigured", http.StatusInternalServerError)
		default:
			http.Error(w, "internal error", http.StatusInternalServerError)
		}
		return
	}
	evt, err := ParseEvent(body)
	if err != nil {
		s.log.LogAttrs(r.Context(), slog.LevelWarn, "malformed webhook payload",
			slog.String("err", err.Error()))
		http.Error(w, "malformed payload", http.StatusBadRequest)
		return
	}
	s.hub.Broadcast(evt)
	s.log.LogAttrs(r.Context(), slog.LevelDebug, "broadcast event",
		slog.String("event", evt.Event),
		slog.String("action", evt.Action),
		slog.String("project", evt.Project))
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	project := r.URL.Query().Get("project")
	if project == "" {
		http.Error(w, "missing project query parameter", http.StatusBadRequest)
		return
	}

	authCtx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	if err := s.verifier.Verify(authCtx, r); err != nil {
		cancel()
		switch {
		case errors.Is(err, ErrSessionUnauthorized):
			http.Error(w, "unauthorized", http.StatusUnauthorized)
		case errors.Is(err, ErrSessionUpstream):
			s.log.LogAttrs(r.Context(), slog.LevelWarn, "session upstream unavailable",
				slog.String("err", err.Error()))
			http.Error(w, "upstream unavailable", http.StatusBadGateway)
		default:
			s.log.LogAttrs(r.Context(), slog.LevelWarn, "session check failed",
				slog.String("err", err.Error()))
			http.Error(w, "upstream unavailable", http.StatusBadGateway)
		}
		return
	}
	cancel()

	header := w.Header()
	header.Set("Content-Type", "text/event-stream")
	header.Set("Cache-Control", "no-cache, no-store, must-revalidate")
	header.Set("Connection", "keep-alive")
	// nginx and other proxies honour X-Accel-Buffering: no; Caddy uses
	// `flush_interval -1` configured on the reverse_proxy directive.
	header.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	rc := http.NewResponseController(w)

	sub, unsub := s.hub.Subscribe(project)
	defer unsub()

	// Opening comment so EventSource fires `open` immediately even on an
	// idle project; some browsers wait for any byte before reporting open.
	if _, err := io.WriteString(w, ":connected\n\n"); err != nil {
		return
	}
	if err := rc.Flush(); err != nil {
		return
	}

	ticker := time.NewTicker(s.keepalive)
	defer ticker.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			if _, err := io.WriteString(w, ":keepalive\n\n"); err != nil {
				return
			}
			if err := rc.Flush(); err != nil {
				return
			}
		case evt, ok := <-sub.Events:
			if !ok {
				return
			}
			payload, err := json.Marshal(evt)
			if err != nil {
				continue
			}
			if _, err := fmt.Fprintf(w, "event: plane\ndata: %s\n\n", payload); err != nil {
				return
			}
			if err := rc.Flush(); err != nil {
				return
			}
		}
	}
}
