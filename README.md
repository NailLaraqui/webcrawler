# webcrawler

Small concurrent crawler in Go, written as a learning project to practice concurrency patterns (semaphore, WaitGroup, context, channels, and worker pools with custom synchronization) prior to taking a course in Go.

## How to Run

Default execution (Semaphore pattern):
```bash
go run ./cmd/crawler -url https://example.com -depth 2 -concurrency 8
```

Worker pool execution:
```bash
go run ./cmd/crawler -url https://example.com -depth 2 -concurrency 8 -use-pool
```

Available flags:

| Flag | Default | Description |
| --- | --- | --- |
| `-url` | - | Starting URL (required) |
| `-depth` | 2 | Maximum link-following depth |
| `-concurrency` | 8 | Max number of concurrent requests (semaphore size) |
| `-timeout` | 30s | Global crawl timeout |
| `-req-timeout` | 5s | Per-HTTP-request timeout |
| `-same-host` | true | Only follow links sharing the same host as the seed |
| `-respect-robots` | true | Check robots.txt before fetching each page |
| `-min-delay` | 0 | Default minimum delay between requests to the same host (a host's robots.txt Crawl-delay, if present, is used instead for that host) |
| `-csv` | - | Write results to this path as CSV in addition to stdout (e.g. `-csv results.csv`) |
| `-use-pool` | false | Use the worker-pool crawler instead of the semaphore crawler |

Pressing `Ctrl + C` gracefully interrupts the crawl (propagated via `context`).

## Dependencies

This project was stdlib-only through the rate limiter. The HTML parser
now uses `golang.org/x/net/html` (a real HTML5 tokenizer) instead of a
regexp. After pulling this code, run once:

```bash
go mod tidy
```

to fetch `golang.org/x/net` and populate `go.sum`.

## Structure

```
cmd/crawler/main.go       — CLI: flags, signal handling, result display
internal/fetcher/         — HTTP client (timeout, capped body size)
internal/parser/          — link extraction from HTML (golang.org/x/net/html tokenizer)
internal/crawler/         — orchestration:
                            • crawler.go: per-request goroutine spawning bounded by a semaphore
                            • pool.go: fixed N-worker pool fed by an unbounded sync.Cond queue
internal/robots/          — robots.txt fetching, parsing, and per-host caching
internal/ratelimit/       — per-host rate limiting (space out requests to the same host)
internal/export/          — CSV export functionality 
```

## What the Code Demonstrates


- **Two Concurrency Paradigms**:
  - **Dynamic Semaphore Pattern** (`crawler.go`): Spawns short-lived goroutines per link discovered, using a buffered channel (`chan struct{}`) to bound active concurrent requests.
  - **Fixed Worker Pool** (`pool.go`): Spawns exactly N long-lived worker goroutines processing work from a custom thread-safe queue.
- **Unbounded Queue via `sync.Cond`**: Implements a slice-backed `workQueue` protected by `sync.Mutex` and signaled via `sync.Cond`. This avoids deadlocks that would occur with fixed-capacity Go channels when workers concurrently consume and produce new tasks (discovered links).
- **Self-Terminating Work Tracking**: Tracks active jobs in flight (`pending`) inside `workQueue`. When pending work drops to zero, the queue auto-closes and broadcasts (`Broadcast()`) to terminate all waiting workers cleanly.
- **Thread-safe deduplication**: `sync.Map.LoadOrStore` ensures the same URL is never crawled twice, even when discovered concurrently by multiple workers.
- **Graceful cancellation**: `context.Context` propagated throughout, featuring a global timeout and `signal.NotifyContext` for `Ctrl+C`. Force-closes queue signaling via `q.cancel()` so waiting workers unwind immediately upon signal/timeout.
- **Race-free result handling**: Workers send each `Result` through a channel rather than writing directly to stdout or a shared slice; a single goroutine (`main`) consumes and displays them.
- **Robust HTML parsing** (`internal/parser`): Uses `golang.org/x/net/html`'s tokenizer instead of regex to handle unquoted attributes, unclosed tags, named entities, and ignore script/style text.
- **robots.txt compliance** (`internal/robots`): Cached per host using `sync.Once` inside a `sync.Map`. Supports wildcard path rules (`*` and `$`), agent grouping, and `Crawl-delay` parsing. Fails open on missing/invalid files.
- **Per-host rate limiting** (`internal/ratelimit`): Spaces out requests to the same host using host-specific mutexes without blocking requests destined for other hosts.

Tested with `go build -race` and `go vet` — zero warnings.

## Testing

```bash
go test ./...              # all packages
go test ./... -v           # verbose output, prints each sub-test
go test ./... -race        # with race detector enabled (crucial here)
go test ./internal/crawler # single package
go test -run TestCrawler_DedupsSharedLinks ./internal/crawler  # single test
```

Each package contains its own `xxx_test.go` file:

- **`parser_test.go`** — table-driven tests covering relative/absolute links, deduplication, fragments, non-HTTP schemes, HTML entities, invalid base URLs, and complex attribute values/tags.
- **`fetcher_test.go`** — uses `httptest.Server` to mock a local HTTP server, testing successful requests, status codes, context cancellation, and body size limits.
- **`robots_test.go`** — covers wildcard and specific `User-agent` rules, `Crawl-delay` parsing, concurrency safety, and wildcard path matching (`*`, `$`).
- **`ratelimit_test.go`** — tests min-delay enforcement, multi-host isolation, context cancellation mid-wait, and worker serialization.
- **`crawler_test.go` / `pool_test.go`** — comprehensive integration test suite verifying max depth limits, shared link deduplication, max concurrency bounds, `SameHostOnly` filtering, context cancellation, robots compliance, and rate limiting for both semaphore and worker pool implementations.

**Gotcha encountered during testing**: In two tests simulating a blocked HTTP handler (`<-block`), `defer` statement ordering was critical. Because `defer` calls run LIFO, `defer srv.Close()` blocks waiting for active requests to finish. If `close(block)` was deferred *before* `srv.Close()`, it executed *after* it, causing a deadlock. Deferring `close(block)` *after* `srv.Close()` ensures it fires first and unblocks the server.

**Gotcha encountered during testing**: In two tests simulating a blocked HTTP handler (`<-block`), `defer` statement ordering was critical. Because `defer` calls run LIFO, `defer srv.Close()` blocks waiting for active requests to finish. If `close(block)` was deferred *before* `srv.Close()`, it executed *after* it, causing a deadlock. Deferring `close(block)` *after* `srv.Close()` ensures it fires first and unblocks the server.

## Known Limitations

- **robots.txt specificity tie-breaking** uses the original pattern's character length as a proxy for "most specific rule wins", which is a simplification of Google's official octet-based longest-match algorithm. It matches correctly for the common cases (see `robots_test.go`) but isn't a byte-perfect reimplementation of every edge case in Google's spec.

## Future Enhancements (Suggested Order)

1. **Additional Export Formats**: Extend output capabilities to produce JSON reports alongside CSV.
2. **Dynamic Worker Resizing**: Allow expanding or shrinking worker pool concurrency dynamically during execution based on server performance or latency metrics.
