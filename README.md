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
| `-respect-robots` | true | Check robots.txt before fetching each page |

Pressing `Ctrl + C` gracefully interrupts the crawl (propagated via `context`).

## Structure

```
cmd/crawler/main.go       — CLI: flags, signal handling, result display
internal/fetcher/         — HTTP client (timeout, capped body size)
internal/parser/          — link extraction from HTML (regexp, stdlib only)
internal/crawler/         — orchestration: semaphore, WaitGroup, dedup, cancellation
internal/robots/          — robots.txt fetching, parsing, and per-host caching
```

## What the Code Demonstrates

- **Semaphore pattern**: buffered channel (`chan struct{}`) to bound the number of active goroutines (`crawler.go`, `sem` field).
- **Thread-safe deduplication**: `sync.Map.LoadOrStore` ensures the same URL is never crawled twice, even when discovered concurrently by multiple goroutines.
- **Graceful cancellation**: `context.Context` propagated throughout, featuring a global timeout and `signal.NotifyContext` for `Ctrl+C`.
- **Race-free result handling**: workers send each `Result` through a channel rather than writing directly to stdout or a shared slice; a single goroutine (`main`) consumes and displays them.
- **Clean result channel shutdown**: a dedicated goroutine runs `wg.Wait()` followed by `close(results)`, allowing the `range` loop in `main` to exit naturally.
- **robots.txt** (`internal/robots`): fetched exactly once per host no matter how many goroutines ask concurrently, via `sync.Once` inside a `sync.Map`-cached entry. Parses `User-agent` groups (including grouped agent lines), applies the longest-matching-prefix rule for `Allow`/`Disallow`, and reads `Crawl-delay`. Fails open (everything allowed) if robots.txt is missing or fails to fetch — the standard convention real crawlers follow.

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
- **`robots_test.go`** — covers wildcard vs. specific `User-agent` matching, grouped agent lines, `Crawl-delay` parsing, fail-open behavior on a missing robots.txt or a malformed URL, and a concurrency test proving robots.txt is fetched exactly once per host even under 20 simultaneous callers.
- **`crawler_test.go`** — the core test suite: serves a mini website using `httptest` to verify max depth limits, deduplication of shared links, adherence to `MaxConcurrent` bounds (semaphore), `SameHostOnly` filtering, `context` cancellation halting the crawl without deadlocking, and an integration test confirming disallowed URLs are skipped rather than fetched.

**Gotcha encountered during testing**: In two tests simulating a blocked HTTP handler (`<-block`), `defer` statement ordering was critical. Because `defer` calls run LIFO, `defer srv.Close()` blocks waiting for active requests to finish. If `close(block)` was deferred *before* `srv.Close()`, it executed *after* it, causing a deadlock. Deferring `close(block)` *after* `srv.Close()` ensures it fires first and unblocks the server.

## Known Limitations

- **robots.txt wildcard matching**: the parser only supports simple prefix matching, not the `*`/`$` wildcard extension used by many real-world sites (e.g. GitHub's `Disallow: /*/*/pulse`). Such rules are parsed as literal path prefixes, so they never match a real URL and are effectively ignored rather than misapplied. See the comment on `type directive` in `robots.go` for what a fix would involve.

## Future Enhancements (Suggested Order)

1. **Wildcard support in robots.txt**: add `*` and `$` matching (Google/Bing extension) to `directive.path` — needed to fully respect robots.txt on sites like GitHub.
2. **Per-host rate limiting**: `internal/robots` already exposes `CrawlDelay()` — the remaining piece is a per-host limiter (mutex + `lastHit time.Time` per domain) called right after the semaphore, before `fetcher.Fetch()`.
3. **Robust HTML parsing**: replace the regex implementation in `parser.go` with `golang.org/x/net/html` (tokenization) for resilient handling of malformed HTML.
4. **Export formats**: output results to JSON or CSV files instead of stdout.
5. **Worker pool alternative**: refactor goroutine spawner into a fixed worker pool consuming from a shared work channel to compare throughput and resource usage patterns.
