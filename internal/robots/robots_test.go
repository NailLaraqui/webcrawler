package robots

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/NailLaraqui/webcrawler/internal/fetcher"
)

func newRobotsServer(t *testing.T, robotsTxt string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/robots.txt", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(robotsTxt))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	})
	return httptest.NewServer(mux)
}

func TestAllowed_WildcardDisallow(t *testing.T) {
	robotsTxt := `
User-agent: *
Disallow: /private
Allow: /private/public-ish
`
	srv := newRobotsServer(t, robotsTxt)
	defer srv.Close()

	c := New(fetcher.New(2*time.Second), "go-webcrawler")
	ctx := context.Background()

	cases := []struct {
		path string
		want bool
	}{
		{"/", true},
		{"/private", false},
		{"/private/secret", false},
		{"/private/public-ish", true}, // longest match wins (Allow is more specific)
		{"/blog/post-1", true},
	}
	for _, tc := range cases {
		got := c.Allowed(ctx, srv.URL+tc.path)
		if got != tc.want {
			t.Errorf("Allowed(%s) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

func TestAllowed_SpecificAgentOverridesWildcard(t *testing.T) {
	robotsTxt := `
User-agent: *
Disallow: /

User-agent: go-webcrawler
Disallow: /admin
Allow: /
`
	srv := newRobotsServer(t, robotsTxt)
	defer srv.Close()

	c := New(fetcher.New(2*time.Second), "go-webcrawler/0.1")
	ctx := context.Background()

	if !c.Allowed(ctx, srv.URL+"/blog") {
		t.Error("expected /blog to be allowed for our specific agent, wildcard block should not apply")
	}
	if c.Allowed(ctx, srv.URL+"/admin") {
		t.Error("expected /admin to be disallowed for our specific agent")
	}
}

func TestAllowed_GroupedUserAgents(t *testing.T) {
	// Two consecutive User-agent lines share the rules that follow.
	robotsTxt := `
User-agent: botA
User-agent: go-webcrawler
Disallow: /no-bots
`
	srv := newRobotsServer(t, robotsTxt)
	defer srv.Close()

	c := New(fetcher.New(2*time.Second), "go-webcrawler")
	if c.Allowed(context.Background(), srv.URL+"/no-bots") {
		t.Error("expected /no-bots to be disallowed via grouped user-agent block")
	}
}

func TestAllowed_MissingRobotsTxtFailsOpen(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/robots.txt", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := New(fetcher.New(2*time.Second), "go-webcrawler")
	if !c.Allowed(context.Background(), srv.URL+"/anything") {
		t.Error("expected fail-open (allowed) when robots.txt is missing")
	}
}

func TestAllowed_MalformedURLFailsOpen(t *testing.T) {
	c := New(fetcher.New(2*time.Second), "go-webcrawler")
	if !c.Allowed(context.Background(), "://not-a-url") {
		t.Error("expected fail-open (allowed) for an unparseable URL")
	}
}

func TestCrawlDelay(t *testing.T) {
	robotsTxt := `
User-agent: *
Crawl-delay: 2
`
	srv := newRobotsServer(t, robotsTxt)
	defer srv.Close()

	c := New(fetcher.New(2*time.Second), "go-webcrawler")
	got := c.CrawlDelay(context.Background(), srv.URL+"/page")
	want := 2 * time.Second
	if got != want {
		t.Errorf("CrawlDelay = %v, want %v", got, want)
	}
}

func TestCrawlDelay_UnspecifiedIsZero(t *testing.T) {
	srv := newRobotsServer(t, "User-agent: *\nDisallow:\n")
	defer srv.Close()

	c := New(fetcher.New(2*time.Second), "go-webcrawler")
	if got := c.CrawlDelay(context.Background(), srv.URL+"/page"); got != 0 {
		t.Errorf("CrawlDelay = %v, want 0", got)
	}
}

func TestRobotsTxtIsFetchedOnlyOncePerHost(t *testing.T) {
	var hits int32
	mux := http.NewServeMux()
	mux.HandleFunc("/robots.txt", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		time.Sleep(10 * time.Millisecond) // widen the race window
		w.Write([]byte("User-agent: *\nDisallow: /private\n"))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := New(fetcher.New(2*time.Second), "go-webcrawler")

	// Fire many concurrent Allowed() calls for the same host; sync.Once
	// inside rulesFor should collapse them into a single robots.txt fetch.
	const n = 20
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			c.Allowed(context.Background(), fmt.Sprintf("%s/page%d", srv.URL, i))
		}(i)
	}
	wg.Wait()

	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Errorf("robots.txt fetched %d times, want exactly 1", got)
	}
}

func TestAllowed_EmptyDisallowMeansAllowAll(t *testing.T) {
	srv := newRobotsServer(t, "User-agent: *\nDisallow:\n")
	defer srv.Close()

	c := New(fetcher.New(2*time.Second), "go-webcrawler")
	if !c.Allowed(context.Background(), srv.URL+"/anything/at/all") {
		t.Error("empty Disallow value should mean everything is allowed")
	}
}

func TestAllowed_CommentsAndBlankLinesIgnored(t *testing.T) {
	robotsTxt := `
# this is a comment
User-agent: *   # inline comment too

Disallow: /secret
`
	srv := newRobotsServer(t, robotsTxt)
	defer srv.Close()

	c := New(fetcher.New(2*time.Second), "go-webcrawler")
	if c.Allowed(context.Background(), srv.URL+"/public") == false {
		t.Error("/public should be allowed")
	}
	if c.Allowed(context.Background(), srv.URL+"/secret") {
		t.Error("/secret should be disallowed")
	}
}
