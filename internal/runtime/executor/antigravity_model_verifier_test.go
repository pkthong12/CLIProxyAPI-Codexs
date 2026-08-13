package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestVerifyAntigravityModelUsesNativeRequestShape(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(pWriter http.ResponseWriter, pRequest *http.Request) {
		if pRequest.URL.Path != "/v1internal:generateContent" {
			t.Fatalf("path = %q", pRequest.URL.Path)
		}
		if pRequest.Header.Get("Authorization") != "Bearer access-token" {
			t.Fatalf("authorization header = %q", pRequest.Header.Get("Authorization"))
		}
		body, errRead := io.ReadAll(pRequest.Body)
		if errRead != nil {
			t.Fatalf("read request: %v", errRead)
		}
		if !strings.Contains(string(body), `"model":"gemini-discovered"`) {
			t.Fatalf("model was not mapped into native payload: %s", body)
		}
		if !strings.Contains(string(body), `"project":"project-id"`) {
			t.Fatalf("project was not mapped into native payload: %s", body)
		}
		pWriter.WriteHeader(http.StatusOK)
		_, _ = pWriter.Write([]byte(`{"candidates":[]}`))
	}))
	defer server.Close()

	auth := &cliproxyauth.Auth{
		Attributes: map[string]string{"base_url": server.URL},
		Metadata: map[string]any{
			"access_token": "access-token",
			"project_id":   "project-id",
		},
	}
	if errVerify := VerifyAntigravityModel(context.Background(), nil, auth, "gemini-discovered"); errVerify != nil {
		t.Fatalf("VerifyAntigravityModel() error = %v", errVerify)
	}
}

func TestVerifyAntigravityModelExposesUpstreamStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(pWriter http.ResponseWriter, _ *http.Request) {
		pWriter.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	auth := &cliproxyauth.Auth{
		Attributes: map[string]string{"base_url": server.URL},
		Metadata: map[string]any{
			"access_token": "access-token",
			"project_id":   "project-id",
		},
	}
	errVerify := VerifyAntigravityModel(context.Background(), nil, auth, "gemini-missing")
	statusError, hasStatus := errVerify.(interface{ StatusCode() int })
	if !hasStatus || statusError.StatusCode() != http.StatusNotFound {
		t.Fatalf("verification error = %v, want 404 status error", errVerify)
	}
}
