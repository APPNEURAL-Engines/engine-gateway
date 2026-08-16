package retry

import (
	"context"
	"errors"
	"sync"
	"time"
)

// RetryPolicy defines retry behavior.
type RetryPolicy struct {
	// MaxRetries is the maximum number of retry attempts.
	MaxRetries int

	// InitialBackoff is the initial retry delay.
	InitialBackoff time.Duration

	// MaxBackoff is the maximum retry delay.
	MaxBackoff time.Duration

	// BackoffMultiplier grows the backoff exponentially.
	BackoffMultiplier float64

	// Jitter adds randomness to backoff to avoid thundering herd.
	Jitter bool

	// RetryableErrors lists error messages that should be retried.
	// Empty means retry all errors.
	RetryableErrors []string

	// RetryIf is a custom predicate for deciding retryability.
	RetryIf func(err error) bool
}

// DefaultPolicy returns a sensible default retry policy.
func DefaultPolicy() RetryPolicy {
	return RetryPolicy{
		MaxRetries:        3,
		InitialBackoff:    100 * time.Millisecond,
		MaxBackoff:        5 * time.Second,
		BackoffMultiplier: 2.0,
		Jitter:            true,
	}
}

// isRetryable checks if an error should be retried.
func (p RetryPolicy) isRetryable(err error) bool {
	if err == nil {
		return false
	}

	// Custom predicate takes precedence
	if p.RetryIf != nil {
		return p.RetryIf(err)
	}

	// Check error message patterns
	for _, pattern := range p.RetryableErrors {
		if contains(err.Error(), pattern) {
			return true
		}
	}

	// An allowlist that matches nothing means the error is not retryable
	if len(p.RetryableErrors) > 0 {
		return false
	}

	// Retry all errors by default
	return true
}

// contains checks if a string contains a substring.
func contains(s string, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// Execute runs the function with retry policy.
func Execute(ctx context.Context, policy RetryPolicy, fn func() error) error {
	var lastErr error

	for attempt := 0; attempt <= policy.MaxRetries; attempt++ {
		// Check context cancellation
		if err := ctx.Err(); err != nil {
			return err
		}

		err := fn()
		if err == nil {
			return nil
		}
		lastErr = err

		// Check if we should retry
		if attempt >= policy.MaxRetries || !policy.isRetryable(err) {
			return err
		}

		// Calculate backoff
		backoff := policy.InitialBackoff
		for i := 0; i < attempt; i++ {
			backoff = time.Duration(float64(backoff) * policy.BackoffMultiplier)
			if backoff > policy.MaxBackoff {
				backoff = policy.MaxBackoff
				break
			}
		}

		// Add jitter
		if policy.Jitter {
			jitter := time.Duration(randInt64(0, int64(backoff)/4))
			backoff += jitter
		}

		// Wait for backoff or cancellation
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}
	}

	return lastErr
}

// randInt64 generates a random number in [min, max).
func randInt64(min int64, max int64) int64 {
	if max <= min {
		return min
	}
	return min + int64(seededRandom()*float64(max-min))
}

// seededRandom returns a pseudo-random float in [0, 1).
// Uses a simple xorshift to avoid external dependencies.
var randomState uint64 = 0x853c49e6748fea9b

func seededRandom() float64 {
	// xorshift64*
	randomState ^= randomState >> 12
	randomState ^= randomState << 25
	randomState ^= randomState >> 27
	return float64(randomState*2685821657736338717) / float64(1<<64)
}

// CircuitState represents circuit breaker states.
type CircuitState string

const (
	// CircuitClosed allows requests through.
	CircuitClosed CircuitState = "closed"

	// CircuitOpen blocks requests.
	CircuitOpen CircuitState = "open"

	// CircuitHalfOpen allows a probe request.
	CircuitHalfOpen CircuitState = "half-open"
)

// CircuitBreaker protects downstream services from failures.
type CircuitBreaker struct {
	mu sync.RWMutex

	state CircuitState

	// FailureThreshold is the number of failures before opening.
	FailureThreshold int

	// SuccessThreshold is the number of successes before closing.
	SuccessThreshold int

	// Timeout is how long the breaker stays open.
	Timeout time.Duration

	// MaxRequests is the max concurrent requests when half-open.
	MaxRequests int

	failures  int
	successes int
	openedAt  time.Time
	requests  int
}

// NewCircuitBreaker creates a circuit breaker.
func NewCircuitBreaker(failureThreshold int, timeout time.Duration) *CircuitBreaker {
	return &CircuitBreaker{
		state:            CircuitClosed,
		FailureThreshold: failureThreshold,
		SuccessThreshold: 2,
		Timeout:          timeout,
		MaxRequests:      1,
	}
}

// State returns the current circuit state.
func (c *CircuitBreaker) State() CircuitState {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.state
}

// Allow checks if a request can proceed.
func (c *CircuitBreaker) Allow() bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	switch c.state {
	case CircuitClosed:
		return true

	case CircuitOpen:
		// Check if timeout elapsed
		if time.Since(c.openedAt) >= c.Timeout {
			c.state = CircuitHalfOpen
			c.successes = 0
			c.requests = 0
			return true
		}
		return false

	case CircuitHalfOpen:
		if c.requests < c.MaxRequests {
			c.requests++
			return true
		}
		return false
	}

	return false
}

// Success records a successful request.
func (c *CircuitBreaker) Success() {
	c.mu.Lock()
	defer c.mu.Unlock()

	switch c.state {
	case CircuitClosed:
		c.failures = 0

	case CircuitHalfOpen:
		c.successes++
		if c.successes >= c.SuccessThreshold {
			c.state = CircuitClosed
			c.failures = 0
			c.successes = 0
			c.requests = 0
		}
	}
}

// Failure records a failed request.
func (c *CircuitBreaker) Failure() {
	c.mu.Lock()
	defer c.mu.Unlock()

	switch c.state {
	case CircuitClosed:
		c.failures++
		if c.failures >= c.FailureThreshold {
			c.state = CircuitOpen
			c.openedAt = time.Now()
		}

	case CircuitHalfOpen:
		c.state = CircuitOpen
		c.openedAt = time.Now()
		c.successes = 0
		c.requests = 0
	}
}

// Reset manually closes the circuit.
func (c *CircuitBreaker) Reset() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.state = CircuitClosed
	c.failures = 0
	c.successes = 0
	c.requests = 0
}

// ErrCircuitOpen is returned when the circuit is open.
var ErrCircuitOpen = errors.New("circuit breaker is open")

// Execute runs the function with circuit breaker protection.
func (c *CircuitBreaker) Execute(ctx context.Context, fn func() error) error {
	if !c.Allow() {
		return ErrCircuitOpen
	}

	err := fn()
	if err != nil {
		c.Failure()
	} else {
		c.Success()
	}
	return err
}

// Metrics contains circuit breaker statistics.
func (c *CircuitBreaker) Metrics() (CircuitState, int, int) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.state, c.failures, c.successes
}
