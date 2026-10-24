package main

import (
        "errors"
        "testing"
        "time"
)

func TestRetryUntilFirstTrySucceeds(t *testing.T) {
        calls := 0
        err := retryUntil(time.Second, time.Millisecond, func() error {
                calls++
                return nil
        }, nil)
        if err != nil {
                t.Fatalf("expected success, got %v", err)
        }
        if calls != 1 {
                t.Fatalf("expected exactly 1 call, got %d", calls)
        }
}

// The field bug this protects against: the destination exe is locked while
// the old build shuts down. The copy must keep trying until the lock
// clears — here the "lock" clears on the 3rd attempt.
func TestRetryUntilLockClearsMidway(t *testing.T) {
        calls := 0
        err := retryUntil(5*time.Second, time.Millisecond, func() error {
                calls++
                if calls < 3 {
                        return errors.New("install copy: file busy (locked by running exe)")
                }
                return nil
        }, nil)
        if err != nil {
                t.Fatalf("expected eventual success, got %v after %d calls", err, calls)
        }
        if calls != 3 {
                t.Fatalf("expected 3 calls, got %d", calls)
        }
}

func TestRetryUntilDeadlineReturnsLastError(t *testing.T) {
        want := errors.New("still locked")
        start := time.Now()
        err := retryUntil(80*time.Millisecond, 10*time.Millisecond, func() error {
                return want
        }, nil)
        if !errors.Is(err, want) {
                t.Fatalf("expected last error %v, got %v", want, err)
        }
        if elapsed := time.Since(start); elapsed < 60*time.Millisecond {
                t.Fatalf("gave up too early: %v", elapsed)
        }
}

func TestRetryUntilReportsRetries(t *testing.T) {
        calls := 0
        reports := 0
        err := retryUntil(time.Second, time.Millisecond, func() error {
                calls++
                if calls < 3 {
                        return errors.New("busy")
                }
                return nil
        }, func(attempt int, err error) {
                reports++
                if attempt < 1 {
                        t.Fatalf("first attempt must not be reported as a retry, got attempt=%d", attempt)
                }
        })
        // Attempts 0 and 1 fail; attempt 1 is the first reported retry.
        if err != nil || reports != 1 {
                t.Fatalf("expected success with 1 retry report, got err=%v reports=%d", err, reports)
        }
}

func TestRetryUntilNilFn(t *testing.T) {
        if err := retryUntil(time.Millisecond, time.Millisecond, nil, nil); err == nil {
                t.Fatal("nil fn must be an error, not a silent success")
        }
}
