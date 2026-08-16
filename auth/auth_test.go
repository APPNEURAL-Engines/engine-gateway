package auth

import (
	"context"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func TestJWTAuthenticator_GenerateAndValidate(t *testing.T) {
	auth := NewJWTAuthenticator(JWTConfig{
		Secret:   "test-secret",
		Issuer:   "test-issuer",
		Audience: "test-audience",
	})

	token, err := auth.GenerateToken(TokenClaims{
		TenantID:    "tenant-1",
		Roles:       []string{"admin"},
		Permissions: []string{"pdf:convert"},
	})
	if err != nil {
		t.Fatalf("GenerateToken failed: %v", err)
	}

	claims, err := auth.ValidateToken(context.Background(), token)
	if err != nil {
		t.Fatalf("ValidateToken failed: %v", err)
	}

	if claims.TenantID != "tenant-1" {
		t.Errorf("expected tenant-1, got %s", claims.TenantID)
	}
	if len(claims.Roles) != 1 || claims.Roles[0] != "admin" {
		t.Errorf("unexpected roles: %v", claims.Roles)
	}
}

func TestJWTAuthenticator_InvalidToken(t *testing.T) {
	auth := NewJWTAuthenticator(JWTConfig{
		Secret:   "test-secret",
		Issuer:   "test-issuer",
		Audience: "test-audience",
	})

	_, err := auth.ValidateToken(context.Background(), "invalid-token")
	if err == nil {
		t.Error("expected error for invalid token")
	}
}

func TestJWTAuthenticator_Authenticate(t *testing.T) {
	auth := NewJWTAuthenticator(JWTConfig{
		Secret:   "test-secret",
		Issuer:   "test-issuer",
		Audience: "test-audience",
	})

	token, _ := auth.GenerateToken(TokenClaims{
		RegisteredClaims: jwt.RegisteredClaims{Subject: "user-1"},
		TenantID:         "tenant-1",
		Roles:            []string{"admin"},
		Scopes:           []string{"storage:put"},
	})

	identity, err := auth.Authenticate(context.Background(), Credentials{
		Type:  CredentialTypeBearer,
		Token: token,
	})
	if err != nil {
		t.Fatalf("Authenticate failed: %v", err)
	}

	if identity.ID != "user-1" {
		t.Errorf("expected user-1, got %s", identity.ID)
	}
	if identity.TenantID != "tenant-1" {
		t.Errorf("expected tenant-1, got %s", identity.TenantID)
	}

	// Scopes should be added to permissions
	found := false
	for _, p := range identity.Permissions {
		if p == "storage:put" {
			found = true
		}
	}
	if !found {
		t.Error("expected scope to be in permissions")
	}
}

func TestAPIKeyAuthenticator(t *testing.T) {
	auth := NewAPIKeyAuthenticator()
	auth.AddKey("key-123", &Identity{
		ID:       "service-1",
		Type:     IdentityTypeService,
		TenantID: "tenant-1",
	})

	identity, err := auth.Authenticate(context.Background(), Credentials{
		Type:   CredentialTypeAPIKey,
		APIKey: "key-123",
	})
	if err != nil {
		t.Fatalf("Authenticate failed: %v", err)
	}

	if identity.ID != "service-1" {
		t.Errorf("expected service-1, got %s", identity.ID)
	}

	// Invalid key
	_, err = auth.Authenticate(context.Background(), Credentials{
		Type:   CredentialTypeAPIKey,
		APIKey: "wrong-key",
	})
	if err == nil {
		t.Error("expected error for invalid key")
	}
}

func TestChainAuthenticator(t *testing.T) {
	apiKeyAuth := NewAPIKeyAuthenticator()
	apiKeyAuth.AddKey("key-1", &Identity{ID: "svc-1", Type: IdentityTypeService})

	jwtAuth := NewJWTAuthenticator(JWTConfig{Secret: "s", Issuer: "i", Audience: "a"})
	chain := NewChainAuthenticator(apiKeyAuth, jwtAuth)

	// API key succeeds
	identity, err := chain.Authenticate(context.Background(), Credentials{
		Type:   CredentialTypeAPIKey,
		APIKey: "key-1",
	})
	if err != nil {
		t.Fatalf("API key authenticate failed: %v", err)
	}
	if identity.ID != "svc-1" {
		t.Errorf("expected svc-1, got %s", identity.ID)
	}

	// Unsupported type fails through chain
	_, err = chain.Authenticate(context.Background(), Credentials{
		Type:  CredentialTypeBearer,
		Token: "invalid",
	})
	if err == nil {
		t.Error("expected error for invalid bearer token")
	}
}

func TestRBACAuthorizer(t *testing.T) {
	auth := NewRBACAuthorizer()

	auth.AddPolicy(Policy{
		Name:     "allow-pdf-admin",
		Action:   "pdf:*",
		Roles:    []string{"admin"},
		Effect:   EffectAllow,
		Priority: 10,
	})

	auth.AddPolicy(Policy{
		Name:     "deny-pdf-delete",
		Action:   "pdf:delete",
		Roles:    []string{"*"},
		Effect:   EffectDeny,
		Priority: 20,
	})

	admin := &Identity{
		ID:    "user-1",
		Roles: []string{"admin"},
	}

	// Allow
	err := auth.Authorize(context.Background(), admin, "pdf:convert", "doc1")
	if err != nil {
		t.Errorf("expected allow, got %v", err)
	}

	// Deny (higher priority)
	err = auth.Authorize(context.Background(), admin, "pdf:delete", "doc1")
	if err == nil {
		t.Error("expected deny for pdf:delete")
	}

	// User without role
	user := &Identity{ID: "user-2", Roles: []string{"viewer"}}
	err = auth.Authorize(context.Background(), user, "pdf:convert", "doc1")
	if err == nil {
		t.Error("expected deny for non-admin user")
	}
}

func TestTenantRegistry(t *testing.T) {
	registry := NewTenantRegistry()
	registry.Register(&Tenant{
		ID:       "tenant-1",
		Name:     "Acme Corp",
		Plan:     "pro",
		Features: []string{"pdf", "storage"},
		Quotas:   map[string]int64{"requests": 1000},
	})

	tenant, err := registry.Get("tenant-1")
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}

	if !tenant.HasFeature("pdf") {
		t.Error("expected pdf feature")
	}
	if tenant.HasFeature("ai") {
		t.Error("did not expect ai feature")
	}

	quota, ok := tenant.GetQuota("requests")
	if !ok || quota != 1000 {
		t.Errorf("unexpected quota: %d, %v", quota, ok)
	}

	// Missing tenant
	_, err = registry.Get("missing")
	if err == nil {
		t.Error("expected error for missing tenant")
	}
}

