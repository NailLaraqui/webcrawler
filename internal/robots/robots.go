// Package robots implements a minimal robots.txt checker: fetch once per
// host, parse Allow/Disallow/Crawl-delay directives, and cache the result
// so repeated lookups for the same host are free.
package robots

import (
	"bufio"
	"context"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/NailLaraqui/webcrawler/internal/fetcher"
)

// ErrDisallowed is a sentinel error a crawler can check for to distinguish
// "robots.txt forbids this" from a real fetch failure.
var ErrDisallowed = errors.New("disallowed by robots.txt")

// directive is a single Allow/Disallow line.
//
// LIMITATION: matching is prefix-only (the classic original robots.txt
// spec). Real-world robots.txt files widely use the Google/Bing
// extension where "*" matches any sequence and "$" anchors end-of-path
// (e.g. GitHub's "Disallow: /*/*/pulse"). Those patterns are parsed here
// like literal path prefixes, so they will simply never match a real URL
// — meaning such rules are effectively ignored (fail open on that
// specific rule, not on the whole file). Fine for a learning project;
// extend allowed() with a small glob matcher if you need to crawl sites
// that rely on wildcard rules.
type directive struct {
	allow bool
	path  string
}

// ruleset holds the directives that apply to us for one host, plus any
// Crawl-delay we should respect there. A zero-value ruleset (no
// directives) means "everything allowed" — this is also what we use when
// robots.txt is missing or fails to fetch, per the standard convention.
type ruleset struct {
	directives []directive
	crawlDelay time.Duration
}

// allowed reports whether path is permitted, using the longest-matching
// prefix rule (the standard robots.txt tie-breaking convention: the most
// specific rule wins regardless of Allow/Disallow order in the file).
func (rs *ruleset) allowed(path string) bool {
	if rs == nil {
		return true
	}

	bestLen := -1
	bestAllow := true
	for _, d := range rs.directives {
		if !strings.HasPrefix(path, d.path) {
			continue
		}
		if len(d.path) > bestLen {
			bestLen = len(d.path)
			bestAllow = d.allow
		}
	}

	return bestAllow
}

// hostEntry lazily fetches and parses robots.txt for one host exactly
// once, even if many goroutines ask for it concurrently: sync.Once
// guarantees a single winner does the fetch while the rest block on Do
// and then read the same cached result.
type hostEntry struct {
	once sync.Once
	rs   *ruleset
}

// Checker answers "am I allowed to fetch this URL?" against cached
// robots.txt rules, one fetch per host no matter how many goroutines ask.
type Checker struct {
	fetch     *fetcher.Client
	userAgent string
	hosts     sync.Map // key: "scheme://host" -> *hostEntry
}

// New builds a Checker. userAgent should match the User-Agent header your
// fetcher actually sends, so robots.txt group matching is accurate.
func New(fc *fetcher.Client, userAgent string) *Checker {
	return &Checker{fetch: fc, userAgent: userAgent}
}

// Allowed reports whether rawURL may be fetched according to the host's
// robots.txt. On any error parsing rawURL, or fetching/parsing
// robots.txt, it fails open (returns true) — a missing or broken
// robots.txt should never be treated as "disallow everything".
func (c *Checker) Allowed(ctx context.Context, rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil {
		return true
	}
	rs := c.rulesFor(ctx, u)
	return rs.allowed(u.Path)
}

// CrawlDelay returns the Crawl-delay robots.txt requests for rawURL's
// host, or 0 if none was specified. Useful as a per-host rate limit hint.
func (c *Checker) CrawlDelay(ctx context.Context, rawURL string) time.Duration {
	u, err := url.Parse(rawURL)
	if err != nil {
		return 0
	}
	rs := c.rulesFor(ctx, u)
	if rs == nil {
		return 0
	}
	return rs.crawlDelay
}

func (c *Checker) rulesFor(ctx context.Context, u *url.URL) *ruleset {
	key := u.Scheme + "://" + u.Host
	val, _ := c.hosts.LoadOrStore(key, &hostEntry{})
	he := val.(*hostEntry)

	// Only the first caller for this host actually fetches; every other
	// concurrent caller blocks here and then shares the same result.
	he.once.Do(func() {
		he.rs = c.fetchAndParse(ctx, key)
	})
	return he.rs
}

func (c *Checker) fetchAndParse(ctx context.Context, hostKey string) *ruleset {
	body, err := c.fetch.Fetch(ctx, hostKey+"/robots.txt")
	if err != nil {
		// No robots.txt, or couldn't fetch it: allow everything. This
		// matches how real crawlers behave (e.g. Googlebot).
		return &ruleset{}
	}
	return parse(body, c.userAgent)
}

// parse extracts the ruleset applicable to userAgent from a robots.txt
// body. It groups User-agent lines the way the spec describes: one or
// more consecutive "User-agent:" lines form a group, and the
// Allow/Disallow/Crawl-delay lines that follow (until the next group)
// apply to every agent named in that group.
func parse(body, userAgent string) *ruleset {
	groups := map[string]*ruleset{} // lowercase agent token -> accumulated rules

	var currentAgents []string
	groupOpen := false // true while we're still inside a "User-agent:" block, i.e. haven't seen a directive yet

	scanner := bufio.NewScanner(strings.NewReader(body))
	for scanner.Scan() {
		line := stripComment(scanner.Text())
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key = strings.ToLower(strings.TrimSpace(key))
		value = strings.TrimSpace(value)

		switch key {
		case "user-agent":
			agent := strings.ToLower(value)
			if !groupOpen {
				// A directive line closed the previous group, so this
				// User-agent starts a fresh one.
				currentAgents = nil
				groupOpen = true
			}
			currentAgents = append(currentAgents, agent)
			if _, exists := groups[agent]; !exists {
				groups[agent] = &ruleset{}
			}
		case "allow", "disallow":
			groupOpen = false
			if value == "" {
				// "Disallow:" with no path is a no-op (means "allow
				// all" per spec); nothing to record.
				continue
			}
			for _, a := range currentAgents {
				groups[a].directives = append(groups[a].directives, directive{
					allow: key == "allow",
					path:  value,
				})
			}
		case "crawl-delay":
			groupOpen = false
			secs, err := strconv.ParseFloat(value, 64)
			if err != nil {
				continue
			}
			d := time.Duration(secs * float64(time.Second))
			for _, a := range currentAgents {
				groups[a].crawlDelay = d
			}
		default:
			// Sitemap:, Host:, and anything else we don't act on.
		}
	}

	return selectGroup(groups, userAgent)
}

// selectGroup picks the most specific matching group for userAgent,
// falling back to the wildcard "*" group, and finally to an empty
// (permissive) ruleset if robots.txt named no groups at all.
func selectGroup(groups map[string]*ruleset, userAgent string) *ruleset {
	lowerAgent := strings.ToLower(userAgent)

	var best *ruleset
	bestLen := -1
	for agent, rs := range groups {
		if agent == "*" || agent == "" {
			continue
		}
		if strings.Contains(lowerAgent, agent) && len(agent) > bestLen {
			best = rs
			bestLen = len(agent)
		}
	}
	if best != nil {
		return best
	}

	if rs, ok := groups["*"]; ok {
		return rs
	}

	return &ruleset{}
}

func stripComment(line string) string {
	if i := strings.IndexByte(line, '#'); i >= 0 {
		return line[:i]
	}

	return line
}
