package reviewledger

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// With no credentials, every upload used to run the credential lookup again
// (including a gcloud subprocess) and print its own error. The failure must be
// a typed error and the lookup must run once per process.
func TestGCS_MissingCredentialsFailOnceAndTyped(t *testing.T) {
	dir := t.TempDir()
	count := filepath.Join(dir, "count")
	script := "#!/bin/sh\necho x >> " + count + "\nexit 1\n"
	if err := os.WriteFile(filepath.Join(dir, "gcloud"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":/usr/bin:/bin")
	t.Setenv("HOME", dir)
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", filepath.Join(dir, "missing.json"))
	t.Setenv("GCE_METADATA_HOST", "127.0.0.1:1")
	t.Setenv("CLOUDSDK_CONFIG", dir)

	g := NewGCS("bucket")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var first error
	for i := 0; i < 5; i++ {
		err := g.Create(ctx, "k", []byte("v"), "text/plain")
		if !errors.Is(err, ErrNoCredentials) {
			t.Fatalf("Create #%d error = %v, want ErrNoCredentials", i, err)
		}
		if i == 0 {
			first = err
		}
	}
	if first == nil || !strings.Contains(first.Error(), "credentials") {
		t.Fatalf("error text = %v", first)
	}
	data, _ := os.ReadFile(count)
	if n := strings.Count(string(data), "x"); n != 1 {
		t.Fatalf("gcloud ran %d times, want 1", n)
	}
}
