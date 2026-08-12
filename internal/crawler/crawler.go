// Package crawler orchestrates concurrent, depth-limited web crawling.
package crawler

import (
	"context"
	"net/url"
	"sync"
	"time"

	"github.com/NailLaraqui/webcrawler/internal/fetcher"
	"github.com/NailLaraqui/webcrawler/internal/parser"
	"github.com/NailLaraqui/webcrawler/internal/ratelimit"
	"github.com/NailLaraqui/webcrawler/internal/robots"
)

// Result is emitted once per page visited (successfully or not).
type Result struct {
	URL        string
	Depth      int
	LinksFound int
	Err        error
}

// Config controls crawl behaviour.
type Config struct {
	MaxDepth       int  // 0 = only the start page
	MaxConcurrency int  // upper bound on simultaneous in-flight requests
	SameHostOnly   bool // if true, never follow links to other hosts

	// Robots, if non-nil, is consulted before every fetch. URLs it
	// disallows are reported as a Result with Err = robots.ErrDisallowed
	// instead of being fetched. Leave nil to skip robots.txt entirely.
	Robots *robots.Checker

	// RateLimiter, if non-nil, is used to space out requests to the same
	// host. If Robots is also set and a host specifies Crawl-delay, that
	// value is used for that host; otherwise RateLimiter's own default
	// delay applies. Leave nil to disable per-host rate limiting.
	RateLimiter *ratelimit.HostLimiter
}

// Crawler crawls pages concurrently starting from a seed URL.
type Crawler struct {
	cfg      Config
	fetch    *fetcher.Client
	visited  sync.Map // url string -> struct{}
	sem      chan struct{}
	wg       sync.WaitGroup
	results  chan Result
	seedHost string
}

// New builds a Crawler ready to Run.
func New(cfg Config, fc *fetcher.Client) *Crawler {
	if cfg.MaxConcurrency <= 0 {
		cfg.MaxConcurrency = 1
	}
	return &Crawler{
		cfg:     cfg,
		fetch:   fc,
		sem:     make(chan struct{}, cfg.MaxConcurrency),
		results: make(chan Result),
	}
}

type job struct {
	url   string
	depth int
}

// Run starts crawling from start and returns a channel of Results.
// The channel is closed once every reachable page (within limits) has
// been visited or ctx is cancelled. Callers should range over it.
func (c *Crawler) Run(ctx context.Context, start string) <-chan Result {
	if u, err := url.Parse(start); err == nil {
		c.seedHost = u.Host
	}

	c.wg.Add(1)
	go c.worker(ctx, job{url: start, depth: 0})

	// Closer goroutine: once all workers are done, close results so the
	// consumer's range loop terminates instead of blocking forever.
	go func() {
		c.wg.Wait()
		close(c.results)
	}()

	return c.results
}

func (c *Crawler) worker(ctx context.Context, j job) {
	defer c.wg.Done()

	// Acquire a semaphore slot, but bail out early if the context is
	// already cancelled (timeout hit, or Ctrl+C).
	select {
	case c.sem <- struct{}{}:
	case <-ctx.Done():
		return
	}
	defer func() { <-c.sem }()

	// Dedup: LoadOrStore is atomic, so two goroutines racing on the same
	// URL can never both "win" and fetch it twice.
	if _, alreadyVisited := c.visited.LoadOrStore(j.url, struct{}{}); alreadyVisited {
		return
	}

	select {
	case <-ctx.Done():
		return
	default:
	}

	// Robots.txt is checked while still holding our semaphore slot: for
	// most URLs this is instant (cached after the first page per host),
	// but the very first request to a new host pays the robots.txt
	// round-trip here. That's an acceptable trade-off for simplicity —
	// it naturally throttles the "thundering herd" of first requests to
	// a brand-new host instead of firing them all before robots.txt
	// rules are known.
	if c.cfg.Robots != nil && !c.cfg.Robots.Allowed(ctx, j.url) {
		select {
		case c.results <- Result{URL: j.url, Depth: j.depth, Err: robots.ErrDisallowed}:
		case <-ctx.Done():
		}
		return
	}

	// Per-host pacing happens after the robots.txt check (no point
	// waiting to fetch a page we're about to skip) but before the
	// actual page fetch. It deliberately does NOT throttle the
	// robots.txt fetch itself — that one is already a single flight per
	// host via sync.Once in the robots package.
	if c.cfg.RateLimiter != nil {
		host := hostOf(j.url)
		delay := time.Duration(0)
		if c.cfg.Robots != nil {
			delay = c.cfg.Robots.CrawlDelay(ctx, j.url)
		}
		if err := c.cfg.RateLimiter.Wait(ctx, host, delay); err != nil {
			return // context was cancelled while waiting
		}
	}

	body, err := c.fetch.Fetch(ctx, j.url)

	result := Result{URL: j.url, Depth: j.depth, Err: err}

	if err == nil {
		links := parser.ExtractLinks(j.url, body)
		links = c.filterLinks(links)
		result.LinksFound = len(links)

		if j.depth < c.cfg.MaxDepth {
			for _, link := range links {
				c.wg.Add(1)
				go c.worker(ctx, job{url: link, depth: j.depth + 1})
			}
		}
	}

	select {
	case c.results <- result:
	case <-ctx.Done():
	}
}

func (c *Crawler) filterLinks(links []string) []string {
	return filterLinks(links, c.cfg.SameHostOnly, c.seedHost)
}

// filterLinks is shared between Crawler and PoolCrawler so the two
// implementations can't silently drift in same-host behaviour.
func filterLinks(links []string, sameHostOnly bool, seedHost string) []string {
	if !sameHostOnly {
		return links
	}

	filtered := links[:0]
	for _, l := range links {
		u, err := url.Parse(l)
		if err != nil {
			continue
		}
		if u.Host == seedHost {
			filtered = append(filtered, l)
		}
	}

	return filtered
}

// hostOf extracts the host from a URL string, returning "" if it can't
// be parsed. Used as the rate limiter's bucket key.
func hostOf(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return u.Host
}
