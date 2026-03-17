package main

import "testing"

func TestTruncate(t *testing.T) {
	tests := []struct {
		input string
		max   int
		want  string
	}{
		{"hello", 10, "hello"},
		{"hello world", 5, "hell…"},
		{"hello", 5, "hello"},
		{"hello", 0, "hello"},
		{"hello", -1, "hello"},
		{"ab", 1, "a"},
		{"hello\nworld", 20, "hello world"},
		{"  hello  ", 20, "hello"},
	}
	for _, tc := range tests {
		got := truncate(tc.input, tc.max)
		if got != tc.want {
			t.Errorf("truncate(%q, %d) = %q, want %q", tc.input, tc.max, got, tc.want)
		}
	}
}

func TestTruncateForDisplayForceFull(t *testing.T) {
	long := "3EB0B0E8A1B2C3D4E5F6A7B8C9D0"

	// with forceFull=true, should always return full string (after cleanup)
	got := truncateForDisplay(long, 14, true)
	if got != long {
		t.Errorf("truncateForDisplay(%q, 14, true) = %q, want %q", long, got, long)
	}

	// newlines should still be replaced
	got = truncateForDisplay("hello\nworld", 5, true)
	if got != "hello world" {
		t.Errorf("truncateForDisplay with newline: got %q, want %q", got, "hello world")
	}
}

func TestParseTime(t *testing.T) {
	// RFC3339
	ts, err := parseTime("2025-01-27T10:30:00Z")
	if err != nil {
		t.Fatalf("parseTime RFC3339: %v", err)
	}
	if ts.Year() != 2025 || ts.Month() != 1 || ts.Day() != 27 {
		t.Fatalf("unexpected parsed time: %v", ts)
	}

	// YYYY-MM-DD
	ts, err = parseTime("2025-01-27")
	if err != nil {
		t.Fatalf("parseTime YYYY-MM-DD: %v", err)
	}
	if ts.Year() != 2025 || ts.Month() != 1 || ts.Day() != 27 {
		t.Fatalf("unexpected parsed time: %v", ts)
	}

	// invalid
	_, err = parseTime("not-a-date")
	if err == nil {
		t.Fatalf("expected error for invalid date")
	}

	// empty
	_, err = parseTime("")
	if err == nil {
		t.Fatalf("expected error for empty date")
	}
}
