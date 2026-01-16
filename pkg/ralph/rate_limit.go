// pkg/ralph/rate_limit.go
package ralph

import (
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// RateLimitInfo captures retry guidance extracted from backend responses.
type RateLimitInfo struct {
	RetryAfter   time.Duration
	WindowResets time.Time
	ErrorPattern string
}

const defaultRateLimitBackoff = time.Minute

var (
	reRetryAfter = regexp.MustCompile(`(?i)retry[- ]after[^\d]*(\d+)\s*(seconds?|secs?|s|minutes?|mins?|m|hours?|hrs?|h)`)
	reTryAgain   = regexp.MustCompile(`(?i)try again in[^\d]*(\d+)\s*(seconds?|secs?|s|minutes?|mins?|m|hours?|hrs?|h)`)
	reResetsAt   = regexp.MustCompile(`(?i)resets? at\s*([^\s]+)`)
)

// ParseRateLimitResponse extracts retry timing hints from response content or headers.
func ParseRateLimitResponse(resp string, headers map[string]string) *RateLimitInfo {
	info := &RateLimitInfo{}
	found := false

	if headers != nil {
		for key, value := range headers {
			if strings.EqualFold(key, "retry-after") {
				found = true
				if retryAfter, reset := parseRetryAfter(value); retryAfter > 0 {
					info.RetryAfter = retryAfter
				} else if !reset.IsZero() {
					info.WindowResets = reset
				}
				info.ErrorPattern = "retry-after header"
				break
			}
		}
	}

	lower := strings.ToLower(resp)
	if strings.Contains(lower, "rate limit") || strings.Contains(lower, "quota exceeded") || strings.Contains(lower, "too many requests") {
		found = true
		if info.ErrorPattern == "" {
			info.ErrorPattern = "rate limit"
		}
	}

	if match := reRetryAfter.FindStringSubmatch(resp); len(match) == 3 {
		found = true
		if dur := parseDuration(match[1], match[2]); dur > 0 {
			info.RetryAfter = dur
		}
		if info.ErrorPattern == "" {
			info.ErrorPattern = "retry-after"
		}
	}

	if match := reTryAgain.FindStringSubmatch(resp); len(match) == 3 {
		found = true
		if dur := parseDuration(match[1], match[2]); dur > 0 {
			info.RetryAfter = dur
		}
		if info.ErrorPattern == "" {
			info.ErrorPattern = "try again in"
		}
	}

	if match := reResetsAt.FindStringSubmatch(resp); len(match) == 2 {
		found = true
		if ts := parseTimestamp(match[1]); !ts.IsZero() {
			info.WindowResets = ts
		}
		if info.ErrorPattern == "" {
			info.ErrorPattern = "resets at"
		}
	}

	if !found {
		return nil
	}

	if info.RetryAfter == 0 && info.WindowResets.IsZero() {
		info.RetryAfter = defaultRateLimitBackoff
	}

	return info
}

func parseRetryAfter(value string) (time.Duration, time.Time) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, time.Time{}
	}
	if seconds, err := strconv.Atoi(value); err == nil && seconds > 0 {
		return time.Duration(seconds) * time.Second, time.Time{}
	}
	if ts, err := http.ParseTime(value); err == nil {
		return 0, ts
	}
	return 0, time.Time{}
}

func parseDuration(numStr, unit string) time.Duration {
	value, err := strconv.Atoi(numStr)
	if err != nil || value <= 0 {
		return 0
	}
	switch strings.ToLower(unit) {
	case "second", "seconds", "sec", "secs", "s":
		return time.Duration(value) * time.Second
	case "minute", "minutes", "min", "mins", "m":
		return time.Duration(value) * time.Minute
	case "hour", "hours", "hr", "hrs", "h":
		return time.Duration(value) * time.Hour
	default:
		return 0
	}
}

func parseTimestamp(value string) time.Time {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}
	}
	if seconds, err := strconv.ParseInt(value, 10, 64); err == nil {
		return time.Unix(seconds, 0)
	}

	formats := []string{
		time.RFC3339,
		time.RFC3339Nano,
		time.RFC1123,
		time.RFC1123Z,
		time.RFC822,
		time.RFC822Z,
		"2006-01-02 15:04:05 MST",
		"2006-01-02 15:04:05",
	}

	for _, format := range formats {
		if ts, err := time.Parse(format, value); err == nil {
			return ts
		}
	}

	return time.Time{}
}
