package main

import "testing"

func TestValidateServerURL(t *testing.T) {
	tests := []struct {
		name          string
		url           string
		allowInsecure bool
		wantErr       bool
	}{
		{"https remote", "https://synq.example.com", false, false},
		{"http localhost", "http://localhost:8080", false, false},
		{"http loopback v4", "http://127.0.0.1:8080", false, false},
		{"http loopback v6", "http://[::1]:8080", false, false},
		{"http remote refused", "http://synq.example.com", false, true},
		{"http LAN refused", "http://192.168.1.20:8080", false, true},
		{"http LAN allowed by override", "http://192.168.1.20:8080", true, false},
		{"unsupported scheme", "ftp://synq.example.com", false, true},
		{"no scheme", "synq.example.com:8080", false, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateServerURL(tc.url, tc.allowInsecure)
			if (err != nil) != tc.wantErr {
				t.Fatalf("validateServerURL(%q, %v) error = %v, wantErr %v", tc.url, tc.allowInsecure, err, tc.wantErr)
			}
		})
	}
}
