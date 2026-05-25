// Copyright 2026 The plane-tug Authors
// SPDX-License-Identifier: Apache-2.0

// Package bridge implements the webhook→SSE side of plane-tug.
//
// The package owns three concerns:
//
//   - HMAC-SHA256 verification of inbound webhook deliveries
//     (signature.go).
//   - A per-project fan-out hub (hub.go) whose Broadcast is
//     non-blocking — slow subscribers drop events rather than
//     stalling the broadcaster.
//   - HTTP handlers (server.go) exposing POST /webhook,
//     GET /events?project=<uuid>, and GET /healthz. The /events
//     handler is the SSE producer; per-write flushes go through
//     http.NewResponseController so a reverse proxy that respects
//     flushes streams immediately.
//
// The package is stateless. A Server holds a Hub and a
// SessionVerifier; restarting the process drops subscribers, who
// reconnect via EventSource's built-in retry.
package bridge
