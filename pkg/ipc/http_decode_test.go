package ipc

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDecodeJSONBodySuccess(t *testing.T) {
	type payload struct {
		Name  string `json:"name"`
		Value int    `json:"value"`
	}

	body := strings.NewReader(`{"name":"test","value":42}`)
	req := httptest.NewRequest(http.MethodPost, "/", body)
	rr := httptest.NewRecorder()

	var dst payload
	status, err := decodeJSONBody(rr, req, &dst, maxBodyBytesSmall, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status != 0 {
		t.Fatalf("expected status 0, got %d", status)
	}
	if dst.Name != "test" || dst.Value != 42 {
		t.Fatalf("unexpected payload: %+v", dst)
	}
}

func TestDecodeJSONBodyNilRequestWithAllowEOF(t *testing.T) {
	var dst struct{}
	status, err := decodeJSONBody(nil, nil, &dst, maxBodyBytesTiny, true)
	if err != nil {
		t.Fatalf("expected nil error with allowEOF=true and nil request, got: %v", err)
	}
	if status != 0 {
		t.Fatalf("expected status 0, got %d", status)
	}
}

func TestDecodeJSONBodyNilRequestWithoutAllowEOF(t *testing.T) {
	rr := httptest.NewRecorder()
	var dst struct{}
	status, err := decodeJSONBody(rr, nil, &dst, maxBodyBytesTiny, false)
	if err == nil {
		t.Fatal("expected error with allowEOF=false and nil request")
	}
	if status != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d", http.StatusBadRequest, status)
	}
}

func TestDecodeJSONBodyEmptyWithAllowEOF(t *testing.T) {
	body := strings.NewReader("")
	req := httptest.NewRequest(http.MethodPost, "/", body)
	rr := httptest.NewRecorder()

	var dst struct{ Name string }
	status, err := decodeJSONBody(rr, req, &dst, maxBodyBytesTiny, true)
	if err != nil {
		t.Fatalf("expected nil error with allowEOF=true and empty body, got: %v", err)
	}
	if status != 0 {
		t.Fatalf("expected status 0, got %d", status)
	}
}

func TestDecodeJSONBodyEmptyWithoutAllowEOF(t *testing.T) {
	body := strings.NewReader("")
	req := httptest.NewRequest(http.MethodPost, "/", body)
	rr := httptest.NewRecorder()

	var dst struct{ Name string }
	status, err := decodeJSONBody(rr, req, &dst, maxBodyBytesTiny, false)
	if err == nil {
		t.Fatal("expected error with allowEOF=false and empty body")
	}
	if status != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d", http.StatusBadRequest, status)
	}
}

func TestDecodeJSONBodyTooLarge(t *testing.T) {
	// Create a body larger than the limit
	body := strings.NewReader(`{"data":"` + strings.Repeat("x", 100) + `"}`)
	req := httptest.NewRequest(http.MethodPost, "/", body)
	rr := httptest.NewRecorder()

	var dst struct{ Data string }
	status, err := decodeJSONBody(rr, req, &dst, 50, false) // 50 byte limit
	if err == nil {
		t.Fatal("expected error for body too large")
	}
	if status != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected status %d, got %d", http.StatusRequestEntityTooLarge, status)
	}
}

func TestDecodeJSONBodyInvalidJSON(t *testing.T) {
	body := strings.NewReader(`{invalid json}`)
	req := httptest.NewRequest(http.MethodPost, "/", body)
	rr := httptest.NewRecorder()

	var dst struct{ Name string }
	status, err := decodeJSONBody(rr, req, &dst, maxBodyBytesTiny, false)
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
	if status != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d", http.StatusBadRequest, status)
	}
}

func TestDecodeJSONBodyNoLimit(t *testing.T) {
	// With maxBytes=0, no limit should be applied
	body := strings.NewReader(`{"name":"test"}`)
	req := httptest.NewRequest(http.MethodPost, "/", body)
	rr := httptest.NewRecorder()

	var dst struct{ Name string }
	status, err := decodeJSONBody(rr, req, &dst, 0, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status != 0 {
		t.Fatalf("expected status 0, got %d", status)
	}
	if dst.Name != "test" {
		t.Fatalf("expected name=test, got %s", dst.Name)
	}
}
