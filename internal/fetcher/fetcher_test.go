package fetcher

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestFetch_Success(t *testing.T) {
	// httptest.NewServer spins up a real local HTTP server, so we test
	// against actual network code without depending on the internet.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("<html>hello</html>"))
	}))
	defer srv.Close()

	c := New(2 * time.Second)
	body, err := c.Fetch(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("Fetch returned error: %v", err)
	}
	if !strings.Contains(body, "hello") {
		t.Errorf("body = %q, want it to contain %q", body, "hello")
	}
}

func TestFetch_NonOKStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	c := New(2 * time.Second)
	_, err := c.Fetch(context.Background(), srv.URL)
	if err == nil {
		t.Fatal("expected an error for a 404 response, got nil")
	}
}

func TestFetch_RespectsContextCancellation(t *testing.T) {
	// A handler that hangs, to prove ctx cancellation actually stops us
	// from waiting for a response that never comes.
	block := make(chan struct{})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-block
	}))
	// Defers run LIFO: srv.Close() waits for in-flight handlers to
	// finish, so close(block) must run FIRST to unstick the handler.
	// Registering it after defer srv.Close() achieves that.
	defer srv.Close()
	defer close(block)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	c := New(5 * time.Second) // client timeout longer than ctx timeout on purpose
	_, err := c.Fetch(ctx, srv.URL)
	if err == nil {
		t.Fatal("expected an error when context deadline is exceeded, got nil")
	}
}

func TestFetch_BodySizeIsCapped(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Write more than the 5MB cap so we can confirm Fetch truncates
		// instead of reading it all into memory.
		chunk := strings.Repeat("a", 1<<20) // 1MB
		for range 6 {
			w.Write([]byte(chunk))
		}
	}))
	defer srv.Close()

	c := New(5 * time.Second)
	body, err := c.Fetch(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("Fetch returned error: %v", err)
	}
	const maxBody = 5 << 20
	if len(body) > maxBody {
		t.Errorf("body length = %d, want <= %d", len(body), maxBody)
	}
}
