package execmode

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBrokerSearchText_SymlinkConfinement(t *testing.T) {
	for _, tc := range []struct {
		name     string
		external bool
		absolute bool
		large    bool
		want     int
	}{
		{name: "relative_inside", want: 1},
		{name: "absolute_inside", absolute: true, want: 1},
		{name: "relative_outside", external: true},
		{name: "absolute_outside", external: true, absolute: true},
		{name: "oversized_inside", large: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			parent := t.TempDir()
			workspace := filepath.Join(parent, "workspace")
			if err := os.Mkdir(workspace, 0700); err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(workspace, "target.data")
			if tc.external {
				target = filepath.Join(parent, "external.data")
			}
			content := "needle\n"
			if tc.large {
				content += strings.Repeat("x", 2<<20)
			}
			if err := os.WriteFile(target, []byte(content), 0600); err != nil {
				t.Fatal(err)
			}
			linkTarget := target
			if !tc.absolute {
				var err error
				linkTarget, err = filepath.Rel(workspace, target)
				if err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Symlink(linkTarget, filepath.Join(workspace, "alias.txt")); err != nil {
				t.Skipf("symlink unavailable: %v", err)
			}
			sink := &recordingSink{}
			broker, err := NewBroker(workspace, sink)
			if err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest(http.MethodPost, "/v1/search/text", strings.NewReader(`{"pattern":"needle","glob":"*.txt"}`))
			req.Header.Set("Authorization", "Bearer "+broker.Token())
			response := httptest.NewRecorder()
			broker.handleContext(CapSearchText, broker.searchText)(response, req)
			var result struct {
				Matches []struct {
					File string
					Line int
					Text string
				}
				Capped bool
			}
			if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if response.Code != http.StatusOK || result.Capped || len(result.Matches) != tc.want {
				t.Fatalf("status=%d capped=%v matches=%d want=%d", response.Code, result.Capped, len(result.Matches), tc.want)
			}
			if tc.want == 1 && (result.Matches[0].File != "alias.txt" || result.Matches[0].Line != 1 || result.Matches[0].Text != "needle") {
				t.Fatalf("alias match changed: %+v", result.Matches)
			}
			if records := sink.byMethod(CapSearchText); len(records) != 1 || records[0].Outcome != "ok" {
				t.Fatalf("audit=%+v", records)
			}
		})
	}
}

func TestBroker_RelativeWorkspace(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "target.data"), []byte("needle\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(workspace, "target.data"), filepath.Join(workspace, "alias.txt")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	t.Chdir(workspace)
	broker, err := NewBroker(".", &recordingSink{})
	if err != nil {
		t.Fatal(err)
	}
	if count := globSearchCount(t, broker, "needle", "*.txt"); count != 1 {
		t.Fatalf("search matches=%d", count)
	}
	result, err := broker.filesRead(map[string]any{"path": "alias.txt"})
	if err != nil || result.(map[string]any)["content"] != "needle\n" {
		t.Fatalf("read=%v err=%v", result, err)
	}
}
