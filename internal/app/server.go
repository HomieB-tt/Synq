package app

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/HomieB-tt/synq/internal/api"
	"github.com/HomieB-tt/synq/internal/db"
)

// pingResultMsg carries the one-line `:ping` report.
type pingResultMsg string

const pingCount = 4

// pingCmd times pingCount sequential requests to the server's /health.
// The first runs on a cold connection (DNS, TCP, TLS), so it is reported
// apart from the rest: a large gap there and small numbers after means
// connection setup, not the server, is slow.
func pingCmd(client *api.Client) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		var times []time.Duration
		for i := 0; i < pingCount; i++ {
			d, err := client.Ping(ctx)
			if err != nil {
				return pingResultMsg(fmt.Sprintf("Ping failed: %v", err))
			}
			times = append(times, d)
		}
		return pingResultMsg(formatPing(times))
	}
}

// formatPing summarises timings: first request, then min/avg/max of the
// remaining ones.
func formatPing(times []time.Duration) string {
	if len(times) == 0 {
		return "Ping: no samples"
	}
	ms := func(d time.Duration) string { return fmt.Sprintf("%d ms", d.Milliseconds()) }
	if len(times) == 1 {
		return "Ping: " + ms(times[0])
	}
	rest := append([]time.Duration(nil), times[1:]...)
	sort.Slice(rest, func(i, j int) bool { return rest[i] < rest[j] })
	var sum time.Duration
	for _, d := range rest {
		sum += d
	}
	avg := sum / time.Duration(len(rest))
	return fmt.Sprintf("Ping: first %s (includes connection setup); then min %s / avg %s / max %s",
		ms(times[0]), ms(rest[0]), ms(avg), ms(rest[len(rest)-1]))
}

// serverCommand implements `:server [address|reset]`.
func (m *Model) serverCommand(args []string) string {
	if len(args) == 0 {
		cur := "none"
		if m.apiClient != nil {
			cur = m.apiClient.BaseURL
		}
		msg := "Server: " + cur + ". Change it with :server <address>"
		if os.Getenv("SYNQ_SERVER_URL") != "" {
			msg += " (SYNQ_SERVER_URL is set and takes priority)"
		}
		return msg + "."
	}
	if m.store == nil {
		return "Can't save the server address here."
	}
	if len(args) == 1 && strings.EqualFold(args[0], "reset") {
		if err := m.store.SavePreference(db.PrefServerURL, ""); err != nil {
			return fmt.Sprintf("Couldn't reset the server: %v", err)
		}
		return "Saved server cleared - the built-in default (if any) applies after you restart Synq."
	}
	addr := strings.TrimSpace(args[0])
	if !strings.Contains(addr, "://") {
		addr = "https://" + addr
	}
	if err := api.ValidateServerURL(addr, os.Getenv("SYNQ_ALLOW_INSECURE_HTTP") == "1"); err != nil {
		return err.Error()
	}
	if err := m.store.SavePreference(db.PrefServerURL, addr); err != nil {
		return fmt.Sprintf("Couldn't save the server: %v", err)
	}
	msg := fmt.Sprintf("Server set to %s - restart Synq to connect.", addr)
	if m.username != "" {
		msg += " Your username and login belong to the old server; you'll need :register or :login on the new one."
	}
	return msg
}
