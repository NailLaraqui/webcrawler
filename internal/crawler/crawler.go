// Package crawler orchestrates concurrent, depth-limited web crawling.
package crawler

import (
	"context"
	"net/url"
	"sync"

	"github.com/NailLaraqui/webcrawler/internal/fetcher"
	"github.com/NailLaraqui/webcrawler/internal/parser"
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
	if !c.cfg.SameHostOnly {
		return links
	}

	filtered := links[:0]
	for _, l := range links {
		u, err := url.Parse(l)
		if err != nil {
			continue
		}
		if u.Host == c.seedHost {
			filtered = append(filtered, l)
		}
	}

	return filtered
}
