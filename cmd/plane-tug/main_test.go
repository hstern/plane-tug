// Copyright 2026 The plane-tug Authors
// SPDX-License-Identifier: Apache-2.0

package main_test

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/hstern/plane-tug/internal/bridge"
)

// buildBinary compiles ./cmd/plane-tug into a temp dir and returns the
// absolute path to the binary. The build is cached per `go test` run by
// putting the output under t.TempDir() — fresh per test invocation but
// reusable across subtests via t.Helper-style sharing if needed.
func buildBinary(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("binary test relies on POSIX signals")
	}
	bin := filepath.Join(t.TempDir(), "plane-tug")
	cmd := exec.Command("go", "build", "-o", bin, ".")
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("go build: %v", err)
	}
	return bin
}

// startBinary spawns the binary with the given env, listening on a free
// port. It waits for /healthz to return 200 before returning. The
// returned cleanup sends SIGTERM and waits for exit.
func startBinary(t *testing.T, bin string, env map[string]string) (baseURL string, stop func()) {
	t.Helper()

	// Pick an ephemeral port and close the listener so the child can bind it.
	// There's a small race window (another process could grab it between
	// close and bind) but it's vanishingly small on a test host.
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := lis.Addr().String()
	_ = lis.Close()

	cmd := exec.Command(bin)
	cmd.Env = os.Environ()
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	cmd.Env = append(cmd.Env, "PLANE_TUG_LISTEN="+addr)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	cmd.Stdout = io.Discard
	// Process group so we can signal the whole tree if needed.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}

	baseURL = "http://" + addr

	// Wait for the server to come up. ~2s should be plenty for a single
	// statically-linked Go binary.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(baseURL + "/healthz")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				goto ready
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	_ = cmd.Process.Kill()
	t.Fatalf("binary never became healthy; stderr:\n%s", stderr.String())

ready:
	stop = func() {
		_ = cmd.Process.Signal(syscall.SIGTERM)
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		select {
		case err := <-done:
			if err != nil {
				var ee *exec.ExitError
				if !errors.As(err, &ee) {
					t.Logf("wait: %v", err)
				}
			}
		case <-time.After(5 * time.Second):
			_ = cmd.Process.Kill()
			<-done
			t.Errorf("binary did not exit on SIGTERM; stderr:\n%s", stderr.String())
		}
	}
	t.Cleanup(stop)
	return baseURL, stop
}

func TestBinary_VersionFlag(t *testing.T) {
	t.Parallel()
	bin := buildBinary(t)
	var out bytes.Buffer
	cmd := exec.Command(bin, "--version")
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("run --version: %v", err)
	}
	if v := strings.TrimSpace(out.String()); v == "" {
		t.Fatal("--version printed nothing")
	}
}

func TestBinary_MissingSecretIsFatal(t *testing.T) {
	t.Parallel()
	bin := buildBinary(t)
	cmd := exec.Command(bin)
	cmd.Env = append(os.Environ(), "PLANE_TUG_PLANE_BASE_URL=http://example.test")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err == nil {
		t.Fatal("expected non-zero exit without PLANE_TUG_WEBHOOK_SECRET")
	}
	if !strings.Contains(stderr.String(), "PLANE_TUG_WEBHOOK_SECRET") {
		t.Fatalf("stderr did not mention the missing var:\n%s", stderr.String())
	}
}

func TestBinary_MissingPlaneBaseIsFatal(t *testing.T) {
	t.Parallel()
	bin := buildBinary(t)
	cmd := exec.Command(bin)
	cmd.Env = append(os.Environ(), "PLANE_TUG_WEBHOOK_SECRET=s")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err == nil {
		t.Fatal("expected non-zero exit without PLANE_TUG_PLANE_BASE_URL")
	}
	if !strings.Contains(stderr.String(), "PLANE_TUG_PLANE_BASE_URL") {
		t.Fatalf("stderr did not mention the missing var:\n%s", stderr.String())
	}
}

func TestBinary_HealthyAndSigterm(t *testing.T) {
	t.Parallel()
	bin := buildBinary(t)
	plane := startFakePlane(t, true)
	baseURL, _ := startBinary(t, bin, map[string]string{
		"PLANE_TUG_WEBHOOK_SECRET": "secret",
		"PLANE_TUG_PLANE_BASE_URL": plane.URL,
		"PLANE_TUG_LOG_LEVEL":      "error",
	})
	resp, err := http.Get(baseURL + "/healthz")
	if err != nil {
		t.Fatalf("healthz: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	// The deferred SIGTERM via t.Cleanup is the actual assertion that
	// the binary shuts down cleanly within 5s.
}

func TestBinary_EndToEnd(t *testing.T) {
	t.Parallel()
	bin := buildBinary(t)
	plane := startFakePlane(t, true)
	const secret = "wire-secret"
	baseURL, _ := startBinary(t, bin, map[string]string{
		"PLANE_TUG_WEBHOOK_SECRET": secret,
		"PLANE_TUG_PLANE_BASE_URL": plane.URL,
		"PLANE_TUG_LOG_LEVEL":      "error",
	})

	// Open a real SSE connection to the real binary.
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet,
		baseURL+"/events?project=p-1", nil)
	req.AddCookie(&http.Cookie{Name: "session", Value: "s"})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("SSE dial: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("SSE status = %d, body = %q", resp.StatusCode, string(body))
	}
	br := bufio.NewReader(resp.Body)

	// Fire a webhook with a valid HMAC.
	body := []byte(`{"event":"issue","action":"updated","data":{"project":"p-1","id":"i-9"}}`)
	post, _ := http.NewRequest(http.MethodPost, baseURL+"/webhook",
		bytes.NewReader(body))
	post.Header.Set(bridge.HeaderSignature, bridge.Sign(secret, body))
	pr, err := http.DefaultClient.Do(post)
	if err != nil {
		t.Fatalf("POST /webhook: %v", err)
	}
	pr.Body.Close()
	if pr.StatusCode != http.StatusNoContent {
		t.Fatalf("webhook status = %d, want 204", pr.StatusCode)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		line, err := br.ReadString('\n')
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if strings.HasPrefix(line, "data: ") &&
			strings.Contains(line, `"event":"issue"`) &&
			strings.Contains(line, `"project":"p-1"`) {
			return
		}
	}
	t.Fatal("did not receive expected event before deadline")
}

// startFakePlane is a tiny duplicate of the integration_test.go fake —
// the binary test is in package main_test (not bridge_test), so the
// helper is not shared. accept governs whether the fake answers 200 or
// 401 on /api/users/me/.
type fakePlaneSrv struct {
	*httptest.Server
}

func startFakePlane(t *testing.T, accept bool) *fakePlaneSrv {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/users/me/" {
			http.NotFound(w, r)
			return
		}
		if !accept {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"u-1"}`))
	}))
	t.Cleanup(s.Close)
	return &fakePlaneSrv{Server: s}
}
