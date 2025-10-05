package auth

import (
    "net/http"
    "testing"
)

func TestExtractKey(t *testing.T) {
    req, _ := http.NewRequest(http.MethodGet, "http://example.com", nil)
    req.Header.Set("Authorization", "Key test-token")

    token, err := ExtractKey(req)
    if err != nil {
        t.Fatalf("expected no error, got %v", err)
    }
    if token != "test-token" {
        t.Fatalf("unexpected token: %s", token)
    }
}

func TestExtractKeyErrors(t *testing.T) {
    req, _ := http.NewRequest(http.MethodGet, "http://example.com", nil)

    if _, err := ExtractKey(req); err != ErrMissingKey {
        t.Fatalf("expected ErrMissingKey, got %v", err)
    }

    req.Header.Set("Authorization", "Bearer abc")
    if _, err := ExtractKey(req); err != ErrInvalidPrefix {
        t.Fatalf("expected ErrInvalidPrefix, got %v", err)
    }

    req.Header.Set("Authorization", "Key ")
    if _, err := ExtractKey(req); err != ErrMissingKey {
        t.Fatalf("expected ErrMissingKey for empty token, got %v", err)
    }
}
