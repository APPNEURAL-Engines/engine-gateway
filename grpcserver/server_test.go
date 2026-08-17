package grpcserver

import (
	"context"
	"net"
	"strings"
	"testing"

	commonv1 "github.com/APPNEURAL-Engines/contracts/gen/go/common"
	enginev1 "github.com/APPNEURAL-Engines/contracts/gen/go/engine"
	"github.com/APPNEURAL-Engines/engine-gateway/auth"
	"github.com/APPNEURAL-Engines/engine-gateway/protocol"
	"github.com/APPNEURAL-Engines/engine-gateway/ratelimit"
	"github.com/APPNEURAL-Engines/engine-gateway/routing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

// fakeEngineClient echoes the request payload back, recording the last
// forwarded request for assertions.
type fakeEngineClient struct {
	lastReq *protocol.Request
	err     error
}

func (f *fakeEngineClient) Forward(_ context.Context, _ *routing.EngineEndpoint, req *protocol.Request) (*protocol.Response, error) {
	f.lastReq = req
	if f.err != nil {
		return nil, f.err
	}
	return &protocol.Response{Status: 200, Payload: req.Payload, ContentType: "application/octet-stream"}, nil
}

// scriptedEngineClient returns a fixed HTTP status/payload regardless of the
// request, simulating what a real HTTP adapter (pdf-engine, storage-engine,
// rule-engine) sends back on success or failure.
type scriptedEngineClient struct {
	status  int
	payload []byte
}

func (s *scriptedEngineClient) Forward(context.Context, *routing.EngineEndpoint, *protocol.Request) (*protocol.Response, error) {
	return &protocol.Response{Status: s.status, Payload: s.payload, ContentType: "application/json"}, nil
}

func startTestServer(t *testing.T, cfg Config) (enginev1.EngineServiceClient, func()) {
	t.Helper()

	lis := bufconn.Listen(1024 * 1024)
	grpcSrv := grpc.NewServer()
	enginev1.RegisterEngineServiceServer(grpcSrv, New(cfg))

	go func() {
		_ = grpcSrv.Serve(lis)
	}()

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("failed to dial bufconn: %v", err)
	}

	cleanup := func() {
		conn.Close()
		grpcSrv.Stop()
	}
	return enginev1.NewEngineServiceClient(conn), cleanup
}

func newTestRegistry(t *testing.T) *routing.Registry {
	t.Helper()
	reg := routing.NewRegistry()
	if err := reg.Register(&routing.EngineEndpoint{Name: "pdf", Version: "1.0.0", Address: "pdf-engine:8080", Protocol: "http"}); err != nil {
		t.Fatalf("failed to register endpoint: %v", err)
	}
	return reg
}

func TestExecute_Success(t *testing.T) {
	reg := newTestRegistry(t)
	versions := routing.NewVersionResolver("1.0.0")
	versions.RegisterVersion("pdf", "1.0.0")
	client := &fakeEngineClient{}

	svcClient, cleanup := startTestServer(t, Config{
		Registry:        reg,
		VersionResolver: versions,
		EngineClient:    client,
	})
	defer cleanup()

	resp, err := svcClient.Execute(context.Background(), &enginev1.ExecuteRequest{
		EngineName: "pdf",
		Capability: "convert",
		Payload:    []byte("hello"),
		RequestMetadata: &commonv1.RequestMetadata{
			Id: "req-1", TenantId: "tenant-1",
		},
	})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if !resp.GetStatus().GetSuccess() {
		t.Fatalf("expected success status")
	}
	if string(resp.GetPayload()) != "hello" {
		t.Fatalf("expected echoed payload, got %q", resp.GetPayload())
	}
	if resp.GetResponseMetadata().GetEngineName() != "pdf" {
		t.Fatalf("expected engine name pdf, got %q", resp.GetResponseMetadata().GetEngineName())
	}
	if client.lastReq.TenantID != "tenant-1" {
		t.Fatalf("expected tenant to be forwarded, got %q", client.lastReq.TenantID)
	}
}

