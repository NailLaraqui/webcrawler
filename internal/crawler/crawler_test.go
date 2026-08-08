package crawler

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/NailLaraqui/webcrawler/internal/fetcher"
	"github.com/NailLaraqui/webcrawler/internal/robots"
)

// newLinkedSite returns a test server whose pages link to each other
// according to the given adjacency map (path -> paths it links to).
// Handlers sleep briefly so concurrent requests actually overlap in time,
// which is what lets TestCrawler_RespectsConcurrencyLimit detect
// violations instead of everything running effectively sequentially.
func newLinkedSite(t *testing.T, graph map[string][]string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	for path, links := range graph {
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			time.Sleep(20 * time.Millisecond)
			body := ""
			for _, l := range links {
				body += fmt.Sprintf(`<a href="%s">link</a>`, l)
			}

			w.Write([]byte(body))
		})
	}
	return httptest.NewServer(mux)
}

func TestCrawler_VisitsReachablePagesWithinDepth(t *testing.T) {
	graph := map[string][]string{
		"/":   {"/a", "/b"},
		"/a":  {"/a1"}, // depth 2, should NOT be visited when MaxDepth=1
		"/b":  {"/b1"}, // depth 2, should NOT be visited when MaxDepth=1
		"/a1": {},
		"/b1": {},
	}
	srv := newLinkedSite(t, graph)
	defer srv.Close()

	cw := New(Config{MaxDepth: 1, MaxConcurrency: 4}, fetcher.New(2*time.Second))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	visited := map[string]int{} // url -> depth
	for r := range cw.Run(ctx, srv.URL+"/") {
		if r.Err != nil {
			t.Errorf("unexpected error visiting %s: %v", r.URL, r.Err)
			continue
		}
		visited[r.URL] = r.Depth
	}

	wantDepths := map[string]int{
		srv.URL + "/":  0,
		srv.URL + "/a": 1,
		srv.URL + "/b": 1,
	}
	if len(visited) != len(wantDepths) {
		t.Fatalf("visited %d pages %v, want %d pages %v", len(visited), visited, len(wantDepths), wantDepths)
	}
	for url, wantDepth := range wantDepths {
		gotDepth, ok := visited[url]
		if !ok {
			t.Errorf("expected %s to be visited, it wasn't", url)
			continue
		}
		if gotDepth != wantDepth {
			t.Errorf("depth for %s = %d, want %d", url, gotDepth, wantDepth)
		}
	}
	// /a1 and /b1 are one hop too far given MaxDepth=1.
	for _, unwanted := range []string{srv.URL + "/a1", srv.URL + "/b1"} {
		if _, ok := visited[unwanted]; ok {
			t.Errorf("%s should not have been visited (exceeds MaxDepth)", unwanted)
		}
	}
}

func TestCrawler_DedupsSharedLinks(t *testing.T) {
	// Both /a and /b link to /shared: it must only be fetched once.
	var sharedHits int32
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`<a href="/a">a</a><a href="/b">b</a>`))
	})
	mux.HandleFunc("/a", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`<a href="/shared">shared</a>`))
	})
	mux.HandleFunc("/b", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`<a href="/shared">shared</a>`))
	})
	mux.HandleFunc("/shared", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&sharedHits, 1)
		w.Write([]byte(""))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	cw := New(Config{MaxDepth: 3, MaxConcurrency: 4}, fetcher.New(2*time.Second))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	for r := range cw.Run(ctx, srv.URL+"/") {
		if r.Err != nil {
			t.Errorf("unexpected error visiting %s: %v", r.URL, r.Err)
		}
	}

	if got := atomic.LoadInt32(&sharedHits); got != 1 {
		t.Errorf("/shared was fetched %d times, want exactly 1", got)
	}
}

