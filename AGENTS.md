# AGENTS.md

Guidance for AI coding agents (Claude Code, Codex, etc.) working in
this repository. Humans reading this are the audience for `README.md`;
this file is the brief-the-newcomer document for an agent that just
opened a session.

## What this project is

A small webhook→SSE bridge for Plane, plus a userscript that consumes
the SSE feed. Module path is `github.com/hstern/plane-tug`. License
is Apache-2.0. Status is pre-v0.1.0.

The bridge is stateless on purpose. There is no database, no event
log, no replay. A new instance just works. Restart drops subscribers;
they reconnect via `EventSource`'s built-in retry. Missed events are
acceptable — the user reloads the page and resyncs.

## Layout

```
cmd/plane-tug/                main package, flag/env handling, signal-aware run loop
internal/bridge/              HMAC verify, hub fan-out, HTTP handlers, SSE wire format
internal/planeauth/           Plane session-cookie verifier (calls /api/users/me/)
userscript/plane-tug.user.js  Browser-side consumer
deploy/                       Example container quadlet and reverse-proxy snippet
```

## Code conventions

- **Copyright header**: every Go source file starts with these two
  lines exactly, then a blank line, then the package declaration:

  ```go
  // Copyright 2026 The plane-tug Authors
  // SPDX-License-Identifier: Apache-2.0
  ```

  Test files included. Generated files (none today) typically carry
  their generator's own header; don't double-stamp.

- **Standard library only.** Zero non-test dependencies. The whole
  service is small enough that adding `chi`, `mux`, a YAML parser, or
  a JOSE library would dwarf the rest. If you reach for one, stop and
  reconsider.

- **Errors**: sentinel `errors.New` for cases callers will switch on;
  `fmt.Errorf("…: %w", err)` for context-wrapping. `errors.Is`, not
  `==`, at every comparison site. Status mapping happens in the HTTP
  handler, not in the helper.

- **Logging**: `log/slog` with `LogAttrs`. Levels: `Debug` for
  per-event chatter, `Info` for lifecycle (start, shutdown,
  broadcast counts if added), `Warn` for caller errors (bad
  signature, malformed payload, unauthorized session), `Error` for
  things that should never happen in a configured deployment.

- **HTTP**: `net/http` only. `http.ServeMux` with method-prefixed
  patterns (`"POST /webhook"`). `http.NewResponseController` for
  per-write flushing on SSE. Read body via `http.MaxBytesReader` —
  the cap lives in `internal/bridge/server.go`.

- **Concurrency**: the hub fan-out is non-blocking by design. Sends
  use `select { case ch <- evt: default: }` so a slow subscriber
  drops events rather than blocking the broadcaster. Tests must hold
  this invariant — see `TestHubSlowSubscriberDoesNotBlock`.

- **Tests**: `t.Parallel()` everywhere it's safe. Table-driven tests
  for small surfaces. `httptest.Server` for end-to-end HTTP tests of
  the handler. No goleak yet — if you find a leak, add it.

## What to keep out of committed files

Anything that would only make sense inside one specific operator's
deployment. Concretely:

- No real hostnames. Examples in README and userscript `@match`
  lines use `plane.example.com`. Operators substitute their own at
  install time.
- No private organisation names, internal infrastructure terms,
  internal ticket identifiers, or personal handles beyond what
  appears on a git commit Author line.
- No references by name to sibling private repositories the author
  may also maintain. If a public library is referenced, name it by
  its public URL.

A useful test: would a Go developer who found this repo via a
search, with no other context, find the text accurate and complete?
If not, it doesn't belong in a tracked file.

## Commit messages

- Imperative, present tense, ≤72 chars on the title line.
- Body wraps at 72 columns, explains *why* not *what*; the diff is
  the *what*.
- One commit per logical change. Prefer "add /healthz handler" over
  "WIP" or "more changes". Larger PRs are fine; sprawling commits
  inside them are not.

## CI

`.github/workflows/ci.yml` runs `go vet`, `go test -race`,
`go build`, and `govulncheck` on every push and pull request. The
matrix targets the current and prior Go minor versions.

## When in doubt

Read `internal/bridge/server.go` and `cmd/plane-tug/main.go` end to
end before adding anything new. The service is meant to stay small —
under ~600 lines of Go total, including tests. If a change would
push significantly past that, sanity-check whether the right fix is
in the userscript, the reverse proxy, or Plane upstream rather than
in plane-tug itself.
