package out

import (
	"strings"
	"unicode/utf8"
)

func SanitizeHuman(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	lastSpace := false
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			i++
			continue
		}
		if r == '\x1b' {
			i += escapeSequenceLen(s[i:])
			if !lastSpace {
				b.WriteByte(' ')
				lastSpace = true
			}
			continue
		}
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
			if !lastSpace {
				b.WriteByte(' ')
				lastSpace = true
			}
			i += size
			continue
		}
		b.WriteRune(r)
		lastSpace = false
		i += size
	}
	return strings.TrimSpace(b.String())
}

func SanitizeBody(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			i++
			continue
		}
		if r == '\x1b' {
			i += escapeSequenceLen(s[i:])
			continue
		}
		switch {
		case r == '\n' || r == '\t':
			b.WriteRune(r)
		case r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f):
		default:
			b.WriteRune(r)
		}
		i += size
	}
	return b.String()
}

func escapeSequenceLen(s string) int {
	if len(s) < 2 || s[0] != '\x1b' {
		return 1
	}
	switch s[1] {
	case '[':
		for i := 2; i < len(s); i++ {
			if s[i] >= 0x40 && s[i] <= 0x7e {
				return i + 1
			}
		}
	case ']':
		for i := 2; i < len(s); i++ {
			if s[i] == 0x07 {
				return i + 1
			}
			if i+1 < len(s) && s[i] == '\x1b' && s[i+1] == '\\' {
				return i + 2
			}
		}
	default:
		return 2
	}
	return len(s)
}
