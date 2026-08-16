package routing

import (
	"errors"
	"fmt"
	"sort"
	"sync"
)

// EngineEndpoint represents a reachable engine instance.
type EngineEndpoint struct {
	// Name is the engine name.
	Name string

	// Version is the engine semantic version.
	Version string

	// Address is the engine address (host:port or URL).
	Address string

	// Protocol is the communication protocol (grpc, http, wasm).
	Protocol string

	// Weight is used for weighted load balancing.
	Weight int

	// Tags are arbitrary labels.
	Tags map[string]string

	// Healthy indicates if the endpoint is currently healthy.
	Healthy bool
}

// Registry manages engine endpoint discovery.
type Registry struct {
	endpoints map[string][]*EngineEndpoint
	mu        sync.RWMutex
}

// NewRegistry creates a new engine endpoint registry.
func NewRegistry() *Registry {
	return &Registry{
		endpoints: make(map[string][]*EngineEndpoint),
	}
}

// Register adds an engine endpoint.
func (r *Registry) Register(endpoint *EngineEndpoint) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if endpoint.Name == "" {
		return errors.New("endpoint name is required")
	}
	if endpoint.Address == "" {
		return errors.New("endpoint address is required")
	}

	if endpoint.Weight <= 0 {
		endpoint.Weight = 1
	}
	if endpoint.Protocol == "" {
		endpoint.Protocol = "grpc"
	}
	endpoint.Healthy = true

	r.endpoints[endpoint.Name] = append(r.endpoints[endpoint.Name], endpoint)
	return nil
}

// Unregister removes an engine endpoint.
func (r *Registry) Unregister(name string, address string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	endpoints := r.endpoints[name]
	for i, ep := range endpoints {
		if ep.Address == address {
			r.endpoints[name] = append(endpoints[:i], endpoints[i+1:]...)
			return
		}
	}
}

// GetEndpoints returns all endpoints for an engine.
func (r *Registry) GetEndpoints(name string) []*EngineEndpoint {
	r.mu.RLock()
	defer r.mu.RUnlock()

	endpoints := make([]*EngineEndpoint, 0, len(r.endpoints[name]))
	for _, ep := range r.endpoints[name] {
		copied := *ep
		endpoints = append(endpoints, &copied)
	}
	return endpoints
}

// GetHealthyEndpoints returns healthy endpoints for an engine.
func (r *Registry) GetHealthyEndpoints(name string) []*EngineEndpoint {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var result []*EngineEndpoint
	for _, ep := range r.endpoints[name] {
		if ep.Healthy {
			copied := *ep
			result = append(result, &copied)
		}
	}
	return result
}

// ListAll returns all registered endpoints.
func (r *Registry) ListAll() []*EngineEndpoint {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var result []*EngineEndpoint
	for _, endpoints := range r.endpoints {
		for _, ep := range endpoints {
			copied := *ep
			result = append(result, &copied)
		}
	}
	return result
}

// SetHealthy marks an endpoint as healthy or unhealthy.
func (r *Registry) SetHealthy(name string, address string, healthy bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	for _, ep := range r.endpoints[name] {
		if ep.Address == address {
			ep.Healthy = healthy
			return
		}
	}
}

// Count returns the number of endpoints for an engine.
func (r *Registry) Count(name string) int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.endpoints[name])
}

// VersionResolver resolves the appropriate engine version for a request.
type VersionResolver struct {
	// DefaultVersion is used when no version constraint is specified.
	DefaultVersion string

	// Versions maps engine name to available versions.
	Versions map[string][]string
}

// NewVersionResolver creates a version resolver.
func NewVersionResolver(defaultVersion string) *VersionResolver {
	return &VersionResolver{
		DefaultVersion: defaultVersion,
		Versions:       make(map[string][]string),
	}
}

// RegisterVersion registers an available version for an engine.
func (v *VersionResolver) RegisterVersion(engine string, version string) {
	v.Versions[engine] = append(v.Versions[engine], version)
}

// Resolve determines the best matching version for the constraint.
func (v *VersionResolver) Resolve(engine string, constraint string) (string, error) {
	versions, exists := v.Versions[engine]
	if !exists || len(versions) == 0 {
		return "", fmt.Errorf("no versions registered for engine %s", engine)
	}

	// Sort versions descending
	sorted := make([]string, len(versions))
	copy(sorted, versions)
	sort.Slice(sorted, func(i, j int) bool {
		return compareVersions(sorted[i], sorted[j]) > 0
	})

	if constraint == "" || constraint == "latest" {
		if v.DefaultVersion != "" {
			for _, ver := range sorted {
				if ver == v.DefaultVersion {
					return ver, nil
				}
			}
		}
		return sorted[0], nil
	}

	// Strip "v" prefix
	if len(constraint) > 0 && constraint[0] == 'v' {
		constraint = constraint[1:]
	}

	for _, ver := range sorted {
		if matchesConstraint(ver, constraint) {
			return ver, nil
		}
	}

	return "", fmt.Errorf("no version for engine %s matches constraint %s", engine, constraint)
}

