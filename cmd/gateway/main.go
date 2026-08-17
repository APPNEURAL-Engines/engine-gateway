// Command gateway runs the AppNeurox Engine Gateway: an HTTP/JSON front
// door and a gRPC front door (the canonical contracts.EngineService) that
// share one auth, routing, rate-limiting and circuit-breaking policy
// engine, and forward requests on to registered engines.
package main

import (
	"context"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	enginev1 "github.com/APPNEURAL-Engines/contracts/gen/go/engine"
	"github.com/APPNEURAL-Engines/engine-gateway/auth"
	"github.com/APPNEURAL-Engines/engine-gateway/config"
	"github.com/APPNEURAL-Engines/engine-gateway/gateway"
	"github.com/APPNEURAL-Engines/engine-gateway/grpcserver"
	"github.com/APPNEURAL-Engines/engine-gateway/ratelimit"
	"github.com/APPNEURAL-Engines/engine-gateway/retry"
	"github.com/APPNEURAL-Engines/engine-gateway/routing"
	"github.com/APPNEURAL-Engines/engine-gateway/telemetry"

	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"
)

func main() {
	cfg := config.Load()

	// Shared policy engine: both the HTTP and gRPC front doors are built
	// from these same instances so a request is authenticated, authorized,
	// rate limited and routed identically regardless of which protocol it
	// arrived on.
	registry := routing.NewRegistry()
	versionResolver := routing.NewVersionResolver("1.0.0")
	limiter := ratelimit.NewTokenBucket(100, 200)
	quota := ratelimit.NewQuota()
	circuitBreaker := retry.NewCircuitBreaker(5, 30*time.Second)
	logger := telemetry.NewStdLogger(telemetry.LevelInfo)
	metrics := telemetry.NewMetrics()
	engineClient := gateway.NewHTTPEngineClient(cfg.DefaultTimeout)

	var authenticator auth.Authenticator
	var authorizer auth.Authorizer
	if cfg.JWTSecret != "" {
		authenticator = auth.NewJWTAuthenticator(auth.JWTConfig{
			Secret:   cfg.JWTSecret,
			Issuer:   cfg.JWTIssuer,
			Audience: cfg.JWTAudience,
		})
		rbac := auth.NewRBACAuthorizer()
		rbac.AddPolicy(auth.Policy{
			Name: "default-allow-authenticated", Action: "*",
			Effect: auth.EffectAllow, Priority: 0,
		})
		authorizer = rbac
	} else {
		logger.Warn("GATEWAY_JWT_SECRET not set; running without authentication or authorization", nil)
	}

	for _, e := range cfg.Engines {
		if err := registry.Register(&routing.EngineEndpoint{
			Name: e.Name, Version: e.Version, Address: e.Address, Protocol: e.Protocol,
		}); err != nil {
			logger.Error("failed to register engine endpoint", err, map[string]interface{}{"engine": e.Name})
			continue
		}
		versionResolver.RegisterVersion(e.Name, e.Version)
		logger.Info("registered engine endpoint", map[string]interface{}{
			"engine": e.Name, "address": e.Address, "protocol": e.Protocol,
		})
	}

	httpGateway := gateway.New(gateway.Config{
		Address:        cfg.HTTPAddr,
		PathPrefix:     "/v1",
		DefaultTimeout: cfg.DefaultTimeout,
	})
	httpGateway.SetAuthenticator(authenticator)
	httpGateway.SetAuthorizer(authorizer)
	httpGateway.SetRateLimiter(limiter)
	httpGateway.SetEngineClient(engineClient)
	httpGateway.SetLogger(logger)
	for _, e := range cfg.Engines {
		_ = httpGateway.Registry().Register(&routing.EngineEndpoint{
			Name: e.Name, Version: e.Version, Address: e.Address, Protocol: e.Protocol,
		})
		httpGateway.VersionResolver().RegisterVersion(e.Name, e.Version)
	}

	grpcSrv := grpc.NewServer()
	engineServer := grpcserver.New(grpcserver.Config{
		Authenticator:   authenticator,
		Authorizer:      authorizer,
		Limiter:         limiter,
		Quota:           quota,
		Registry:        registry,
		VersionResolver: versionResolver,
		CircuitBreaker:  circuitBreaker,
		EngineClient:    engineClient,
		Logger:          logger,
		Metrics:         metrics,
		DefaultTimeout:  cfg.DefaultTimeout,
	})
	enginev1.RegisterEngineServiceServer(grpcSrv, engineServer)
	reflection.Register(grpcSrv)

	grpcListener, err := net.Listen("tcp", cfg.GRPCAddr)
	if err != nil {
		log.Fatalf("failed to listen on %s: %v", cfg.GRPCAddr, err)
	}

	go func() {
		logger.Info("grpc server starting", map[string]interface{}{"address": cfg.GRPCAddr})
		if err := grpcSrv.Serve(grpcListener); err != nil {
			log.Fatalf("grpc server failed: %v", err)
		}
	}()

	go func() {
		if err := httpGateway.Start(); err != nil && err.Error() != "http: Server closed" {
			log.Fatalf("http gateway failed: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop

	logger.Info("shutting down", nil)
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	grpcSrv.GracefulStop()
	if err := httpGateway.Shutdown(shutdownCtx); err != nil {
		logger.Error("http gateway shutdown error", err, nil)
	}
}
