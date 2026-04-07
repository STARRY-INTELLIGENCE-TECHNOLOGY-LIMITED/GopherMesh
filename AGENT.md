# AGENT.md

This file is for AI coding agents and vibe-coding workflows that need to work with GopherMesh.

The goal is to help agents generate correct integrations, correct `config.json` files, and correct code changes without inventing unsupported architecture.

## What GopherMesh Is

GopherMesh is a lightweight local/edge/server-side gateway plus process orchestration layer for HTTP services, TCP services, and request-driven STDIO workers.

It is not just a reverse proxy.

It combines:

- config-driven routing
- HTTP and TCP ingress
- request-driven STDIO bridging
- optional cold start of local backends
- simple load balancing
- dashboard-based hot reload and observability

The main use case is:

1. A frontend, desktop app, or upstream system needs a stable local/nearby endpoint.
2. The real business service is a separate local process, often written in Go, Python, C++, or similar.
3. GopherMesh owns the public port, selects a backend, optionally starts it, and forwards traffic.

## Recommended Mental Model

When an agent integrates a business service with GopherMesh, the default assumption should be:

- The business service is an independent HTTP or TCP server.
- For Serverless/sandbox-style integration, the business service may also be a short-lived process that reads from stdin and writes to stdout.
- GopherMesh is the gateway in front of it.
- The business service usually does not need to embed GopherMesh SDK code.
- The first integration step is usually writing `config.json`, not modifying the business service.

Do not assume service-mesh style worker registration.

Current state:

- `RoleMaster` is implemented and is the normal runtime mode.
- `RoleWorker` exists in code, but worker registration/discovery is not implemented yet.

Therefore:

- Do not propose self-registering workers.
- Do not propose distributed control-plane patterns.
- Do not describe GopherMesh as a complete service mesh.

## Integration Decision Tree

When asked to integrate a new service, use this decision order.

### Mode A: Config-only integration (default)

Use this when:

- the business service already exposes HTTP or TCP
- the service can be started by command line
- the service can already be started outside GopherMesh

This is the preferred mode.

### Mode B: Embedded SDK launcher

Use this when:

- the user wants a custom Go bootstrap binary
- the user wants to embed GopherMesh engine startup into their own Go program
- the user wants to ship one custom entrypoint instead of using `main.go` in this repo

Important:

- Embedding the SDK means embedding the GopherMesh gateway engine.
- It does not mean the business service becomes a self-registering worker.

### Mode C: Pure proxy mode

Use this when:

- the backend is already running
- GopherMesh should forward traffic only
- GopherMesh should not spawn the backend process

In this mode, leave backend `cmd` empty.

### Mode D: Request-driven STDIO backend

Use this when:

- the business process should not bind a local port
- the process can handle one request/connection through stdin/stdout
- the target environment is closer to FaaS, CLI worker, or lightweight sandbox execution

In this mode:

- set route `protocol` to `stdio`
- choose `stdio_mode: "stream"` for raw byte streams or `stdio_mode: "http"` for browser-friendly HTTP-over-STDIO
- keep backend `cmd` and `args`
- omit `internal_host` and `internal_port`
- assume one child process is spawned per request/connection

## Current Integration Rules

### The business service only needs to do one of these

- expose an HTTP server on a known local port
- expose a TCP server on a known local port
- read request bytes from stdin and write response bytes to stdout for `protocol: "stdio"`

That is enough for GopherMesh integration.

### GopherMesh can manage backend lifecycle

If backend `cmd` and `args` are set, GopherMesh can:

- start the process on first request or connection
- wait for the internal port to become ready
- keep logs
- show runtime state in the dashboard
- kill managed local processes from the dashboard

For `protocol: "stdio"`:

- GopherMesh does not wait for port readiness
- it spawns a short-lived child per request/connection
- it bridges network traffic directly to stdin/stdout
- `stdio_mode: "stream"` preserves raw L4 byte streaming
- `stdio_mode: "http"` parses inbound HTTP requests and streams child stdout back as chunked HTTP
- `stdio_mode: "auto"` exists for legacy compatibility only and may mis-detect protocols
- the child is not tracked as a persistent managed process
- the dashboard exposes logs and config editing, but no kill button

### GopherMesh can also act as a pure proxy

If backend `cmd` is empty, GopherMesh will:

- check whether the target is reachable
- forward traffic if reachable
- not try to spawn the backend
- not expose a kill button for that backend in the dashboard

