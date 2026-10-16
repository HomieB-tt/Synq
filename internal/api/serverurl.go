package api

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ValidateServerURL accepts https:// URLs, and http:// only for
// loopback hosts (localhost, 127.0.0.0/8, ::1) used in local
// development. allowInsecure is an explicit override for a trusted
// network such as a home LAN (SYNQ_ALLOW_INSECURE_HTTP=1).
//
// A cleartext URL to any other host is refused because access tokens,
// refresh tokens and all traffic metadata would cross the network
// unencrypted.
func ValidateServerURL(raw string, allowInsecure bool) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("server URL is invalid: %w", err)
	}
	if u.Host == "" {
		return fmt.Errorf("server URL must look like https://host (got %q)", raw)
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		if allowInsecure || isLoopbackHost(u.Hostname()) {
			return nil
		}
		return fmt.Errorf("server URL uses cleartext http:// for %q, which would send your tokens unencrypted; use https://, or set SYNQ_ALLOW_INSECURE_HTTP=1 if you trust this network", u.Hostname())
	default:
		return fmt.Errorf("server URL must start with https:// (or http://localhost for development), got scheme %q", u.Scheme)
	}
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Ping times one GET of the server's /health endpoint. It measures the
// full round trip as the app experiences it (DNS, TCP and TLS on a cold
// connection, then the request itself), which is what matters when
// diagnosing a sluggish connection.
func (c *Client) Ping(ctx context.Context) (time.Duration, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/health", nil)
	if err != nil {
		return 0, err
	}
	start := time.Now()
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	elapsed := time.Since(start)
	if resp.StatusCode/100 != 2 {
		return elapsed, fmt.Errorf("server answered %d", resp.StatusCode)
	}
	return elapsed, nil
}