// matchesConstraint checks if a version matches a constraint.
func matchesConstraint(version string, constraint string) bool {
	if version == constraint {
		return true
	}

	// Major version match
	if len(constraint) == 1 && constraint >= "1" && constraint <= "9" {
		vParts := splitVersion(version)
		if len(vParts) > 0 && vParts[0] == constraint {
			return true
		}
	}

	// Major.minor match
	cParts := splitVersion(constraint)
	vParts := splitVersion(version)
	if len(cParts) == 2 && len(vParts) >= 2 {
		return cParts[0] == vParts[0] && cParts[1] == vParts[1]
	}

	return false
}

// splitVersion splits a version string into parts.
func splitVersion(version string) []string {
	parts := []string{}
	current := ""
	for _, c := range version {
		if c == '.' || c == '-' || c == '+' {
			if current != "" {
				parts = append(parts, current)
				current = ""
			}
		} else {
			current += string(c)
		}
	}
	if current != "" {
		parts = append(parts, current)
	}
	return parts
}

// compareVersions compares two version strings (returns >0 if a > b).
func compareVersions(a string, b string) int {
	ap := splitVersion(a)
	bp := splitVersion(b)

	for i := 0; i < len(ap) && i < len(bp); i++ {
		var an, bn int
		_, aErr := fmt.Sscanf(ap[i], "%d", &an)
		_, bErr := fmt.Sscanf(bp[i], "%d", &bn)

		if aErr == nil && bErr == nil {
			if an != bn {
				return an - bn
			}
		} else {
			if ap[i] != bp[i] {
				if ap[i] > bp[i] {
					return 1
				}
				return -1
			}
		}
	}

	return len(ap) - len(bp)
}

// Resolver combines version resolution and endpoint discovery.
type Resolver struct {
	registry *Registry
	versions *VersionResolver
}

// NewResolver creates a combined resolver.
func NewResolver(registry *Registry, versions *VersionResolver) *Resolver {
	return &Resolver{
		registry: registry,
		versions: versions,
	}
}

// Resolve finds an endpoint for the given engine and version constraint.
func (r *Resolver) Resolve(engine string, constraint string) (*EngineEndpoint, error) {
	version, err := r.versions.Resolve(engine, constraint)
	if err != nil {
		return nil, err
	}

	endpoints := r.registry.GetHealthyEndpoints(engine)
	if len(endpoints) == 0 {
		return nil, fmt.Errorf("no healthy endpoints for engine %s", engine)
	}

	// Prefer matching version
	for _, ep := range endpoints {
		if ep.Version == version {
			return ep, nil
		}
	}

	// Fall back to first healthy endpoint
	return endpoints[0], nil
}

// LoadBalancer distributes requests across endpoints.
type LoadBalancer interface {
	// Next returns the next endpoint for the engine.
	Next(engine string) (*EngineEndpoint, error)
}

// RoundRobinLoadBalancer distributes requests in round-robin order.
type RoundRobinLoadBalancer struct {
	registry *Registry
	counters map[string]int
	mu       sync.Mutex
}

// NewRoundRobinLoadBalancer creates a round-robin load balancer.
func NewRoundRobinLoadBalancer(registry *Registry) *RoundRobinLoadBalancer {
	return &RoundRobinLoadBalancer{
		registry: registry,
		counters: make(map[string]int),
	}
}

// Next returns the next endpoint in round-robin order.
func (b *RoundRobinLoadBalancer) Next(engine string) (*EngineEndpoint, error) {
	endpoints := b.registry.GetHealthyEndpoints(engine)
	if len(endpoints) == 0 {
		return nil, fmt.Errorf("no healthy endpoints for engine %s", engine)
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	counter := b.counters[engine]
	b.counters[engine] = (counter + 1) % len(endpoints)

	return endpoints[counter], nil
}

// WeightedLoadBalancer distributes requests by endpoint weight.
type WeightedLoadBalancer struct {
	registry *Registry
	mu       sync.Mutex
}

// NewWeightedLoadBalancer creates a weighted load balancer.
func NewWeightedLoadBalancer(registry *Registry) *WeightedLoadBalancer {
	return &WeightedLoadBalancer{
		registry: registry,
	}
}

// Next returns the next endpoint based on weight.
func (b *WeightedLoadBalancer) Next(engine string) (*EngineEndpoint, error) {
	endpoints := b.registry.GetHealthyEndpoints(engine)
	if len(endpoints) == 0 {
		return nil, fmt.Errorf("no healthy endpoints for engine %s", engine)
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	totalWeight := 0
	for _, ep := range endpoints {
		totalWeight += ep.Weight
	}

	pick := int(hash(engine+fmt.Sprint(len(endpoints)))) % totalWeight
	for _, ep := range endpoints {
		pick -= ep.Weight
		if pick < 0 {
			return ep, nil
		}
	}

	return endpoints[0], nil
}

// hash is a simple deterministic hash for weighted selection.
func hash(s string) uint32 {
	var h uint32 = 2166136261
	for i := 0; i < len(s); i++ {
		h ^= uint32(s[i])
		h *= 16777619
	}
	return h
}
