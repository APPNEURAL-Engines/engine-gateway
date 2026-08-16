package auth

import (
	"context"
	"errors"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Authenticator validates authentication credentials (JWT tokens, API keys).
type Authenticator interface {
	// Authenticate validates credentials and returns an identity.
	Authenticate(ctx context.Context, credentials Credentials) (*Identity, error)

	// ValidateToken validates a token and returns its claims.
	ValidateToken(ctx context.Context, token string) (*TokenClaims, error)
}

// Credentials represents authentication credentials.
type Credentials struct {
	// Type is the credential type (bearer, api-key).
	Type CredentialType

	// Token is the JWT bearer token.
	Token string

	// APIKey is the API key.
	APIKey string
}

// CredentialType represents credential types.
type CredentialType string

const (
	// CredentialTypeBearer is a JWT bearer token.
	CredentialTypeBearer CredentialType = "bearer"

	// CredentialTypeAPIKey is an API key.
	CredentialTypeAPIKey CredentialType = "api-key"
)

// Identity represents an authenticated identity.
type Identity struct {
	// ID is the unique identity identifier.
	ID string

	// Type is the identity type (user, service, api-key).
	Type IdentityType

	// TenantID is the tenant this identity belongs to.
	TenantID string

	// Roles are the identity's roles.
	Roles []string

	// Permissions are the identity's permissions.
	Permissions []string

	// AuthenticatedAt is when the identity was authenticated.
	AuthenticatedAt time.Time
}

// IdentityType represents identity types.
type IdentityType string

const (
	// IdentityTypeUser is a user identity.
	IdentityTypeUser IdentityType = "user"

	// IdentityTypeService is a service-to-service identity.
	IdentityTypeService IdentityType = "service"

	// IdentityTypeAPIKey is an API key identity.
	IdentityTypeAPIKey IdentityType = "api-key"
)

// TokenClaims represents JWT token claims.
type TokenClaims struct {
	jwt.RegisteredClaims

	// TenantID is the tenant context.
	TenantID string `json:"tid,omitempty"`

	// Roles are the user's roles.
	Roles []string `json:"roles,omitempty"`

	// Permissions are the user's permissions.
	Permissions []string `json:"perms,omitempty"`

	// Scopes are the token scopes.
	Scopes []string `json:"scopes,omitempty"`
}

// JWTConfig configures JWT authentication.
type JWTConfig struct {
	// Secret is the HMAC secret key.
	Secret string

	// Issuer is the expected token issuer.
	Issuer string

	// Audience is the expected token audience.
	Audience string

	// TokenTTL is the token validity duration.
	TokenTTL time.Duration

	// ClockSkew is the allowed clock skew.
	ClockSkew time.Duration
}

// JWTAuthenticator implements Authenticator using JWT.
type JWTAuthenticator struct {
	config JWTConfig
}

// NewJWTAuthenticator creates a new JWT authenticator.
func NewJWTAuthenticator(config JWTConfig) *JWTAuthenticator {
	if config.TokenTTL == 0 {
		config.TokenTTL = 15 * time.Minute
	}
	if config.ClockSkew == 0 {
		config.ClockSkew = 30 * time.Second
	}
	return &JWTAuthenticator{config: config}
}

// Authenticate validates credentials and returns an identity.
func (a *JWTAuthenticator) Authenticate(ctx context.Context, credentials Credentials) (*Identity, error) {
	switch credentials.Type {
	case CredentialTypeBearer:
		claims, err := a.ValidateToken(ctx, credentials.Token)
		if err != nil {
			return nil, err
		}

		identity := &Identity{
			ID:              claims.Subject,
			Type:            IdentityTypeUser,
			TenantID:        claims.TenantID,
			Roles:           claims.Roles,
			Permissions:     claims.Permissions,
			AuthenticatedAt: time.Now(),
		}

		// Scopes can be treated as additional permissions
		identity.Permissions = append(identity.Permissions, claims.Scopes...)
		return identity, nil

	case CredentialTypeAPIKey:
		return nil, errors.New("api key validation requires APIKeyAuthenticator")

	default:
		return nil, errors.New("unsupported credential type")
	}
}

// ValidateToken validates a JWT token and returns its claims.
func (a *JWTAuthenticator) ValidateToken(ctx context.Context, token string) (*TokenClaims, error) {
	parsed, err := jwt.ParseWithClaims(token, &TokenClaims{}, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, errors.New("unexpected signing method")
		}
		return []byte(a.config.Secret), nil
	}, jwt.WithIssuer(a.config.Issuer), jwt.WithAudience(a.config.Audience), jwt.WithExpirationRequired())

	if err != nil {
		return nil, err
	}

	claims, ok := parsed.Claims.(*TokenClaims)
	if !ok || !parsed.Valid {
		return nil, errors.New("invalid token claims")
	}

	return claims, nil
}

