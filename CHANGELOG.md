# Changelog

All notable changes to this project will be documented in this file.
The format is loosely based on [Keep a Changelog](https://keepachangelog.com/),
and the project adheres to [Semantic Versioning](https://semver.org/).

## [Unreleased]

## [0.1.0] - 2026-05-25

First release. Tagged `v0.1.0`; published as
`ghcr.io/hstern/plane-tug:0.1.0`, `:0.1`, `:0`, `:latest`.

### Added

- Webhook → Server-Sent Events bridge.
  - `POST /webhook` with HMAC-SHA256 verification of `X-Plane-Signature`.
  - `GET /events?project=<uuid>` SSE stream with 15-second
    keepalive comment frames and per-project fan-out.
  - `GET /healthz` liveness probe.
  - `internal/planeauth` session verifier that replays the
    request's cookies against Plane's `/api/users/me/`.
- Userscript `userscript/plane-tug.user.js` that consumes the
  SSE feed, debounces 500 ms, preserves scroll on reload, and
  falls back to a 45-second timer after three consecutive
  `EventSource` errors.
- Multi-stage Dockerfile producing a distroless static runtime
  image; the in-image `test` stage runs `go vet`, `go test -race`,
  and `govulncheck` and gates the runtime build.
- Example podman quadlet and Caddy reverse-proxy snippet under
  `deploy/`.
- Three-layer test suite: unit tests, in-process integration tests
  (real `http.Server`, real SSE clients, binary-level subprocess
  tests), and a real-Plane-CE docker-compose e2e that drives an
  issue change end-to-end through the bridge.
- GitHub Actions CI with `build-image` / `lint` / `e2e` / `publish`
  jobs and branch protection on `main` requiring the first three.
