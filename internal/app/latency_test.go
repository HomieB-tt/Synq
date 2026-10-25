package app

import (
	"errors"
	"testing"
	"time"

	"github.com/HomieB-tt/synq/internal/api"
)

func latencyModel() Model {
	return Model{apiClient: api.NewClient("https://example.invalid"), accessToken: "tok", connected: true, latencyGen: 3}
}

func TestLatencyResultShowsInTheLabelOnlyWhenConnected(t *testing.T) {
	m := latencyModel()
	next, _ := m.handleLatencyResult(latencyResultMsg{gen: 3, d: 158 * time.Millisecond})
	got := next.(Model)
	if got.latencyLabel() != "158 ms" {
		t.Errorf("label = %q", got.latencyLabel())
	}
	got.connected = false
	if got.latencyLabel() != "" {
		t.Errorf("label while offline = %q", got.latencyLabel())
	}
	got.connected, got.latency = true, 20*time.Second
	if got.latencyLabel() != ">10 s" {
		t.Errorf("slow label = %q", got.latencyLabel())
	}
}

func TestLatencyIgnoresRetiredLoopsAndClearsOnFailure(t *testing.T) {
	m := latencyModel()
	m.latency = 50 * time.Millisecond
	next, _ := m.handleLatencyResult(latencyResultMsg{gen: 2, d: time.Second})
	if got := next.(Model); got.latency != 50*time.Millisecond {
		t.Errorf("stale loop changed latency: %v", got.latency)
	}
	next, _ = m.handleLatencyResult(latencyResultMsg{gen: 3, err: errors.New("boom")})
	if got := next.(Model); got.latency != 0 {
		t.Errorf("failure kept a stale reading: %v", got.latency)
	}
	if _, cmd := m.handleLatencyTick(latencyTickMsg{gen: 2}); cmd != nil {
		t.Error("a retired loop kept polling")
	}
	if _, cmd := m.handleLatencyTick(latencyTickMsg{gen: 3}); cmd == nil {
		t.Error("the live loop stopped polling")
	}
	m.accessToken = ""
	if _, cmd := m.handleLatencyTick(latencyTickMsg{gen: 3}); cmd != nil {
		t.Error("polling continued after logout")
	}
}

func TestRestartLatencyRetiresTheOldLoop(t *testing.T) {
	m := latencyModel()
	old := m.latencyGen
	if cmd := m.restartLatencyCmd(); cmd == nil || m.latencyGen == old {
		t.Errorf("restart: cmd=%v gen %d->%d", cmd != nil, old, m.latencyGen)
	}
}
