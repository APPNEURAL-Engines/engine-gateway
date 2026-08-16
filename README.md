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

| Package     | Responsibility                                                    |
|-------------|-------------------------------------------------------------------|
| `auth`      | JWT + API-key authentication, RBAC authorization, tenant resolution |
| `routing`   | Endpoint registry, semantic version resolver, load balancing (round-robin, weighted) |
| `ratelimit` | Token bucket, fixed window, sliding window limiters + per-tenant quotas |
| `retry`     | Exponential backoff retry policy + circuit breaker (closed/open/half-open) |
| `telemetry` | Structured logging, request metrics, distributed tracing spans |
| `protocol`  | HTTP ↔ Request/Response conversion (JSON), gRPC envelope conversion, error responses |
| `gateway`   | The gateway server: middleware chain, health endpoint, engine forwarding client |

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

## Testing

```bash
go test ./... -cover
```

All packages have unit tests (auth, routing, ratelimit, retry, protocol, telemetry)
plus end-to-end gateway tests covering the full middleware chain.

## License

Proprietary — AppNeurox Engine Platform.
