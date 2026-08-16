package gateway

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/APPNEURAL-Engines/engine-gateway/auth"
	"github.com/APPNEURAL-Engines/engine-gateway/protocol"
	"github.com/APPNEURAL-Engines/engine-gateway/ratelimit"
	"github.com/APPNEURAL-Engines/engine-gateway/retry"
	"github.com/APPNEURAL-Engines/engine-gateway/routing"
	"github.com/golang-jwt/jwt/v5"
)

// mockEngineClient is a configurable EngineClient for tests.
type mockEngineClient struct {
	resp *protocol.Response
	err  error
}

func (m *mockEngineClient) Forward(ctx context.Context, endpoint *routing.EngineEndpoint, req *protocol.Request) (*protocol.Response, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.resp, nil
}

// newTestGateway builds a gateway with one registered pdf engine.
func newTestGateway() *Gateway {
	gw := New(Config{PathPrefix: "/v1"})
	err := gw.Registry().Register(&routing.EngineEndpoint{
		Name:     "pdf",
		Version:  "1.0.0",
		Address:  "pdf-engine:8080",
		Protocol: "grpc",
	})
	if err != nil {
		panic(err)
	}
	gw.VersionResolver().RegisterVersion("pdf", "1.0.0")
	gw.SetEngineClient(&mockEngineClient{
		resp: &protocol.Response{
			Status:      http.StatusOK,
			Payload:     []byte(`{"result":"ok"}`),
			ContentType: "application/json",
		},
	})
	return gw
}

// doRequest sends a request through the gateway handler.
func doRequest(gw *Gateway, method string, path string, body []byte, headers map[string]string) *httptest.ResponseRecorder {
	var reader *bytes.Reader
	if body == nil {
		reader = bytes.NewReader(nil)
	} else {
		reader = bytes.NewReader(body)
	}

	req := httptest.NewRequest(method, path, reader)
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	rec := httptest.NewRecorder()
	gw.Handler().ServeHTTP(rec, req)
	return rec
}

func TestHealthEndpoint(t *testing.T) {
	gw := newTestGateway()

	rec := doRequest(gw, http.MethodGet, "/health", nil, nil)
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
	if rec.Body.String() != `{"status":"healthy"}` {
		t.Errorf("unexpected body: %s", rec.Body.String())
	}
}

func TestGateway_HappyPath(t *testing.T) {
	gw := newTestGateway()

	rec := doRequest(gw, http.MethodPost, "/v1/pdf/convert", []byte(`{"format":"text"}`), map[string]string{
		"Content-Type":  "application/json",
		"X-Tenant-ID":   "tenant-1",
		"X-Trace-ID":    "trace-1",
		"X-User-ID":     "user-1",
	})

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != `{"result":"ok"}` {
		t.Errorf("unexpected body: %s", rec.Body.String())
	}

	// Metrics should be recorded
	if gw.Metrics().RequestCount != 1 || gw.Metrics().SuccessCount != 1 {
		t.Errorf("unexpected metrics: %+v", gw.Metrics().Snapshot())
	}
}

func TestGateway_InvalidPath(t *testing.T) {
	gw := newTestGateway()

	rec := doRequest(gw, http.MethodPost, "/v1/pdf", nil, nil)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

func TestGateway_Unauthenticated(t *testing.T) {
	gw := newTestGateway()
	gw.SetAuthenticator(auth.NewJWTAuthenticator(auth.JWTConfig{
		Secret:   "test-secret",
		Issuer:   "test-issuer",
		Audience: "test-audience",
	}))

	rec := doRequest(gw, http.MethodPost, "/v1/pdf/convert", nil, nil)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "UNAUTHENTICATED") {
		t.Errorf("expected UNAUTHENTICATED code, got %s", rec.Body.String())
	}
}

func TestGateway_InvalidToken(t *testing.T) {
	gw := newTestGateway()
	gw.SetAuthenticator(auth.NewJWTAuthenticator(auth.JWTConfig{
		Secret:   "test-secret",
		Issuer:   "test-issuer",
		Audience: "test-audience",
	}))

	rec := doRequest(gw, http.MethodPost, "/v1/pdf/convert", nil, map[string]string{
		"Authorization": "Bearer invalid-token",
	})
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rec.Code)
	}
}

