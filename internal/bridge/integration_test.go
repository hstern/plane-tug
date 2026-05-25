// Copyright 2026 The plane-tug Authors
// SPDX-License-Identifier: Apache-2.0

package bridge_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hstern/plane-tug/internal/bridge"
	"github.com/hstern/plane-tug/internal/planeauth"
)

// integrationServer wires the bridge against a real http.Server on a
// localhost port (not httptest's in-process transport). The returned URL
// is reachable by any net/http client.
type integrationServer struct {
	URL    string
	Secret string
	Hub    *bridge.Hub
	stop   func()
}

func startIntegrationServer(t *testing.T, planeBaseURL string) *integrationServer {
	t.Helper()
	const secret = "integration-secret"
	verifier, err := planeauth.New(planeBaseURL)
	if err != nil {
		t.Fatalf("planeauth.New: %v", err)
	}
	hub := bridge.NewHub()
	srv := bridge.NewServer(secret, hub, verifier, bridge.ServerOptions{
		Keepalive: 100 * time.Millisecond,
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
	})

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	httpSrv := &http.Server{
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		if err := httpSrv.Serve(lis); err != nil && !errors.Is(err, http.ErrServerClosed) {
			t.Logf("Serve: %v", err)
		}
	}()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(ctx)
	})
	return &integrationServer{
		URL:    "http://" + lis.Addr().String(),
		Secret: secret,
		Hub:    hub,
		// stop force-closes active connections (Server.Close, not
		// Shutdown): the restart test needs the SSE stream to break
		// immediately, the same way a container SIGKILL would. Production
		// uses Shutdown for graceful drain.
		stop: func() { _ = httpSrv.Close() },
	}
}

// fakePlane returns an httptest.Server pretending to be Plane's
// /api/users/me/. accept controls whether the next call is authorized.
type fakePlane struct {
	srv    *httptest.Server
	accept atomic.Bool
}

