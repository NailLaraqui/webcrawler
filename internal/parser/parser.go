// Package parser extracts links from raw HTML.
//
// This uses a regexp for simplicity so the project has zero external
// dependencies. For a more robust parser, swap this out for
// golang.org/x/net/html (a proper tokenizer) once you add module deps.

package parser

import (
	"net/url"
	"strings"

	"golang.org/x/net/html"
)

// ExtractLinks finds every <a href="..."> in body and resolves it
// against base, returning only absolute http(s) URLs, deduplicated and
// in document order. Fragments are stripped; mailto:, javascript:, and
// other non-http(s) schemes are dropped, as are malformed hrefs.
func ExtractLinks(base string, body string) []string {
	baseURL, err := url.Parse(base)
	if err != nil {
		return nil
	}

	z := html.NewTokenizer(strings.NewReader(body))
	seen := make(map[string]struct{})
	var links []string

	for {
		switch z.Next() {
		case html.ErrorToken:
			// z.Err() is io.EOF on a clean end of input; any other
			// tokenizer error (malformed byte sequences, etc.) is
			// non-fatal too — we just stop and return what we found so
			// far, the same "best effort" behaviour a browser has.
			return links

		case html.StartTagToken, html.SelfClosingTagToken:
			tok := z.Token()
			if tok.Data != "a" {
				continue
			}
			if link, ok := resolveHref(baseURL, tok); ok {
				if _, dup := seen[link]; !dup {
					seen[link] = struct{}{}
					links = append(links, link)
				}
			}
		}
	}
}

// resolveHref pulls the href attribute off an <a> tag (if any) and
// resolves it into an absolute http(s) URL string. The tokenizer has
// already HTML-unescaped the attribute value for us (so "&amp;" in the
// source is already "&" here), and already ignores anything between
// unrelated tags, so there's no separate entity-decoding step needed
// like the old regexp version required.
func resolveHref(base *url.URL, tok html.Token) (string, bool) {
	for _, attr := range tok.Attr {
		if attr.Key != "href" {
			continue
		}
		u, err := url.Parse(attr.Val)
		if err != nil {
			return "", false
		}
		resolved := base.ResolveReference(u)
		if resolved.Scheme != "http" && resolved.Scheme != "https" {
			return "", false
		}
		resolved.Fragment = ""
		return resolved.String(), true
	}
	return "", false
}