func TestGateway_AuthenticatedSuccess(t *testing.T) {
	gw := newTestGateway()
	jwtAuth := auth.NewJWTAuthenticator(auth.JWTConfig{
		Secret:   "test-secret",
		Issuer:   "test-issuer",
		Audience: "test-audience",
	})
	gw.SetAuthenticator(jwtAuth)

	token, err := jwtAuth.GenerateToken(auth.TokenClaims{
		RegisteredClaims: jwt.RegisteredClaims{Subject: "user-1"},
		TenantID:         "tenant-1",
		Roles:            []string{"admin"},
	})
	if err != nil {
		t.Fatalf("GenerateToken failed: %v", err)
	}

	rec := doRequest(gw, http.MethodPost, "/v1/pdf/convert", nil, map[string]string{
		"Authorization": "Bearer " + token,
	})
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestGateway_PermissionDenied(t *testing.T) {
	gw := newTestGateway()
	jwtAuth := auth.NewJWTAuthenticator(auth.JWTConfig{
		Secret:   "test-secret",
		Issuer:   "test-issuer",
		Audience: "test-audience",
	})
	gw.SetAuthenticator(jwtAuth)

	// Only allow storage:put, not pdf:convert
	authorizer := auth.NewRBACAuthorizer()
	authorizer.AddPolicy(auth.Policy{
		Name:     "storage-only",
		Action:   "storage:put",
		Roles:    []string{"admin"},
		Effect:   auth.EffectAllow,
		Priority: 1,
	})
	gw.SetAuthorizer(authorizer)

	token, _ := jwtAuth.GenerateToken(auth.TokenClaims{
		RegisteredClaims: jwt.RegisteredClaims{Subject: "user-1"},
		Roles:            []string{"admin"},
	})

	rec := doRequest(gw, http.MethodPost, "/v1/pdf/convert", nil, map[string]string{
		"Authorization": "Bearer " + token,
	})
	if rec.Code != http.StatusForbidden {
		t.Errorf("expected 403, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "PERMISSION_DENIED") {
		t.Errorf("expected PERMISSION_DENIED code, got %s", rec.Body.String())
	}
}

func TestGateway_Authorized(t *testing.T) {
	gw := newTestGateway()
	jwtAuth := auth.NewJWTAuthenticator(auth.JWTConfig{
		Secret:   "test-secret",
		Issuer:   "test-issuer",
		Audience: "test-audience",
	})
	gw.SetAuthenticator(jwtAuth)

	authorizer := auth.NewRBACAuthorizer()
	authorizer.AddPolicy(auth.Policy{
		Name:     "pdf-convert",
		Action:   "pdf:convert",
		Roles:    []string{"admin"},
		Effect:   auth.EffectAllow,
		Priority: 1,
	})
	gw.SetAuthorizer(authorizer)

	token, _ := jwtAuth.GenerateToken(auth.TokenClaims{
		RegisteredClaims: jwt.RegisteredClaims{Subject: "user-1"},
		Roles:            []string{"admin"},
	})

	rec := doRequest(gw, http.MethodPost, "/v1/pdf/convert", nil, map[string]string{
		"Authorization": "Bearer " + token,
	})
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestGateway_EngineNotFound(t *testing.T) {
	gw := New(Config{PathPrefix: "/v1"})
	gw.SetEngineClient(&mockEngineClient{
		resp: &protocol.Response{Status: http.StatusOK, Payload: []byte(`{}`)},
	})

	rec := doRequest(gw, http.MethodPost, "/v1/pdf/convert", nil, nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "ENGINE_NOT_FOUND") {
		t.Errorf("expected ENGINE_NOT_FOUND code, got %s", rec.Body.String())
	}
}

func TestGateway_RateLimited(t *testing.T) {
	gw := newTestGateway()
	gw.SetRateLimiter(ratelimit.NewTokenBucket(1, 1)) // 1 token/sec, capacity 1

	first := doRequest(gw, http.MethodPost, "/v1/pdf/convert", nil, nil)
	if first.Code != http.StatusOK {
		t.Fatalf("expected 200 on first request, got %d", first.Code)
	}

	second := doRequest(gw, http.MethodPost, "/v1/pdf/convert", nil, nil)
	if second.Code != http.StatusTooManyRequests {
		t.Errorf("expected 429 on second request, got %d", second.Code)
	}
	if !strings.Contains(second.Body.String(), "RATE_LIMITED") {
		t.Errorf("expected RATE_LIMITED code, got %s", second.Body.String())
	}
}

func TestGateway_QuotaExceeded(t *testing.T) {
	gw := newTestGateway()
	gw.Quota().SetLimit("tenant-1", "requests", 1)

	headers := map[string]string{"X-Tenant-ID": "tenant-1"}

	first := doRequest(gw, http.MethodPost, "/v1/pdf/convert", nil, headers)
	if first.Code != http.StatusOK {
		t.Fatalf("expected 200 on first request, got %d", first.Code)
	}

	second := doRequest(gw, http.MethodPost, "/v1/pdf/convert", nil, headers)
	if second.Code != http.StatusTooManyRequests {
		t.Errorf("expected 429 on second request, got %d", second.Code)
	}
	if !strings.Contains(second.Body.String(), "QUOTA_EXCEEDED") {
		t.Errorf("expected QUOTA_EXCEEDED code, got %s", second.Body.String())
	}
}

func TestGateway_CircuitBreakerOpen(t *testing.T) {
	gw := newTestGateway()
	gw.circuitBreaker = retry.NewCircuitBreaker(2, time.Minute)
	gw.SetEngineClient(&mockEngineClient{err: errors.New("engine down")})

	for i := 0; i < 2; i++ {
		rec := doRequest(gw, http.MethodPost, "/v1/pdf/convert", nil, nil)
		if rec.Code != http.StatusBadGateway {
			t.Errorf("request %d: expected 502, got %d", i+1, rec.Code)
		}
	}

	// Circuit is now open
	rec := doRequest(gw, http.MethodPost, "/v1/pdf/convert", nil, nil)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 when circuit open, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "ENGINE_UNAVAILABLE") {
		t.Errorf("expected ENGINE_UNAVAILABLE code, got %s", rec.Body.String())
	}
}

func TestHTTPEngineClient_Forward(t *testing.T) {
	var gotPath, gotTenant, gotTrace string
	engine := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotTenant = r.Header.Get("X-Tenant-ID")
		gotTrace = r.Header.Get("X-Trace-ID")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok":true}`))
	}))
	defer engine.Close()

	client := NewHTTPEngineClient(5 * time.Second)
	resp, err := client.Forward(context.Background(), &routing.EngineEndpoint{
		Name:     "pdf",
		Address:  engine.URL,
		Protocol: "http",
	}, &protocol.Request{
		Capability: "convert",
		Payload:    []byte(`{"format":"text"}`),
		TenantID:   "tenant-1",
		TraceID:    "trace-1",
	})
	if err != nil {
		t.Fatalf("Forward failed: %v", err)
	}

	if gotPath != "/convert" {
		t.Errorf("expected /convert, got %s", gotPath)
	}
	if gotTenant != "tenant-1" {
		t.Errorf("expected tenant header, got %s", gotTenant)
	}
	if gotTrace != "trace-1" {
		t.Errorf("expected trace header, got %s", gotTrace)
	}
	if resp.Status != http.StatusOK || string(resp.Payload) != `{"ok":true}` {
		t.Errorf("unexpected response: %+v", resp)
	}
}

