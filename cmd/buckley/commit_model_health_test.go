package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/oneshot"
)

type commitHealthRoundTripFunc func(*http.Request) (*http.Response, error)

const configuredOpenRouterFallbackModel = "openai/gpt-5.6-luna-pro"

func (fn commitHealthRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

func TestSelectAvailableDefaultCommitModel_HealthyLocalRoutes(t *testing.T) {
	for _, providerID := range []string{"litellm", "openai_compatible"} {
		t.Run(providerID, func(t *testing.T) {
			t.Setenv("BUCKLEY_MODEL_COMMIT", "")
			const apiKey = "health-test-key"
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				if req.URL.Path != "/v1/models" {
					t.Errorf("request path = %q, want /v1/models", req.URL.Path)
				}
				if got := req.Header.Get("Authorization"); got != "Bearer "+apiKey {
					t.Errorf("Authorization = %q, want configured bearer token", got)
				}
				fmt.Fprint(w, `{"data":[{"id":"local-commit"}]}`)
			}))
			defer server.Close()

			cfg, selected := localCommitHealthConfig(providerID, server.URL+"/v1", apiKey)
			client := server.Client()
			client.Timeout = time.Second
			got, fellBack := selectAvailableDefaultCommitModelWithClient(
				commitCommandOptions{backend: oneshotBackendAPI}, cfg, providerID, selected, client,
			)
			if fellBack || got != selected {
				t.Fatalf("selection = (%q, %t), want healthy model %q", got, fellBack, selected)
			}
		})
	}
}