// GenerateToken creates a signed JWT token for testing and integration.
func (a *JWTAuthenticator) GenerateToken(claims TokenClaims) (string, error) {
	now := time.Now()
	claims.Issuer = a.config.Issuer
	claims.Audience = jwt.ClaimStrings{a.config.Audience}
	claims.IssuedAt = jwt.NewNumericDate(now)
	claims.ExpiresAt = jwt.NewNumericDate(now.Add(a.config.TokenTTL))
	claims.NotBefore = jwt.NewNumericDate(now)

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString([]byte(a.config.Secret))
}

// APIKeyAuthenticator implements Authenticator for API keys.
type APIKeyAuthenticator struct {
	keys map[string]*Identity
}

// NewAPIKeyAuthenticator creates a new API key authenticator.
func NewAPIKeyAuthenticator() *APIKeyAuthenticator {
	return &APIKeyAuthenticator{
		keys: make(map[string]*Identity),
	}
}

// AddKey registers an API key with an identity.
func (a *APIKeyAuthenticator) AddKey(key string, identity *Identity) {
	a.keys[key] = identity
}

// Authenticate validates an API key.
func (a *APIKeyAuthenticator) Authenticate(ctx context.Context, credentials Credentials) (*Identity, error) {
	if credentials.Type != CredentialTypeAPIKey {
		return nil, errors.New("expected api-key credential type")
	}

	identity, exists := a.keys[credentials.APIKey]
	if !exists {
		return nil, errors.New("invalid api key")
	}

	copied := *identity
	copied.AuthenticatedAt = time.Now()
	return &copied, nil
}

// ValidateToken is not supported for API keys.
func (a *APIKeyAuthenticator) ValidateToken(ctx context.Context, token string) (*TokenClaims, error) {
	return nil, errors.New("token validation not supported for api key authenticator")
}

// ChainAuthenticator tries multiple authenticators in order.
type ChainAuthenticator struct {
	authenticators []Authenticator
}

// NewChainAuthenticator creates a chain of authenticators.
func NewChainAuthenticator(authenticators ...Authenticator) *ChainAuthenticator {
	return &ChainAuthenticator{authenticators: authenticators}
}

// Authenticate tries each authenticator in order.
func (c *ChainAuthenticator) Authenticate(ctx context.Context, credentials Credentials) (*Identity, error) {
	var lastErr error
	for _, authenticator := range c.authenticators {
		identity, err := authenticator.Authenticate(ctx, credentials)
		if err == nil {
			return identity, nil
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = errors.New("no authenticators configured")
	}
	return nil, lastErr
}

// ValidateToken tries each authenticator in order.
func (c *ChainAuthenticator) ValidateToken(ctx context.Context, token string) (*TokenClaims, error) {
	var lastErr error
	for _, authenticator := range c.authenticators {
		claims, err := authenticator.ValidateToken(ctx, token)
		if err == nil {
			return claims, nil
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = errors.New("no authenticators configured")
	}
	return nil, lastErr
}