func TestExecute_MissingEngineName(t *testing.T) {
	svcClient, cleanup := startTestServer(t, Config{
		Registry:        routing.NewRegistry(),
		VersionResolver: routing.NewVersionResolver("1.0.0"),
		EngineClient:    &fakeEngineClient{},
	})
	defer cleanup()

	_, err := svcClient.Execute(context.Background(), &enginev1.ExecuteRequest{Capability: "convert"})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", err)
	}
}

func TestExecute_EngineNotFound(t *testing.T) {
	svcClient, cleanup := startTestServer(t, Config{
		Registry:        routing.NewRegistry(),
		VersionResolver: routing.NewVersionResolver("1.0.0"),
		EngineClient:    &fakeEngineClient{},
	})
	defer cleanup()

	_, err := svcClient.Execute(context.Background(), &enginev1.ExecuteRequest{EngineName: "missing", Capability: "convert"})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("expected NotFound, got %v", err)
	}
}

func TestExecute_Unauthenticated(t *testing.T) {
	authenticator := auth.NewJWTAuthenticator(auth.JWTConfig{Secret: "s3cret", Issuer: "appneurox", Audience: "engine-gateway"})

	svcClient, cleanup := startTestServer(t, Config{
		Registry:        newTestRegistry(t),
		VersionResolver: routing.NewVersionResolver("1.0.0"),
		EngineClient:    &fakeEngineClient{},
		Authenticator:   authenticator,
	})
	defer cleanup()

	_, err := svcClient.Execute(context.Background(), &enginev1.ExecuteRequest{EngineName: "pdf", Capability: "convert"})
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("expected Unauthenticated, got %v", err)
	}
}

func TestExecute_RateLimited(t *testing.T) {
	svcClient, cleanup := startTestServer(t, Config{
		Registry:        newTestRegistry(t),
		VersionResolver: routing.NewVersionResolver("1.0.0"),
		EngineClient:    &fakeEngineClient{},
		Limiter:         ratelimit.NewTokenBucket(0, 0),
	})
	defer cleanup()

	_, err := svcClient.Execute(context.Background(), &enginev1.ExecuteRequest{EngineName: "pdf", Capability: "convert"})
	if status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("expected ResourceExhausted, got %v", err)
	}
}

func TestGetHealth(t *testing.T) {
	svcClient, cleanup := startTestServer(t, Config{
		Registry:        newTestRegistry(t),
		VersionResolver: routing.NewVersionResolver("1.0.0"),
		EngineClient:    &fakeEngineClient{},
	})
	defer cleanup()

	resp, err := svcClient.GetHealth(context.Background(), &enginev1.GetHealthRequest{EngineName: "pdf"})
	if err != nil {
		t.Fatalf("GetHealth returned error: %v", err)
	}
	if resp.GetHealthStatus() != enginev1.HealthStatus_HEALTH_STATUS_HEALTHY {
		t.Fatalf("expected healthy status, got %v", resp.GetHealthStatus())
	}
	if len(resp.GetEngineHealth()) != 1 {
		t.Fatalf("expected 1 engine health entry, got %d", len(resp.GetEngineHealth()))
	}
}

func TestListEngines(t *testing.T) {
	svcClient, cleanup := startTestServer(t, Config{
		Registry:        newTestRegistry(t),
		VersionResolver: routing.NewVersionResolver("1.0.0"),
		EngineClient:    &fakeEngineClient{},
	})
	defer cleanup()

	resp, err := svcClient.ListEngines(context.Background(), &enginev1.ListEnginesRequest{})
	if err != nil {
		t.Fatalf("ListEngines returned error: %v", err)
	}
	if len(resp.GetEngines()) != 1 || resp.GetEngines()[0].GetName() != "pdf" {
		t.Fatalf("unexpected engines list: %+v", resp.GetEngines())
	}
}

func TestGetEngine_NotFound(t *testing.T) {
	svcClient, cleanup := startTestServer(t, Config{
		Registry:        routing.NewRegistry(),
		VersionResolver: routing.NewVersionResolver("1.0.0"),
		EngineClient:    &fakeEngineClient{},
	})
	defer cleanup()

	_, err := svcClient.GetEngine(context.Background(), &enginev1.GetEngineRequest{EngineName: "missing"})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("expected NotFound, got %v", err)
	}
}

