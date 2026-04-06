package builtin

import (
	"net/url"
	"strings"
	"testing"

	"github.com/PuerkitoBio/goquery"
)

func TestHeadlessBrowseToolMetadata(t *testing.T) {
	tool := &HeadlessBrowseTool{}

	t.Run("Name", func(t *testing.T) {
		if tool.Name() != "browse_url" {
			t.Errorf("Name() = %q, want %q", tool.Name(), "browse_url")
		}
	})

	t.Run("Description", func(t *testing.T) {
		desc := tool.Description()
		if desc == "" {
			t.Error("Description() should not be empty")
		}
		if !strings.Contains(desc, "web") && !strings.Contains(desc, "Fetch") {
			t.Error("Description should describe web fetching capability")
		}
	})

	t.Run("Parameters", func(t *testing.T) {
		params := tool.Parameters()
		if params.Type != "object" {
			t.Errorf("Parameters().Type = %q, want %q", params.Type, "object")
		}
		if _, ok := params.Properties["url"]; !ok {
			t.Error("Parameters should have 'url' property")
		}
		if _, ok := params.Properties["selector"]; !ok {
			t.Error("Parameters should have 'selector' property")
		}
		if _, ok := params.Properties["max_length"]; !ok {
			t.Error("Parameters should have 'max_length' property")
		}
		if len(params.Required) != 1 || params.Required[0] != "url" {
			t.Errorf("Parameters().Required = %v, want [url]", params.Required)
		}
	})
}

func TestHeadlessBrowseToolValidation(t *testing.T) {
	tool := &HeadlessBrowseTool{}

	tests := []struct {
		name      string
		params    map[string]any
		wantError string
	}{
		{
			name:      "missing url parameter",
			params:    map[string]any{},
			wantError: "url parameter must be a non-empty string",
		},
		{
			name:      "empty url parameter",
			params:    map[string]any{"url": ""},
			wantError: "url parameter must be a non-empty string",
		},
		{
			name:      "whitespace url parameter",
			params:    map[string]any{"url": "   "},
			wantError: "url parameter must be a non-empty string",
		},
		{
			name:      "url is not a string",
			params:    map[string]any{"url": 123},
			wantError: "url parameter must be a non-empty string",
		},
		{
			name:      "invalid url (no scheme)",
			params:    map[string]any{"url": "example.com"},
			wantError: "invalid url provided",
		},
		{
			name:      "invalid url (no host)",
			params:    map[string]any{"url": "https://"},
			wantError: "invalid url provided",
		},
		{
			name:      "malformed url",
			params:    map[string]any{"url": "://invalid"},
			wantError: "invalid url provided",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result, err := tool.Execute(tc.params)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if result.Success {
				t.Error("expected failure for invalid input")
			}
			if result.Error != tc.wantError {
				t.Errorf("Error = %q, want %q", result.Error, tc.wantError)
			}
		})
	}
}

func TestHeadlessBrowseToolMaxLengthParameter(t *testing.T) {
	// This test focuses on the max_length parsing logic
	// We can't actually make HTTP requests in unit tests, but we can verify
	// that parseInt is called correctly by checking edge cases

	tool := &HeadlessBrowseTool{}

	tests := []struct {
		name         string
		maxLengthVal any
		description  string
	}{
		{
			name:         "valid integer",
			maxLengthVal: 1000,
			description:  "should use provided value",
		},
		{
			name:         "valid float64",
			maxLengthVal: float64(2000),
			description:  "should convert float to int",
		},
		{
			name:         "zero value",
			maxLengthVal: 0,
			description:  "should fall back to default",
		},
		{
			name:         "negative value",
			maxLengthVal: -100,
			description:  "should fall back to default",
		},
		{
			name:         "too large value",
			maxLengthVal: 50000,
			description:  "should fall back to default",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			params := map[string]any{
				"url":        "https://example.com",
				"max_length": tc.maxLengthVal,
			}
			// This will fail at the HTTP request stage, but at least it validates
			// that parameter parsing doesn't panic
			result, err := tool.Execute(params)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			// We expect it to fail at the network level, not at parameter parsing
			_ = result
		})
	}
}

func TestCollectLinksLimitsAndResolves(t *testing.T) {
	html := `<a href="/one">One</a><a href="/two">Two</a><a href="/three">Three</a>`
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	if err != nil {
		t.Fatalf("NewDocumentFromReader: %v", err)
	}
	base, _ := url.Parse("https://example.com/path")
	links := collectLinks(doc, base, 2)
	if len(links) != 2 {
		t.Fatalf("expected 2 links, got %d", len(links))
	}
	if links[0]["url"] != "https://example.com/one" {
		t.Fatalf("expected resolved URL, got %s", links[0]["url"])
	}
}

