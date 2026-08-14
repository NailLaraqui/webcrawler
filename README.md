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
| `-min-delay` | 0 | Default minimum delay between requests to the same host (a host's robots.txt Crawl-delay, if present, is used instead for that host) |

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
internal/crawler/         — orchestration: semaphore, WaitGroup, dedup, cancellation
internal/robots/          — robots.txt fetching, parsing, and per-host caching
internal/ratelimit/       — per-host rate limiting (space out requests to the same host)
```

## What the Code Demonstrates

- **Semaphore pattern**: buffered channel (`chan struct{}`) to bound the number of active goroutines (`crawler.go`, `sem` field).
- **Thread-safe deduplication**: `sync.Map.LoadOrStore` ensures the same URL is never crawled twice, even when discovered concurrently by multiple goroutines.
- **Graceful cancellation**: `context.Context` propagated throughout, featuring a global timeout and `signal.NotifyContext` for `Ctrl+C`.
- **Race-free result handling**: workers send each `Result` through a channel rather than writing directly to stdout or a shared slice; a single goroutine (`main`) consumes and displays them.
- **Clean result channel shutdown**: a dedicated goroutine runs `wg.Wait()` followed by `close(results)`, allowing the `range` loop in `main` to exit naturally.
- **Robust HTML parsing** (`internal/parser`): uses `golang.org/x/net/html`'s tokenizer instead of a regexp, so it correctly handles unquoted attributes, unclosed tags, HTML entities beyond just `&amp;`, and ignores href-like text sitting inside `<script>` blocks — all things a regexp either mishandles or gets fooled by. See `parser_test.go` for concrete before/after cases.
- **robots.txt** (`internal/robots`): fetched exactly once per host no matter how many goroutines ask concurrently, via `sync.Once` inside a `sync.Map`-cached entry. Parses `User-agent` groups (including grouped agent lines), applies the longest-matching rule for `Allow`/`Disallow` (now with `*` and `$` wildcard support — see below), and reads `Crawl-delay`. Fails open (everything allowed) if robots.txt is missing or fails to fetch — the standard convention real crawlers follow.
- **Per-host rate limiting** (`internal/ratelimit`): a `HostLimiter` spaces out requests to the *same* host by a minimum delay (from `-min-delay`, or from that host's robots.txt `Crawl-delay` when one is specified), while different hosts never block each other. The serialization trick: each host's mutex is held for the full "wait, then record" sequence in `Wait`, so concurrent callers for one host naturally queue up in the right order instead of racing on a shared timestamp.

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

- **`parser_test.go`** — table-driven tests covering relative/absolute links, deduplication, fragments, non-HTTP schemes, HTML entities (including named entities beyond `&amp;`), invalid base URLs, and HTML the old regexp handled incorrectly: unquoted attribute values, unclosed tags, and href-like text inside `<script>` blocks.
- **`fetcher_test.go`** — uses `httptest.Server` to mock a real local HTTP server, testing successful requests, non-2xx status codes, `context` cancellation, and response body caps.
- **`robots_test.go`** — covers wildcard vs. specific `User-agent` matching, grouped agent lines, `Crawl-delay` parsing, fail-open behavior on a missing robots.txt or a malformed URL, a concurrency test proving robots.txt is fetched exactly once per host even under 20 simultaneous callers, and wildcard path matching (`*` mid-pattern, `$` end-anchor, specificity tie-breaking, and confirming plain prefixes still behave exactly as before).
- **`ratelimit_test.go`** — covers minimum-delay enforcement, that different hosts never block each other, the zero-delay/no-default disables-limiting case, default-delay fallback, `context` cancellation mid-wait, and that concurrent calls for the same host serialize correctly (n calls take at least (n-1)×delay).
- **`crawler_test.go`** — the core test suite: serves a mini website using `httptest` to verify max depth limits, deduplication of shared links, adherence to `MaxConcurrent` bounds (semaphore), `SameHostOnly` filtering, `context` cancellation halting the crawl without deadlocking, and integration tests confirming disallowed URLs are skipped rather than fetched, that per-host pacing is actually enforced end-to-end, and that skipped (robots-disallowed) URLs never consume rate-limiter budget.

**Gotcha encountered during testing**: In two tests simulating a blocked HTTP handler (`<-block`), `defer` statement ordering was critical. Because `defer` calls run LIFO, `defer srv.Close()` blocks waiting for active requests to finish. If `close(block)` was deferred *before* `srv.Close()`, it executed *after* it, causing a deadlock. Deferring `close(block)` *after* `srv.Close()` ensures it fires first and unblocks the server.

## Known Limitations

- **robots.txt specificity tie-breaking** uses the original pattern's character length as a proxy for "most specific rule wins", which is a simplification of Google's official octet-based longest-match algorithm. It matches correctly for the common cases (see `robots_test.go`) but isn't a byte-perfect reimplementation of every edge case in Google's spec.

## Future Enhancements (Suggested Order)

1. **Worker pool alternative**: refactor goroutine spawner into a fixed worker pool consuming from a shared work channel to compare throughput and resource usage patterns.
