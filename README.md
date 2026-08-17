# Engine Gateway

The **Engine Gateway** is the central entry point of the AppNeurox Engine Platform. It
sits between SDK consumers and engine instances, hiding *which language an engine is
written in* and *where it runs* behind a single, uniform API.

> **Philosophy:** SDK consumers should never know whether an engine is written in Go,
> Rust, Java, or Python — or whether it runs in-process, in a container, or as a WASM
> module. The gateway makes every engine look identical.

## Architecture

```
Application/SDK
      │
      ▼
┌─────────────────────────────────────────────────┐
│                  ENGINE GATEWAY                 │
│  ┌───────────┐ ┌──────────┐ ┌────────────────┐  │
│  │  Auth     │→│ Authorize│→│ Tenant Resolve │  │
│  └───────────┘ └──────────┘ └────────────────┘  │
│  ┌───────────┐ ┌──────────┐ ┌────────────────┐  │
│  │ RateLimit │→│  Quota   │→│ Version Resolve│  │
│  └───────────┘ └──────────┘ └────────────────┘  │
│  ┌───────────┐ ┌──────────┐ ┌────────────────┐  │
│  │ LoadBalanc│→│ Retry/CB │→│ Protocol Conv  │  │
│  └───────────┘ └──────────┘ └────────────────┘  │
└─────────────────────────────────────────────────┘
      │
      ▼
Engine instance (HTTP / gRPC / WASM / in-process)
```

## Packages

