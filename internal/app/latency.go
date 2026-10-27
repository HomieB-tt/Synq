package app

import (
	"context"
	"fmt"
	"time"

	tea "charm.land/bubbletea/v2"
)

// The header shows the round trip to synq-server beside "online", so a
// slow link is visible without running :ping. It is one small GET /health
// every latencyInterval while logged in - nothing else is sent, and
// nothing about it reaches anyone but the server.

const (
	latencyInterval = 30 * time.Second
	latencyTimeout  = 5 * time.Second
)

type latencyTickMsg struct{ gen int }

type latencyResultMsg struct {
	gen int
	d   time.Duration
	err error
}

// latencyTickCmd fires the next poll for loop gen after delay.
func latencyTickCmd(gen int, delay time.Duration) tea.Cmd {
	if delay <= 0 {
		return func() tea.Msg { return latencyTickMsg{gen: gen} }
	}
	return tea.Tick(delay, func(time.Time) tea.Msg { return latencyTickMsg{gen: gen} })
}

// restartLatencyCmd starts a fresh polling loop (after register or login)
// and retires any earlier one: its next tick carries the old generation
// and is dropped.
func (m *Model) restartLatencyCmd() tea.Cmd {
	m.latencyGen++
	return latencyTickCmd(m.latencyGen, 0)
}

func (m Model) handleLatencyTick(msg latencyTickMsg) (tea.Model, tea.Cmd) {
	if msg.gen != m.latencyGen || m.apiClient == nil || m.accessToken == "" {
		return m, nil // retired loop, or logged out: stop
	}
	client, gen := m.apiClient, msg.gen
	ping := func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), latencyTimeout)
		defer cancel()
		d, err := client.Ping(ctx)
		return latencyResultMsg{gen: gen, d: d, err: err}
	}
	return m, tea.Batch(ping, latencyTickCmd(gen, latencyInterval))
}

func (m Model) handleLatencyResult(msg latencyResultMsg) (tea.Model, tea.Cmd) {
	if msg.gen != m.latencyGen || m.accessToken == "" {
		return m, nil
	}
	if msg.err != nil {
		m.latency = 0 // unknown beats stale
		return m, nil
	}
	m.latency = max(msg.d, time.Millisecond)
	return m, nil
}

// latencyLabel is "158 ms", or empty when unknown or not connected.
func (m Model) latencyLabel() string {
	if !m.connected || m.latency <= 0 {
		return ""
	}
	ms := m.latency.Milliseconds()
	if ms >= 10000 {
		return ">10 s"
	}
	return fmt.Sprintf("%d ms", ms)
}
