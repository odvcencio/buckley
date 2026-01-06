package builtin

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
)

// HeadlessBrowseTool fetches and extracts text from web pages
type HeadlessBrowseTool struct{}

func (t *HeadlessBrowseTool) Name() string {
	return "browse_url"
}

func (t *HeadlessBrowseTool) Description() string {
	return "**WEB RESEARCH** when user asks about external docs, APIs, libraries, or comparisons. Trigger phrases: 'check the docs', 'look up', 'what does the API say', 'compare with', 'research'. Fetches web pages and extracts clean text. ALWAYS use this instead of hallucinating documentation or guessing API details. Essential for accurate, up-to-date information from official sources."
}

func (t *HeadlessBrowseTool) Parameters() ParameterSchema {
	return ParameterSchema{
		Type: "object",
		Properties: map[string]PropertySchema{
			"url": {
				Type:        "string",
				Description: "URL to fetch (https://...)",
			},
			"selector": {
				Type:        "string",
				Description: "Optional CSS selector to narrow extracted content",
			},
			"max_length": {
				Type:        "integer",
				Description: "Maximum characters of text to return (default 4000)",
				Default:     4000,
			},
		},
		Required: []string{"url"},
	}
}

func (t *HeadlessBrowseTool) Execute(params map[string]any) (*Result, error) {
	rawURL, ok := params["url"].(string)
	if !ok || strings.TrimSpace(rawURL) == "" {
		return &Result{Success: false, Error: "url parameter must be a non-empty string"}, nil
	}

	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return &Result{Success: false, Error: "invalid url provided"}, nil
	}

	selector, _ := params["selector"].(string)
	maxLength := parseInt(params["max_length"], 4000)
	if maxLength <= 0 || maxLength > 20000 {
		maxLength = 4000
	}

	client := &http.Client{
		Timeout: 15 * time.Second,
	}

	req, err := http.NewRequest("GET", parsed.String(), nil)
	if err != nil {
		return &Result{Success: false, Error: fmt.Sprintf("failed to build request: %v", err)}, nil
	}
	req.Header.Set("User-Agent", "BuckleyBot/1.0 (+https://github.com/odvcencio/buckley)")

	resp, err := client.Do(req)
	if err != nil {
		return &Result{Success: false, Error: fmt.Sprintf("request failed: %v", err)}, nil
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return &Result{Success: false, Error: fmt.Sprintf("received status code %d", resp.StatusCode)}, nil
	}

	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return &Result{Success: false, Error: fmt.Sprintf("failed to parse HTML: %v", err)}, nil
	}

	var selection *goquery.Selection
	if strings.TrimSpace(selector) != "" {
		selection = doc.Find(selector)
		if selection.Length() == 0 {
			return &Result{Success: false, Error: "selector returned no elements"}, nil
		}
	} else {
		selection = doc.Find("body")
	}

	text := strings.Join(strings.Fields(selection.Text()), " ")
	if len(text) > maxLength {
		text = text[:maxLength] + "…"
	}

	title := strings.TrimSpace(doc.Find("title").First().Text())

	links := collectLinks(doc, parsed, 10)

	return &Result{
		Success: true,
		Data: map[string]any{
			"url":         parsed.String(),
			"title":       title,
			"text":        text,
			"status_code": resp.StatusCode,
			"links":       links,
		},
	}, nil
}

func collectLinks(doc *goquery.Document, base *url.URL, limit int) []map[string]string {
	links := []map[string]string{}
	doc.Find("a[href]").EachWithBreak(func(i int, s *goquery.Selection) bool {
		if len(links) >= limit {
			return false
		}
		href, exists := s.Attr("href")
		if !exists {
			return true
		}
		linkURL, err := base.Parse(strings.TrimSpace(href))
		if err != nil {
			return true
		}
		text := strings.TrimSpace(s.Text())
		if text == "" {
			text = linkURL.String()
		}
		links = append(links, map[string]string{
			"title": text,
			"url":   linkURL.String(),
		})
		return true
	})
	return links
}
