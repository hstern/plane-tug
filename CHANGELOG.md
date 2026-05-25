# Changelog

All notable changes to this project will be documented in this file.
The format is loosely based on [Keep a Changelog](https://keepachangelog.com/),
and the project adheres to [Semantic Versioning](https://semver.org/).

## [Unreleased]

### Added

- Initial implementation of the webhook→SSE bridge.
  - `POST /webhook` with HMAC-SHA256 verification of `X-Plane-Signature`.
  - `GET /events?project=<uuid>` SSE stream with 15-second
    keepalive comment frames and per-project fan-out.
  - `GET /healthz` liveness probe.
  - `internal/planeauth` session verifier that replays the
    request's cookies against Plane's `/api/users/me/`.
- Userscript `userscript/plane-tug.user.js` that consumes the
  SSE feed, debounces 500 ms, preserves scroll on reload, and
  falls back to a 45-second timer after three consecutive
  EventSource errors.
- Multi-stage Dockerfile producing a distroless static runtime
  image.
- Example podman quadlet and Caddy reverse-proxy snippet under
  `deploy/`.
- GitHub Actions CI: `go vet`, `go test -race`, `go build`,
  `govulncheck`.