func startFakePlane(t *testing.T) *fakePlane {
	t.Helper()
	fp := &fakePlane{}
	fp.accept.Store(true)
	fp.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/users/me/" {
			http.NotFound(w, r)
			return
		}
		if !fp.accept.Load() {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		// Plane returns a JSON user object on 200; the bridge only checks
		// the status code so an empty 200 body suffices.
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"u-1","email":"u@example.test"}`))
	}))
	t.Cleanup(fp.srv.Close)
	return fp
}

// sseClient connects to GET /events?project=… on the bridge with a
// pre-seeded session cookie, parses the SSE wire format, and exposes a
// channel of decoded Event values plus a counter of keepalive comments.
type sseClient struct {
	resp       *http.Response
	cancel     context.CancelFunc
	Events     chan bridge.Event
	Keepalives atomic.Int32
	readErr    chan error
}

func dialSSE(t *testing.T, baseURL, project string) *sseClient {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		baseURL+"/events?project="+project, nil)
	if err != nil {
		cancel()
		t.Fatalf("NewRequest: %v", err)
	}
	req.AddCookie(&http.Cookie{Name: "session", Value: "test-session"})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		cancel()
		t.Fatalf("Do: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		cancel()
		t.Fatalf("status = %d, body = %q", resp.StatusCode, string(body))
	}
	c := &sseClient{
		resp:    resp,
		cancel:  cancel,
		Events:  make(chan bridge.Event, 32),
		readErr: make(chan error, 1),
	}
	go c.read()
	t.Cleanup(c.Close)
	return c
}

// read parses the SSE frames. Each frame is one or more lines terminated
// by a blank line; comment lines start with ':'. We track event/data and
// emit a decoded bridge.Event when a frame ends.
func (c *sseClient) read() {
	defer close(c.Events)
	br := bufio.NewReader(c.resp.Body)
	var dataLines []string
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			c.readErr <- err
			return
		}
		line = strings.TrimRight(line, "\r\n")
		switch {
		case line == "":
			// Frame terminator.
			if len(dataLines) > 0 {
				payload := strings.Join(dataLines, "\n")
				var evt bridge.Event
				if json.Unmarshal([]byte(payload), &evt) == nil {
					c.Events <- evt
				}
				dataLines = dataLines[:0]
			}
		case strings.HasPrefix(line, ":"):
			if strings.HasPrefix(line, ":keepalive") {
				c.Keepalives.Add(1)
			}
		case strings.HasPrefix(line, "data: "):
			dataLines = append(dataLines, strings.TrimPrefix(line, "data: "))
		case strings.HasPrefix(line, "data:"):
			dataLines = append(dataLines, strings.TrimPrefix(line, "data:"))
			// "event: …" frames are dropped: the bridge sends
			// "event: plane" but bridge.Event is identified by JSON
			// structure on the data line, not by the named-event header.
		}
	}
}

func (c *sseClient) Close() {
	c.cancel()
	_ = c.resp.Body.Close()
}

// post sends a webhook delivery with a valid HMAC signature and returns
// the response status. The body is drained and closed before return —
// /webhook returns 204 with no body, so callers never need it.
func post(t *testing.T, srv *integrationServer, body []byte) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/webhook", strings.NewReader(string(body)))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set(bridge.HeaderSignature, bridge.Sign(srv.Secret, body))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /webhook: %v", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}

func waitForSubscribers(t *testing.T, hub *bridge.Hub, project string, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if hub.SubscriberCount(project) >= want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("subscriber count for %q never reached %d (got %d)", project, want, hub.SubscriberCount(project))
}

func recvEvent(t *testing.T, c *sseClient, d time.Duration) bridge.Event {
	t.Helper()
	select {
	case evt, ok := <-c.Events:
		if !ok {
			t.Fatal("event channel closed unexpectedly")
		}
		return evt
	case <-time.After(d):
		t.Fatalf("no event within %s", d)
		return bridge.Event{}
	}
}

// --- Tests -------------------------------------------------------------------

func TestIntegration_EndToEndDelivery(t *testing.T) {
	t.Parallel()
	plane := startFakePlane(t)
	srv := startIntegrationServer(t, plane.srv.URL)

	c := dialSSE(t, srv.URL, "p-1")
	waitForSubscribers(t, srv.Hub, "p-1", 1)

	body := []byte(`{"event":"issue","action":"updated","data":{"project":"p-1","id":"i-1"}}`)
	if got := post(t, srv, body); got != http.StatusNoContent {
		t.Fatalf("webhook status = %d, want 204", got)
	}

	got := recvEvent(t, c, 2*time.Second)
	if got.Event != "issue" || got.Action != "updated" || got.Project != "p-1" {
		t.Fatalf("got %+v", got)
	}
}

func TestIntegration_PerProjectFanout(t *testing.T) {
	t.Parallel()
	plane := startFakePlane(t)
	srv := startIntegrationServer(t, plane.srv.URL)

	a := dialSSE(t, srv.URL, "p-A")
	b := dialSSE(t, srv.URL, "p-B")
	waitForSubscribers(t, srv.Hub, "p-A", 1)
	waitForSubscribers(t, srv.Hub, "p-B", 1)

	post(t, srv, []byte(`{"event":"issue","action":"updated","data":{"project":"p-A"}}`))
	if got := recvEvent(t, a, 2*time.Second); got.Project != "p-A" {
		t.Fatalf("subscriber A got %+v", got)
	}
	select {
	case got := <-b.Events:
		t.Fatalf("subscriber B unexpectedly received %+v", got)
	case <-time.After(200 * time.Millisecond):
	}
}

func TestIntegration_WorkspaceEventFansToAll(t *testing.T) {
	t.Parallel()
	plane := startFakePlane(t)
	srv := startIntegrationServer(t, plane.srv.URL)

	a := dialSSE(t, srv.URL, "p-A")
	b := dialSSE(t, srv.URL, "p-B")
	waitForSubscribers(t, srv.Hub, "p-A", 1)
	waitForSubscribers(t, srv.Hub, "p-B", 1)

	// project event without a data.project (a project-creation payload
	// where data is the project object itself) — bridge passes it through
	// with empty Project, fans to everyone.
	post(t, srv, []byte(`{"event":"project","action":"created","data":{"id":"p-NEW"}}`))

	for _, c := range []*sseClient{a, b} {
		got := recvEvent(t, c, 2*time.Second)
		if got.Event != "project" || got.Action != "created" || got.Project != "" {
			t.Fatalf("got %+v", got)
		}
	}
}

func TestIntegration_KeepaliveOnIdle(t *testing.T) {
	t.Parallel()
	plane := startFakePlane(t)
	srv := startIntegrationServer(t, plane.srv.URL)

	c := dialSSE(t, srv.URL, "p-idle")
	waitForSubscribers(t, srv.Hub, "p-idle", 1)

	// Server keepalive is 100ms in tests; wait long enough to see several.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if c.Keepalives.Load() >= 3 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("saw only %d keepalive frames in 1s", c.Keepalives.Load())
}

func TestIntegration_UnauthorizedSessionRejected(t *testing.T) {
	t.Parallel()
	plane := startFakePlane(t)
	plane.accept.Store(false)
	srv := startIntegrationServer(t, plane.srv.URL)

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/events?project=p", nil)
	req.AddCookie(&http.Cookie{Name: "session", Value: "x"})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
}

func TestIntegration_ReconnectAfterBridgeRestart(t *testing.T) {
	t.Parallel()
	plane := startFakePlane(t)
	srv := startIntegrationServer(t, plane.srv.URL)

	c := dialSSE(t, srv.URL, "p-1")
	waitForSubscribers(t, srv.Hub, "p-1", 1)

	// Tear down the bridge — simulates a container restart.
	srv.stop()
	select {
	case err := <-c.readErr:
		if err == nil {
			t.Fatal("expected stream read to error after shutdown")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("stream did not close after server shutdown")
	}

	// A fresh bridge instance on a fresh port; a real EventSource would
	// reconnect to the same URL via DNS, but for the test we redial.
	srv2 := startIntegrationServer(t, plane.srv.URL)
	c2 := dialSSE(t, srv2.URL, "p-1")
	waitForSubscribers(t, srv2.Hub, "p-1", 1)
	post(t, srv2, []byte(`{"event":"issue","action":"updated","data":{"project":"p-1"}}`))
	if got := recvEvent(t, c2, 2*time.Second); got.Project != "p-1" {
		t.Fatalf("after reconnect got %+v", got)
	}
}

func TestIntegration_ManyConcurrentSubscribers(t *testing.T) {
	t.Parallel()
	plane := startFakePlane(t)
	srv := startIntegrationServer(t, plane.srv.URL)

	const N = 20
	clients := make([]*sseClient, N)
	for i := range clients {
		clients[i] = dialSSE(t, srv.URL, "p-1")
	}
	waitForSubscribers(t, srv.Hub, "p-1", N)

	body := []byte(`{"event":"issue","action":"updated","data":{"project":"p-1"}}`)
	post(t, srv, body)

	var wg sync.WaitGroup
	for _, c := range clients {
		wg.Go(func() {
			got := recvEvent(t, c, 3*time.Second)
			if got.Project != "p-1" {
				t.Errorf("got %+v", got)
			}
		})
	}
	wg.Wait()
}

func TestIntegration_HealthzReachable(t *testing.T) {
	t.Parallel()
	plane := startFakePlane(t)
	srv := startIntegrationServer(t, plane.srv.URL)

	resp, err := http.Get(srv.URL + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.HasPrefix(string(body), "ok") {
		t.Fatalf("body = %q", string(body))
	}
}

// TestIntegration_TamperedWebhookRejected complements the unit test by
// hitting the live HTTP server: an attacker who knows the event shape but
// not the secret cannot inject a fake event into the stream.
func TestIntegration_TamperedWebhookRejected(t *testing.T) {
	t.Parallel()
	plane := startFakePlane(t)
	srv := startIntegrationServer(t, plane.srv.URL)

	c := dialSSE(t, srv.URL, "p-1")
	waitForSubscribers(t, srv.Hub, "p-1", 1)

	body := []byte(`{"event":"issue","action":"updated","data":{"project":"p-1"}}`)
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/webhook", strings.NewReader(string(body)))
	req.Header.Set(bridge.HeaderSignature, bridge.Sign("wrong-secret", body))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}

	select {
	case evt := <-c.Events:
		t.Fatalf("forged webhook reached subscriber as %+v", evt)
	case <-time.After(200 * time.Millisecond):
	}
}