| Package      | Responsibility                                                    |
|--------------|-------------------------------------------------------------------|
| `auth`       | JWT + API-key authentication, RBAC authorization, tenant resolution |
| `routing`    | Endpoint registry, semantic version resolver, load balancing (round-robin, weighted) |
| `ratelimit`  | Token bucket, fixed window, sliding window limiters + per-tenant quotas |
| `retry`      | Exponential backoff retry policy + circuit breaker (closed/open/half-open) |
| `telemetry`  | Structured logging, request metrics, distributed tracing spans |
| `protocol`   | HTTP ↔ Request/Response conversion (JSON), gRPC envelope conversion, error responses |
| `gateway`    | The HTTP front door: middleware chain, health endpoint, engine forwarding client |
| `grpcserver` | The gRPC front door: implements the canonical `EngineService` from [contracts](https://github.com/APPNEURAL-Engines/contracts), backed by the same auth/routing/ratelimit/retry instances as `gateway` |
| `config`     | Environment-based runtime configuration for `cmd/gateway` |
| `cmd/gateway`| The deployable binary — starts both front doors from one process |

## Two Front Doors, One Policy Engine

The gateway is the "Gateway" step in the platform's contract flow:

```
Application → SDK → Contract (proto) → Gateway → Engine → Provider
```

It exposes two protocols that share one instance of every policy component
(auth, routing registry, rate limiter, quota, circuit breaker):

- **gRPC** (`grpcserver`) implements `appneurox.engine.v1.EngineService` —
  `Execute`, `GetHealth`, `ListEngines`, `GetEngine` — exactly as defined in
  `contracts/proto/engine/gateway.proto`. This is the canonical, typed API
  SDKs generated from `contracts` talk to.
- **HTTP/JSON** (`gateway`) exposes `POST /v1/{engine}/{capability}` for
  clients that prefer plain REST.

Because both share the same `auth.Authenticator`, `auth.Authorizer`,
`ratelimit.Limiter`, `routing.Registry` and `retry.CircuitBreaker`
instances, a request is authenticated, authorized, rate limited and routed
identically no matter which protocol it arrived on.

## Request Flow

A request to `POST /v1/{engine}/{capability}` passes through the middleware chain:

1. **Authenticate** — validates `Authorization: Bearer <JWT>` or `X-API-Key`
2. **Authorize** — RBAC check for `{engine}:{capability}` against the identity
3. **Resolve tenant** — from header, identity, or subdomain
4. **Rate limit** — per `tenant:engine` key
5. **Quota** — per-tenant usage limits
6. **Resolve endpoint** — version constraint → healthy endpoint (load-balanced)
7. **Forward** — circuit-breaker protected call to the engine
8. **Convert** — engine response back to the SDK's protocol

Errors are returned as `{ "error": { "code": "...", "message": "..." } }` with a
trace ID header for correlation.

## Quick Start

```go
package main

import (
	"net/http"
	"time"

	"github.com/APPNEURAL-Engines/engine-gateway/auth"
	"github.com/APPNEURAL-Engines/engine-gateway/gateway"
	"github.com/APPNEURAL-Engines/engine-gateway/ratelimit"
	"github.com/APPNEURAL-Engines/engine-gateway/routing"
)

func main() {
	gw := gateway.New(gateway.Config{Address: ":8080", PathPrefix: "/v1"})

	// Authentication
	jwtAuth := auth.NewJWTAuthenticator(auth.JWTConfig{
		Secret:   "change-me",
		Issuer:   "appneurox",
		Audience: "engine-gateway",
	})
	gw.SetAuthenticator(jwtAuth)

	// Authorization
	authz := auth.NewRBACAuthorizer()
	authz.AddPolicy(auth.Policy{
		Name: "pdf-admin", Action: "pdf:*", Roles: []string{"admin"},
		Effect: auth.EffectAllow, Priority: 1,
	})
	gw.SetAuthorizer(authz)

	// Rate limiting + quotas
	gw.SetRateLimiter(ratelimit.NewTokenBucket(100, 200))
	gw.Quota().SetLimit("tenant-1", "requests", 10000)

	// Register an engine instance
	gw.Registry().Register(&routing.EngineEndpoint{
		Name: "pdf", Version: "1.0.0",
		Address: "pdf-engine:8080", Protocol: "http",
	})
	gw.VersionResolver().RegisterVersion("pdf", "1.0.0")

	// Forward to engines over HTTP
	gw.SetEngineClient(gateway.NewHTTPEngineClient(30 * time.Second))

	if err := gw.Start(); err != nil && err != http.ErrServerClosed {
		panic(err)
	}
}
```

See [examples/basic](examples/basic) for a complete runnable program including a mock
engine.

## Running the gateway binary

`cmd/gateway` wires the HTTP and gRPC front doors together and is what actually
gets deployed. It's configured entirely from environment variables:

| Variable                  | Default              | Purpose                                   |
|----------------------------|----------------------|--------------------------------------------|
| `GATEWAY_HTTP_ADDR`        | `:8080`               | HTTP/JSON listen address                    |
| `GATEWAY_GRPC_ADDR`        | `:9090`               | gRPC (`EngineService`) listen address       |
| `GATEWAY_JWT_SECRET`       | *(unset)*             | HMAC secret for JWT auth; auth/authz are disabled if unset (dev only) |
| `GATEWAY_JWT_ISSUER`       | `appneurox`            | Expected JWT issuer                         |
| `GATEWAY_JWT_AUDIENCE`     | `engine-gateway`       | Expected JWT audience                       |
| `GATEWAY_DEFAULT_TIMEOUT`  | `10s`                  | Default per-request timeout                 |
| `GATEWAY_ENGINES`          | *(unset)*              | Static bootstrap registry: `name=protocol://address,...` (e.g. `pdf=http://pdf-engine:8080`) |

```bash
make build
GATEWAY_ENGINES="pdf=http://localhost:7100" ./bin/gateway
```

`GATEWAY_ENGINES` is a static bootstrap for local development and small
deployments; production deployments should replace it with dynamic service
discovery feeding `routing.Registry`.

See [deploy/Dockerfile](deploy/Dockerfile) for a container build and the
[Makefile](Makefile) for `build`/`run`/`test`/`docker` targets.

## Testing

```bash
go test ./... -cover
```

All packages have unit tests (auth, routing, ratelimit, retry, protocol, telemetry)
plus end-to-end tests for both the HTTP middleware chain (`gateway`) and the
gRPC `EngineService` implementation (`grpcserver`, using `bufconn`).

## License

Proprietary — AppNeurox Engine Platform.
