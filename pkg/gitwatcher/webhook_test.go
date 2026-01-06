package gitwatcher

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestNewHandler(t *testing.T) {
	callback := func(event MergeEvent) {}
	handler := NewHandler("secret123", callback)

	if handler == nil {
		t.Fatal("NewHandler returned nil")
	}
	if handler.secret != "secret123" {
		t.Errorf("expected secret 'secret123', got %q", handler.secret)
	}
	if handler.callback == nil {
		t.Error("callback should not be nil")
	}
}

func TestNewHandler_TrimWhitespace(t *testing.T) {
	handler := NewHandler("  secret  ", nil)
	if handler.secret != "secret" {
		t.Errorf("expected trimmed secret 'secret', got %q", handler.secret)
	}
}

func TestHandler_MethodNotAllowed(t *testing.T) {
	handler := NewHandler("", nil)
	req := httptest.NewRequest(http.MethodGet, "/webhook", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected status %d, got %d", http.StatusMethodNotAllowed, w.Code)
	}
}

func TestHandler_BadRequest(t *testing.T) {
	handler := NewHandler("", nil)
	req := httptest.NewRequest(http.MethodPost, "/webhook", nil)
	req.Body = &brokenReader{}
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected status %d, got %d", http.StatusBadRequest, w.Code)
	}
}

func TestHandler_Unauthorized(t *testing.T) {
	handler := NewHandler("secret123", nil)
	payload := []byte(`{"action":"closed"}`)
	req := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewReader(payload))
	req.Header.Set("X-Hub-Signature-256", "sha256=wrongsignature")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected status %d, got %d", http.StatusUnauthorized, w.Code)
	}
}

func TestHandler_UnprocessableEntity(t *testing.T) {
	handler := NewHandler("", nil)
	payload := []byte(`not valid json`)
	req := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewReader(payload))
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnprocessableEntity {
		t.Errorf("expected status %d, got %d", http.StatusUnprocessableEntity, w.Code)
	}
}

func TestHandler_Success(t *testing.T) {
	handler := NewHandler("", nil)
	payload := []byte(`{"action":"opened"}`)
	req := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewReader(payload))
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusAccepted {
		t.Errorf("expected status %d, got %d", http.StatusAccepted, w.Code)
	}
	if w.Body.String() != "ok" {
		t.Errorf("expected body 'ok', got %q", w.Body.String())
	}
}

func TestHandler_MergeCallback(t *testing.T) {
	eventChan := make(chan MergeEvent, 1)
	callback := func(event MergeEvent) {
		eventChan <- event
	}

	handler := NewHandler("", callback)

	event := map[string]any{
		"action": "closed",
		"merged": true,
		"repository": map[string]any{
			"full_name": "owner/repo",
		},
		"pull_request": map[string]any{
			"base": map[string]any{
				"ref": "main",
			},
			"merge_commit_sha": "abc123",
		},
	}

	payload, _ := json.Marshal(event)
	req := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewReader(payload))
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	// Wait for goroutine to process
	select {
	case e := <-eventChan:
		if e.Repository != "owner/repo" {
			t.Errorf("expected repository 'owner/repo', got %q", e.Repository)
		}
		if e.Branch != "main" {
			t.Errorf("expected branch 'main', got %q", e.Branch)
		}
		if e.SHA != "abc123" {
			t.Errorf("expected SHA 'abc123', got %q", e.SHA)
		}
	case <-time.After(100 * time.Millisecond):
		t.Error("callback was not invoked")
	}
}

func TestHandler_NotMerged(t *testing.T) {
	eventChan := make(chan MergeEvent, 1)
	callback := func(event MergeEvent) {
		eventChan <- event
	}

	handler := NewHandler("", callback)

	event := map[string]any{
		"action": "closed",
		"merged": false,
	}

	payload, _ := json.Marshal(event)
	req := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewReader(payload))
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	select {
	case <-eventChan:
		t.Error("callback should not be invoked for non-merged PR")
	default:
		// Success
	}
}

func TestValidateSignature_Valid(t *testing.T) {
	payload := []byte("test payload")
	secret := "mysecret"

	// Generate valid signature
	signature := generateSignature(payload, secret)

	if !validateSignature(signature, payload, secret) {
		t.Error("valid signature was rejected")
	}
}

func TestValidateSignature_Invalid(t *testing.T) {
	payload := []byte("test payload")
	secret := "mysecret"

	if validateSignature("sha256=wrongsig", payload, secret) {
		t.Error("invalid signature was accepted")
	}
}

func TestValidateSignature_EmptySignature(t *testing.T) {
	if validateSignature("", []byte("test"), "secret") {
		t.Error("empty signature should be rejected")
	}
}

func TestValidateSignature_NoPrefix(t *testing.T) {
	if validateSignature("noshaprefix", []byte("test"), "secret") {
		t.Error("signature without sha256= prefix should be rejected")
	}
}

func TestValidateSignature_InvalidHex(t *testing.T) {
	if validateSignature("sha256=notvalidhex!", []byte("test"), "secret") {
		t.Error("signature with invalid hex should be rejected")
	}
}

func TestReadString(t *testing.T) {
	payload := map[string]any{
		"key": "value",
	}

	result := readString(payload, "key")
	if result != "value" {
		t.Errorf("expected 'value', got %q", result)
	}

	result = readString(payload, "missing")
	if result != "" {
		t.Errorf("expected empty string for missing key, got %q", result)
	}

	payload["notstring"] = 123
	result = readString(payload, "notstring")
	if result != "" {
		t.Errorf("expected empty string for non-string value, got %q", result)
	}
}

func TestReadBool(t *testing.T) {
	payload := map[string]any{
		"key": true,
	}

	result := readBool(payload, "key")
	if !result {
		t.Error("expected true, got false")
	}

	result = readBool(payload, "missing")
	if result {
		t.Error("expected false for missing key, got true")
	}

	payload["notbool"] = "string"
	result = readBool(payload, "notbool")
	if result {
		t.Error("expected false for non-bool value, got true")
	}
}

func TestReadNestedString(t *testing.T) {
	payload := map[string]any{
		"level1": map[string]any{
			"level2": map[string]any{
				"value": "nested",
			},
		},
	}

	result := readNestedString(payload, "level1", "level2", "value")
	if result != "nested" {
		t.Errorf("expected 'nested', got %q", result)
	}

	result = readNestedString(payload, "level1", "missing", "value")
	if result != "" {
		t.Errorf("expected empty string for missing nested key, got %q", result)
	}

	result = readNestedString(payload, "level1", "level2")
	if result != "" {
		t.Errorf("expected empty string for non-string nested value, got %q", result)
	}
}

func TestReadNestedString_NonMapValue(t *testing.T) {
	payload := map[string]any{
		"key": "notamap",
	}

	result := readNestedString(payload, "key", "nested")
	if result != "" {
		t.Errorf("expected empty string when intermediate value is not a map, got %q", result)
	}
}

// Helper types and functions

type brokenReader struct{}

func (b *brokenReader) Read(p []byte) (n int, err error) {
	return 0, http.ErrBodyReadAfterClose
}

func (b *brokenReader) Close() error {
	return nil
}

func generateSignature(payload []byte, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)
	expectedMAC := mac.Sum(nil)
	return "sha256=" + hex.EncodeToString(expectedMAC)
}
