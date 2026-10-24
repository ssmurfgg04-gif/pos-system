package main

import (
	"fmt"
	"time"
)

// retryUntil runs fn until it succeeds or the timeout elapses. Failed
// attempts are retried every interval; when the deadline passes the LAST
// error is returned. onRetry (optional) observes each failed retry —
// attempt 0's failure is not reported, only subsequent ones.
//
// The reason this exists is a Windows constraint: a running executable
// cannot be overwritten. The self-installer refreshing an existing install
// must wait out the previous build's shutdown instead of failing — the
// original fail-fast copy is what left tills stuck on an old version while
// Settings kept offering the update they had "already installed".
func retryUntil(timeout, interval time.Duration, fn func() error, onRetry func(attempt int, err error)) error {
	if fn == nil {
		return fmt.Errorf("retryUntil: nil fn")
	}
	if interval <= 0 {
		interval = 100 * time.Millisecond
	}
	deadline := time.Now().Add(timeout)
	for attempt := 0; ; attempt++ {
		err := fn()
		if err == nil {
			return nil
		}
		if attempt > 0 && onRetry != nil {
			onRetry(attempt, err)
		}
		if !time.Now().Add(interval).Before(deadline) {
			return err
		}
		time.Sleep(interval)
	}
}
