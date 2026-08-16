package gateway

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/APPNEURAL-Engines/engine-gateway/auth"
	"github.com/APPNEURAL-Engines/engine-gateway/protocol"
	"github.com/APPNEURAL-Engines/engine-gateway/ratelimit"
	"github.com/APPNEURAL-Engines/engine-gateway/retry"
	"github.com/APPNEURAL-Engines/engine-gateway/routing"
	"github.com/APPNEURAL-Engines/engine-gateway/telemetry"
)

// EngineClient forwards requests to an engine.
type EngineClient interface {
	// Forward sends a request to the engine and returns the response.
	Forward(ctx context.Context, endpoint *routing.EngineEndpoint, req *protocol.Request) (*protocol.Response, error)
}

// Config configures the gateway.
type Config struct {
	// Address is the HTTP listen address.
	Address string

	// PathPrefix is stripped from request paths.
	PathPrefix string

	// ReadTimeout is the server read timeout.
	ReadTimeout time.Duration

	// WriteTimeout is the server write timeout.
	WriteTimeout time.Duration

	// DefaultTimeout is used when no request timeout is specified.
	DefaultTimeout time.Duration
}

// Gateway is the main gateway server.
type Gateway struct {
	config Config

	// Dependencies
	authenticator   auth.Authenticator
	authorizer      auth.Authorizer
	tenantResolver  auth.TenantResolver
	limiter         ratelimit.Limiter
	quota           *ratelimit.Quota
	registry        *routing.Registry
	versionResolver *routing.VersionResolver
	loadBalancer    routing.LoadBalancer
	circuitBreaker  *retry.CircuitBreaker
	converter       protocol.Converter
	logger          telemetry.Logger
	metrics         *telemetry.Metrics
	tracer          *telemetry.Tracer

	// Engine client for forwarding
	engineClient EngineClient

	server *http.Server
}

// New creates a new gateway.
func New(config Config) *Gateway {
	if config.ReadTimeout == 0 {
		config.ReadTimeout = 30 * time.Second
	}
	if config.WriteTimeout == 0 {
		config.WriteTimeout = 60 * time.Second
	}
	if config.DefaultTimeout == 0 {
		config.DefaultTimeout = 10 * time.Second
	}

	return &Gateway{
		config:          config,
		limiter:         ratelimit.NewTokenBucket(100, 200),
		quota:           ratelimit.NewQuota(),
		registry:        routing.NewRegistry(),
		versionResolver: routing.NewVersionResolver("1.0.0"),
		circuitBreaker:  retry.NewCircuitBreaker(5, 30*time.Second),
		converter:       protocol.NewJSONConverter(config.PathPrefix),
		logger:          telemetry.NewStdLogger(telemetry.LevelInfo),
		metrics:         telemetry.NewMetrics(),
		tracer:          telemetry.NewTracer(1.0),
	}
}

// SetAuthenticator sets the authentication handler.
func (g *Gateway) SetAuthenticator(a auth.Authenticator) *Gateway {
	g.authenticator = a
	return g
}

// SetAuthorizer sets the authorization handler.
func (g *Gateway) SetAuthorizer(a auth.Authorizer) *Gateway {
	g.authorizer = a
	return g
}

// SetTenantResolver sets the tenant resolver.
func (g *Gateway) SetTenantResolver(t auth.TenantResolver) *Gateway {
	g.tenantResolver = t
	return g
}

// SetRateLimiter sets the rate limiter.
func (g *Gateway) SetRateLimiter(l ratelimit.Limiter) *Gateway {
	g.limiter = l
	return g
}

// SetLoadBalancer sets the load balancer.
func (g *Gateway) SetLoadBalancer(lb routing.LoadBalancer) *Gateway {
	g.loadBalancer = lb
	return g
}

// SetLogger sets the logger.
func (g *Gateway) SetLogger(l telemetry.Logger) *Gateway {
	g.logger = l
	return g
}

// SetEngineClient sets the engine forwarding client.
func (g *Gateway) SetEngineClient(c EngineClient) *Gateway {
	g.engineClient = c
	return g
}

