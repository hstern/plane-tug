// Copyright 2026 The plane-tug Authors
// SPDX-License-Identifier: Apache-2.0

package planeauth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNewRejectsBadURL(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		url  string
	}{
		{"empty", ""},
		{"relative", "/api/users"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, err := New(tc.url); err == nil {
				t.Fatalf("New(%q) succeeded, want error", tc.url)
			}
		})
	}
}

func TestVerifyForwardsCookies(t *testing.T) {
	t.Parallel()
	var gotCookies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/users/me/" {
			t.Errorf("path = %q, want /api/users/me/", r.URL.Path)
		}
		for _, c := range r.Cookies() {
			gotCookies = append(gotCookies, c.Name+"="+c.Value)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	v, err := New(srv.URL)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/events?project=p", nil)
	req.AddCookie(&http.Cookie{Name: "session", Value: "abc"})
	req.AddCookie(&http.Cookie{Name: "csrftoken", Value: "xyz"})

	if err := v.Verify(context.Background(), req); err != nil {
		t.Fatalf("Verify: %v", err)
	}
	got := strings.Join(gotCookies, ",")
	if !strings.Contains(got, "session=abc") || !strings.Contains(got, "csrftoken=xyz") {
		t.Fatalf("upstream got cookies %q, want session+csrftoken", got)
	}
}

func TestVerifyNoCookiesIsUnauthorized(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("upstream should not be called when request has no cookies")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	v, _ := New(srv.URL)
	req := httptest.NewRequest(http.MethodGet, "/events", nil)
	if err := v.Verify(context.Background(), req); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("err = %v, want ErrUnauthorized", err)
	}
}

func TestVerifyStatusMapping(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		status  int
		wantErr error
	}{
		{"ok", http.StatusOK, nil},
		{"401", http.StatusUnauthorized, ErrUnauthorized},
		{"403", http.StatusForbidden, ErrUnauthorized},
		{"500", http.StatusInternalServerError, ErrUpstreamUnavailable},
		{"302", http.StatusFound, ErrUpstreamUnavailable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
			}))
			defer srv.Close()
			v, _ := New(srv.URL)
			req := httptest.NewRequest(http.MethodGet, "/events", nil)
			req.AddCookie(&http.Cookie{Name: "session", Value: "abc"})
			err := v.Verify(context.Background(), req)
			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("err = %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
		})
	}
}
