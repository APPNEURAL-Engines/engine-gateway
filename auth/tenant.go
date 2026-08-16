package auth

import (
	"context"
	"errors"
	"sync"
)

// TenantResolver determines the tenant context from a request.
type TenantResolver interface {
	// Resolve determines the tenant ID from the given hints.
	Resolve(ctx context.Context, hints TenantHints) (*Tenant, error)
}

// TenantHints contains hints for resolving the tenant.
type TenantHints struct {
	// TenantID from the authenticated identity.
	TenantID string

	// Subdomain from the request host (e.g., "acme.appneurox.com").
	Subdomain string

	// HeaderValue from a tenant header (e.g., X-Tenant-ID).
	HeaderValue string

	// APIKey used for the request.
	APIKey string
}

// Tenant represents a resolved tenant.
type Tenant struct {
	// ID is the unique tenant identifier.
	ID string

	// Name is the tenant's display name.
	Name string

	// Plan is the tenant's subscription plan.
	Plan string

	// Region is the tenant's deployment region.
	Region string

	// Features are enabled features.
	Features []string

	// Quotas are resource quotas.
	Quotas map[string]int64
}

// HasFeature checks if the tenant has a feature enabled.
func (t *Tenant) HasFeature(feature string) bool {
	for _, f := range t.Features {
		if f == feature {
			return true
		}
	}
	return false
}

// GetQuota returns a quota value.
func (t *Tenant) GetQuota(resource string) (int64, bool) {
	quota, ok := t.Quotas[resource]
	return quota, ok
}

// TenantRegistry stores tenant information.
type TenantRegistry struct {
	tenants map[string]*Tenant
	mu      sync.RWMutex
}

// NewTenantRegistry creates a new tenant registry.
func NewTenantRegistry() *TenantRegistry {
	return &TenantRegistry{
		tenants: make(map[string]*Tenant),
	}
}

// Register adds a tenant to the registry.
func (r *TenantRegistry) Register(tenant *Tenant) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tenants[tenant.ID] = tenant
}

// Get retrieves a tenant by ID.
func (r *TenantRegistry) Get(tenantID string) (*Tenant, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	tenant, exists := r.tenants[tenantID]
	if !exists {
		return nil, errors.New("tenant not found: " + tenantID)
	}
	return tenant, nil
}

// ResolveTenant resolves the tenant from hints with priority:
// 1. Header value (explicit)
// 2. Identity tenant ID
// 3. Subdomain
// 4. API key
type ResolveTenant struct {
	registry *TenantRegistry
}

// NewResolveTenant creates a tenant resolver.
func NewResolveTenant(registry *TenantRegistry) *ResolveTenant {
	return &ResolveTenant{registry: registry}
}

// Resolve determines the tenant ID from hints.
func (r *ResolveTenant) Resolve(ctx context.Context, hints TenantHints) (*Tenant, error) {
	// Priority 1: Explicit header
	if hints.HeaderValue != "" {
		return r.registry.Get(hints.HeaderValue)
	}

	// Priority 2: Authenticated identity
	if hints.TenantID != "" {
		return r.registry.Get(hints.TenantID)
	}

	// Priority 3: Subdomain
	if hints.Subdomain != "" {
		tenant, err := r.registry.Get(hints.Subdomain)
		if err == nil {
			return tenant, nil
		}
	}

	return nil, errors.New("unable to resolve tenant")
}