func TestHTTPEngineClient_Forward_EnsureScheme(t *testing.T) {
	engine := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{}`))
	}))
	defer engine.Close()

	// Strip the scheme from the address to test ensureScheme
	addr := strings.TrimPrefix(engine.URL, "http://")

	client := NewHTTPEngineClient(5 * time.Second)
	if _, err := client.Forward(context.Background(), &routing.EngineEndpoint{
		Name:     "pdf",
		Address:  addr,
		Protocol: "",
	}, &protocol.Request{Capability: "convert"}); err != nil {
		t.Fatalf("Forward without scheme failed: %v", err)
	}
}

func TestGateway_ShutdownWithoutStart(t *testing.T) {
	gw := newTestGateway()
	if err := gw.Shutdown(context.Background()); err != nil {
		t.Errorf("Shutdown without Start should be a no-op, got %v", err)
	}
}

func TestSubdomainFromHost(t *testing.T) {
	cases := map[string]string{
		"tenant-1.appneurox.io": "tenant-1",
		"appneurox.io":          "",
		"localhost:8080":        "",
		"":                      "",
	}

	for host, expected := range cases {
		if got := subdomainFromHost(host); got != expected {
			t.Errorf("subdomainFromHost(%q) = %q, want %q", host, got, expected)
		}
	}
}
