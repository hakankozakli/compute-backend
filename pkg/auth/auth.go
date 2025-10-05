package auth

import (
    "errors"
    "net/http"
    "strings"
)

var (
    // ErrMissingKey indicates that the Authorization header was not provided.
    ErrMissingKey = errors.New("missing API key")
    // ErrInvalidPrefix indicates the header did not use the required Key prefix.
    ErrInvalidPrefix = errors.New("invalid authorization prefix")
)

// ExtractKey parses the fal.ai compatible Authorization header.
func ExtractKey(r *http.Request) (string, error) {
    header := r.Header.Get("Authorization")
    if header == "" {
        return "", ErrMissingKey
    }

    if !strings.HasPrefix(header, "Key ") {
        return "", ErrInvalidPrefix
    }

    token := strings.TrimPrefix(header, "Key ")
    if token == "" {
        return "", ErrMissingKey
    }

    return token, nil
}
