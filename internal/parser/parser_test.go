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
