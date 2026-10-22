package engine

import (
	"fmt"
	"time"
)

// Terminal formatting helpers
func bold(s string) string  { return "\033[1m" + s + "\033[0m" }
func dim(s string) string   { return "\033[2m" + s + "\033[0m" }
func green(s string) string { return "\033[32m" + s + "\033[0m" }

var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// Spinner handles animated progress display.
type Spinner struct {
	name   string
	stopCh chan struct{}
	doneCh chan struct{}
}

// StartSpinner creates and starts an animated spinner for a step.
func (m *Engine) StartSpinner(name string) *Spinner {
	s := &Spinner{
		name:   name,
		stopCh: make(chan struct{}),
		doneCh: make(chan struct{}),
	}

	// Pad name to 16 chars
	paddedName := name
	for len(paddedName) < 16 {
		paddedName += " "
	}

	go func() {
		ticker := time.NewTicker(80 * time.Millisecond)
		defer ticker.Stop()
		defer close(s.doneCh)

		idx := 0
		for {
			select {
			case <-s.stopCh:
				return
			case <-ticker.C:
				fmt.Printf("\r\033[K  %s%s", paddedName, dim(spinnerFrames[idx]))
				idx = (idx + 1) % len(spinnerFrames)
			}
		}
	}()

	return s
}

// Stop stops the spinner and shows success or failure.
func (s *Spinner) Stop(success bool) {
	close(s.stopCh)
	<-s.doneCh

	paddedName := s.name
	for len(paddedName) < 16 {
		paddedName += " "
	}

	fmt.Print("\r\033[K")
	if success {
		fmt.Printf("  %s%s\n", paddedName, green("✓"))
	} else {
		fmt.Printf("  %s%s\n", paddedName, "✗")
	}
}

// printWarn prints a warning message.
func printWarn(format string, args ...any) {
	fmt.Printf("  %s %s\n", dim("!"), fmt.Sprintf(format, args...))
}
