# End-to-end test against real Plane CE

This directory stands up a real Plane CE stack, builds the plane-tug
image from the repo, deploys both onto a single compose network, and
runs a Go test program that:

1. Subscribes to a plane-tug SSE stream for a known project.
2. Drives an issue change through Plane's REST API.
3. Asserts the change arrives over SSE within the expected window.

This complements the unit and in-process integration tests by
exercising the parts they can't:

- Plane's actual webhook payload shape (the `data` JSON in real
  deliveries).
- Plane's HMAC signing (rather than our test code calling
  `bridge.Sign` against itself).
- The `WEBHOOK_ALLOWED_HOSTS` SSRF allowlist that gates deliveries.
- The compose network DNS hop (`http://plane-tug:8080/webhook`).

It is gated behind a `e2e` build tag so it does not run in normal
`go test ./...`.

## Scope

- **Real**: Plane CE (api + worker + beat-worker + db + redis + mq +
  minio), plane-tug (built from this checkout), the webhook
  HMAC roundtrip, the SSE wire format.
- **Stubbed**: Plane's `/api/users/me/` session endpoint. The bridge
  is pointed at a `traefik/whoami` container that 200s any path,
  so test clients can connect with a hand-crafted cookie. The
  session pass-through is already well-covered by
  `internal/planeauth/session_test.go`; pulling Plane's actual
  cookie-based auth flow into the e2e adds substantial complexity
  for marginal additional coverage.

## Cost

- Cold cache: ~90 s to pull Plane images and bring up the stack.
- Warm cache: ~30 s for the test run.

CI gates this job on `workflow_dispatch` and a `run-e2e` label on
pull requests; it does not run on every push.

## Running locally

```sh
# bring up + seed (prints connection info JSON on the last stdout line)
bash test/e2e-docker/run.sh up

# run the test
go test -tags e2e -count=1 -v ./test/e2e-docker/...

# tear down
bash test/e2e-docker/run.sh down
```

`run.sh up` is idempotent: the seed script uses Django ORM
`get_or_create` everywhere.

## Files

- `compose.yaml` — Plane CE stack + `fake-plane` (whoami) + the
  `plane-tug` service built from the repo root.
- `seed.py` — creates admin user, workspace, project, API token,
  and the workspace-level webhook pointing at
  `http://plane-tug:8080/webhook` with a known HMAC secret.
- `run.sh` — orchestrator with `up`, `seed`, `down` subcommands.
- `e2e_test.go` — the Go test, build-tagged `e2e`.
