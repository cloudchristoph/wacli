package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"golang.org/x/term"
)

func isTTY() bool {
	return term.IsTerminal(int(os.Stdout.Fd()))
}

func parseTime(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, fmt.Errorf("time is required")
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	if t, err := time.Parse("2006-01-02", s); err == nil {
		return t, nil
	}
	return time.Time{}, fmt.Errorf("unsupported time format %q (use RFC3339 or YYYY-MM-DD)", s)
}

func sanitize(s string) string {
	return strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
}

func truncate(s string, max int) string {
	s = sanitize(s)
	if max <= 0 || len(s) <= max {
		return s
	}
	if max <= 1 {
		return s[:max]
	}
	return s[:max-1] + "…"
}

// truncateForDisplay truncates strings for tabular output.
// When forceFull is true or stdout is not a TTY (piped), returns the full string.
func truncateForDisplay(s string, max int, forceFull bool) string {
	if forceFull || !isTTY() {
		return sanitize(s)
	}
	return truncate(s, max)
}
