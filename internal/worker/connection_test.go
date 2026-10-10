package worker

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestPairRequiresPinnedControllerFingerprint(t *testing.T) {
	server, err := New(Config{Workspace: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()

	_, err = server.Pair(context.Background(), "https://localhost:8764", "", t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "certificate fingerprint is required") {
		t.Fatalf("expected fingerprint error, got %v", err)
	}
}

func TestRetryHTTPRetriesTransientNetworkErrors(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	attempts := 0
	resp, err := retryHTTP(ctx, func() (*http.Response, error) {
		attempts++
		if attempts < 3 {
			return nil, errors.New("temporary network failure")
		}
		return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Body: io.NopCloser(strings.NewReader("ok"))}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if attempts != 3 {
		t.Fatalf("attempts = %d, want 3", attempts)
	}
}

func TestPairingHTTPErrorIncludesStatusWhenBodyHasNoMessage(t *testing.T) {
	resp := &http.Response{
		StatusCode: http.StatusNotFound,
		Status:     "404 Not Found",
		Body:       io.NopCloser(strings.NewReader(`{"unexpected":"value"}`)),
	}
	err := pairingHTTPError("pairing status request failed", resp)
	if !strings.Contains(err.Error(), "404 Not Found") || !strings.Contains(err.Error(), "Not Found") {
		t.Fatalf("error = %v", err)
	}
}