func TestSelectAvailableDefaultCommitModel_UnhealthyLocalRouteFallsBack(t *testing.T) {
	t.Setenv("BUCKLEY_MODEL_COMMIT", "")
	tests := []struct {
		name   string
		status int
		body   string
	}{
		{name: "non-2xx", status: http.StatusServiceUnavailable, body: `{"error":"unavailable"}`},
		{name: "malformed response", status: http.StatusOK, body: `{"data":`},
		{name: "missing model", status: http.StatusOK, body: `{"data":[{"id":"another-model"}]}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(test.status)
				fmt.Fprint(w, test.body)
			}))
			defer server.Close()

			cfg, selected := localCommitHealthConfig("litellm", server.URL+"/v1", "")
			client := server.Client()
			client.Timeout = time.Second
			got, fellBack := selectAvailableDefaultCommitModelWithClient(
				commitCommandOptions{backend: oneshotBackendAPI}, cfg, "litellm", selected, client,
			)
			if !fellBack || got != configuredOpenRouterFallbackModel {
				t.Fatalf("selection = (%q, %t), want fallback %q", got, fellBack, configuredOpenRouterFallbackModel)
			}
		})
	}
}

func TestSelectAvailableDefaultCommitModel_ConnectionFailureFallsBack(t *testing.T) {
	t.Setenv("BUCKLEY_MODEL_COMMIT", "")
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	baseURL := server.URL + "/v1"
	server.Close()

	cfg, selected := localCommitHealthConfig("litellm", baseURL, "")
	got, fellBack := selectAvailableDefaultCommitModelWithClient(
		commitCommandOptions{backend: oneshotBackendAPI}, cfg, "litellm", selected, &http.Client{Timeout: time.Second},
	)
	if !fellBack || got != configuredOpenRouterFallbackModel {
		t.Fatalf("selection = (%q, %t), want fallback %q", got, fellBack, configuredOpenRouterFallbackModel)
	}
}

func TestSelectAvailableDefaultCommitModel_TimeoutFallsBack(t *testing.T) {
	t.Setenv("BUCKLEY_MODEL_COMMIT", "")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(100 * time.Millisecond)
		fmt.Fprint(w, `{"data":[{"id":"local-commit"}]}`)
	}))
	defer server.Close()

	cfg, selected := localCommitHealthConfig("litellm", server.URL+"/v1", "")
	got, fellBack := selectAvailableDefaultCommitModelWithClient(
		commitCommandOptions{backend: oneshotBackendAPI}, cfg, "litellm", selected, &http.Client{Timeout: 10 * time.Millisecond},
	)
	if !fellBack || got != configuredOpenRouterFallbackModel {
		t.Fatalf("selection = (%q, %t), want fallback %q", got, fellBack, configuredOpenRouterFallbackModel)
	}
}

func TestSelectAvailableDefaultCommitModel_ExplicitSelectionsRemainExact(t *testing.T) {
	cfg, selected := localCommitHealthConfig("litellm", "http://127.0.0.1:1/v1", "")
	requestCount := 0
	client := &http.Client{Transport: commitHealthRoundTripFunc(func(*http.Request) (*http.Response, error) {
		requestCount++
		return nil, fmt.Errorf("unexpected health request")
	})}

	t.Run("flag", func(t *testing.T) {
		t.Setenv("BUCKLEY_MODEL_COMMIT", "")
		got, fellBack := selectAvailableDefaultCommitModelWithClient(
			commitCommandOptions{backend: oneshotBackendAPI, model: selected}, cfg, "litellm", selected, client,
		)
		if fellBack || got != selected {
			t.Fatalf("selection = (%q, %t), want explicit model %q", got, fellBack, selected)
		}
	})

	t.Run("environment", func(t *testing.T) {
		t.Setenv("BUCKLEY_MODEL_COMMIT", selected)
		got, fellBack := selectAvailableDefaultCommitModelWithClient(
			commitCommandOptions{backend: oneshotBackendAPI}, cfg, "litellm", selected, client,
		)
		if fellBack || got != selected {
			t.Fatalf("selection = (%q, %t), want explicit model %q", got, fellBack, selected)
		}
	})

	if requestCount != 0 {
		t.Fatalf("explicit selections made %d health requests, want 0", requestCount)
	}
}

func TestSelectAvailableDefaultCommitModel_IneligibleRoutesRemainUnchanged(t *testing.T) {
	t.Setenv("BUCKLEY_MODEL_COMMIT", "")
	client := &http.Client{Transport: commitHealthRoundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("ineligible route made a health request")
		return nil, nil
	})}
	tests := []struct {
		name       string
		backend    string
		providerID string
		baseURL    string
	}{
		{name: "non-api backend", backend: oneshot.CLIBackendCodex, providerID: "litellm", baseURL: "http://127.0.0.1:1/v1"},
		{name: "remote compatible endpoint", backend: oneshotBackendAPI, providerID: "litellm", baseURL: "https://models.example.com/v1"},
		{name: "different provider", backend: oneshotBackendAPI, providerID: "openrouter", baseURL: "http://127.0.0.1:1/v1"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg, selected := localCommitHealthConfig("litellm", test.baseURL, "")
			got, fellBack := selectAvailableDefaultCommitModelWithClient(
				commitCommandOptions{backend: test.backend}, cfg, test.providerID, selected, client,
			)
			if fellBack || got != selected {
				t.Fatalf("selection = (%q, %t), want unchanged %q", got, fellBack, selected)
			}
		})
	}
}

func TestConfiguredProviderIDForModel_PrefersLongestConfiguredRoute(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Providers.ModelRouting = map[string]string{
		"local/":         "litellm",
		"local/special/": "openai_compatible",
	}

	if got := configuredProviderIDForModel(cfg, "local/special/commit"); got != "openai_compatible" {
		t.Fatalf("provider = %q, want openai_compatible", got)
	}
	if got := configuredProviderIDForModel(cfg, "local/commit"); got != "litellm" {
		t.Fatalf("provider = %q, want litellm", got)
	}
}

func TestConfiguredOpenRouterCommitFallbackModel_PrefersExecutionDefault(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Models.DefaultProvider = "openrouter"
	cfg.Models.Execution = configuredOpenRouterFallbackModel

	if got := configuredOpenRouterCommitFallbackModel(cfg); got != configuredOpenRouterFallbackModel {
		t.Fatalf("fallback = %q, want configured execution model %q", got, configuredOpenRouterFallbackModel)
	}
}

func TestConfiguredOpenRouterCommitFallbackModel_UsesSafePackageDefault(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*config.Config)
	}{
		{name: "nil config"},
		{name: "different default provider", configure: func(cfg *config.Config) {
			cfg.Models.DefaultProvider = "litellm"
		}},
		{name: "empty execution model", configure: func(cfg *config.Config) {
			cfg.Models.Execution = ""
		}},
		{name: "local provider prefix", configure: func(cfg *config.Config) {
			cfg.Models.Execution = "litellm/local-execution"
		}},
		{name: "explicit direct-provider route", configure: func(cfg *config.Config) {
			cfg.Models.Execution = "direct/execution"
			cfg.Providers.ModelRouting = map[string]string{"direct/": "openai"}
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var cfg *config.Config
			if test.configure != nil || test.name != "nil config" {
				cfg = config.DefaultConfig()
				cfg.Models.DefaultProvider = "openrouter"
				cfg.Models.Execution = configuredOpenRouterFallbackModel
				if test.configure != nil {
					test.configure(cfg)
				}
			}
			if got := configuredOpenRouterCommitFallbackModel(cfg); got != config.DefaultCommitModel {
				t.Fatalf("fallback = %q, want package default %q", got, config.DefaultCommitModel)
			}
		})
	}
}

func localCommitHealthConfig(providerID, baseURL, apiKey string) (*config.Config, string) {
	cfg := config.DefaultConfig()
	selected := providerID + "/local-commit"
	cfg.Models.Utility.Commit = selected
	cfg.Models.DefaultProvider = "openrouter"
	cfg.Models.Execution = configuredOpenRouterFallbackModel
	provider := config.OpenAICompatibleConfig{
		Enabled: true,
		BaseURL: baseURL,
		APIKey:  apiKey,
	}
	if providerID == "openai_compatible" {
		cfg.Providers.OpenAICompatible = provider
	} else {
		cfg.Providers.LiteLLM = provider
	}
	return cfg, selected
}
