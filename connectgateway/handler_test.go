package connectgateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	enginev1 "github.com/APPNEURAL-Engines/contracts/gen/go/engine"
	"github.com/APPNEURAL-Engines/contracts/gen/go/engine/enginev1connect"
	"github.com/APPNEURAL-Engines/engine-gateway/auth"
	"github.com/APPNEURAL-Engines/engine-gateway/grpcserver"
	"github.com/APPNEURAL-Engines/engine-gateway/protocol"
	"github.com/APPNEURAL-Engines/engine-gateway/ratelimit"
	"github.com/APPNEURAL-Engines/engine-gateway/routing"

	"connectrpc.com/connect"
)

type fakeEngineClient struct {
	lastReq *protocol.Request
}

func (f *fakeEngineClient) Forward(_ context.Context, _ *routing.EngineEndpoint, req *protocol.Request) (*protocol.Response, error) {
	f.lastReq = req
	return &protocol.Response{Status: 200, Payload: req.Payload, ContentType: "application/octet-stream"}, nil
}

func newTestRegistry(t *testing.T) *routing.Registry {
	t.Helper()
	reg := routing.NewRegistry()
	if err := reg.Register(&routing.EngineEndpoint{Name: "pdf", Version: "1.0.0", Address: "pdf-engine:8080", Protocol: "http"}); err != nil {
		t.Fatalf("failed to register endpoint: %v", err)
	}
	return reg
}

// startTestServer boots the connect handler over a real HTTP server
// (httptest), the same way the standalone grpcserver tests use bufconn --
// this is what actually proves the Connect/HTTP protocol translation
// works, not just that the delegation compiles.
func startTestServer(t *testing.T, cfg grpcserver.Config) (enginev1connect.EngineServiceClient, func()) {
	t.Helper()

	if cfg.Registry == nil {
		cfg.Registry = newTestRegistry(t)
	}
	if cfg.VersionResolver == nil {
		cfg.VersionResolver = routing.NewVersionResolver("1.0.0")
	}
	if cfg.EngineClient == nil {
		cfg.EngineClient = &fakeEngineClient{}
	}

	grpcSrv := grpcserver.New(cfg)
	path, handler := NewHTTPHandler(grpcSrv)
	mux := http.NewServeMux()
	mux.Handle(path, handler)

	server := httptest.NewServer(mux)
	client := enginev1connect.NewEngineServiceClient(server.Client(), server.URL)

	return client, server.Close
}

func TestExecute_Success(t *testing.T) {
	client, cleanup := startTestServer(t, grpcserver.Config{})
	defer cleanup()

	resp, err := client.Execute(context.Background(), connect.NewRequest(&enginev1.ExecuteRequest{
		EngineName: "pdf",
		Capability: "pdf.metadata.read",
		Payload:    []byte(`{"echo":true}`),
	}))
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if !resp.Msg.GetStatus().GetSuccess() {
		t.Fatalf("expected success status")
	}
	if string(resp.Msg.GetPayload()) != `{"echo":true}` {
		t.Fatalf("unexpected payload: %s", resp.Msg.GetPayload())
	}
}

func TestExecute_EngineNotFound(t *testing.T) {
	client, cleanup := startTestServer(t, grpcserver.Config{Registry: routing.NewRegistry()})
	defer cleanup()

	_, err := client.Execute(context.Background(), connect.NewRequest(&enginev1.ExecuteRequest{
		EngineName: "missing", Capability: "x",
	}))
	if connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("expected CodeNotFound, got %v (%v)", connect.CodeOf(err), err)
	}
}

func TestExecute_BearerTokenBridgedToAuth(t *testing.T) {
	authenticator := auth.NewJWTAuthenticator(auth.JWTConfig{Secret: "s3cret", Issuer: "appneurox", Audience: "engine-gateway"})
	token, err := authenticator.GenerateToken(auth.TokenClaims{})
	if err != nil {
		t.Fatalf("failed to generate token: %v", err)
	}

	client, cleanup := startTestServer(t, grpcserver.Config{Authenticator: authenticator})
	defer cleanup()

	req := connect.NewRequest(&enginev1.ExecuteRequest{EngineName: "pdf", Capability: "x", Payload: []byte("{}")})
	req.Header().Set("Authorization", "Bearer "+token)

	if _, err := client.Execute(context.Background(), req); err != nil {
		t.Fatalf("Execute with valid bearer token failed: %v", err)
	}
}

func TestExecute_MissingAuthRejected(t *testing.T) {
	authenticator := auth.NewJWTAuthenticator(auth.JWTConfig{Secret: "s3cret", Issuer: "appneurox", Audience: "engine-gateway"})
	client, cleanup := startTestServer(t, grpcserver.Config{Authenticator: authenticator})
	defer cleanup()

	_, err := client.Execute(context.Background(), connect.NewRequest(&enginev1.ExecuteRequest{
		EngineName: "pdf", Capability: "x",
	}))
	if connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("expected CodeUnauthenticated, got %v (%v)", connect.CodeOf(err), err)
	}
}

func TestListEngines(t *testing.T) {
	client, cleanup := startTestServer(t, grpcserver.Config{})
	defer cleanup()

	resp, err := client.ListEngines(context.Background(), connect.NewRequest(&enginev1.ListEnginesRequest{}))
	if err != nil {
		t.Fatalf("ListEngines returned error: %v", err)
	}
	if len(resp.Msg.GetEngines()) != 1 || resp.Msg.GetEngines()[0].GetName() != "pdf" {
		t.Fatalf("unexpected engines list: %+v", resp.Msg.GetEngines())
	}
}

func TestGetHealth(t *testing.T) {
	client, cleanup := startTestServer(t, grpcserver.Config{})
	defer cleanup()

	resp, err := client.GetHealth(context.Background(), connect.NewRequest(&enginev1.GetHealthRequest{EngineName: "pdf"}))
	if err != nil {
		t.Fatalf("GetHealth returned error: %v", err)
	}
	if resp.Msg.GetHealthStatus() != enginev1.HealthStatus_HEALTH_STATUS_HEALTHY {
		t.Fatalf("expected healthy status, got %v", resp.Msg.GetHealthStatus())
	}
}

func TestGetEngine_NotFound(t *testing.T) {
	client, cleanup := startTestServer(t, grpcserver.Config{Registry: routing.NewRegistry()})
	defer cleanup()

	_, err := client.GetEngine(context.Background(), connect.NewRequest(&enginev1.GetEngineRequest{EngineName: "missing"}))
	if connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("expected CodeNotFound, got %v", connect.CodeOf(err))
	}
}

func TestExecute_RateLimited(t *testing.T) {
	client, cleanup := startTestServer(t, grpcserver.Config{Limiter: ratelimit.NewTokenBucket(0, 0)})
	defer cleanup()

	_, err := client.Execute(context.Background(), connect.NewRequest(&enginev1.ExecuteRequest{
		EngineName: "pdf", Capability: "x",
	}))
	if connect.CodeOf(err) != connect.CodeResourceExhausted {
		t.Fatalf("expected CodeResourceExhausted, got %v (%v)", connect.CodeOf(err), err)
	}
}
