// Copyright 2026 The plane-tug Authors
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

// Package e2e drives a real Plane CE instance through the plane-tug
// bridge and asserts pushed events arrive over SSE.
//
// Prerequisites (see ./README.md):
//
//	bash test/e2e-docker/run.sh up
//
// then
//
//	go test -tags e2e -count=1 -v ./test/e2e-docker/...
package e2e

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// seedInfo mirrors the JSON blob run.sh writes to .seed-info.json after
// a successful `up`. Fields are populated by the Django ORM seed.
type seedInfo struct {
	WorkspaceSlug     string `json:"workspace_slug"`
	ProjectID         string `json:"project_id"`
	ProjectIdentifier string `json:"project_identifier"`
	APIToken          string `json:"api_token"`
	WebhookSecret     string `json:"webhook_secret"`
	PlaneBaseURL      string `json:"plane_base_url"`
	PlaneTugBaseURL   string `json:"plane_tug_base_url"`
}

func loadSeedInfo(t *testing.T) seedInfo {
	t.Helper()
	path := filepath.Join("..", "..", "test", "e2e-docker", ".seed-info.json")
	// The test runs from test/e2e-docker/, so the seed file is next door.
	if cwd, err := os.Getwd(); err == nil {
		// Prefer the relative-to-test-file path; fall back to cwd-relative.
		candidates := []string{
			".seed-info.json",
			filepath.Join(cwd, ".seed-info.json"),
			path,
		}
		for _, p := range candidates {
			if data, err := os.ReadFile(p); err == nil {
				var s seedInfo
				if err := json.Unmarshal(data, &s); err != nil {
					t.Fatalf("parse seed info from %s: %v", p, err)
				}
				return s
			}
		}
	}
	t.Fatalf(".seed-info.json not found — run `bash test/e2e-docker/run.sh up` first")
	return seedInfo{}
}

// dialSSE opens a real SSE connection to plane-tug. Returns an io-decoded
// channel of SSE event payloads (data: lines, concatenated) and a cancel
// function the caller defers.
func dialSSE(t *testing.T, baseURL, projectID string) (<-chan string, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		baseURL+"/events?project="+projectID, nil)
	if err != nil {
		cancel()
		t.Fatalf("NewRequest: %v", err)
	}
	// fake-plane in compose 200s any path, so any cookie is accepted; the
	// presence of a cookie is enough to pass the bridge's "has cookies"
	// early-rejection check.
	req.AddCookie(&http.Cookie{Name: "session", Value: "e2e"})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		cancel()
		t.Fatalf("dial SSE: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		cancel()
		t.Fatalf("SSE status = %d, body = %q", resp.StatusCode, string(body))
	}

	ch := make(chan string, 16)
	go func() {
		defer close(ch)
		defer resp.Body.Close()
		br := bufio.NewReader(resp.Body)
		var dataLines []string
		for {
			line, err := br.ReadString('\n')
			if err != nil {
				return
			}
			line = strings.TrimRight(line, "\r\n")
			switch {
			case line == "":
				if len(dataLines) > 0 {
					select {
					case ch <- strings.Join(dataLines, "\n"):
					default:
					}
					dataLines = dataLines[:0]
				}
			case strings.HasPrefix(line, "data: "):
				dataLines = append(dataLines, strings.TrimPrefix(line, "data: "))
			case strings.HasPrefix(line, "data:"):
				dataLines = append(dataLines, strings.TrimPrefix(line, "data:"))
			}
		}
	}()
	return ch, cancel
}

// createIssue posts a new issue via Plane's REST API.
func createIssue(t *testing.T, info seedInfo, name string) string {
	t.Helper()
	url := fmt.Sprintf("%s/api/v1/workspaces/%s/projects/%s/issues/",
		info.PlaneBaseURL, info.WorkspaceSlug, info.ProjectID)
	payload := map[string]any{
		"name":             name,
		"description_html": "<p>e2e</p>",
	}
	body, _ := json.Marshal(payload)
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("build POST: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", info.APIToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST issue: %v", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		t.Fatalf("create issue status = %d, body = %s", resp.StatusCode, string(respBody))
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(respBody, &created); err != nil {
		t.Fatalf("parse create response: %v", err)
	}
	if created.ID == "" {
		t.Fatalf("create response missing id: %s", respBody)
	}
	return created.ID
}

// updateIssue patches an existing issue's name.
func updateIssue(t *testing.T, info seedInfo, issueID, name string) {
	t.Helper()
	url := fmt.Sprintf("%s/api/v1/workspaces/%s/projects/%s/issues/%s/",
		info.PlaneBaseURL, info.WorkspaceSlug, info.ProjectID, issueID)
	body, _ := json.Marshal(map[string]any{"name": name})
	req, err := http.NewRequest(http.MethodPatch, url, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("build PATCH: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", info.APIToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PATCH issue: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(resp.Body)
		t.Fatalf("patch issue status = %d, body = %s", resp.StatusCode, string(respBody))
	}
}

// waitForEvent reads from ch until an SSE payload matching predicate is
// seen, or the deadline expires.
func waitForEvent(t *testing.T, ch <-chan string, want func(string) bool, d time.Duration) string {
	t.Helper()
	deadline := time.After(d)
	for {
		select {
		case payload, ok := <-ch:
			if !ok {
				t.Fatal("SSE channel closed before match")
			}
			if want(payload) {
				return payload
			}
		case <-deadline:
			t.Fatalf("no matching SSE event within %s", d)
			return ""
		}
	}
}

// --- Tests -------------------------------------------------------------------

func TestE2E_IssueCreateFiresSSE(t *testing.T) {
	info := loadSeedInfo(t)

	events, cancel := dialSSE(t, info.PlaneTugBaseURL, info.ProjectID)
	defer cancel()

	// Plane's webhook worker fires on commit, then plane-tug fans out.
	// The "issue" event family covers create/update/delete.
	issueID := createIssue(t, info, "e2e create "+time.Now().Format(time.RFC3339Nano))

	payload := waitForEvent(t, events, func(p string) bool {
		// Plane payload: {"event":"issue","action":"created",
		//                 "data":{"project":"<uuid>","id":"<uuid>",…}}
		// plane-tug projects this to {"event":"issue","action":"created",
		// "project":"<uuid>"} — Project must match info.ProjectID.
		return strings.Contains(p, `"event":"issue"`) &&
			strings.Contains(p, `"project":"`+info.ProjectID+`"`)
	}, 15*time.Second)
	t.Logf("create event payload: %s", payload)

	// Update the issue we just created and confirm the update event
	// also arrives.
	updateIssue(t, info, issueID, "e2e renamed "+time.Now().Format(time.RFC3339Nano))
	payload = waitForEvent(t, events, func(p string) bool {
		return strings.Contains(p, `"event":"issue"`) &&
			strings.Contains(p, `"action":"updated"`) &&
			strings.Contains(p, `"project":"`+info.ProjectID+`"`)
	}, 15*time.Second)
	t.Logf("update event payload: %s", payload)
}

func TestE2E_HealthzReachable(t *testing.T) {
	info := loadSeedInfo(t)
	resp, err := http.Get(info.PlaneTugBaseURL + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
}
