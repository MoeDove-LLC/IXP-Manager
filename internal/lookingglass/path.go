package lookingglass

import (
	"strconv"
	"strings"
	"unicode"
)

// ParseASPath preserves AS_SET and confederation boundaries. Unknown notation
// is rejected instead of flattening an unordered set into a sequence.
func ParseASPath(raw string) ([]PathSegment, error) {
	if len(raw) > 16384 {
		return nil, ErrOutputLimit
	}
	out := []PathSegment{}
	current := PathSegment{Type: "sequence"}
	closing := rune(0)
	digits := ""
	count := 0
	commaPending := false
	lastNumber := false
	appendASN := func() error {
		if digits == "" {
			return nil
		}
		value, e := strconv.ParseUint(digits, 10, 32)
		if e != nil {
			return ErrFormat
		}
		current.ASNs = append(current.ASNs, uint32(value))
		lastNumber = true
		commaPending = false
		digits = ""
		count++
		if count > 512 {
			return ErrOutputLimit
		}
		return nil
	}
	flush := func() {
		if len(current.ASNs) > 0 {
			out = append(out, current)
		}
		current = PathSegment{Type: "sequence"}
	}
	for _, r := range strings.TrimSpace(raw) {
		if r >= '0' && r <= '9' {
			digits += string(r)
			if len(digits) > 10 {
				return nil, ErrFormat
			}
			continue
		}
		if e := appendASN(); e != nil {
			return nil, e
		}
		switch r {
		case '{', '(', '[':
			if closing != 0 {
				return nil, ErrFormat
			}
			flush()
			lastNumber = false
			switch r {
			case '{':
				current.Type = "set"
				closing = '}'
			case '(':
				current.Type = "confed-sequence"
				closing = ')'
			case '[':
				current.Type = "confed-set"
				closing = ']'
			}
		case '}', ')', ']':
			if closing != r || len(current.ASNs) == 0 || commaPending {
				return nil, ErrFormat
			}
			flush()
			closing = 0
			lastNumber = false
		case ',':
			if closing == 0 || current.Type == "confed-sequence" || !lastNumber || commaPending {
				return nil, ErrFormat
			}
			commaPending = true
			lastNumber = false
		default:
			if !unicode.IsSpace(r) || r == '\n' || r == '\r' {
				return nil, ErrFormat
			}
		}
	}
	if e := appendASN(); e != nil {
		return nil, e
	}
	if closing != 0 {
		return nil, ErrFormat
	}
	flush()
	if len(out) > 64 {
		return nil, ErrOutputLimit
	}
	return out, nil
}
