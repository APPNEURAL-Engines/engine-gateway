package routing

import (
	"testing"
)

func TestRegistry_RegisterAndGet(t *testing.T) {
	registry := NewRegistry()

	err := registry.Register(&EngineEndpoint{
		Name:    "pdf-engine",
		Version: "1.0.0",
		Address: "localhost:50051",
	})
	if err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	// Duplicate registration is allowed (multiple instances)
	err = registry.Register(&EngineEndpoint{
		Name:    "pdf-engine",
		Version: "1.0.0",
		Address: "localhost:50052",
	})
	if err != nil {
		t.Fatalf("Register second instance failed: %v", err)
	}

	endpoints := registry.GetEndpoints("pdf-engine")
	if len(endpoints) != 2 {
		t.Errorf("expected 2 endpoints, got %d", len(endpoints))
	}

	// Missing name
	err = registry.Register(&EngineEndpoint{Address: "x"})
	if err == nil {
		t.Error("expected error for missing name")
	}
}

func TestRegistry_HealthyFiltering(t *testing.T) {
	registry := NewRegistry()

	registry.Register(&EngineEndpoint{Name: "e1", Version: "1.0.0", Address: "a:1"})
	registry.Register(&EngineEndpoint{Name: "e1", Version: "1.0.0", Address: "a:2"})

	registry.SetHealthy("e1", "a:2", false)

	healthy := registry.GetHealthyEndpoints("e1")
	if len(healthy) != 1 {
		t.Errorf("expected 1 healthy endpoint, got %d", len(healthy))
	}
	if healthy[0].Address != "a:1" {
		t.Errorf("expected a:1, got %s", healthy[0].Address)
	}
}

func TestRegistry_Unregister(t *testing.T) {
	registry := NewRegistry()
	registry.Register(&EngineEndpoint{Name: "e1", Version: "1.0.0", Address: "a:1"})
	registry.Register(&EngineEndpoint{Name: "e1", Version: "1.0.0", Address: "a:2"})

	registry.Unregister("e1", "a:1")

	if registry.Count("e1") != 1 {
		t.Errorf("expected 1 endpoint after unregister, got %d", registry.Count("e1"))
	}
}

func TestVersionResolver_ExactMatch(t *testing.T) {
	resolver := NewVersionResolver("1.0.0")
	resolver.RegisterVersion("pdf-engine", "1.0.0")
	resolver.RegisterVersion("pdf-engine", "2.1.0")

	version, err := resolver.Resolve("pdf-engine", "2.1.0")
	if err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}
	if version != "2.1.0" {
		t.Errorf("expected 2.1.0, got %s", version)
	}
}

func TestVersionResolver_MajorMatch(t *testing.T) {
	resolver := NewVersionResolver("1.0.0")
	resolver.RegisterVersion("pdf-engine", "1.0.0")
	resolver.RegisterVersion("pdf-engine", "2.0.0")
	resolver.RegisterVersion("pdf-engine", "2.3.1")

	version, err := resolver.Resolve("pdf-engine", "2")
	if err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}
	if version != "2.3.1" {
		t.Errorf("expected 2.3.1 (highest 2.x), got %s", version)
	}
}

func TestVersionResolver_Latest(t *testing.T) {
	resolver := NewVersionResolver("")
	resolver.RegisterVersion("pdf-engine", "1.0.0")
	resolver.RegisterVersion("pdf-engine", "3.0.0")
	resolver.RegisterVersion("pdf-engine", "2.0.0")

	version, err := resolver.Resolve("pdf-engine", "latest")
	if err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}
	if version != "3.0.0" {
		t.Errorf("expected 3.0.0 (latest), got %s", version)
	}
}

func TestVersionResolver_NoMatch(t *testing.T) {
	resolver := NewVersionResolver("1.0.0")
	resolver.RegisterVersion("pdf-engine", "1.0.0")

	_, err := resolver.Resolve("pdf-engine", "9.0.0")
	if err == nil {
		t.Error("expected error for no matching version")
	}

	_, err = resolver.Resolve("unknown", "1.0.0")
	if err == nil {
		t.Error("expected error for unknown engine")
	}
}

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"1.0.0", "1.0.0", 0},
		{"2.0.0", "1.0.0", 1},
		{"1.0.0", "2.0.0", -1},
		{"1.10.0", "1.9.0", 1},
		{"2.0.0", "2.0.1", -1},
	}

	for _, tc := range cases {
		got := compareVersions(tc.a, tc.b)
		if (tc.want > 0 && got <= 0) || (tc.want < 0 && got >= 0) || (tc.want == 0 && got != 0) {
			t.Errorf("compareVersions(%s, %s) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestRoundRobinLoadBalancer(t *testing.T) {
	registry := NewRegistry()
	registry.Register(&EngineEndpoint{Name: "e1", Version: "1.0.0", Address: "a:1"})
	registry.Register(&EngineEndpoint{Name: "e1", Version: "1.0.0", Address: "a:2"})

	lb := NewRoundRobinLoadBalancer(registry)

	seen := map[string]bool{}
	for i := 0; i < 6; i++ {
		ep, err := lb.Next("e1")
		if err != nil {
			t.Fatalf("Next failed: %v", err)
		}
		seen[ep.Address] = true
	}

	if len(seen) != 2 {
		t.Errorf("expected both endpoints to be used, got %v", seen)
	}

	// No endpoints
	_, err := lb.Next("missing")
	if err == nil {
		t.Error("expected error for missing engine")
	}
}

func TestWeightedLoadBalancer(t *testing.T) {
	registry := NewRegistry()
	registry.Register(&EngineEndpoint{Name: "e1", Version: "1.0.0", Address: "a:1", Weight: 3})
	registry.Register(&EngineEndpoint{Name: "e1", Version: "1.0.0", Address: "a:2", Weight: 1})

	lb := NewWeightedLoadBalancer(registry)

	// Should not error
	for i := 0; i < 10; i++ {
		if _, err := lb.Next("e1"); err != nil {
			t.Fatalf("Next failed: %v", err)
		}
	}
}
