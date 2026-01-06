package ipc

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAuthorizeRejectsQueryTokenOnRemoteBind(t *testing.T) {
	s := &Server{cfg: Config{
		BindAddress:  "0.0.0.0:4488",
		RequireToken: true,
		AuthToken:    "tok",
	}}

	req := httptest.NewRequest(http.MethodGet, "/?token=tok", nil)
	if _, ok := s.authorize(req); ok {
		t.Fatalf("expected query token rejected on remote bind")
	}

	headerReq := httptest.NewRequest(http.MethodGet, "/", nil)
	headerReq.Header.Set("Authorization", "Bearer tok")
	if _, ok := s.authorize(headerReq); !ok {
		t.Fatalf("expected bearer token in header to be accepted")
	}
}

func TestAuthorizeAllowsQueryTokenOnLoopbackBind(t *testing.T) {
	s := &Server{cfg: Config{
		BindAddress:  "127.0.0.1:4488",
		RequireToken: true,
		AuthToken:    "tok",
	}}

	req := httptest.NewRequest(http.MethodGet, "/?token=tok", nil)
	if _, ok := s.authorize(req); !ok {
		t.Fatalf("expected query token accepted on loopback bind")
	}
}
