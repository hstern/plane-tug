// Copyright 2026 The plane-tug Authors
// SPDX-License-Identifier: Apache-2.0

package bridge

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
)

// HeaderSignature is the header Plane sets on outbound webhook deliveries.
const HeaderSignature = "X-Plane-Signature"

var (
	// ErrEmptySecret is returned if the bridge is configured without a webhook
	// secret. We refuse rather than computing HMAC over an empty key, which
	// would always succeed against an attacker who knows the body.
	ErrEmptySecret = errors.New("webhook secret is empty")

	// ErrMissingSignature is returned when the request has no X-Plane-Signature
	// header at all.
	ErrMissingSignature = errors.New("missing X-Plane-Signature header")

	// ErrInvalidSignature is returned when the supplied signature does not
	// match the HMAC-SHA256 of the raw body. Covers both malformed-hex and
	// valid-hex/wrong-MAC; we deliberately do not distinguish so callers
	// cannot oracle one case from the other.
	ErrInvalidSignature = errors.New("invalid X-Plane-Signature")
)

// VerifySignature checks the X-Plane-Signature header against an
// HMAC-SHA256 of body, keyed by secret, hex-encoded. The comparison is
// constant-time.
//
// The body passed here MUST be the exact raw request body — re-serializing
// the JSON will reorder keys and break verification.
func VerifySignature(secret string, headers http.Header, body []byte) error {
	if secret == "" {
		return ErrEmptySecret
	}
	got := headers.Get(HeaderSignature)
	if got == "" {
		return ErrMissingSignature
	}
	gotMAC, err := hex.DecodeString(got)
	if err != nil {
		return ErrInvalidSignature
	}

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	wantMAC := mac.Sum(nil)

	if !hmac.Equal(wantMAC, gotMAC) {
		return ErrInvalidSignature
	}
	return nil
}

// Sign returns the hex-encoded HMAC-SHA256 of body keyed by secret. Exported
// for use by tests and any future signing utility; the bridge itself only
// verifies.
func Sign(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}