## `config.json` Writing Guide

This repo uses the `routes + backends` schema.

Do not generate the deprecated `endpoints` schema.

### Top-level fields

Supported top-level fields:

- `dashboard_host`
- `dashboard_port`
- `trusted_origins`
- `routes`

### Route object

Each route is keyed by its public port:

```json
{
  "routes": {
    "18081": {
      "name": "Example-HTTP-Route",
      "protocol": "http",
      "load_balance": "round_robin",
      "backends": []
    }
  }
}
```

Route fields:

- `name`: optional, defaults to `Route-<publicPort>`
- `protocol`: `http`, `tcp`, or `stdio`, defaults to `http`
- `stdio_mode`: only for `protocol: "stdio"`; supported values are `stream`, `http`, and `auto`; default is `stream`
- `load_balance`: `round_robin`, `least_conn`, or `ip_hash`
- `backends`: required, must not be empty

### Backend object

Backend fields:

- `name`: optional but recommended
- `cmd`: command to start backend; empty means pure proxy mode
- `args`: command line args array
- `internal_host`: optional, defaults to `127.0.0.1` for `http`/`tcp`
- `internal_port`: required for `http`/`tcp`, omitted for `stdio`

### Load balancing values

Only generate these values:

- `round_robin`
- `least_conn`
- `ip_hash`

Do not invent:

- `random`
- `least_time`
- `hash`
- `sticky`
- `weighted_rr`

Those are not implemented.

### Route validation constraints

Agents must respect these constraints:

- public route port keys must not be blank
- each route must have at least one backend
- external backend `internal_port` values must be unique across routes for non-stdio backends
- internal routes must use exactly one backend with `cmd: "internal"`
- do not mix `internal` backend and external backends in the same route
- `stdio` backends must have non-empty `cmd`
- `stdio` backends must not use `cmd: "internal"`

### `trusted_origins`

If not sure, use:

```json
"trusted_origins": ["*"]
```

If the user explicitly wants stricter browser access, narrow it to exact origins.

## Templates Agents Should Reuse

### Managed HTTP backend

Use this when GopherMesh should cold-start a local HTTP service:

```json
{
  "dashboard_host": "127.0.0.1",
  "dashboard_port": "19999",
  "trusted_origins": ["*"],
  "routes": {
    "18081": {
      "name": "Bayes-HTTP",
      "protocol": "http",
      "load_balance": "least_conn",
      "backends": [
        {
          "name": "bayes-http-a",
          "cmd": "./bayes-service",
          "args": ["-mode", "http", "-port", "19081"],
          "internal_host": "127.0.0.1",
          "internal_port": "19081"
        }
      ]
    }
  }
}
```

### Managed TCP backend

Use this when GopherMesh should cold-start a local TCP service:

```json
{
  "dashboard_host": "127.0.0.1",
  "dashboard_port": "19999",
  "trusted_origins": ["*"],
  "routes": {
    "17081": {
      "name": "Bayes-TCP",
      "protocol": "tcp",
      "load_balance": "round_robin",
      "backends": [
        {
          "name": "bayes-tcp-a",
          "cmd": "./bayes-service",
          "args": ["-mode", "tcp", "-port", "19091"],
          "internal_host": "127.0.0.1",
          "internal_port": "19091"
        }
      ]
    }
  }
}
```

### Pure proxy backend

Use this when the backend is already running and GopherMesh should not spawn it:

```json
{
  "dashboard_host": "127.0.0.1",
  "dashboard_port": "19999",
  "trusted_origins": ["*"],
  "routes": {
    "18081": {
      "name": "Remote-Or-Prestarted-HTTP",
      "protocol": "http",
      "load_balance": "ip_hash",
      "backends": [
        {
          "name": "bayes-http-a",
          "cmd": "",
          "internal_host": "127.0.0.1",
          "internal_port": "19081"
        }
      ]
    }
  }
}
```

### Request-driven STDIO backend

Use this when the backend should process one connection/request through stdin/stdout without binding a local port:

```json
{
  "dashboard_host": "127.0.0.1",
  "dashboard_port": "19999",
  "trusted_origins": ["*"],
  "routes": {
    "17083": {
      "name": "Bayes-STDIO",
      "protocol": "stdio",
      "stdio_mode": "stream",
      "load_balance": "round_robin",
      "backends": [
        {
          "name": "bayes-stdio-a",
          "cmd": "./bayes-worker",
          "args": ["--mode", "stdio"]
        }
      ]
    }
  }
}
```

