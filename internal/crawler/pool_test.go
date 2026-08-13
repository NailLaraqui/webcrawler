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
	"github.com/NailLaraqui/webcrawler/internal/ratelimit"
	"github.com/NailLaraqui/webcrawler/internal/robots"
)

func TestPoolCrawler_VisitsReachablePagesWithinDepth(t *testing.T) {
	graph := map[string][]string{
		"/":   {"/a", "/b"},
		"/a":  {"/a1"},
		"/b":  {"/b1"},
		"/a1": {},
		"/b1": {},
	}

	srv := newLinkedSite(t, graph)
	defer srv.Close()

	cw := NewPool(Config{MaxDepth: 1, MaxConcurrency: 4}, fetcher.New(2*time.Second))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	visited := map[string]int{}
	for r := range cw.Run(ctx, srv.URL+"/") {
		if r.Err != nil {
			t.Errorf("unexpected error visiting %s: %v", r.URL, r.Err)
			continue
		}
		visited[r.URL] = r.Depth
	}

	want := map[string]int{srv.URL + "/": 0, srv.URL + "/a": 1, srv.URL + "/b": 1}
	if len(visited) != len(want) {
		t.Fatalf("visited %d pages %v, want %d pages %v", len(visited), visited, len(want), want)
	}
	for url, depth := range want {
		if got, ok := visited[url]; !ok || got != depth {
			t.Errorf("depth for %s = %d, ok=%v, want %d", url, got, ok, depth)
		}
	}
	for _, unwanted := range []string{srv.URL + "/a1", srv.URL + "/b1"} {
		if _, ok := visited[unwanted]; ok {
			t.Errorf("%s should not have been visited (exceeds MaxDepth)", unwanted)
		}
	}
}

func TestPoolCrawler_DedupsSharedLinks(t *testing.T) {
	var sharedHits atomic.Int32
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
		sharedHits.Add(1)
		w.Write([]byte(""))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	cw := NewPool(Config{MaxDepth: 3, MaxConcurrency: 4}, fetcher.New(2*time.Second))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	for r := range cw.Run(ctx, srv.URL+"/") {
		if r.Err != nil {
			t.Errorf("unexpected error visiting %s: %v", r.URL, r.Err)
		}
	}

	if got := sharedHits.Load(); got != 1 {
		t.Errorf("/shared was fetched %d times, want exactly 1", got)
	}
}

func TestPoolCrawler_NeverExceedsWorkerCount(t *testing.T) {
	const numWorkers = 3
	const fanout = 12

	graph := map[string][]string{"/": {}}
	for i := 0; i < fanout; i++ {
		path := fmt.Sprintf("/p%d", i)
		graph["/"] = append(graph["/"], path)
		graph[path] = nil
	}
	srv := newLinkedSite(t, graph)
	defer srv.Close()

	var mu sync.Mutex
	var current, peak int
	base := srv.Config.Handler
	tracking := http.NewServeMux()
	tracking.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
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
	srv.Config.Handler = tracking

	cw := NewPool(Config{MaxDepth: 1, MaxConcurrency: numWorkers}, fetcher.New(2*time.Second))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	for r := range cw.Run(ctx, srv.URL+"/") {
		if r.Err != nil {
			t.Errorf("unexpected error visiting %s: %v", r.URL, r.Err)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	if peak > numWorkers {
		t.Errorf("peak concurrent requests = %d, want <= %d (fixed worker count)", peak, numWorkers)
	}
}

func TestPoolCrawler_RespectsRobotsTxt(t *testing.T) {
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
	cw := NewPool(Config{MaxDepth: 1, MaxConcurrency: 4, Robots: checker}, fc)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	results := map[string]Result{}
	for r := range cw.Run(ctx, srv.URL+"/") {
		results[r.URL] = r
	}

	if !errors.Is(results[srv.URL+"/forbidden"].Err, robots.ErrDisallowed) {
		t.Errorf("/forbidden Err = %v, want robots.ErrDisallowed", results[srv.URL+"/forbidden"].Err)
	}
	if results[srv.URL+"/ok"].Err != nil {
		t.Errorf("/ok should have been fetched normally, got err %v", results[srv.URL+"/ok"].Err)
	}
}

func TestPoolCrawler_RespectsRateLimiter(t *testing.T) {
	graph := map[string][]string{
		"/":  {"/a", "/b", "/c", "/d"},
		"/a": {}, "/b": {}, "/c": {}, "/d": {},
	}
	srv := newLinkedSite(t, graph)
	defer srv.Close()

	fc := fetcher.New(2 * time.Second)
	limiter := ratelimit.New(50 * time.Millisecond)
	cw := NewPool(Config{MaxDepth: 1, MaxConcurrency: 10, RateLimiter: limiter}, fc)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	start := time.Now()
	visited := 0
	for r := range cw.Run(ctx, srv.URL+"/") {
		if r.Err != nil {
			t.Errorf("unexpected error visiting %s: %v", r.URL, r.Err)
		}
		visited++
	}
	elapsed := time.Since(start)

	if visited != 5 {
		t.Fatalf("visited %d pages, want 5", visited)
	}
	want := 4 * 50 * time.Millisecond
	if elapsed < want-15*time.Millisecond {
		t.Errorf("crawl took %v, want at least ~%v given the per-host rate limit", elapsed, want)
	}
}

func TestPoolCrawler_StopsOnContextCancellation(t *testing.T) {
	block := make(chan struct{})
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		<-block
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	defer close(block)

	// Fixed pool of 2 workers, but the seed page never lets a worker
	// return: this proves cancellation unblocks pop() directly rather
	// than only working when a job happens to finish naturally.
	cw := NewPool(Config{MaxDepth: 1, MaxConcurrency: 2}, fetcher.New(10*time.Second))
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
	case <-time.After(2 * time.Second):
		t.Fatal("pool crawl did not stop after context cancellation; possible deadlock in workQueue")
	}
}

func TestPoolCrawler_ZeroFanoutClosesCleanly(t *testing.T) {
	// A page with zero links: pending must still correctly reach 0 and
	// close the queue, or every worker blocks forever in pop().
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("<p>no links here</p>"))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	cw := NewPool(Config{MaxDepth: 2, MaxConcurrency: 4}, fetcher.New(2*time.Second))
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	count := 0
	for range cw.Run(ctx, srv.URL+"/") {
		count++
	}
	if count != 1 {
		t.Errorf("visited %d pages, want exactly 1", count)
	}
}
