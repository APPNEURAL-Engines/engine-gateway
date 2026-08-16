package auth

import (
	"context"
	"errors"
	"strings"
	"sync"
)

// Authorizer determines whether an identity can perform an action.
type Authorizer interface {
	// Authorize checks if the identity is allowed to perform the action.
	Authorize(ctx context.Context, identity *Identity, action string, resource string) error
}

// Policy represents an authorization rule.
type Policy struct {
	// Name is the policy name.
	Name string

	// Action is the action pattern (e.g., "pdf:convert", "storage:put:*").
	Action string

	// Roles are roles that this policy applies to.
	Roles []string

	// Permissions are permissions that this policy grants.
	Permissions []string

	// Effect is allow or deny.
	Effect Effect

	// Priority determines evaluation order (higher wins).
	Priority int
}

// Effect represents a policy effect.
type Effect string

const (
	// EffectAllow permits the action.
	EffectAllow Effect = "allow"

	// EffectDeny blocks the action.
	EffectDeny Effect = "deny"
)

// RBACAuthorizer implements role-based access control.
type RBACAuthorizer struct {
	policies []Policy
	mu       sync.RWMutex
}

// NewRBACAuthorizer creates a new RBAC authorizer.
func NewRBACAuthorizer() *RBACAuthorizer {
	return &RBACAuthorizer{
		policies: make([]Policy, 0),
	}
}

// AddPolicy adds a policy to the authorizer.
func (a *RBACAuthorizer) AddPolicy(policy Policy) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.policies = append(a.policies, policy)
}

// Authorize checks if the identity can perform the action.
func (a *RBACAuthorizer) Authorize(ctx context.Context, identity *Identity, action string, resource string) error {
	if identity == nil {
		return errors.New("identity is required")
	}

	a.mu.RLock()
	policies := make([]Policy, len(a.policies))
	copy(policies, a.policies)
	a.mu.RUnlock()

	// Sort by priority (highest first) - simple selection sort
	for i := 0; i < len(policies); i++ {
		for j := i + 1; j < len(policies); j++ {
			if policies[j].Priority > policies[i].Priority {
				policies[i], policies[j] = policies[j], policies[i]
			}
		}
	}

	// Build full action string
	fullAction := action
	if resource != "" {
		fullAction = action + ":" + resource
	}

	// Evaluate policies in priority order
	for _, policy := range policies {
		// Check if action matches
		if !matchPattern(policy.Action, fullAction) && !matchPattern(policy.Action, action) {
			continue
		}

		// Check if identity matches policy
		if !identityMatches(identity, policy) {
			continue
		}

		switch policy.Effect {
		case EffectDeny:
			return &AuthorizationError{
				Action:   action,
				Resource: resource,
				Reason:   "denied by policy " + policy.Name,
			}
		case EffectAllow:
			return nil
		}
	}

	return &AuthorizationError{
		Action:   action,
		Resource: resource,
		Reason:   "no matching policy",
	}
}

// identityMatches checks if the identity matches the policy.
func identityMatches(identity *Identity, policy Policy) bool {
	// Check roles
	if len(policy.Roles) > 0 {
		for _, requiredRole := range policy.Roles {
			for _, role := range identity.Roles {
				if requiredRole == role || requiredRole == "*" {
					return true
				}
			}
		}
	}

	// Check permissions
	if len(policy.Permissions) > 0 {
		for _, requiredPerm := range policy.Permissions {
			for _, perm := range identity.Permissions {
				if matchPattern(requiredPerm, perm) || requiredPerm == "*" {
					return true
				}
			}
		}
	}

	// Policy with no role/permission constraints applies to everyone
	return len(policy.Roles) == 0 && len(policy.Permissions) == 0
}

// matchPattern matches a pattern with wildcards (* and ?).
func matchPattern(pattern string, value string) bool {
	// Simple wildcard matching
	if pattern == "*" || pattern == value {
		return true
	}

	// Split by wildcard and check segments
	if strings.Contains(pattern, "*") {
		parts := strings.Split(pattern, "*")
		index := 0
		for _, part := range parts {
			if part == "" {
				continue
			}
			found := strings.Index(value[index:], part)
			if found == -1 {
				return false
			}
			index += found + len(part)
		}
		return true
	}

	return false
}

// AuthorizationError represents a denied authorization.
type AuthorizationError struct {
	Action   string
	Resource string
	Reason   string
}

func (e *AuthorizationError) Error() string {
	return "authorization denied: " + e.Reason + " (action: " + e.Action + ", resource: " + e.Resource + ")"
}

// PermissionMap is a simple map-based authorizer for testing.
type PermissionMap struct {
	permissions map[string][]string
	mu          sync.RWMutex
}

// NewPermissionMap creates a permission map authorizer.
func NewPermissionMap() *PermissionMap {
	return &PermissionMap{
		permissions: make(map[string][]string),
	}
}

// Grant grants a permission to a role.
func (p *PermissionMap) Grant(role string, permission string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.permissions[role] = append(p.permissions[role], permission)
}

// Authorize checks permission for the identity.
func (p *PermissionMap) Authorize(ctx context.Context, identity *Identity, action string, resource string) error {
	if identity == nil {
		return errors.New("identity is required")
	}

	p.mu.RLock()
	defer p.mu.RUnlock()

	for _, role := range identity.Roles {
		perms, exists := p.permissions[role]
		if !exists {
			continue
		}
		for _, perm := range perms {
			if matchPattern(perm, action) || perm == action {
				return nil
			}
		}
	}

	return &AuthorizationError{
		Action:   action,
		Resource: resource,
		Reason:   "permission not granted",
	}
}
