// Package ratelimit provides a simple per-host rate limiter: it spaces
// out requests to the same host by a minimum delay, while requests to
// different hosts never block each other.
package ratelimit

import (
	"context"
	"sync"
	"time"
)

// hostState tracks the last time we hit one specific host. Its own mutex
// is held for the full wait-then-record sequence in Wait, which is what
// actually serializes requests to that host — two goroutines racing for
// the same host will queue up on hs.mu, and each will see the other's
// updated lastHit once it's their turn.
type hostState struct {
	mu      sync.Mutex
	lastHit time.Time
}

// HostLimiter enforces a minimum delay between requests to the same
// host. Different hosts are independent: a slow/rate-limited host never
// holds up requests to any other host.
type HostLimiter struct {
	defaultDelay time.Duration

	mu    sync.Mutex // protects hosts map only, held briefly
	hosts map[string]*hostState
}

// New builds a HostLimiter. defaultDelay is used whenever Wait is called
// with delay <= 0 (e.g. no robots.txt Crawl-delay was specified for that
// host). Pass 0 for defaultDelay to disable rate limiting by default and
// only rate-limit hosts that explicitly request a Crawl-delay.
func New(defaultDelay time.Duration) *HostLimiter {
	return &HostLimiter{
		defaultDelay: defaultDelay,
		hosts:        make(map[string]*hostState),
	}
}

func (h *HostLimiter) stateFor(host string) *hostState {
	h.mu.Lock()
	defer h.mu.Unlock()
	hs, ok := h.hosts[host]
	if !ok {
		hs = &hostState{}
		h.hosts[host] = hs
	}
	return hs
}

// Wait blocks until it's been at least delay since the last request to
// host (or h.defaultDelay if delay <= 0), then records this moment as
// the new "last hit" before returning. If ctx is cancelled while
// waiting, Wait returns ctx.Err() early without recording a hit.
//
// Concurrent calls for the *same* host queue up here, which is exactly
// the point: it's what turns "space out requests" into an actual
// guarantee instead of a best-effort check. Calls for different hosts
// never contend with each other beyond the brief map lookup in
// stateFor.
func (h *HostLimiter) Wait(ctx context.Context, host string, delay time.Duration) error {
	if delay <= 0 {
		delay = h.defaultDelay
	}
	if delay <= 0 {
		return nil // rate limiting disabled for this call
	}

	hs := h.stateFor(host)
	hs.mu.Lock()
	defer hs.mu.Unlock()

	wait := delay - time.Since(hs.lastHit)
	if wait > 0 {
		timer := time.NewTimer(wait)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	hs.lastHit = time.Now()
	return nil
}
