# plane-tug

A small Go service that turns the outbound webhook stream from a
[Plane](https://plane.so) workspace into a per-project
[Server-Sent Events](https://html.spec.whatwg.org/multipage/server-sent-events.html)
feed, plus a userscript that consumes that feed so browser tabs
refresh on real changes instead of on a clock.

The name is a metaphor: a small craft that tugs a much larger one
into the right position. plane-tug doesn't *do* anything to Plane;
it just nudges the browser when the server says something changed.

## Why

Plane Community Edition does not push state changes to connected
browser clients, so a tab showing an issue list goes stale until
the user manually reloads. A common workaround is a userscript that
reloads on a fixed interval (e.g. every 45 s). That works but it
also reloads the SPA — losing in-flight UI state — even when
nothing changed, and lags behind real changes by up to the interval.

plane-tug replaces the timer with a push channel:

```
plane-api   ──┐
              ├── webhook POST ──► plane-tug ──SSE──► browser userscript
plane-worker ─┘                       │
                                       ├── HMAC verify
                                       ├── per-project fan-out
                                       └── session check on connect
```

The bridge is stateless. Events are hints, not authoritative records:
on restart, clients reconnect via `EventSource`'s built-in retry and
the next event triggers a reload.

Upstream tracking issue for a native real-time channel in Plane:
[makeplane/plane#7944](https://github.com/makeplane/plane/issues/7944).
plane-tug is meant to retire when that lands.

## What it does (and doesn't)

- Verifies HMAC on every inbound webhook (`X-Plane-Signature`,
  HMAC-SHA256 of the raw body, hex-encoded).
- Fans events out to subscribers of the affected project. Workspace-
  level events (where the payload has no project) fan out to all
  open subscribers.
- Authenticates every SSE connect by replaying the request's cookies
  against Plane's `/api/users/me/`. One call per connection open,
  not per event.
- Sends a `:keepalive` SSE comment frame every 15 seconds so idle
  connections don't get reaped by intermediate proxies.

It deliberately does **not**:

- Persist events. A restart drops subscribers; clients reconnect
  and resync on the next event.
- Replay missed events. The client should reload on each tickle, or
  the userscript is free to refetch with whatever logic suits.
- Modify the SPA in flight. The userscript reloads the page; an
  upcoming approach that pokes the SPA's store directly is left for
  a future version.

## Endpoints

| Method | Path       | Notes |
|--------|------------|-------|
| POST   | `/webhook` | Inbound webhook deliveries. Verifies HMAC, parses `{event, action, data.project}`, broadcasts. Returns 204 on success, 401 on bad signature, 400 on malformed JSON. |
| GET    | `/events?project=<uuid>` | SSE stream for a single project. Requires a valid Plane session cookie. Sends `event: plane\ndata: {…}\n\n` per change and `:keepalive\n\n` every 15 s. |
| GET    | `/healthz` | Liveness probe. Always 200 OK. |

The SSE payload is the same `Event` shape the bridge keeps
internally:

```json
{ "event": "issue", "action": "updated", "project": "…uuid…" }
```

Unknown event types are passed through unchanged — the bridge is a
dumb pipe and clients are free to treat anything they don't
recognise as a generic "something changed" tickle.

## Configuration

All configuration is via environment variables:

| Variable | Required | Default | Description |
|----------|----------|---------|-------------|
| `PLANE_TUG_WEBHOOK_SECRET` | yes | — | HMAC secret matching the workspace webhook. Plane generates this when the webhook is created and returns it in the create response only. |
| `PLANE_TUG_PLANE_BASE_URL` | yes | — | Absolute URL where the bridge can reach Plane's API for session verification (e.g. `http://plane-web:3000`). Internal name is fine if the bridge runs in the same network as Plane. |
| `PLANE_TUG_LISTEN` | no | `:8080` | Address to listen on. |
| `PLANE_TUG_LOG_LEVEL` | no | `info` | One of `debug`, `info`, `warn`, `error`. |

## Reverse-proxy front-end

plane-tug speaks plain HTTP; it expects a reverse proxy in front of it
that terminates TLS and rides on the same origin as Plane (so the
browser hands the Plane session cookie to the bridge automatically).

A typical Caddy snippet:

```caddy
plane.example.com {
    # … your existing Plane reverse_proxy block …

    handle /live/* {
        uri strip_prefix /live
        reverse_proxy plane-tug:8080 {
            flush_interval -1
        }
    }
}
```

`flush_interval -1` is mandatory. Caddy buffers proxied responses by
default; SSE breaks under buffering. Verify with `curl -N`:

```sh
curl -N https://plane.example.com/live/healthz
curl -N -b 'session=…' https://plane.example.com/live/events?project=…
```

Once the bridge is reachable behind the proxy, configure a workspace-
level webhook in Plane pointing to
`https://plane.example.com/live/webhook` subscribing to `project`,
`issue`, `issue_comment`, `cycle`, `module`, `cycle_issue`, and
`module_issue`. Capture the HMAC secret from the create response —
it's only returned once.

If your Plane deployment has the `WEBHOOK_ALLOWED_HOSTS` SSRF
allowlist set (it does by default on recent CE versions), make sure
your bridge hostname is on it. The allowlist is checked both at
webhook-create time and at every delivery; restart `plane-api` and
the worker after editing it.

## Userscript

`userscript/plane-tug.user.js` is a Tampermonkey-/Violentmonkey-
compatible userscript. Edit the `@match` line to point at your Plane
host, install, and the script will open an `EventSource` against
same-origin `/live/events` when you're on a refreshable view.

Behaviour:

- Opens the SSE stream only on URLs that benefit from refresh
  (issue list, cycles, modules, views, your-work). Page editors and
  settings are left alone.
- On each SSE event, debounces 500 ms (so bulk edits coalesce into
  one reload), checks that the user isn't typing or has a dialog
  open, then preserves scroll position and reloads.
- If the SSE stream fails three times consecutively without a
  message, falls back to a 45 s timer until the next successful
  connect.

## Build and run

```sh
go build ./cmd/plane-tug
PLANE_TUG_WEBHOOK_SECRET=… PLANE_TUG_PLANE_BASE_URL=http://localhost:3000 \
    ./plane-tug
```

Container image (multi-stage, distroless runtime):

```sh
docker build -t plane-tug .
docker run --rm -p 8080:8080 \
    -e PLANE_TUG_WEBHOOK_SECRET=… \
    -e PLANE_TUG_PLANE_BASE_URL=http://plane-web:3000 \
    plane-tug
```

A sample `quadlet/plane-tug.container` is provided under `deploy/`
for podman-quadlet-based hosts.

## License

[Apache License 2.0](LICENSE).