## SDK Integration Guide

The module path is:

```go
github.com/SUTFutureCoder/gophermesh/sdk
```

### Default recommendation

Prefer standalone runtime plus `config.json`.

Only embed the SDK when the user explicitly wants a custom Go launcher.

### Minimal embedded launcher example

```go
package main

import (
  "context"
  "log"
  "os"
  "os/signal"
  "syscall"
  "time"

  mesh "github.com/SUTFutureCoder/gophermesh/sdk"
)

func main() {
  cfg, err := mesh.LoadConfig("config.json")
  if err != nil {
    log.Fatal(err)
  }
  cfg.ConfigPath = "config.json"

  engine, err := mesh.NewEngine(cfg)
  if err != nil {
    log.Fatal(err)
  }

  ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
  defer cancel()

  if err := engine.Run(ctx); err != nil {
    log.Printf("engine stopped: %v", err)
  }

  shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
  defer shutdownCancel()

  if err := engine.Shutdown(shutdownCtx); err != nil {
    log.Fatal(err)
  }
}
```

### What agents should not claim

Do not claim that:

- the business service must embed GopherMesh SDK
- workers auto-register with a master
- the SDK currently provides a distributed node protocol

That is not implemented.

## Auto-bootstrap Behavior

Agents should know and mention this when useful:

- the default runtime command is effectively `-config config.json`
- if `config.json` does not exist, GopherMesh will create a default one
- shipping a prebuilt `config.json` is the preferred out-of-box experience

This is an important product feature.

## Desktop Protocol Bootstrap Pattern

When the user wants a browser or desktop frontend to wake a local service on demand, agents should prefer this pattern:

1. Probe the local HTTP or TCP endpoint first.
2. If it is not available, use a custom protocol only to bootstrap the local launcher, for example `gophermesh://launch`.
3. After launch, continue all real traffic through the normal HTTP or TCP route.

Important constraints:

- keep custom protocol payload minimal
- `port` is optional and should only be added when route-level validation or deduplication is useful
- optional `conf` can be used when a non-default config file must be selected
- validate that requested port against local `config.json`
- do not use the custom protocol as the data plane
- do not put heavy business payloads into protocol URLs
- if the user wants to disable protocol registration and launch handling, prefer the CLI flag `-noprotocol`

## Dashboard Facts Agents Should Respect

The dashboard can:

- show status, PID, uptime, and logs
- edit route JSON
- edit child backends from form UI
- change `load_balance` from a dropdown
- delete a child backend and remove the parent route if it becomes empty
- kill managed local processes
- show `Request-Driven` status for `stdio` backends
- fetch logs by backend ref even when no `internal_port` exists

The dashboard should not show a kill button for pure proxy backends with no managed local PID, or for `stdio` backends that have no persistent process.

## Validation Checklist For Agents

When generating integration changes, validate with this order:

1. Does the business service expose HTTP/TCP on the declared `internal_port`, or does it correctly speak stdin/stdout for `protocol: "stdio"`?
2. Is `protocol` correct?
3. Is `load_balance` one of the supported values?
4. If cold start is required, are `cmd` and `args` correct?
5. If pure proxy mode is required, is `cmd` empty?
6. For non-stdio backends, are internal ports unique across external backends?
7. Can the config be started with:

```bash
go run . -config config.json
```

8. Can the route be verified with `curl` for HTTP or `nc`/equivalent for TCP/STDIO?

## Repo-specific Change Rules

If an agent edits this repo itself, preserve these invariants:

- keep the `routes + backends` config schema
- keep config reload transactional
- keep config reads synchronized with config writes
- keep dashboard form options aligned with supported `load_balance` values
- if new config fields are added, update validation, dashboard UI, sample config, README, and tests
- if a new protocol is added, update normalization, runtime behavior, dashboard UI, sample config, README, and tests together
- if new load balancing strategies are added, update normalization, runtime selection, UI dropdown, and tests together

## Good Default Recommendation To Users

When in doubt, tell users to do this:

1. Keep their business service as a normal HTTP/TCP server, or a stdin/stdout worker when portless request-driven execution is the better fit.
2. Put GopherMesh in front of it.
3. Use `config.json` to define public ports, protocol, load balance, and backend startup command.
4. Let JS or the upstream client talk only to GopherMesh.

That is the intended architecture today.
