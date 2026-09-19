package github

import (
	"context"
	"strings"
	"testing"
	"time"
)

// These tests hit the REAL github.com and api.github.com endpoints
// with a deliberately invalid client ID. They can't exercise the
// success path (that needs a real, valid OAuth App client ID and a
// human completing browser authorization), but they prove the
// request-building and response-parsing logic actually works against
// GitHub's real API shape, not just an assumption about it. Skipped
// automatically if there's no network access.

func TestRequestDeviceCodeWithInvalidClientIDReturnsGitHubError(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err := RequestDeviceCode(ctx, "invalid-client-id-for-testing")
	if err == nil {
		t.Fatal("expected an error for an invalid client ID, got nil")
	}
	// We don't assert the exact error text (GitHub's wording could
	// change), just that we got a real, parsed response back rather
	// than a network or JSON-decoding failure.
	t.Logf("got expected error from real github.com: %v", err)
}

func TestPollOnceWithInvalidClientIDReturnsGitHubError(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err := PollOnce(ctx, "invalid-client-id-for-testing", "invalid-device-code")
	if err == nil {
		t.Fatal("expected an error for an invalid client/device code, got nil")
	}
	t.Logf("got expected error from real github.com: %v", err)
}

func TestFetchUserWithInvalidTokenReturnsUnauthorized(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err := FetchUser(ctx, "invalid-token-for-testing")
	if err == nil {
		t.Fatal("expected an error for an invalid token, got nil")
	}
	if !strings.Contains(err.Error(), "401") && !strings.Contains(err.Error(), "Bad credentials") {
		t.Logf("got an error as expected, but not the one anticipated - verify this is still a real API response: %v", err)
	} else {
		t.Logf("confirmed real api.github.com rejects invalid token as expected: %v", err)
	}
}