// The following two tests guard against the bug this file's Execute() used
// to have: it always reported Status.Success: true and never returned a
// gRPC error, no matter what HTTP status the engine adapter actually
// returned -- discovered only once a real SDK made a real call through a
// real gateway to a real (failing) engine and silently got back "success".

func TestExecute_AdapterErrorBecomesGRPCError(t *testing.T) {
	scripted := &scriptedEngineClient{
		status:  422,
		payload: []byte(`{"error":{"code":"UNKNOWN_OPERATOR","message":"unknown operator 'nope'","details":{"ruleId":"r1"}}}`),
	}
	svcClient, cleanup := startTestServer(t, Config{
		Registry:        newTestRegistry(t),
		VersionResolver: routing.NewVersionResolver("1.0.0"),
		EngineClient:    scripted,
	})
	defer cleanup()

	_, err := svcClient.Execute(context.Background(), &enginev1.ExecuteRequest{
		EngineName: "pdf", Capability: "rule.evaluate", Payload: []byte(`{}`),
	})
	if err == nil {
		t.Fatal("expected an error, got success")
	}
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("expected FailedPrecondition for HTTP 422, got %v", status.Code(err))
	}
	msg := status.Convert(err).Message()
	if !strings.Contains(msg, "UNKNOWN_OPERATOR") || !strings.Contains(msg, "unknown operator 'nope'") {
		t.Fatalf("expected error message to carry the adapter's code and message, got %q", msg)
	}
}

func TestExecute_UnwrapsDataEnvelopeOnSuccess(t *testing.T) {
	scripted := &scriptedEngineClient{
		status:  200,
		payload: []byte(`{"data":{"matched":true,"matchedRuleIds":["vip-discount"]}}`),
	}
	svcClient, cleanup := startTestServer(t, Config{
		Registry:        newTestRegistry(t),
		VersionResolver: routing.NewVersionResolver("1.0.0"),
		EngineClient:    scripted,
	})
	defer cleanup()

	resp, err := svcClient.Execute(context.Background(), &enginev1.ExecuteRequest{
		EngineName: "pdf", Capability: "rule.evaluate", Payload: []byte(`{}`),
	})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if !resp.GetStatus().GetSuccess() {
		t.Fatal("expected success status")
	}
	want := `{"matched":true,"matchedRuleIds":["vip-discount"]}`
	if string(resp.GetPayload()) != want {
		t.Fatalf("expected unwrapped data payload %q, got %q", want, resp.GetPayload())
	}
}

func TestExecute_NonEnvelopedErrorStillBecomesGRPCError(t *testing.T) {
	// An adapter that doesn't follow the {"error": {...}} convention (or
	// returns a plain-text body) still must not be reported as success.
	scripted := &scriptedEngineClient{status: 500, payload: []byte("internal server error")}
	svcClient, cleanup := startTestServer(t, Config{
		Registry:        newTestRegistry(t),
		VersionResolver: routing.NewVersionResolver("1.0.0"),
		EngineClient:    scripted,
	})
	defer cleanup()

	_, err := svcClient.Execute(context.Background(), &enginev1.ExecuteRequest{
		EngineName: "pdf", Capability: "x", Payload: []byte(`{}`),
	})
	if status.Code(err) != codes.Internal {
		t.Fatalf("expected Internal for HTTP 500, got %v (%v)", status.Code(err), err)
	}
}

func TestGRPCCodeForHTTPStatus(t *testing.T) {
	cases := map[int]codes.Code{
		400: codes.InvalidArgument,
		401: codes.Unauthenticated,
		403: codes.PermissionDenied,
		404: codes.NotFound,
		408: codes.DeadlineExceeded,
		409: codes.Aborted,
		413: codes.ResourceExhausted,
		429: codes.ResourceExhausted,
		501: codes.Unimplemented,
		502: codes.Unavailable,
		503: codes.Unavailable,
		422: codes.FailedPrecondition,
		500: codes.Internal,
	}
	for httpStatus, want := range cases {
		if got := grpcCodeForHTTPStatus(httpStatus); got != want {
			t.Errorf("grpcCodeForHTTPStatus(%d) = %v, want %v", httpStatus, got, want)
		}
	}
}
