// Copyright 2026 The plane-tug Authors
// SPDX-License-Identifier: Apache-2.0

// Package planeauth verifies a browser request carries a valid Plane
// session by replaying its cookies against Plane's own /api/users/me/
// endpoint. The bridge does not parse, validate, or store the session
// itself — it leans on Plane to be the single source of truth for who
// is logged in.
package planeauth

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/hstern/plane-tug/internal/bridge"
)

// ErrUnauthorized aliases bridge.ErrSessionUnauthorized so callers can
// match on the planeauth name and the bridge's switch on its own.
var ErrUnauthorized = bridge.ErrSessionUnauthorized

// ErrUpstreamUnavailable aliases bridge.ErrSessionUpstream for the same
// reason.
var ErrUpstreamUnavailable = bridge.ErrSessionUpstream

// Verifier checks whether a request's cookies grant access to Plane.
type Verifier struct {
	baseURL    *url.URL
	httpClient *http.Client
	mePath     string // override for tests
}

// New returns a Verifier configured against baseURL (e.g. "http://plane-web:3000")
// using a 5s-timeout HTTP client. baseURL must be a non-empty absolute URL.
func New(baseURL string) (*Verifier, error) {
	if baseURL == "" {
		return nil, errors.New("plane base URL is required")
	}
	u, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("parse plane base URL: %w", err)
	}
	if !u.IsAbs() {
		return nil, fmt.Errorf("plane base URL must be absolute, got %q", baseURL)
	}
	return &Verifier{
		baseURL:    u,
		httpClient: &http.Client{Timeout: 5 * time.Second},
		mePath:     "/api/users/me/",
	}, nil
}

// Verify makes a GET to <baseURL>/api/users/me/ carrying every cookie the
// inbound request sent. Returns nil if Plane responds 200, ErrUnauthorized
// (== bridge.ErrSessionUnauthorized) if Plane responds 401/403, or
// ErrUpstreamUnavailable (== bridge.ErrSessionUpstream) for anything else
// (network error, 5xx, malformed response).
//
// The returned error always errors.Is the appropriate sentinel, so the
// bridge server's status-code switch works without further coupling.
func (v *Verifier) Verify(ctx context.Context, r *http.Request) error {
	cookies := r.Cookies()
	if len(cookies) == 0 {
		return ErrUnauthorized
	}
	meURL := *v.baseURL
	meURL.Path = v.mePath
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, meURL.String(), nil)
	if err != nil {
		return fmt.Errorf("%w: build request: %w", ErrUpstreamUnavailable, err)
	}
	for _, c := range cookies {
		req.AddCookie(c)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := v.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrUpstreamUnavailable, err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()
	switch resp.StatusCode {
	case http.StatusOK:
		return nil
	case http.StatusUnauthorized, http.StatusForbidden:
		return ErrUnauthorized
	default:
		return fmt.Errorf("%w: upstream status %d", ErrUpstreamUnavailable, resp.StatusCode)
	}
}

// Compile-time check: *Verifier implements bridge.SessionVerifier.
var _ bridge.SessionVerifier = (*Verifier)(nil)
