// Command basic demonstrates a complete engine gateway setup:
// a mock PDF engine, JWT auth, RBAC, rate limiting, and a live request.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/APPNEURAL-Engines/engine-gateway/auth"
	"github.com/APPNEURAL-Engines/engine-gateway/gateway"
	"github.com/APPNEURAL-Engines/engine-gateway/ratelimit"
	"github.com/APPNEURAL-Engines/engine-gateway/routing"
	"github.com/golang-jwt/jwt/v5"
)

func main() {
	// 1. Start a mock PDF engine (stands in for any language/runtime).
	mockAddr := startMockEngine()
	log.Printf("mock pdf engine listening on %s", mockAddr)

	// 2. Build the gateway.
	gw := gateway.New(gateway.Config{
		Address:    "127.0.0.1:8080",
		PathPrefix: "/v1",
	})

	// 3. Authentication: JWT bearer tokens.
	jwtAuth := auth.NewJWTAuthenticator(auth.JWTConfig{
		Secret:   "dev-secret-change-me",
		Issuer:   "appneurox",
		Audience: "engine-gateway",
	})
	gw.SetAuthenticator(jwtAuth)

	// 4. Authorization: RBAC — admins may call any pdf capability.
	authorizer := auth.NewRBACAuthorizer()
	authorizer.AddPolicy(auth.Policy{
		Name:     "pdf-admin",
		Action:   "pdf:*",
		Roles:    []string{"admin"},
		Effect:   auth.EffectAllow,
		Priority: 1,
	})
	gw.SetAuthorizer(authorizer)

	// 5. Rate limiting and tenant quotas.
	gw.SetRateLimiter(ratelimit.NewTokenBucket(100, 200))
	gw.Quota().SetLimit("tenant-1", "requests", 10000)

	// 6. Register the engine instance.
	if err := gw.Registry().Register(&routing.EngineEndpoint{
		Name:     "pdf",
		Version:  "1.0.0",
		Address:  mockAddr,
		Protocol: "http",
	}); err != nil {
		log.Fatalf("register endpoint: %v", err)
	}
	gw.VersionResolver().RegisterVersion("pdf", "1.0.0")

	// 7. Forward engine calls over HTTP.
	gw.SetEngineClient(gateway.NewHTTPEngineClient(10 * time.Second))

	// 8. Start the gateway.
	go func() {
		if err := gw.Start(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("gateway: %v", err)
		}
	}()
	log.Printf("engine gateway listening on http://%s", gwAddress())

	// Give the server a moment to bind.
	time.Sleep(200 * time.Millisecond)

	// 9. Issue a request with a valid admin token.
	token, err := jwtAuth.GenerateToken(auth.TokenClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   "user-1",
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(15 * time.Minute)),
		},
		TenantID: "tenant-1",
		Roles:    []string{"admin"},
	})
	if err != nil {
		log.Fatalf("generate token: %v", err)
	}

	status, body := callGateway(token)
	log.Printf("gateway response: %d %s", status, body)

	// 10. Wait for SIGINT/SIGTERM, then shut down gracefully.
	done := make(chan os.Signal, 1)
	signal.Notify(done, syscall.SIGINT, syscall.SIGTERM)
	<-done

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := gw.Shutdown(ctx); err != nil {
		log.Printf("shutdown: %v", err)
	}
	log.Println("gateway stopped")
}

// startMockEngine starts a fake PDF engine and returns its address.
func startMockEngine() string {
	mux := http.NewServeMux()

	mux.HandleFunc("POST /convert", func(w http.ResponseWriter, r *http.Request) {
		payload, _ := io.ReadAll(r.Body)
		log.Printf("mock engine /convert received: %s", payload)
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"status": "converted",
			"pages":  3,
		})
	})

	mux.HandleFunc("POST /extract_text", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"text": "mock extracted text",
		})
	})

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		log.Fatalf("mock engine listen: %v", err)
	}

	go func() {
		if err := http.Serve(ln, mux); err != nil {
			log.Printf("mock engine stopped: %v", err)
		}
	}()

	return ln.Addr().String()
}

// callGateway sends a convert request through the gateway.
func callGateway(token string) (int, string) {
	body := []byte(`{"format":"pdf","input":"report.docx"}`)
	req, err := http.NewRequest(http.MethodPost, "http://"+gwAddress()+"/v1/pdf/convert", bytes.NewReader(body))
	if err != nil {
		log.Fatalf("build request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Trace-ID", "example-trace-1")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Fatalf("gateway call: %v", err)
	}
	defer resp.Body.Close()

	data, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(data)
}

// gwAddress returns the gateway listen address.
func gwAddress() string {
	return "127.0.0.1:8080"
}

// writeJSON writes a JSON response.
func writeJSON(w http.ResponseWriter, status int, value interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		fmt.Fprintf(w, `{"error":%q}`, err.Error())
	}
}
