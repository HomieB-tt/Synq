package ws

import "testing"

func TestDialTargetMovesTokenToHeader(t *testing.T) {
	target, header := dialTarget("wss://synq.example/ws?token=abc.def.ghi")
	if target != "wss://synq.example/ws" {
		t.Errorf("target = %q, want the URL with no token", target)
	}
	if got := header.Get("Authorization"); got != "Bearer abc.def.ghi" {
		t.Errorf("Authorization = %q, want %q", got, "Bearer abc.def.ghi")
	}
}

func TestDialTargetKeepsOtherQueryParams(t *testing.T) {
	target, header := dialTarget("ws://h/ws?user=u1&token=t")
	if target != "ws://h/ws?user=u1" {
		t.Errorf("target = %q, want other query params preserved", target)
	}
	if header.Get("Authorization") != "Bearer t" {
		t.Errorf("Authorization = %q", header.Get("Authorization"))
	}
}

func TestDialTargetWithoutTokenIsUnchanged(t *testing.T) {
	target, header := dialTarget("ws://h/ws?user=u1")
	if target != "ws://h/ws?user=u1" || header != nil {
		t.Errorf("got (%q, %v), want the URL unchanged and no header", target, header)
	}
}
