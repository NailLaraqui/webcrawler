package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/NailLaraqui/webcrawler/internal/crawler"
	"github.com/NailLaraqui/webcrawler/internal/fetcher"
	"github.com/NailLaraqui/webcrawler/internal/robots"
)

func main() {
	var (
		start          = flag.String("url", "", "seed URL to start crawling from (required)")
		maxDepth       = flag.Int("depth", 2, "maximum link depth to follow")
		maxConcurrent  = flag.Int("concurrency", 8, "maximum simultaneous requests")
		timeout        = flag.Duration("timeout", 30*time.Second, "overall crawl timeout")
		reqTimeout     = flag.Duration("req-duration", 5*time.Second, "per-request timeout")
		sameHost       = flag.Bool("same-host", true, "only follow links on the same host as the seed URL")
		respectsRobots = flag.Bool("respect-robots", true, "check robots.txt before fetching each page")
	)
	flag.Parse()

	if *start == "" {
		fmt.Fprintln(os.Stderr, "error: -url is required")
		flag.Usage()
		os.Exit(1)
	}

	// Overall deadline for the whole crawl.
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	// Let Ctrl+C cancel the same context, so in-flight goroutines unwind
	// cleanly instead of the process just dying mid-request.
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	fc := fetcher.New(*reqTimeout)

	var robotsChecker *robots.Checker
	if *respectsRobots {
		robotsChecker = robots.New(fc, fetcher.UserAgent)
	}

	cw := crawler.New(crawler.Config{
		MaxDepth:       *maxDepth,
		MaxConcurrency: *maxConcurrent,
		SameHostOnly:   *sameHost,
		Robots:         robotsChecker,
	}, fc)

	fmt.Printf("Crawling %s (depth=%d, concurrency=%d, timeout=%s)\n\n", *start, *maxDepth, *maxConcurrent, *timeout)

	var visited, failed, totalLinks int
	started := time.Now()

	for r := range cw.Run(ctx, *start) {
		visited++
		indent := strings.Repeat(" ", r.Depth)
		if r.Err != nil {
			failed++
			fmt.Printf("%s[depth %d] FAIL %s (%v)\n", indent, r.Depth, r.URL, r.Err)
			continue
		}
		totalLinks += r.LinksFound
		fmt.Printf("%s[depth %d] OK %s (%d links)\n", indent, r.Depth, r.URL, r.LinksFound)
	}

	fmt.Printf("\ndone in %s - %d pages visited, %d failed, %d links discovered\n",
		time.Since(started).Round(time.Millisecond), visited, failed, totalLinks)

	if err := ctx.Err(); err != nil {
		fmt.Fprintf(os.Stderr, "crawl stopped early: %v\n", err)
	}
}
