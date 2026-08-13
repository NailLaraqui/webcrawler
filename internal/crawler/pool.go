package crawler

import (
	"context"
	"net/url"
	"sync"
	"time"

	"github.com/NailLaraqui/webcrawler/internal/fetcher"
	"github.com/NailLaraqui/webcrawler/internal/parser"
	"github.com/NailLaraqui/webcrawler/internal/robots"
)

// workQueue is an unbounded, concurrency-safe FIFO queue of jobs, built
// on sync.Cond instead of a channel. A plain buffered channel doesn't
// work here: workers both consume jobs AND produce new ones (discovered
// links), so a fixed-capacity channel can deadlock if every worker is
// blocked trying to push a follow-up job into a full channel while no
// one is left to drain it. An unbounded slice-backed queue sidesteps
// that entirely.
//
// The queue also tracks how the crawl ends: pending counts jobs that
// have been pushed but not yet fully processed (processing a job
// includes having already pushed any of its children — see done()).
// When pending drops to zero, no worker could possibly produce more
// work, so the queue closes itself and wakes every blocked worker.
type workQueue struct {
	mu      sync.Mutex
	cond    *sync.Cond
	items   []job
	pending int
	closed  bool
}

func newWorkQueue() *workQueue {
	q := &workQueue{}
	q.cond = sync.NewCond(&q.mu)
	return q
}

// push adds a job to the queue. It's a no-op once the queue is closed
// (crawl finished or was cancelled) — there's no one left to consume it.
func (q *workQueue) push(j job) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return
	}
	q.items = append(q.items, j)
	q.pending++
	q.cond.Signal() // wake exactly one waiting worker; each push adds exactly one job
}

// pop blocks until a job is available or the queue has closed, in which
// case it returns ok=false — the signal for a worker to exit its loop.
func (q *workQueue) pop() (j job, ok bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for len(q.items) == 0 && !q.closed {
		q.cond.Wait()
	}
	if len(q.items) == 0 { // closed and drained
		return job{}, false
	}
	j = q.items[0]
	q.items = q.items[1:]
	return j, true
}

// done marks one previously-pushed job as fully processed. Call it
// exactly once per job, after any children it discovered have already
// been pushed (a deferred call naturally satisfies this ordering).
func (q *workQueue) done() {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.pending--
	if q.pending == 0 {
		q.closed = true
		q.cond.Broadcast() // every blocked pop() must wake up now, not just one
	}
}

// cancel force-closes the queue immediately, regardless of pending
// count. Used when ctx is cancelled: workers should stop promptly
// instead of waiting for in-flight jobs to naturally drain to zero.
func (q *workQueue) cancel() {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return
	}
	q.closed = true
	q.cond.Broadcast()
}

// PoolCrawler is an alternative to Crawler that uses a fixed number of
// long-lived worker goroutines pulling from a shared queue, instead of
// spawning a new goroutine per page and bounding them with a semaphore.
//
// Both implementations enforce the same MaxConcurrent limit and produce
// the same Result stream, so they're drop-in alternatives — useful for
// comparing goroutine churn and scheduling behaviour between "spawn
// unboundedly, throttle with a semaphore" and "spawn exactly N, feed
// them work" under the same crawl.
type PoolCrawler struct {
	cfg      Config
	fetch    *fetcher.Client
	visited  sync.Map
	seedHost string
}

// NewPool builds a PoolCrawler ready to Run. cfg.MaxConcurrent becomes
// the fixed number of worker goroutines.
func NewPool(cfg Config, fc *fetcher.Client) *PoolCrawler {
	if cfg.MaxConcurrency <= 0 {
		cfg.MaxConcurrency = 1
	}
	return &PoolCrawler{cfg: cfg, fetch: fc}
}

// Run starts cfg.MaxConcurrent worker goroutines and returns a channel
// of Results, closed once every reachable page has been visited (or ctx
// is cancelled). Same contract as Crawler.Run.
func (c *PoolCrawler) Run(ctx context.Context, start string) <-chan Result {
	if u, err := url.Parse(start); err != nil {
		c.seedHost = u.Host
	}

	q := newWorkQueue()
	results := make(chan Result)

	var workers sync.WaitGroup
	workers.Add(c.cfg.MaxConcurrency)
	for i := 0; i < c.cfg.MaxConcurrency; i++ {
		go func() {
			defer workers.Done()
			c.workerLoop(ctx, q, results)
		}()
	}

	q.push(job{url: start, depth: 0})

	// If ctx is cancelled, force the queue closed so every worker
	// blocked in pop() wakes up immediately instead of waiting for
	// pending in-flight jobs to drain naturally to zero.
	go func() {
		<-ctx.Done()
		q.cancel()
	}()

	go func() {
		workers.Wait()
		close(results)
	}()

	return results
}

// workerLoop is the body of one fixed pool worker: pop a job, process
// it, repeat until the queue closes. Unlike Crawler's per-job
// goroutines, this goroutine is reused across every job it handles.
func (c *PoolCrawler) workerLoop(ctx context.Context, q *workQueue, results chan<- Result) {
	for {
		j, ok := q.pop()
		if !ok {
			return
		}
		c.process(ctx, q, results, j)
	}
}

func (c *PoolCrawler) process(ctx context.Context, q *workQueue, results chan<- Result, j job) {
	// Deferred so it runs after any child jobs below have already been
	// pushed — see workQueue's doc comment for why that ordering matters.
	defer q.done()

	select {
	case <-ctx.Done():
		return
	default:
	}

	if _, alreadyVisited := c.visited.LoadOrStore(j.url, struct{}{}); alreadyVisited {
		return
	}

	if c.cfg.Robots != nil && !c.cfg.Robots.Allowed(ctx, j.url) {
		select {
		case results <- Result{URL: j.url, Depth: j.depth, Err: robots.ErrDisallowed}:
		case <-ctx.Done():
		}
		return
	}

	if c.cfg.RateLimiter != nil {
		host := hostOf(j.url)
		delay := time.Duration(0)
		if c.cfg.Robots != nil {
			delay = c.cfg.Robots.CrawlDelay(ctx, j.url)
		}
		if err := c.cfg.RateLimiter.Wait(ctx, host, delay); err != nil {
			return
		}
	}

	body, err := c.fetch.Fetch(ctx, j.url)
	result := Result{URL: j.url, Depth: j.depth, Err: err}

	if err == nil {
		links := parser.ExtractLinks(j.url, body)
		links = filterLinks(links, c.cfg.SameHostOnly, c.seedHost)
		result.LinksFound = len(links)

		if j.depth < c.cfg.MaxDepth {
			for _, link := range links {
				q.push(job{url: link, depth: j.depth + 1})
			}
		}
	}

	select {
	case results <- result:
	case <-ctx.Done():
	}
}
