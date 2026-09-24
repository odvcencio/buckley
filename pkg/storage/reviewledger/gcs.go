package reviewledger

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os/exec"
	"strings"
	"sync"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

type GCS struct {
	client           *http.Client
	endpoint, bucket string
}

func NewGCS(bucket string) *GCS {
	return &GCS{client: &http.Client{Transport: &gcsAuthTransport{}, Timeout: time.Minute}, endpoint: "https://storage.googleapis.com", bucket: bucket}
}

type gcsAuthTransport struct {
	mu     sync.Mutex
	source oauth2.TokenSource
	token  *oauth2.Token
}

func (t *gcsAuthTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	token, err := t.accessToken(req.Context())
	if err != nil {
		return nil, err
	}
	cloned := req.Clone(req.Context())
	cloned.Header.Set("Authorization", "Bearer "+token)
	return http.DefaultTransport.RoundTrip(cloned)
}

func (t *gcsAuthTransport) accessToken(ctx context.Context) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.token.Valid() {
		return t.token.AccessToken, nil
	}
	if t.source == nil {
		// The source outlives one review request; individual uploads remain bounded.
		client := &http.Client{Timeout: 20 * time.Second}
		authCtx := context.WithValue(context.Background(), oauth2.HTTPClient, client)
		t.source, _ = google.DefaultTokenSource(authCtx, "https://www.googleapis.com/auth/devstorage.read_write")
	}
	if t.source != nil {
		token, err := t.source.Token()
		if err == nil {
			t.token = token
			return token.AccessToken, nil
		}
	}
	tokenCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	// Use the operator's existing gcloud login when ADC is unavailable.
	// Never attach the command's output to an error or diagnostic.
	output, err := exec.CommandContext(tokenCtx, "gcloud", "auth", "print-access-token", "--quiet").Output()
	if err != nil {
		return "", fmt.Errorf("GCS authentication requires application default credentials or a gcloud login")
	}
	value := strings.TrimSpace(string(output))
	if value == "" {
		return "", fmt.Errorf("GCS authentication returned an empty token")
	}
	t.token = &oauth2.Token{AccessToken: value, Expiry: time.Now().Add(5 * time.Minute)}
	return value, nil
}

func (g *GCS) Create(ctx context.Context, key string, body []byte, mediaType string) error {
	query := url.Values{"uploadType": {"media"}, "name": {key}, "ifGenerationMatch": {"0"}}
	endpoint := g.endpoint + "/upload/storage/v1/b/" + url.PathEscape(g.bucket) + "/o?" + query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", mediaType)
	resp, err := g.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusPreconditionFailed {
		return ErrExists
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("GCS create %s: HTTP %d", key, resp.StatusCode)
	}
	_, err = io.Copy(io.Discard, resp.Body)
	return err
}

func (g *GCS) Read(ctx context.Context, key string) ([]byte, error) {
	endpoint := g.endpoint + "/storage/v1/b/" + url.PathEscape(g.bucket) + "/o/" + url.PathEscape(key) + "?alt=media"
	return g.get(ctx, endpoint)
}

func (g *GCS) get(ctx context.Context, endpoint string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	resp, err := g.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, ErrNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GCS read: HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

func (g *GCS) List(ctx context.Context, prefix string) ([]string, error) {
	var keys []string
	token := ""
	for {
		query := url.Values{"prefix": {prefix}, "fields": {"items(name),nextPageToken"}, "pageToken": {token}}
		body, err := g.get(ctx, g.endpoint+"/storage/v1/b/"+url.PathEscape(g.bucket)+"/o?"+query.Encode())
		if err != nil {
			return nil, err
		}
		var page struct {
			Items []struct {
				Name string `json:"name"`
			} `json:"items"`
			Next string `json:"nextPageToken"`
		}
		if err := json.Unmarshal(body, &page); err != nil {
			return nil, err
		}
		for _, item := range page.Items {
			keys = append(keys, item.Name)
		}
		if page.Next == "" {
			return keys, nil
		}
		if page.Next == token {
			return nil, fmt.Errorf("GCS repeated a page token")
		}
		token = page.Next
	}
}
