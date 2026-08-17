// Package grpcserver exposes the gateway as the canonical gRPC EngineService
// defined in github.com/APPNEURAL-Engines/contracts. It reuses the same
// auth, routing, rate limiting and circuit breaking building blocks as the
// HTTP gateway (package gateway) so both protocols enforce identical policy.
package grpcserver

import (
	"context"
	"errors"
	"strings"
	"time"

	commonv1 "github.com/APPNEURAL-Engines/contracts/gen/go/common"
	enginev1 "github.com/APPNEURAL-Engines/contracts/gen/go/engine"
	"github.com/APPNEURAL-Engines/engine-gateway/auth"
	"github.com/APPNEURAL-Engines/engine-gateway/gateway"
	"github.com/APPNEURAL-Engines/engine-gateway/protocol"
	"github.com/APPNEURAL-Engines/engine-gateway/ratelimit"
	"github.com/APPNEURAL-Engines/engine-gateway/retry"
	"github.com/APPNEURAL-Engines/engine-gateway/routing"
	"github.com/APPNEURAL-Engines/engine-gateway/telemetry"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Config configures a Server.
type Config struct {
	Authenticator   auth.Authenticator
	Authorizer      auth.Authorizer
	Limiter         ratelimit.Limiter
	Quota           *ratelimit.Quota
	Registry        *routing.Registry
	VersionResolver *routing.VersionResolver
	LoadBalancer    routing.LoadBalancer
	CircuitBreaker  *retry.CircuitBreaker
	EngineClient    gateway.EngineClient
	Logger          telemetry.Logger
	Metrics         *telemetry.Metrics
	DefaultTimeout  time.Duration
}

// Server implements enginev1.EngineServiceServer on top of the gateway's
// policy engine (auth, routing, rate limiting, circuit breaking).
type Server struct {
	cfg Config
}

// New creates a gRPC EngineService server. Callers construct the shared
// auth/routing/ratelimit/retry instances once and pass them both here and
// to gateway.New so HTTP and gRPC front doors enforce the same policy.
func New(cfg Config) *Server {
	if cfg.DefaultTimeout == 0 {
		cfg.DefaultTimeout = 10 * time.Second
	}
	if cfg.Registry == nil {
		cfg.Registry = routing.NewRegistry()
	}
	if cfg.VersionResolver == nil {
		cfg.VersionResolver = routing.NewVersionResolver("")
	}
	if cfg.LoadBalancer == nil {
		cfg.LoadBalancer = routing.NewRoundRobinLoadBalancer(cfg.Registry)
	}
	if cfg.CircuitBreaker == nil {
		cfg.CircuitBreaker = retry.NewCircuitBreaker(5, 30*time.Second)
	}
	if cfg.Logger == nil {
		cfg.Logger = telemetry.NewStdLogger(telemetry.LevelInfo)
	}
	if cfg.Metrics == nil {
		cfg.Metrics = telemetry.NewMetrics()
	}
	return &Server{cfg: cfg}
}

// Execute implements enginev1.EngineServiceServer.
func (s *Server) Execute(ctx context.Context, req *enginev1.ExecuteRequest) (*enginev1.ExecuteResponse, error) {
	start := time.Now()

	if req.GetEngineName() == "" || req.GetCapability() == "" {
		return nil, status.Error(codes.InvalidArgument, "engine_name and capability are required")
	}

	identity, err := s.authenticate(ctx)
	if err != nil {
		return nil, status.Error(codes.Unauthenticated, err.Error())
	}

	if s.cfg.Authorizer != nil {
		if err := s.cfg.Authorizer.Authorize(ctx, identity, req.GetEngineName()+":"+req.GetCapability(), req.GetTenantContext().GetId()); err != nil {
			return nil, status.Error(codes.PermissionDenied, err.Error())
		}
	}

	tenantID := req.GetTenantContext().GetId()
	if tenantID == "" {
		tenantID = req.GetRequestMetadata().GetTenantId()
	}

	if s.cfg.Limiter != nil && !s.cfg.Limiter.Allow(tenantID+":"+req.GetEngineName()) {
		return nil, status.Error(codes.ResourceExhausted, "rate limit exceeded")
	}

	if s.cfg.Quota != nil && tenantID != "" {
		if err := s.cfg.Quota.Consume(tenantID, "requests", 1); err != nil {
			return nil, status.Error(codes.ResourceExhausted, err.Error())
		}
	}

	endpoint, err := s.resolveEndpoint(req.GetEngineName())
	if err != nil {
		return nil, status.Error(codes.NotFound, err.Error())
	}

	timeout := s.cfg.DefaultTimeout
	if ms := req.GetExecutionContext().GetTimeoutMs(); ms > 0 {
		timeout = time.Duration(ms) * time.Millisecond
	}
	execCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	pReq := &protocol.Request{
		Engine:      req.GetEngineName(),
		Capability:  req.GetCapability(),
		Payload:     req.GetPayload(),
		ContentType: "application/octet-stream",
		TenantID:    tenantID,
		UserID:      req.GetRequestMetadata().GetUserId(),
		TraceID:     req.GetRequestMetadata().GetTraceId(),
		Timeout:     timeout,
	}

	var pResp *protocol.Response
	callErr := s.cfg.CircuitBreaker.Execute(execCtx, func() error {
		var forwardErr error
		pResp, forwardErr = s.cfg.EngineClient.Forward(execCtx, endpoint, pReq)
		return forwardErr
	})

	if callErr != nil {
		s.cfg.Metrics.RecordRequest(req.GetEngineName(), false, time.Since(start))
		if errors.Is(callErr, retry.ErrCircuitOpen) {
			return nil, status.Error(codes.Unavailable, callErr.Error())
		}
		return nil, status.Error(codes.Internal, callErr.Error())
	}

	s.cfg.Metrics.RecordRequest(req.GetEngineName(), true, time.Since(start))

	return &enginev1.ExecuteResponse{
		Status:  &commonv1.Status{Success: true},
		Payload: pResp.Payload,
		ResponseMetadata: &commonv1.ResponseMetadata{
			RequestId:     req.GetRequestMetadata().GetId(),
			DurationMs:    time.Since(start).Milliseconds(),
			Timestamp:     timestamppb.Now(),
			EngineName:    req.GetEngineName(),
			EngineVersion: endpoint.Version,
		},
	}, nil
}

// GetHealth implements enginev1.EngineServiceServer.
func (s *Server) GetHealth(_ context.Context, req *enginev1.GetHealthRequest) (*enginev1.GetHealthResponse, error) {
	names := s.engineNames(req.GetEngineName())

	resp := &enginev1.GetHealthResponse{}
	overall := enginev1.HealthStatus_HEALTH_STATUS_HEALTHY
	for _, name := range names {
		h := s.engineHealth(name)
		resp.EngineHealth = append(resp.EngineHealth, h)
		if h.Status != enginev1.HealthStatus_HEALTH_STATUS_HEALTHY {
			overall = h.Status
		}
	}
	if len(names) == 0 {
		overall = enginev1.HealthStatus_HEALTH_STATUS_UNKNOWN
	}
	resp.HealthStatus = overall
	return resp, nil
}

// ListEngines implements enginev1.EngineServiceServer.
func (s *Server) ListEngines(_ context.Context, req *enginev1.ListEnginesRequest) (*enginev1.ListEnginesResponse, error) {
	pattern := req.GetFilter().GetNamePattern()
	resp := &enginev1.ListEnginesResponse{}
	for _, name := range s.engineNames("") {
		if pattern != "" && !strings.Contains(name, pattern) {
			continue
		}
		resp.Engines = append(resp.Engines, s.engineInfo(name))
	}
	return resp, nil
}

// GetEngine implements enginev1.EngineServiceServer.
func (s *Server) GetEngine(_ context.Context, req *enginev1.GetEngineRequest) (*enginev1.GetEngineResponse, error) {
	if s.cfg.Registry.Count(req.GetEngineName()) == 0 {
		return nil, status.Errorf(codes.NotFound, "engine not found: %s", req.GetEngineName())
	}
	return &enginev1.GetEngineResponse{Engine: s.engineInfo(req.GetEngineName())}, nil
}

func (s *Server) authenticate(ctx context.Context) (*auth.Identity, error) {
	if s.cfg.Authenticator == nil {
		return nil, nil
	}

	md, _ := metadata.FromIncomingContext(ctx)
	if token := firstMD(md, "authorization"); token != "" {
		token = strings.TrimPrefix(token, "Bearer ")
		return s.cfg.Authenticator.Authenticate(ctx, auth.Credentials{Type: auth.CredentialTypeBearer, Token: token})
	}
	if key := firstMD(md, "x-api-key"); key != "" {
		return s.cfg.Authenticator.Authenticate(ctx, auth.Credentials{Type: auth.CredentialTypeAPIKey, APIKey: key})
	}
	return nil, errors.New("missing authentication credentials")
}

func (s *Server) resolveEndpoint(engine string) (*routing.EngineEndpoint, error) {
	if endpoints := s.cfg.Registry.GetHealthyEndpoints(engine); len(endpoints) > 0 {
		return s.cfg.LoadBalancer.Next(engine)
	}
	return nil, errors.New("no endpoints for engine: " + engine)
}

func (s *Server) engineNames(filter string) []string {
	if filter != "" {
		if s.cfg.Registry.Count(filter) == 0 {
			return nil
		}
		return []string{filter}
	}

	seen := make(map[string]struct{})
	var names []string
	for _, ep := range s.cfg.Registry.ListAll() {
		if _, ok := seen[ep.Name]; ok {
			continue
		}
		seen[ep.Name] = struct{}{}
		names = append(names, ep.Name)
	}
	return names
}

func (s *Server) engineHealth(name string) *enginev1.EngineHealth {
	endpoints := s.cfg.Registry.GetEndpoints(name)
	healthy := s.cfg.Registry.GetHealthyEndpoints(name)

	st := enginev1.HealthStatus_HEALTH_STATUS_UNHEALTHY
	switch {
	case len(healthy) == 0 && len(endpoints) > 0:
		st = enginev1.HealthStatus_HEALTH_STATUS_UNHEALTHY
	case len(healthy) < len(endpoints):
		st = enginev1.HealthStatus_HEALTH_STATUS_DEGRADED
	case len(healthy) > 0:
		st = enginev1.HealthStatus_HEALTH_STATUS_HEALTHY
	default:
		st = enginev1.HealthStatus_HEALTH_STATUS_UNKNOWN
	}

	version := ""
	if v, err := s.cfg.VersionResolver.Resolve(name, ""); err == nil {
		version = v
	}

	return &enginev1.EngineHealth{
		EngineName: name,
		Status:     st,
		Version:    version,
	}
}

func (s *Server) engineInfo(name string) *enginev1.EngineInfo {
	h := s.engineHealth(name)
	return &enginev1.EngineInfo{
		Name:    name,
		Version: h.Version,
		Status:  h.Status,
	}
}

func firstMD(md metadata.MD, key string) string {
	if vs := md.Get(key); len(vs) > 0 {
		return vs[0]
	}
	return ""
}