func TestCrawler_RespectsConcurrencyLimit(t *testing.T) {
	const maxConcurrent = 3
	const fanout = 10

	graph := map[string][]string{"/": {}}
	for i := 0; i < fanout; i++ {
		path := fmt.Sprintf("/p%d", i)
		graph["/"] = append(graph["/"], path)
		graph[path] = nil
	}
	srv := newLinkedSite(t, graph)
	defer srv.Close()

	// Wrap the handler logic manually so we can track how many requests
	// are in flight at once, independent of newLinkedSite's own sleep.
	var (
		mu      sync.Mutex
		current int
		peak    int
	)
	trackingMux := http.NewServeMux()
	base := srv.Config.Handler
	trackingMux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		current++
		if current > peak {
			peak = current
		}
		mu.Unlock()

		base.ServeHTTP(w, r)

		mu.Lock()
		current--
		mu.Unlock()
	})
	srv.Config.Handler = trackingMux

	cw := New(Config{MaxDepth: 1, MaxConcurrency: maxConcurrent}, fetcher.New(2*time.Second))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	for r := range cw.Run(ctx, srv.URL+"/") {
		if r.Err != nil {
			t.Errorf("unexpected error visiting %s: %v", r.URL, r.Err)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	if peak > maxConcurrent {
		t.Errorf("peak concurrent requests = %d, want <= %d", peak, maxConcurrent)
	}
}

func TestCrawler_SameHostOnlyFiltersExternalLinks(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`<a href="/internal">internal</a><a href="https://external.example.com/x">external</a>`))
	})
	mux.HandleFunc("/internal", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(""))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	cw := New(Config{MaxDepth: 1, MaxConcurrency: 4, SameHostOnly: true}, fetcher.New(2*time.Second))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	visited := map[string]bool{}
	for r := range cw.Run(ctx, srv.URL+"/") {
		visited[r.URL] = true
	}

	if !visited[srv.URL+"/internal"] {
		t.Errorf("expected internal link to be visited, visited=%v", visited)
	}
	if visited["https://external.example.com/x"] {
		t.Errorf("external link should have been filtered out by SameHostOnly, visited=%v", visited)
	}
}

func TestCrawler_StopsOnContextCancellation(t *testing.T) {
	// A handler that never returns: without cancellation this would hang
	// forever. This proves ctx propagation actually unblocks the crawl.
	block := make(chan struct{})

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		<-block
	})
	srv := httptest.NewServer(mux)
	// Same LIFO ordering concern as in fetcher_test.go: close(block)
	// must run before srv.Close(), so it's deferred after.
	defer srv.Close()
	defer close(block)

	cw := New(Config{MaxDepth: 1, MaxConcurrency: 2}, fetcher.New(10*time.Second))
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	done := make(chan struct{})
	go func() {
		for range cw.Run(ctx, srv.URL+"/") {
		}
		close(done)
	}()

	select {
	case <-done:
		// good: Run's results channel closed once context was cancelled
	case <-time.After(2 * time.Second):
		t.Fatal("crawl did not stop after context cancellation; possible goroutine/deadlock bug")
	}
}

func TestCrawler_RespectsRobotsTxt(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/robots.txt", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("User-agent: *\nDisallow: /forbidden\n"))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`<a href="/forbidden">nope</a><a href="/ok">ok</a>`))
	})
	mux.HandleFunc("/forbidden", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("should never be fetched"))
	})
	mux.HandleFunc("/ok", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("fine"))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	fc := fetcher.New(2 * time.Second)
	checker := robots.New(fc, fetcher.UserAgent)
	cw := New(Config{MaxDepth: 1, MaxConcurrency: 4, Robots: checker}, fc)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	results := map[string]Result{}
	for r := range cw.Run(ctx, srv.URL+"/") {
		results[r.URL] = r
	}

	forbidden, ok := results[srv.URL+"/forbidden"]
	if !ok {
		t.Fatal("expected a Result for /forbidden even though it was skipped")
	}
	if !errors.Is(forbidden.Err, robots.ErrDisallowed) {
		t.Errorf("/forbidden Err = %v, want robots.ErrDisallowed", forbidden.Err)
	}

	okResult, exists := results[srv.URL+"/ok"]
	if !exists {
		t.Fatal("expected /ok to be visited")
	}
	if okResult.Err != nil {
		t.Errorf("/ok should have been fetched normally, got err %v", okResult.Err)
	}
}
