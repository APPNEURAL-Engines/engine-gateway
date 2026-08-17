// Package config loads engine-gateway's runtime configuration from
// environment variables, following the platform's 12-factor convention.
package config

import (
	"os"
	"strings"
	"time"
)

// Config holds the settings needed to start the gateway's HTTP and gRPC
// front doors and bootstrap its static engine registry.
type Config struct {
	HTTPAddr string
	GRPCAddr string

	JWTSecret   string
	JWTIssuer   string
	JWTAudience string

	DefaultTimeout time.Duration

	// Engines is a static bootstrap list of "name=protocol://address"
	// entries, e.g. "pdf=http://pdf-engine:8080,storage=http://storage-engine:8080".
	// Real deployments should replace this with dynamic service discovery.
	Engines []EngineEndpoint
}

// EngineEndpoint is one statically configured engine target.
type EngineEndpoint struct {
	Name     string
	Protocol string
	Address  string
	Version  string
}

// Load reads configuration from the environment, applying defaults for
// anything unset.
func Load() Config {
	cfg := Config{
		HTTPAddr:       getEnv("GATEWAY_HTTP_ADDR", ":8080"),
		GRPCAddr:       getEnv("GATEWAY_GRPC_ADDR", ":9090"),
		JWTSecret:      getEnv("GATEWAY_JWT_SECRET", ""),
		JWTIssuer:      getEnv("GATEWAY_JWT_ISSUER", "appneurox"),
		JWTAudience:    getEnv("GATEWAY_JWT_AUDIENCE", "engine-gateway"),
		DefaultTimeout: getEnvDuration("GATEWAY_DEFAULT_TIMEOUT", 10*time.Second),
		Engines:        parseEngines(getEnv("GATEWAY_ENGINES", "")),
	}
	return cfg
}

func parseEngines(raw string) []EngineEndpoint {
	if raw == "" {
		return nil
	}

	var endpoints []EngineEndpoint
	for _, entry := range strings.Split(raw, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}

		nameAndTarget := strings.SplitN(entry, "=", 2)
		if len(nameAndTarget) != 2 {
			continue
		}
		name := strings.TrimSpace(nameAndTarget[0])
		target := strings.TrimSpace(nameAndTarget[1])

		protocol := "http"
		address := target
		if idx := strings.Index(target, "://"); idx != -1 {
			protocol = target[:idx]
			address = target[idx+3:]
		}

		endpoints = append(endpoints, EngineEndpoint{
			Name:     name,
			Protocol: protocol,
			Address:  address,
			Version:  "1.0.0",
		})
	}
	return endpoints
}

func getEnv(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return fallback
}

func getEnvDuration(key string, fallback time.Duration) time.Duration {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return fallback
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return fallback
	}
	return d
}
