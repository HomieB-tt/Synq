package transfer

import (
	"fmt"
	"os/exec"
	"runtime"
)

// Opener hands a file to the operating system to open with whatever
// application the user has set as the default for its type. It is a
// function value so tests - and anything that must never launch a real
// program - can substitute their own.
type Opener func(path string) error

// OpenDefault is the real Opener: xdg-open on Linux and the BSDs, open on
// macOS, the shell's file handler on Windows. It starts the program and
// returns without waiting for it; the viewer lives or dies independently
// of synq.
//
// The path is passed as a single argument, never through a shell, so a
// filename can't inject commands. Callers must check CanView first -
// this function will open whatever it is given.
func OpenDefault(path string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", path)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", path)
	default:
		cmd = exec.Command("xdg-open", path)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("couldn't start the system opener: %w", err)
	}
	// Reap the opener (xdg-open exits quickly) so it doesn't linger as a
	// zombie; its exit status says nothing useful about the viewer.
	go func() { _ = cmd.Wait() }()
	return nil
}
