package rlm

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCatalogConfirmedToollessModel_UnknownMetadataKeepsToolsEligible(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer server.Close()
	mgr := newCoordinatorTestManager(t, server)
	if catalogConfirmedToollessModel(mgr, "openai/future-model") {
		t.Fatal("unknown model metadata must not be treated as catalog-confirmed toolless")
	}
}

func TestCatalogConfirmedToollessModel_OpenAIO3MiniKeepsToolsEligible(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer server.Close()
	mgr := newCoordinatorTestManager(t, server)
	if catalogConfirmedToollessModel(mgr, "openai/o3-mini") {
		t.Fatal("openai/o3-mini supports function/tool calling and must stay tool-eligible")
	}
}