// Registry returns the endpoint registry for configuration.
func (g *Gateway) Registry() *routing.Registry {
	return g.registry
}

// VersionResolver returns the version resolver for configuration.
func (g *Gateway) VersionResolver() *routing.VersionResolver {
	return g.versionResolver
}

// Quota returns the quota tracker for configuration.
func (g *Gateway) Quota() *ratelimit.Quota {
	return g.quota
}

// Metrics returns the metrics collector.
func (g *Gateway) Metrics() *telemetry.Metrics {
	return g.metrics
}

// Start begins serving HTTP requests.
func (g *Gateway) Start() error {
	g.server = &http.Server{
		Addr:         g.config.Address,
		Handler:      g.Handler(),
		ReadTimeout:  g.config.ReadTimeout,
		WriteTimeout: g.config.WriteTimeout,
	}

	g.logger.Info("gateway starting", map[string]interface{}{
		"address": g.config.Address,
	})

	return g.server.ListenAndServe()
}

// Handler returns the HTTP handler with all routes registered.
func (g *Gateway) Handler() http.Handler {
	mux := http.NewServeMux()

	// Health endpoint
	mux.HandleFunc("GET /health", g.handleHealth)

	// Engine routing endpoint: /v1/{engine}/{capability}[/{sub-resource}]
	mux.HandleFunc("/", g.handleRequest)

	return mux
}

// Shutdown gracefully stops the gateway.
func (g *Gateway) Shutdown(ctx context.Context) error {
	if g.server == nil {
		return nil
	}
	return g.server.Shutdown(ctx)
}

