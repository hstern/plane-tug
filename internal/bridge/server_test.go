// Copyright 2026 The plane-tug Authors
// SPDX-License-Identifier: Apache-2.0

package bridge

import (
	"bufio"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// fakeVerifier is a stub SessionVerifier whose return value is controlled
// per-test via the err field. The Verify method records call count.
type fakeVerifier struct {
	err   error
	calls atomic.Int32
}

func (f *fakeVerifier) Verify(_ context.Context, _ *http.Request) error {
	f.calls.Add(1)
	return f.err
}

func newTestServer(t *testing.T, verifier SessionVerifier) (*Server, *httptest.Server) {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	hub := NewHub()
	s := NewServer("secret", hub, verifier, ServerOptions{
		Keepalive: 50 * time.Millisecond,
		Logger:    logger,
	})
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return s, ts
}

func TestHealthz(t *testing.T) {
	t.Parallel()
	_, ts := newTestServer(t, &fakeVerifier{})
	resp, err := http.Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
}

func TestWebhookValidSignatureBroadcasts(t *testing.T) {
	t.Parallel()
	srv, ts := newTestServer(t, &fakeVerifier{})

	sub, unsub := srv.hub.Subscribe("p-1")
	defer unsub()

	body := []byte(`{"event":"issue","action":"updated","data":{"project":"p-1"}}`)
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/webhook", strings.NewReader(string(body)))
	req.Header.Set(HeaderSignature, Sign("secret", body))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /webhook: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", resp.StatusCode)
	}

	select {
	case evt := <-sub.Events:
		if evt.Event != "issue" || evt.Project != "p-1" {
			t.Fatalf("got %+v", evt)
		}
	case <-time.After(time.Second):
		t.Fatal("subscriber did not receive event")
	}
}

func TestWebhookBadSignatureReturns401(t *testing.T) {
	t.Parallel()
	_, ts := newTestServer(t, &fakeVerifier{})
	body := []byte(`{"event":"x","data":{}}`)
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/webhook", strings.NewReader(string(body)))
	req.Header.Set(HeaderSignature, Sign("wrong-secret", body))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
}

func TestWebhookMalformedBodyReturns400(t *testing.T) {
	t.Parallel()
	_, ts := newTestServer(t, &fakeVerifier{})
	body := []byte(`not json`)
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/webhook", strings.NewReader(string(body)))
	req.Header.Set(HeaderSignature, Sign("secret", body))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

func TestEventsMissingProjectReturns400(t *testing.T) {
	t.Parallel()
	_, ts := newTestServer(t, &fakeVerifier{})
	resp, err := http.Get(ts.URL + "/events")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

func TestEventsUnauthorizedReturns401(t *testing.T) {
	t.Parallel()
	_, ts := newTestServer(t, &fakeVerifier{err: ErrSessionUnauthorized})
	resp, err := http.Get(ts.URL + "/events?project=p-1")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
}

func TestEventsUpstreamFailReturns502(t *testing.T) {
	t.Parallel()
	_, ts := newTestServer(t, &fakeVerifier{err: ErrSessionUpstream})
	resp, err := http.Get(ts.URL + "/events?project=p-1")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", resp.StatusCode)
	}
}

func TestEventsStreamsBroadcasts(t *testing.T) {
	t.Parallel()
	srv, ts := newTestServer(t, &fakeVerifier{})

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/events?project=p-1", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /events: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("Content-Type = %q, want text/event-stream", got)
	}

	br := bufio.NewReader(resp.Body)
	// Drain the :connected\n\n preamble.
	if _, err := br.ReadString('\n'); err != nil {
		t.Fatalf("read preamble: %v", err)
	}
	if _, err := br.ReadString('\n'); err != nil {
		t.Fatalf("read preamble blank: %v", err)
	}

	// Broadcast must reach the open stream. Give the subscribe call a
	// moment to register, then wait until the broadcast definitely fans
	// out (poll on subscriber count).
	deadline := time.Now().Add(time.Second)
	for srv.hub.SubscriberCount("p-1") == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	srv.hub.Broadcast(Event{Event: "issue", Action: "updated", Project: "p-1"})

	var saw string
	for time.Now().Before(deadline) {
		line, err := br.ReadString('\n')
		if err != nil {
			t.Fatalf("read line: %v", err)
		}
		if strings.HasPrefix(line, "data:") {
			saw = line
			break
		}
	}
	if !strings.Contains(saw, `"event":"issue"`) || !strings.Contains(saw, `"project":"p-1"`) {
		t.Fatalf("did not see expected event payload, got %q", saw)
	}
}

func TestEventsKeepaliveOnIdle(t *testing.T) {
	t.Parallel()
	_, ts := newTestServer(t, &fakeVerifier{})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/events?project=p-2", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /events: %v", err)
	}
	defer resp.Body.Close()
	br := bufio.NewReader(resp.Body)
	// Read until we see at least one :keepalive comment frame.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		line, err := br.ReadString('\n')
		if err != nil {
			t.Fatalf("read line: %v", err)
		}
		if strings.HasPrefix(line, ":keepalive") {
			return
		}
	}
	t.Fatal("did not see a :keepalive frame within deadline")
}
