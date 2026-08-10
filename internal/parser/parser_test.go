package parser

import (
	"reflect"
	"testing"
)

func TestExtractLinks(t *testing.T) {
	// Table-driven test: each case is a scenario, run independently via
	// t.Run so a failure in one doesn't hide failures in the others and
	// you get a clear "TestExtractLinks/case_name" in the output.
	cases := []struct {
		name string
		base string
		body string
		want []string
	}{
		{
			name: "absolute and relative links",
			base: "https://example.com/blog/",
			body: `<a href="https://other.com/x">x</a> <a href="/about">about</a>`,
			want: []string{"https://other.com/x", "https://example.com/about"},
		},
		{
			name: "dedups repeated links",
			base: "https://example.com",
			body: `<a href="/a">1</a><a href="/a">2</a>`,
			want: []string{"https://example.com/a"},
		},
		{
			name: "drops fragments",
			base: "https://example.com",
			body: `<a href="/page#section2">jump</a>`,
			want: []string{"https://example.com/page"},
		},
		{
			name: "drops non-http schemes",
			base: "https://example.com",
			body: `<a href="mailto:x@y.com">mail</a><a href="javascript:void(0)">js</a><a href="/ok">ok</a>`,
			want: []string{"https://example.com/ok"},
		},
		{
			name: "decodes html entities in href",
			base: "https://example.com",
			body: `<a href="/search?a=1&amp;b=2">search</a>`,
			want: []string{"https://example.com/search?a=1&b=2"},
		},
		{
			name: "no links found",
			base: "https://example.com",
			body: `<p>nothing here</p>`,
			want: nil,
		},
		{
			name: "malformed href ignored, rest still parsed",
			body: `<a href="ht!tp://[bad">bad</a><a href="/good">good</a>`,
			base: "https://example.com",
			want: []string{"https://example.com/good"},
		},
		{
			// A regexp would either need a special case for unquoted
			// attributes or silently miss this link entirely; a real
			// tokenizer handles it the same way a browser would.
			name: "unquoted attribute value",
			base: "https://example.com",
			body: `<a href=/no-quotes>link</a>`,
			want: []string{"https://example.com/no-quotes"},
		},
		{
			// Unclosed <a> tag followed by more markup — the kind of
			// malformed HTML real sites produce constantly. A regexp
			// matching on `href=...` alone doesn't care whether tags
			// are ever closed, but this is exactly the class of input
			// the tokenizer is meant to handle gracefully instead of
			// getting confused by.
			name: "unclosed tag before another link",
			base: "https://example.com",
			body: `<a href="/first"><p>oops no closing tag<a href="/second">second</a>`,
			want: []string{"https://example.com/first", "https://example.com/second"},
		},
		{
			// href inside a <script> block must NOT be extracted — it's
			// not a real link, it's JS source text. A naive regexp over
			// raw bytes can't tell the difference; a tokenizer that
			// understands script-tag content can.
			name: "href-like text inside script tag is ignored",
			base: "https://example.com",
			body: `<script>var = '<a href="/fake-link">not real</a>';</script><a href="/real">real</a>`,
			want: []string{"https://example.com/real"},
		},
		{
			// Attributes in mixed order, extra whitespace, and other
			// attributes present alongside href — the tokenizer parses
			// attributes by name, not by position or exact spacing.
			name: "extra attributes and irregular spacing",
			base: "https://example.com",
			body: `<a 	class="btn"   href = "/spaced"  target="_blank" >click</a>`,
			want: []string{"https://example.com/spaced"},
		},
		{
			// A named HTML entity (not just &amp;) in the href, to
			// confirm the tokenizer's built-in unescaping isn't limited
			// to the one entity the old regexp-era code handled by hand.
			name: "named entity othen than amp",
			base: "https://example.com",
			body: `<a href="/caf&eacute;">caf&eacute;</a>`,
			want: []string{"https://example.com/caf%C3%A9"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ExtractLinks(tc.base, tc.body)

			// ExtractLinks returns a non-nil empty slice when nothing
			// matches, not nil — treat "no links" (len 0) as equal
			// regardless of nilness, since that distinction isn't part
			// of the function's actual contract.
			if len(got) == 0 && len(tc.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("ExtractLinks(%q, ...) = %v, want %v", tc.base, got, tc.want)
			}
		})
	}
}

func TestExtractLinks_InvalidBase(t *testing.T) {
	// A malformed base URL should fail gracefully (nil), not panic.
	got := ExtractLinks("://not-a-url", `<a href="/a">a</a>`)
	if got != nil {
		t.Errorf("expected nil for invalid base URL, got %v", got)
	}
}