// handleHealth serves the health endpoint.
func (g *Gateway) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"status":"healthy"}`))
}

// handleRequest processes engine requests.
func (g *Gateway) handleRequest(w http.ResponseWriter, r *http.Request) {
	start := time.Now()

	// Convert HTTP request to gateway request
	req, err := g.converter.FromHTTP(r)
	if err != nil {
		protocol.ErrorResponse(w, http.StatusBadRequest, "INVALID_REQUEST", err.Error(), "")
		return
	}

	// Start trace span
	span, ctx := g.tracer.StartSpan(r.Context(), "gateway."+req.Engine+"."+req.Capability, req.TraceID, "")
	defer g.tracer.EndSpan(span, "ok")

	// Middleware chain
	identity, err := g.authenticate(ctx, w, r, req)
	if err != nil {
		protocol.ErrorResponse(w, http.StatusUnauthorized, "UNAUTHENTICATED", err.Error(), req.TraceID)
		g.logger.Warn("authentication failed", map[string]interface{}{
			"engine": req.Engine, "capability": req.Capability, "error": err.Error(),
		})
		return
	}

	if err := g.authorize(ctx, identity, req); err != nil {
		protocol.ErrorResponse(w, http.StatusForbidden, "PERMISSION_DENIED", err.Error(), req.TraceID)
		return
	}

	if err := g.resolveTenant(ctx, w, r, req); err != nil {
		protocol.ErrorResponse(w, http.StatusBadRequest, "TENANT_RESOLUTION_FAILED", err.Error(), req.TraceID)
		return
	}

	if !g.limiter.Allow(rateLimitKey(req)) {
		protocol.ErrorResponse(w, http.StatusTooManyRequests, "RATE_LIMITED", "too many requests", req.TraceID)
		return
	}

	if err := g.checkQuota(req); err != nil {
		protocol.ErrorResponse(w, http.StatusTooManyRequests, "QUOTA_EXCEEDED", err.Error(), req.TraceID)
		return
	}

	// Resolve engine endpoint
	endpoint, err := g.resolveEndpoint(ctx, req)
	if err != nil {
		protocol.ErrorResponse(w, http.StatusNotFound, "ENGINE_NOT_FOUND", err.Error(), req.TraceID)
		return
	}

	// Forward request with timeout
	timeout := req.Timeout
	if timeout == 0 {
		timeout = g.config.DefaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Use circuit breaker for engine calls
	var resp *protocol.Response
	callErr := g.circuitBreaker.Execute(ctx, func() error {
		var forwardErr error
		resp, forwardErr = g.engineClient.Forward(ctx, endpoint, req)
		return forwardErr
	})

	if callErr != nil {
		g.metrics.RecordRequest(req.Engine, false, time.Since(start))
		status := http.StatusBadGateway
		code := "ENGINE_ERROR"
		if errors.Is(callErr, retry.ErrCircuitOpen) {
			status = http.StatusServiceUnavailable
			code = "ENGINE_UNAVAILABLE"
		}
		protocol.ErrorResponse(w, status, code, callErr.Error(), req.TraceID)
		g.logger.Error("engine call failed", callErr, map[string]interface{}{
			"engine": req.Engine, "capability": req.Capability,
		})
		return
	}

	g.metrics.RecordRequest(req.Engine, true, time.Since(start))

	// Convert response
	if err := g.converter.ToHTTP(w, resp); err != nil {
		g.logger.Error("response conversion failed", err, nil)
	}
}

// authenticate validates request credentials and returns the identity.
func (g *Gateway) authenticate(ctx context.Context, w http.ResponseWriter, r *http.Request, req *protocol.Request) (*auth.Identity, error) {
	if g.authenticator == nil {
		return nil, nil
	}

	token := r.Header.Get("Authorization")
	if token != "" {
		token = strings.TrimPrefix(token, "Bearer ")
		if token != "" {
			return g.authenticator.Authenticate(ctx, auth.Credentials{
				Type:  auth.CredentialTypeBearer,
				Token: token,
			})
		}
	}

	apiKey := r.Header.Get("X-API-Key")
	if apiKey != "" {
		return g.authenticator.Authenticate(ctx, auth.Credentials{
			Type:   auth.CredentialTypeAPIKey,
			APIKey: apiKey,
		})
	}

	return nil, errors.New("missing authentication credentials")
}

// authorize checks if the request is permitted for the identity.
func (g *Gateway) authorize(ctx context.Context, identity *auth.Identity, req *protocol.Request) error {
	if g.authorizer == nil {
		return nil
	}

	return g.authorizer.Authorize(ctx, identity, req.Engine+":"+req.Capability, req.TenantID)
}

// resolveTenant determines the tenant for the request.
func (g *Gateway) resolveTenant(ctx context.Context, w http.ResponseWriter, r *http.Request, req *protocol.Request) error {
	if g.tenantResolver == nil {
		return nil
	}

	_, err := g.tenantResolver.Resolve(ctx, auth.TenantHints{
		TenantID:    req.TenantID,
		Subdomain:   subdomainFromHost(r.Host),
		HeaderValue: req.TenantID,
	})
	return err
}

// checkQuota validates the request against tenant quotas.
func (g *Gateway) checkQuota(req *protocol.Request) error {
	if g.quota == nil || req.TenantID == "" {
		return nil
	}
	return g.quota.Consume(req.TenantID, "requests", 1)
}

// resolveEndpoint finds the target engine endpoint.
func (g *Gateway) resolveEndpoint(ctx context.Context, req *protocol.Request) (*routing.EngineEndpoint, error) {
	lb := g.loadBalancer
	if lb == nil {
		lb = routing.NewRoundRobinLoadBalancer(g.registry)
	}

	if endpoints := g.registry.GetHealthyEndpoints(req.Engine); len(endpoints) > 0 {
		return lb.Next(req.Engine)
	}

	// Fall back to version-based resolution
	version, err := g.versionResolver.Resolve(req.Engine, req.Version)
	if err != nil {
		return nil, err
	}

	endpoints := g.registry.GetHealthyEndpoints(req.Engine)
	for _, ep := range endpoints {
		if ep.Version == version {
			return ep, nil
		}
	}

	return nil, errors.New("no endpoints for engine: " + req.Engine)
}

// rateLimitKey builds a rate limiting key.
func rateLimitKey(req *protocol.Request) string {
	return req.TenantID + ":" + req.Engine
}

// subdomainFromHost extracts the subdomain from a host.
func subdomainFromHost(host string) string {
	parts := strings.Split(host, ".")
	if len(parts) >= 3 {
		return parts[0]
	}
	return ""
}
