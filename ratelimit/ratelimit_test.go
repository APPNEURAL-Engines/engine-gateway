package ratelimit

import (
	"testing"
	"time"
)

func TestTokenBucket_Allow(t *testing.T) {
	limiter := NewTokenBucket(10, 5) // 10 tokens/sec, capacity 5

	// First 5 requests should be allowed (capacity)
	for i := 0; i < 5; i++ {
		if !limiter.Allow("user-1") {
			t.Fatalf("request %d should be allowed", i)
		}
	}

	// 6th request should be denied (bucket empty)
	if limiter.Allow("user-1") {
		t.Error("expected 6th request to be denied")
	}
}

func TestTokenBucket_Refill(t *testing.T) {
	limiter := NewTokenBucket(10, 1) // 1 token/sec, capacity 1

	if !limiter.Allow("user-1") {
		t.Fatal("first request should be allowed")
	}
	if limiter.Allow("user-1") {
		t.Fatal("second immediate request should be denied")
	}

	// Wait for refill
	time.Sleep(1100 * time.Millisecond)

	if !limiter.Allow("user-1") {
		t.Error("request after refill should be allowed")
	}
}

func TestTokenBucket_Remaining(t *testing.T) {
	limiter := NewTokenBucket(10, 5)

	if remaining := limiter.Remaining("user-1"); remaining != 5 {
		t.Errorf("expected 5 remaining, got %d", remaining)
	}

	limiter.Allow("user-1")
	limiter.Allow("user-1")

	if remaining := limiter.Remaining("user-1"); remaining != 3 {
		t.Errorf("expected 3 remaining, got %d", remaining)
	}
}

func TestFixedWindow_Allow(t *testing.T) {
	limiter := NewFixedWindow(3, time.Minute)

	for i := 0; i < 3; i++ {
		if !limiter.Allow("user-1") {
			t.Fatalf("request %d should be allowed", i)
		}
	}

	if limiter.Allow("user-1") {
		t.Error("expected 4th request to be denied")
	}
}

func TestFixedWindow_NewWindow(t *testing.T) {
	limiter := NewFixedWindow(1, 50*time.Millisecond)

	if !limiter.Allow("user-1") {
		t.Fatal("first request should be allowed")
	}
	if limiter.Allow("user-1") {
		t.Fatal("second request should be denied")
	}

	// Wait for window to reset
	time.Sleep(60 * time.Millisecond)

	if !limiter.Allow("user-1") {
		t.Error("request in new window should be allowed")
	}
}

func TestSlidingWindow_Allow(t *testing.T) {
	limiter := NewSlidingWindow(2, time.Minute)

	if !limiter.Allow("user-1") {
		t.Fatal("first request should be allowed")
	}
	if !limiter.Allow("user-1") {
		t.Fatal("second request should be allowed")
	}
	if limiter.Allow("user-1") {
		t.Error("third request should be denied")
	}
}

func TestSlidingWindow_Expiry(t *testing.T) {
	limiter := NewSlidingWindow(1, 50*time.Millisecond)

	if !limiter.Allow("user-1") {
		t.Fatal("first request should be allowed")
	}

	// Wait for entries to expire
	time.Sleep(60 * time.Millisecond)

	if !limiter.Allow("user-1") {
		t.Error("request after expiry should be allowed")
	}
}

func TestQuota_Consume(t *testing.T) {
	quota := NewQuota()
	quota.SetLimit("tenant-1", "requests", 100)

	if err := quota.Consume("tenant-1", "requests", 60); err != nil {
		t.Fatalf("Consume failed: %v", err)
	}

	if usage := quota.Usage("tenant-1", "requests"); usage != 60 {
		t.Errorf("expected 60 usage, got %d", usage)
	}

	// Exceed limit
	err := quota.Consume("tenant-1", "requests", 50)
	if err == nil {
		t.Fatal("expected quota exceeded error")
	}

	qe, ok := err.(*QuotaExceededError)
	if !ok {
		t.Fatalf("expected QuotaExceededError, got %T", err)
	}
	if qe.Limit != 100 || qe.Used != 60 {
		t.Errorf("unexpected quota error details: %+v", qe)
	}
}

func TestQuota_NoLimit(t *testing.T) {
	quota := NewQuota()

	// No limit set means unlimited
	if err := quota.Consume("tenant-1", "requests", 1000); err != nil {
		t.Errorf("expected no error without limit, got %v", err)
	}
}

func TestQuota_Reset(t *testing.T) {
	quota := NewQuota()
	quota.SetLimit("tenant-1", "requests", 10)
	quota.Consume("tenant-1", "requests", 5)

	quota.Reset("tenant-1")

	if usage := quota.Usage("tenant-1", "requests"); usage != 0 {
		t.Errorf("expected 0 usage after reset, got %d", usage)
	}
}
