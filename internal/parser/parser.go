// Package parser extracts links from raw HTML.
//
// This uses a regexp for simplicity so the project has zero external
// dependencies. For a more robust parser, swap this out for
// golang.org/x/net/html (a proper tokenizer) once you add module deps.

package parser

import (
	"html"
	"net/url"
	"regexp"
)

var hrefRe = regexp.MustCompile(`(?i)href\s*=\s*["']([^"'#]+)`)

// ExtractLinks finds every href in body and resolves it against base,
// returning only absolute http(s) URLs. Fragments, mailto:, javascript:
// and malformed links are dropped.
func ExtractLinks(base string, body string) []string {
	baseURL, err := url.Parse(base)
	if err != nil {
		return nil
	}

	matches := hrefRe.FindAllStringSubmatch(body, -1)
	seen := make(map[string]struct{}, len(matches))
	links := make([]string, 0, len(matches))

	for _, m := range matches {
		// HTML attributes commonly encode "&" as "&amp;"; decode before
		// parsing or query strings like "?a=1&amp;b=2" break.
		raw := html.UnescapeString(m[1])

		u, err := url.Parse(raw)
		if err != nil {
			continue
		}

		resolved := baseURL.ResolveReference(u)
		if resolved.Scheme != "http" && resolved.Scheme != "https" {
			continue
		}

		resolved.Fragment = ""

		final := resolved.String()
		if _, ok := seen[final]; ok {
			continue
		}
		seen[final] = struct{}{}
		links = append(links, final)
	}

	return links
}
