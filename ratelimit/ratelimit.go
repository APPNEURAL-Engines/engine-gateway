package ratelimit

import (
	"sync"
	"time"
)

// Limiter enforces request rate limits.
type Limiter interface {
	// Allow checks if a request is allowed.
	Allow(key string) bool

	// AllowN checks if n requests are allowed.
	AllowN(key string, n int) bool

	// Remaining returns the remaining allowed requests.
	Remaining(key string) int
}

// TokenBucket implements the token bucket algorithm.
type TokenBucket struct {
	mu       sync.Mutex
	buckets  map[string]*bucketState
	rate     float64 // tokens per second
	capacity int     // max bucket size
}

type bucketState struct {
	tokens     float64
	lastRefill time.Time
}

// NewTokenBucket creates a token bucket limiter.
func NewTokenBucket(rate float64, capacity int) *TokenBucket {
	return &TokenBucket{
		buckets:  make(map[string]*bucketState),
		rate:     rate,
		capacity: capacity,
	}
}

// Allow checks if a request is allowed.
func (t *TokenBucket) Allow(key string) bool {
	return t.AllowN(key, 1)
}

// AllowN checks if n requests are allowed.
func (t *TokenBucket) AllowN(key string, n int) bool {
	t.mu.Lock()
	defer t.mu.Unlock()

	state, exists := t.buckets[key]
	if !exists {
		state = &bucketState{
			tokens:     float64(t.capacity),
			lastRefill: time.Now(),
		}
		t.buckets[key] = state
	}

	// Refill tokens
	now := time.Now()
	elapsed := now.Sub(state.lastRefill).Seconds()
	state.tokens += elapsed * t.rate
	if state.tokens > float64(t.capacity) {
		state.tokens = float64(t.capacity)
	}
	state.lastRefill = now

	// Check if enough tokens
	if state.tokens >= float64(n) {
		state.tokens -= float64(n)
		return true
	}

	return false
}

// Remaining returns the remaining allowed requests.
func (t *TokenBucket) Remaining(key string) int {
	t.mu.Lock()
	defer t.mu.Unlock()

	state, exists := t.buckets[key]
	if !exists {
		return t.capacity
	}

	now := time.Now()
	elapsed := now.Sub(state.lastRefill).Seconds()
	tokens := state.tokens + elapsed*t.rate
	if tokens > float64(t.capacity) {
		tokens = float64(t.capacity)
	}

	return int(tokens)
}

// FixedWindow implements the fixed window algorithm.
type FixedWindow struct {
	mu      sync.Mutex
	windows map[string]*windowState
	limit   int
	window  time.Duration
}

type windowState struct {
	count     int
	windowEnd time.Time
}

// NewFixedWindow creates a fixed window limiter.
func NewFixedWindow(limit int, window time.Duration) *FixedWindow {
	return &FixedWindow{
		windows: make(map[string]*windowState),
		limit:   limit,
		window:  window,
	}
}

// Allow checks if a request is allowed.
func (f *FixedWindow) Allow(key string) bool {
	return f.AllowN(key, 1)
}

// AllowN checks if n requests are allowed.
func (f *FixedWindow) AllowN(key string, n int) bool {
	f.mu.Lock()
	defer f.mu.Unlock()

	now := time.Now()
	state, exists := f.windows[key]
	if !exists || now.After(state.windowEnd) {
		state = &windowState{
			count:     0,
			windowEnd: now.Add(f.window),
		}
		f.windows[key] = state
	}

	if state.count+n <= f.limit {
		state.count += n
		return true
	}

	return false
}

// Remaining returns the remaining allowed requests.
func (f *FixedWindow) Remaining(key string) int {
	f.mu.Lock()
	defer f.mu.Unlock()

	now := time.Now()
	state, exists := f.windows[key]
	if !exists || now.After(state.windowEnd) {
		return f.limit
	}

	remaining := f.limit - state.count
	if remaining < 0 {
		return 0
	}
	return remaining
}