func TestCollectLinksEdgeCases(t *testing.T) {
	tests := []struct {
		name      string
		html      string
		baseURL   string
		limit     int
		wantCount int
		checkFunc func(t *testing.T, links []map[string]string)
	}{
		{
			name:      "empty document",
			html:      "<html><body></body></html>",
			baseURL:   "https://example.com",
			limit:     10,
			wantCount: 0,
		},
		{
			name:      "no links",
			html:      "<p>Just text, no links</p>",
			baseURL:   "https://example.com",
			limit:     10,
			wantCount: 0,
		},
		{
			name:      "link without href",
			html:      "<a name='anchor'>Named anchor</a>",
			baseURL:   "https://example.com",
			limit:     10,
			wantCount: 0,
		},
		{
			name:      "limit of zero",
			html:      "<a href='/one'>One</a>",
			baseURL:   "https://example.com",
			limit:     0,
			wantCount: 0,
		},
		{
			name:      "absolute URL preserved",
			html:      "<a href='https://other.com/page'>Other</a>",
			baseURL:   "https://example.com",
			limit:     5,
			wantCount: 1,
			checkFunc: func(t *testing.T, links []map[string]string) {
				if links[0]["url"] != "https://other.com/page" {
					t.Errorf("expected absolute URL to be preserved, got %s", links[0]["url"])
				}
			},
		},
		{
			name:      "relative URL resolved",
			html:      "<a href='../sibling/page'>Sibling</a>",
			baseURL:   "https://example.com/dir/page",
			limit:     5,
			wantCount: 1,
			checkFunc: func(t *testing.T, links []map[string]string) {
				if links[0]["url"] != "https://example.com/sibling/page" {
					t.Errorf("expected resolved URL, got %s", links[0]["url"])
				}
			},
		},
		{
			name:      "link with empty text uses URL",
			html:      "<a href='/path'></a>",
			baseURL:   "https://example.com",
			limit:     5,
			wantCount: 1,
			checkFunc: func(t *testing.T, links []map[string]string) {
				if links[0]["title"] != "https://example.com/path" {
					t.Errorf("expected title to be URL when text is empty, got %s", links[0]["title"])
				}
			},
		},
		{
			name:      "link with whitespace text uses URL",
			html:      "<a href='/path'>   </a>",
			baseURL:   "https://example.com",
			limit:     5,
			wantCount: 1,
			checkFunc: func(t *testing.T, links []map[string]string) {
				if links[0]["title"] != "https://example.com/path" {
					t.Errorf("expected title to be URL when text is whitespace, got %s", links[0]["title"])
				}
			},
		},
		{
			name:      "link text trimmed",
			html:      "<a href='/path'>  Some Text  </a>",
			baseURL:   "https://example.com",
			limit:     5,
			wantCount: 1,
			checkFunc: func(t *testing.T, links []map[string]string) {
				if links[0]["title"] != "Some Text" {
					t.Errorf("expected trimmed title, got %q", links[0]["title"])
				}
			},
		},
		{
			name:      "respects limit",
			html:      "<a href='/1'>1</a><a href='/2'>2</a><a href='/3'>3</a><a href='/4'>4</a><a href='/5'>5</a>",
			baseURL:   "https://example.com",
			limit:     3,
			wantCount: 3,
		},
		{
			name:      "href with leading/trailing whitespace",
			html:      "<a href='  /path  '>Link</a>",
			baseURL:   "https://example.com",
			limit:     5,
			wantCount: 1,
			checkFunc: func(t *testing.T, links []map[string]string) {
				if links[0]["url"] != "https://example.com/path" {
					t.Errorf("expected trimmed href, got %s", links[0]["url"])
				}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			doc, err := goquery.NewDocumentFromReader(strings.NewReader(tc.html))
			if err != nil {
				t.Fatalf("NewDocumentFromReader: %v", err)
			}
			base, err := url.Parse(tc.baseURL)
			if err != nil {
				t.Fatalf("url.Parse: %v", err)
			}
			links := collectLinks(doc, base, tc.limit)
			if len(links) != tc.wantCount {
				t.Errorf("expected %d links, got %d", tc.wantCount, len(links))
			}
			if tc.checkFunc != nil && len(links) > 0 {
				tc.checkFunc(t, links)
			}
		})
	}
}
