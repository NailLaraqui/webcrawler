// Package fetcher handles HTTP retrieval of page bodies.
package fetcher

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Client wraps an http.Client configured for crawling.
type Client struct {
	http *http.Client
}

// New returns a Client with a sane per-request timeout.
func New(timeout time.Duration) *Client {
	return &Client{
		http: &http.Client{
			Timeout: timeout,
		},
	}
}

// Fetch retrieves the body of url as a string, respecting ctx cancellation.
// Non-2xx responses are treated as errors.

func (c *Client) Fetch(ctx context.Context, url string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("building request: %w", err)
	}

	req.Header.Set("User-Agent", "go-webcrawler/0.1 (learning project)")

	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetching %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("fetching %s: unexpected status %d", url, resp.StatusCode)
	}

	// Cap body size to avoid pathological pages blowing up memory.
	const maxBody = 2 << 20 // 5 MB
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return "", fmt.Errorf("reading body of %s: %w", url, err)
	}

	return string(body), nil
}
