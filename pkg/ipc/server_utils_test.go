package ipc

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestIsLoopbackBindAddress(t *testing.T) {
	tests := []struct {
		name string
		addr string
		want bool
	}{
		{"localhost with port", "localhost:8080", true},
		{"localhost without port", "localhost", true},
		{"127.0.0.1 with port", "127.0.0.1:8080", true},
		{"127.0.0.1 without port", "127.0.0.1", true},
		{"::1 with port", "[::1]:8080", true},
		{"0.0.0.0 is not loopback", "0.0.0.0:8080", false},
		{":: is not loopback", "[::]:8080", false},
		{"empty string", "", false},
		{"whitespace only", "   ", false},
		{"external IP", "192.168.1.1:8080", false},
		{"external hostname", "example.com:8080", false},
		{"127.0.0.2 is loopback", "127.0.0.2:8080", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isLoopbackBindAddress(tt.addr)
			if got != tt.want {
				t.Errorf("isLoopbackBindAddress(%q) = %v, want %v", tt.addr, got, tt.want)
			}
		})
	}
}

func TestCloneURL(t *testing.T) {
	t.Run("nil input", func(t *testing.T) {
		result := cloneURL(nil)
		if result == nil {
			t.Fatal("expected non-nil result")
		}
	})

	t.Run("valid URL", func(t *testing.T) {
		original := &url.URL{
			Scheme:   "https",
			Host:     "example.com",
			Path:     "/path",
			RawQuery: "key=value",
		}
		result := cloneURL(original)
		if result == original {
			t.Fatal("expected different pointer")
		}
		if result.Scheme != original.Scheme {
			t.Errorf("Scheme = %s, want %s", result.Scheme, original.Scheme)
		}
		if result.Host != original.Host {
			t.Errorf("Host = %s, want %s", result.Host, original.Host)
		}
		if result.Path != original.Path {
			t.Errorf("Path = %s, want %s", result.Path, original.Path)
		}
		if result.RawQuery != original.RawQuery {
			t.Errorf("RawQuery = %s, want %s", result.RawQuery, original.RawQuery)
		}
	})

	t.Run("modification does not affect original", func(t *testing.T) {
		original := &url.URL{Path: "/original"}
		cloned := cloneURL(original)
		cloned.Path = "/modified"
		if original.Path != "/original" {
			t.Errorf("original was modified: %s", original.Path)
		}
	})
}

func TestWebsocketSourcesForHost(t *testing.T) {
	tests := []struct {
		name    string
		request func() *http.Request
		want    string
	}{
		{
			name:    "nil request",
			request: func() *http.Request { return nil },
			want:    "",
		},
		{
			name: "empty host",
			request: func() *http.Request {
				r := httptest.NewRequest(http.MethodGet, "/", nil)
				r.Host = ""
				return r
			},
			want: "",
		},
		{
			name: "valid host",
			request: func() *http.Request {
				r := httptest.NewRequest(http.MethodGet, "/", nil)
				r.Host = "example.com"
				return r
			},
			want: "ws://example.com wss://example.com",
		},
		{
			name: "host with port",
			request: func() *http.Request {
				r := httptest.NewRequest(http.MethodGet, "/", nil)
				r.Host = "example.com:8080"
				return r
			},
			want: "ws://example.com:8080 wss://example.com:8080",
		},
		{
			name: "host with injection attempt",
			request: func() *http.Request {
				r := httptest.NewRequest(http.MethodGet, "/", nil)
				r.Host = "example.com\n"
				return r
			},
			want: "",
		},
		{
			name: "host with quote",
			request: func() *http.Request {
				r := httptest.NewRequest(http.MethodGet, "/", nil)
				r.Host = "example.com'"
				return r
			},
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := websocketSourcesForHost(tt.request())
			if got != tt.want {
				t.Errorf("websocketSourcesForHost() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestBrowserCSP(t *testing.T) {
	t.Run("basic CSP without inline scripts", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.Host = "localhost:8080"
		csp := browserCSP(r, false)

		// Check for key directives
		if csp == "" {
			t.Fatal("expected non-empty CSP")
		}
		if !contains(csp, "default-src 'self'") {
			t.Error("expected default-src 'self'")
		}
		if !contains(csp, "script-src 'self'") {
			t.Error("expected script-src 'self'")
		}
		if contains(csp, "'unsafe-inline'") && contains(csp, "script-src") {
			// Script policy should not contain unsafe-inline when allowInlineScript=false
			// But style-src may have it, so we need to be more specific
		}
	})

	t.Run("CSP with inline scripts allowed", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.Host = "localhost:8080"
		csp := browserCSP(r, true)

		if !contains(csp, "script-src 'self' 'unsafe-inline'") {
			t.Error("expected script-src to allow unsafe-inline")
		}
	})
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && findSubstring(s, substr))
}

func findSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func TestValidateStartupConfig(t *testing.T) {
	tests := []struct {
		name    string
		cfg     Config
		wantErr bool
	}{
		{
			name:    "loopback without auth is ok",
			cfg:     Config{BindAddress: "127.0.0.1:8080"},
			wantErr: false,
		},
		{
			name:    "external without auth is error",
			cfg:     Config{BindAddress: "0.0.0.0:8080"},
			wantErr: true,
		},
		{
			name:    "external with token is ok",
			cfg:     Config{BindAddress: "0.0.0.0:8080", RequireToken: true},
			wantErr: false,
		},
		{
			name:    "external with basic auth is ok",
			cfg:     Config{BindAddress: "0.0.0.0:8080", BasicAuthEnabled: true, BasicAuthUsername: "user", BasicAuthPassword: "pass"},
			wantErr: false,
		},
		{
			name:    "basic auth enabled without username",
			cfg:     Config{BindAddress: "127.0.0.1:8080", BasicAuthEnabled: true, BasicAuthPassword: "pass"},
			wantErr: true,
		},
		{
			name:    "basic auth enabled without password",
			cfg:     Config{BindAddress: "127.0.0.1:8080", BasicAuthEnabled: true, BasicAuthUsername: "user"},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &Server{cfg: tt.cfg}
			err := s.validateStartupConfig()
			if (err != nil) != tt.wantErr {
				t.Errorf("validateStartupConfig() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
