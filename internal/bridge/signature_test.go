// Copyright 2026 The plane-tug Authors
// SPDX-License-Identifier: Apache-2.0

package bridge

import (
	"errors"
	"net/http"
	"testing"
)

func TestVerifySignature(t *testing.T) {
	t.Parallel()

	const secret = "s3cret"
	body := []byte(`{"event":"issue","action":"created","data":{"project":"abc"}}`)
	good := Sign(secret, body)

	cases := []struct {
		name    string
		secret  string
		header  string
		body    []byte
		wantErr error
	}{
		{name: "valid", secret: secret, header: good, body: body, wantErr: nil},
		{name: "empty secret", secret: "", header: good, body: body, wantErr: ErrEmptySecret},
		{name: "missing header", secret: secret, header: "", body: body, wantErr: ErrMissingSignature},
		{name: "bad hex", secret: secret, header: "not-hex!!", body: body, wantErr: ErrInvalidSignature},
		{name: "wrong mac", secret: secret, header: Sign("other", body), body: body, wantErr: ErrInvalidSignature},
		{name: "tampered body", secret: secret, header: good, body: []byte(`{"event":"x"}`), wantErr: ErrInvalidSignature},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := http.Header{}
			if tc.header != "" {
				h.Set(HeaderSignature, tc.header)
			}
			err := VerifySignature(tc.secret, h, tc.body)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("VerifySignature() error = %v, want %v", err, tc.wantErr)
			}
		})
	}
}
