package retry

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestExecute_Success(t *testing.T) {
	policy := DefaultPolicy()
	policy.MaxRetries = 3

	calls := 0
	err := Execute(context.Background(), policy, func() error {
		calls++
		return nil
	})

	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if calls != 1 {
		t.Errorf("expected 1 call, got %d", calls)
	}
}

func TestExecute_RetriesThenSuccess(t *testing.T) {
	policy := DefaultPolicy()
	policy.MaxRetries = 3
	policy.InitialBackoff = 5 * time.Millisecond

	calls := 0
	err := Execute(context.Background(), policy, func() error {
		calls++
		if calls < 3 {
			return errors.New("temporary failure")
		}
		return nil
	})

	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if calls != 3 {
		t.Errorf("expected 3 calls, got %d", calls)
	}
}

func TestExecute_GivesUp(t *testing.T) {
	policy := DefaultPolicy()
	policy.MaxRetries = 2
	policy.InitialBackoff = 5 * time.Millisecond

	calls := 0
	err := Execute(context.Background(), policy, func() error {
		calls++
		return errors.New("persistent failure")
	})

	if err == nil {
		t.Fatal("expected error")
	}
	if calls != 3 { // initial + 2 retries
		t.Errorf("expected 3 calls, got %d", calls)
	}
}

func TestExecute_Cancelled(t *testing.T) {
	policy := DefaultPolicy()
	policy.MaxRetries = 5
	policy.InitialBackoff = 50 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	calls := 0
	err := Execute(ctx, policy, func() error {
		calls++
		if calls == 1 {
			// Cancel after first failure
			go func() {
				time.Sleep(10 * time.Millisecond)
				cancel()
			}()
		}
		return errors.New("failure")
	})

	if err != context.Canceled {
		t.Errorf("expected context.Canceled, got %v", err)
	}
}

func TestRetryPolicy_NotRetryable(t *testing.T) {
	policy := DefaultPolicy()
	policy.MaxRetries = 3
	policy.RetryableErrors = []string{"temporary"}

	calls := 0
	err := Execute(context.Background(), policy, func() error {
		calls++
		return errors.New("permanent failure")
	})

	if err == nil {
		t.Fatal("expected error")
	}
	if calls != 1 {
		t.Errorf("expected 1 call (not retryable), got %d", calls)
	}
}

func TestRetryPolicy_CustomPredicate(t *testing.T) {
	policy := DefaultPolicy()
	policy.MaxRetries = 2
	policy.RetryIf = func(err error) bool {
		return err.Error() == "retry-me"
	}

	calls := 0
	err := Execute(context.Background(), policy, func() error {
		calls++
		return errors.New("retry-me")
	})

	if err == nil {
		t.Fatal("expected error")
	}
	if calls != 3 {
		t.Errorf("expected 3 calls (retryable), got %d", calls)
	}
}

func TestCircuitBreaker_ClosedToOpen(t *testing.T) {
	cb := NewCircuitBreaker(3, time.Minute)

	if cb.State() != CircuitClosed {
		t.Fatal("expected closed state initially")
	}

	// 3 failures should open the circuit
	for i := 0; i < 3; i++ {
		cb.Failure()
	}

	if cb.State() != CircuitOpen {
		t.Error("expected open state after 3 failures")
	}

	if cb.Allow() {
		t.Error("expected request to be denied when open")
	}
}

func TestCircuitBreaker_SuccessResets(t *testing.T) {
	cb := NewCircuitBreaker(3, time.Minute)

	cb.Failure()
	cb.Failure()
	cb.Success()

	if cb.State() != CircuitClosed {
		t.Error("expected closed state after success")
	}

	// Verify failures were reset
	cb.Failure()
	cb.Failure()
	cb.Success()
	if cb.State() != CircuitClosed {
		t.Error("expected still closed after reset")
	}
}

func TestCircuitBreaker_HalfOpenRecovery(t *testing.T) {
	cb := NewCircuitBreaker(2, 50*time.Millisecond)

	cb.Failure()
	cb.Failure()
	if cb.State() != CircuitOpen {
		t.Fatal("expected open state")
	}

	// Wait for timeout
	time.Sleep(60 * time.Millisecond)

	if !cb.Allow() {
		t.Fatal("expected request allowed in half-open")
	}

	cb.Success()
	cb.Success()

	if cb.State() != CircuitClosed {
		t.Error("expected closed after successful recovery")
	}
}

func TestCircuitBreaker_Execute(t *testing.T) {
	cb := NewCircuitBreaker(2, time.Minute)

	// Succeed
	if err := cb.Execute(context.Background(), func() error { return nil }); err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	// Fail twice
	cb.Execute(context.Background(), func() error { return errors.New("err") })
	cb.Execute(context.Background(), func() error { return errors.New("err") })

	// Circuit open
	err := cb.Execute(context.Background(), func() error { return nil })
	if err != ErrCircuitOpen {
		t.Errorf("expected ErrCircuitOpen, got %v", err)
	}
}

func TestCircuitBreaker_Reset(t *testing.T) {
	cb := NewCircuitBreaker(2, time.Minute)

	cb.Failure()
	cb.Failure()
	if cb.State() != CircuitOpen {
		t.Fatal("expected open state")
	}

	cb.Reset()
	if cb.State() != CircuitClosed {
		t.Error("expected closed state after reset")
	}
}
