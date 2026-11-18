package traffic

import (
	"encoding/json"
	"errors"
	"io"
	"strings"
	"unicode/utf8"
)

// Reject ambiguous duplicate members before unmarshalling maps, and bound
// nesting independently of the total export-byte limit.
func legacyJSONShape(raw string) error {
	if !utf8.ValidString(raw) {
		return errors.New("legacy JSON must be UTF-8")
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	tokens := 0
	var value func(int) error
	value = func(depth int) error {
		if depth > 16 {
			return errors.New("legacy JSON nesting exceeds16")
		}
		token, e := decoder.Token()
		if e != nil {
			return errors.New("invalid legacy JSON")
		}
		tokens++
		if tokens > 2000000 {
			return errors.New("legacy JSON token count exceeds bound")
		}
		delimiter, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delimiter {
		case '{':
			keys := map[string]bool{}
			for decoder.More() {
				token, e := decoder.Token()
				if e != nil {
					return errors.New("invalid legacy JSON key")
				}
				key, ok := token.(string)
				if !ok || len(key) > 128 || keys[key] {
					return errors.New("legacy JSON has a duplicate or oversized object key")
				}
				keys[key] = true
				if e = value(depth + 1); e != nil {
					return e
				}
			}
			token, e = decoder.Token()
			if e != nil || token != json.Delim('}') {
				return errors.New("invalid legacy JSON object")
			}
		case '[':
			count := 0
			for decoder.More() {
				count++
				if count > LegacyMaxRows {
					return errors.New("legacy JSON array exceeds20000 entries")
				}
				if e = value(depth + 1); e != nil {
					return e
				}
			}
			token, e = decoder.Token()
			if e != nil || token != json.Delim(']') {
				return errors.New("invalid legacy JSON array")
			}
		default:
			return errors.New("invalid legacy JSON delimiter")
		}
		return nil
	}
	if e := value(0); e != nil {
		return e
	}
	if _, e := decoder.Token(); e != io.EOF {
		return errors.New("legacy JSON contains trailing data")
	}
	return nil
}
