package app

import (
	"strings"
	"testing"
	"time"
)

func TestFormatPing(t *testing.T) {
	ms := time.Millisecond
	got := formatPing([]time.Duration{900 * ms, 300 * ms, 100 * ms, 200 * ms})
	for _, want := range []string{"first 900 ms", "min 100 ms", "avg 200 ms", "max 300 ms"} {
		if !strings.Contains(got, want) {
			t.Errorf("formatPing = %q, missing %q", got, want)
		}
	}
	if got := formatPing([]time.Duration{5 * ms}); got != "Ping: 5 ms" {
		t.Errorf("single = %q", got)
	}
	if got := formatPing(nil); !strings.Contains(got, "no samples") {
		t.Errorf("empty = %q", got)
	}
}

func TestServerCommandWithoutStore(t *testing.T) {
	m := Model{}
	if got := m.serverCommand(nil); !strings.Contains(got, "Server: none") {
		t.Errorf("show = %q", got)
	}
	if got := m.serverCommand([]string{"https://x.example"}); !strings.Contains(got, "Can't save") {
		t.Errorf("set without store = %q", got)
	}
}