func TestResolveTenant(t *testing.T) {
	registry := NewTenantRegistry()
	registry.Register(&Tenant{ID: "tenant-1", Name: "Acme"})
	registry.Register(&Tenant{ID: "acme", Name: "Acme Subdomain"})

	resolver := NewResolveTenant(registry)

	// Header takes priority
	tenant, err := resolver.Resolve(context.Background(), TenantHints{
		HeaderValue: "tenant-1",
		TenantID:    "other",
	})
	if err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}
	if tenant.ID != "tenant-1" {
		t.Errorf("expected tenant-1, got %s", tenant.ID)
	}

	// Subdomain fallback
	tenant, err = resolver.Resolve(context.Background(), TenantHints{
		Subdomain: "acme",
	})
	if err != nil {
		t.Fatalf("Subdomain resolve failed: %v", err)
	}
	if tenant.ID != "acme" {
		t.Errorf("expected acme, got %s", tenant.ID)
	}

	// No hints
	_, err = resolver.Resolve(context.Background(), TenantHints{})
	if err == nil {
		t.Error("expected error for no hints")
	}
}

func TestJWTAuthenticator_ExpiredToken(t *testing.T) {
	auth := NewJWTAuthenticator(JWTConfig{
		Secret:   "test-secret",
		Issuer:   "test-issuer",
		Audience: "test-audience",
		TokenTTL: -1 * time.Hour, // Already expired
	})

	token, err := auth.GenerateToken(TokenClaims{RegisteredClaims: jwt.RegisteredClaims{Subject: "user-1"}})
	if err != nil {
		t.Fatalf("GenerateToken failed: %v", err)
	}

	_, err = auth.ValidateToken(context.Background(), token)
	if err == nil {
		t.Error("expected error for expired token")
	}
}
