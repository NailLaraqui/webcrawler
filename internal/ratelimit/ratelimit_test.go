package ratelimit

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestWait_EnforceMinimumDelay(t *testing.T) {
	h := New(0)
	ctx := context.Background()
	const delay = 100 * time.Millisecond

	start := time.Now()
	if err := h.Wait(ctx, "example.com", delay); err != nil {
		t.Fatalf("first Wait returned error: %v", err)
	}
	firstElapsed := time.Since(start)
	if firstElapsed > 20*time.Millisecond {
		t.Errorf("first call to a fresh host should not wait, took %v", firstElapsed)
	}

	start = time.Now()
	if err := h.Wait(ctx, "example.com", delay); err != nil {
		t.Fatalf("second Wait returned error: %v", err)
	}
	secondElapsed := time.Since(start)
	if secondElapsed < delay-10*time.Millisecond {
		t.Errorf("second call should have waited ~%v, only waited %v", delay, secondElapsed)
	}
}

func TestWait_DifferentHostsDoNotBlockEachOther(t *testing.T) {
	h := New(0)
	ctx := context.Background()
	const delay = 200 * time.Millisecond

	// Consume host A's budget so a second call to A would have to wait.
	if err := h.Wait(ctx, "a.example.com", delay); err != nil {
		t.Fatalf("Wait for host A failed: %v", err)
	}

	// Host B has never been hit, so this must return immediately even
	// though A is "on cooldown" — hosts must not share a lock.
	start := time.Now()
	if err := h.Wait(ctx, "b.example.com", delay); err != nil {
		t.Fatalf("Wait for host B failed: %v", err)
	}
	elapsed := time.Since(start)
	if elapsed > 20*time.Millisecond {
		t.Errorf("host B should not be throttled by host A's cooldown, took %v", elapsed)
	}
}

func TestWait_ZeroDelayDisablesLimiting(t *testing.T) {
	h := New(0) // no default delay configured
	ctx := context.Background()

	start := time.Now()
	for i := 0; i < 5; i++ {
		if err := h.Wait(ctx, "example.com", 0); err != nil {
			t.Fatalf("Wait returned error: %v", err)
		}
	}
	if elapsed := time.Since(start); elapsed > 20*time.Millisecond {
		t.Errorf("calls with delay=0 and no default should never wait, took %v", elapsed)
	}
}

func TestWait_FallsBackToDefaultDelay(t *testing.T) {
	const def = 100 * time.Millisecond
	h := New(def)
	ctx := context.Background()

	if err := h.Wait(ctx, "example.com", 0); err != nil { // delay<=0 -> use default
		t.Fatalf("first Wait returned error: %v", err)
	}
	start := time.Now()
	if err := h.Wait(ctx, "example.com", 0); err != nil {
		t.Fatalf("second Wait returned error: %v", err)
	}
	if elapsed := time.Since(start); elapsed < def-10*time.Millisecond {
		t.Errorf("expected default delay ~%v to be applied, only waited %v", def, elapsed)
	}
}

func TestWait_RespectsContextCancellation(t *testing.T) {
	h := New(0)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	// Prime the host so the next call would normally wait a long time.
	if err := h.Wait(context.Background(), "example.com", time.Hour); err != nil {
		t.Fatalf("priming Wait failed: %v", err)
	}

	err := h.Wait(ctx, "example.com", time.Hour)
	if err == nil {
		t.Fatal("expected an error when context is cancelled mid-wait, got nil")
	}
}

func TestWait_ConcurrentCallsForSameHostAreSerialized(t *testing.T) {
	h := New(0)
	const delay = 30 * time.Millisecond
	const n = 5

	var wg sync.WaitGroup
	wg.Add(n)
	start := time.Now()
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			_ = h.Wait(context.Background(), "example.com", delay)
		}()
	}
	wg.Wait()
	elapsed := time.Since(start)

	// n calls spaced by `delay` each should take at least (n-1)*delay,
	// since they all contend for the same host and must queue up.
	want := time.Duration(n-1) * delay
	if elapsed < want-10*time.Millisecond {
		t.Errorf("n=%d concurrent calls took %v, want at least ~%v (calls should serialize per host)", n, elapsed, want)
	}
}
