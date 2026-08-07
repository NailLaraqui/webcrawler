# webcrawler

Small concurrent crawler in Go, written as a learning project to practice concurrency patterns (semaphore, WaitGroup, context, channels) prior to taking a course in Go.

## How to Run

```bash

go run ./cmd/crawler -url https://example.com -depth 2 -concurrency 8
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

Pressing `Ctrl + C` gracefully interrupts the crawl (propagated via `context`).

## Structure

```
cmd/crawler/main.go       — CLI: flags, signal handling, result display
internal/fetcher/         — HTTP client (timeout, capped body size)
internal/parser/          — link extraction from HTML (regexp, stdlib only)
internal/crawler/         — orchestration: semaphore, WaitGroup, dedup, cancellation
```

## What the Code Demonstrates

- **Semaphore pattern**: buffered channel (`chan struct{}`) to bound the number of active goroutines (`crawler.go`, `sem` field).
- **Thread-safe deduplication**: `sync.Map.LoadOrStore` ensures the same URL is never crawled twice, even when discovered concurrently by multiple goroutines.
- **Graceful cancellation**: `context.Context` propagated throughout, featuring a global timeout and `signal.NotifyContext` for `Ctrl+C`.
- **Race-free result handling**: workers send each `Result` through a channel rather than writing directly to stdout or a shared slice; a single goroutine (`main`) consumes and displays them.
- **Clean result channel shutdown**: a dedicated goroutine runs `wg.Wait()` followed by `close(results)`, allowing the `range` loop in `main` to exit naturally.

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

- **`parser_test.go`** — pure table-driven tests (no network calls) covering relative/absolute links, deduplication, fragments, non-HTTP schemes, HTML entities, and invalid base URLs.
- **`fetcher_test.go`** — uses `httptest.Server` to mock a real local HTTP server, testing successful requests, non-2xx status codes, `context` cancellation, and response body caps.
- **`crawler_test.go`** — the core test suite: serves a mini website using `httptest` to verify max depth limits, deduplication of shared links, adherence to `MaxConcurrent` bounds (semaphore), `SameHostOnly` filtering, and ensuring `context` cancellation cleanly halts the crawl without deadlocking.

**Gotcha encountered during testing**: In two tests simulating a blocked HTTP handler (`<-block`), `defer` statement ordering was critical. Because `defer` calls run LIFO, `defer srv.Close()` blocks waiting for active requests to finish. If `close(block)` was deferred *before* `srv.Close()`, it executed *after* it, causing a deadlock. Deferring `close(block)` *after* `srv.Close()` ensures it fires first and unblocks the server.

## Future Enhancements (Suggested Order)

1. **robots.txt**: parse and respect `Disallow` rules prior to fetching.
2. **Per-host rate limiting**: implement `golang.org/x/time/rate` on a per-domain basis instead of relying solely on a global semaphore, preventing request spam to single hosts.
3. **Robust HTML parsing**: replace the regex implementation in `parser.go` with `golang.org/x/net/html` (tokenization) for resilient handling of malformed HTML.
4. **Export formats**: output results to JSON or CSV files instead of stdout.
5. **Worker pool alternative**: refactor goroutine spawner into a fixed worker pool consuming from a shared work channel to compare throughput and resource usage patterns.