// SlidingWindow implements the sliding window log algorithm.
type SlidingWindow struct {
	mu     sync.Mutex
	logs   map[string][]time.Time
	limit  int
	window time.Duration
}

// NewSlidingWindow creates a sliding window limiter.
func NewSlidingWindow(limit int, window time.Duration) *SlidingWindow {
	return &SlidingWindow{
		logs:   make(map[string][]time.Time),
		limit:  limit,
		window: window,
	}
}

// Allow checks if a request is allowed.
func (s *SlidingWindow) Allow(key string) bool {
	return s.AllowN(key, 1)
}

// AllowN checks if n requests are allowed.
func (s *SlidingWindow) AllowN(key string, n int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	cutoff := now.Add(-s.window)

	// Remove expired entries
	entries := s.logs[key]
	valid := entries[:0]
	for _, t := range entries {
		if t.After(cutoff) {
			valid = append(valid, t)
		}
	}
	s.logs[key] = valid

	if len(valid)+n <= s.limit {
		for i := 0; i < n; i++ {
			s.logs[key] = append(s.logs[key], now)
		}
		return true
	}

	return false
}

// Remaining returns the remaining allowed requests.
func (s *SlidingWindow) Remaining(key string) int {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	cutoff := now.Add(-s.window)

	valid := s.logs[key][:0]
	for _, t := range s.logs[key] {
		if t.After(cutoff) {
			valid = append(valid, t)
		}
	}
	s.logs[key] = valid

	remaining := s.limit - len(valid)
	if remaining < 0 {
		return 0
	}
	return remaining
}

// Quota tracks resource quotas per tenant.
type Quota struct {
	mu     sync.Mutex
	usage  map[string]map[string]int64
	limits map[string]map[string]int64
}

// NewQuota creates a quota tracker.
func NewQuota() *Quota {
	return &Quota{
		usage:  make(map[string]map[string]int64),
		limits: make(map[string]map[string]int64),
	}
}

// SetLimit sets a quota limit for a tenant.
func (q *Quota) SetLimit(tenantID string, resource string, limit int64) {
	q.mu.Lock()
	defer q.mu.Unlock()

	if _, exists := q.limits[tenantID]; !exists {
		q.limits[tenantID] = make(map[string]int64)
	}
	q.limits[tenantID][resource] = limit
}

// Consume consumes quota for a tenant.
func (q *Quota) Consume(tenantID string, resource string, amount int64) error {
	q.mu.Lock()
	defer q.mu.Unlock()

	limit, hasLimit := q.limits[tenantID][resource]
	if !hasLimit {
		return nil // No limit set
	}

	used := q.usage[tenantID][resource]
	if used+amount > limit {
		return &QuotaExceededError{
			TenantID:  tenantID,
			Resource:  resource,
			Limit:     limit,
			Used:      used,
			Requested: amount,
		}
	}

	if _, exists := q.usage[tenantID]; !exists {
		q.usage[tenantID] = make(map[string]int64)
	}
	q.usage[tenantID][resource] = used + amount
	return nil
}

// Reset resets usage for a tenant.
func (q *Quota) Reset(tenantID string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	delete(q.usage, tenantID)
}

// Usage returns current usage for a tenant.
func (q *Quota) Usage(tenantID string, resource string) int64 {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.usage[tenantID][resource]
}

// QuotaExceededError represents a quota violation.
type QuotaExceededError struct {
	TenantID  string
	Resource  string
	Limit     int64
	Used      int64
	Requested int64
}

func (e *QuotaExceededError) Error() string {
	return "quota exceeded for tenant " + e.TenantID + " on " + e.Resource +
		" (used: " + itoa(e.Used) + ", limit: " + itoa(e.Limit) + ")"
}

func itoa(v int64) string {
	if v == 0 {
		return "0"
	}
	negative := v < 0
	if negative {
		v = -v
	}
	var digits []byte
	for v > 0 {
		digits = append([]byte{byte('0' + v%10)}, digits...)
		v /= 10
	}
	if negative {
		return "-" + string(digits)
	}
	return string(digits)
}
