package main

import (
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const remoteSessionCookieName = "buckley_session"

type remoteAuthStore struct {
	path    string
	mu      sync.Mutex
	entries map[string]storedCookie
}

type storedCookie struct {
	Name    string    `json:"name"`
	Value   string    `json:"value"`
	Domain  string    `json:"domain"`
	Path    string    `json:"path"`
	Expires time.Time `json:"expires"`
	Secure  bool      `json:"secure"`
}

func newRemoteAuthStore() *remoteAuthStore {
	path, err := authStorePath()
	if err != nil {
		return nil
	}
	entries := make(map[string]storedCookie)
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &entries)
	}
	return &remoteAuthStore{path: path, entries: entries}
}

func (s *remoteAuthStore) CookiesFor(u *url.URL) []*http.Cookie {
	if s == nil || u == nil {
		return nil
	}
	key := hostKey(u)
	s.mu.Lock()
	defer s.mu.Unlock()
	cookie, ok := s.entries[key]
	if !ok {
		return nil
	}
	if time.Now().After(cookie.Expires) || strings.TrimSpace(cookie.Value) == "" {
		delete(s.entries, key)
		_ = s.persistLocked()
		return nil
	}
	return []*http.Cookie{{
		Name:    cookie.Name,
		Value:   cookie.Value,
		Domain:  cookie.Domain,
		Path:    cookie.Path,
		Secure:  cookie.Secure,
		Expires: cookie.Expires,
	}}
}

func (s *remoteAuthStore) Save(u *url.URL, cookies []*http.Cookie) error {
	if s == nil || u == nil || len(cookies) == 0 {
		return nil
	}
	key := hostKey(u)
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, ck := range cookies {
		if ck == nil || ck.Name != remoteSessionCookieName || strings.TrimSpace(ck.Value) == "" {
			continue
		}
		s.entries[key] = storedCookie{
			Name:    ck.Name,
			Value:   ck.Value,
			Domain:  ck.Domain,
			Path:    ck.Path,
			Expires: ck.Expires,
			Secure:  ck.Secure,
		}
		return s.persistLocked()
	}
	return nil
}

func (s *remoteAuthStore) persistLocked() error {
	if s == nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s.entries, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.path, data, 0o600)
}

func authStorePath() (string, error) {
	if path := strings.TrimSpace(os.Getenv(envBuckleyRemoteAuthPath)); path != "" {
		return expandHomePath(path)
	}

	if dir := strings.TrimSpace(os.Getenv(envBuckleyDataDir)); dir != "" {
		dir, err := expandHomePath(dir)
		if err != nil {
			return "", err
		}
		return filepath.Join(dir, "remote-auth.json"), nil
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".buckley", "remote-auth.json"), nil
}

func hostKey(u *url.URL) string {
	if u == nil {
		return ""
	}
	return strings.ToLower(u.Host)
}
