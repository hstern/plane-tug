// Copyright 2026 The plane-tug Authors
// SPDX-License-Identifier: Apache-2.0

package bridge

import (
	"errors"
	"testing"
)

func TestParseEvent(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		body    string
		want    Event
		wantErr error
	}{
		{
			name: "issue event with project",
			body: `{"event":"issue","action":"updated","data":{"project":"p-1","id":"i-1"}}`,
			want: Event{Event: "issue", Action: "updated", Project: "p-1"},
		},
		{
			name: "no project field",
			body: `{"event":"project","action":"created","data":{"id":"p-1"}}`,
			want: Event{Event: "project", Action: "created"},
		},
		{
			name: "data is an array (bulk)",
			body: `{"event":"cycle_issue","action":"added","data":[{"project":"p-1"}]}`,
			want: Event{Event: "cycle_issue", Action: "added"},
		},
		{
			name: "data absent",
			body: `{"event":"ping"}`,
			want: Event{Event: "ping"},
		},
		{
			name: "unknown extra fields ignored",
			body: `{"event":"issue","action":"x","data":{"project":"p","novel":42},"future":true}`,
			want: Event{Event: "issue", Action: "x", Project: "p"},
		},
		{name: "not json", body: `not json`, wantErr: ErrMalformedPayload},
		{name: "missing event", body: `{"action":"x"}`, wantErr: ErrMalformedPayload},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := ParseEvent([]byte(tc.body))
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v, want %v", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}
