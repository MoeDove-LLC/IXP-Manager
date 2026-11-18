package terminal

import (
	"regexp"
	"strconv"
	"strings"
)

type eventKind uint8

const (
	lineEvent eventKind = iota
	promptEvent
	pageEvent
	cursorEvent
	deviceEvent
	hijackEvent
	lostEvent
	loginEvent
)

type event struct {
	kind         eventKind
	text         string
	safe         bool
	throughBytes int64
}

var promptPattern = regexp.MustCompile(`^\[[^\]\r\n]{1,160}@[^\]\r\n]{1,160}\][ \t]*(?:[/a-zA-Z0-9_. -]*)?(?:<SAFE>[ \t]*>?|>)[ \t]*$`)
var pagerPattern = regexp.MustCompile(`^--\s*\[Q quit\|D dump(?:\|(?:up|down|left|right|next|previous))*\]\s*$`)

func classify(text string) (eventKind, bool) {
	t := strings.TrimSpace(text)
	if strings.EqualFold(t, "[Safe mode released by another user]") {
		return lostEvent, false
	}
	if strings.HasPrefix(t, "Hijacking Safe Mode from ") && strings.HasSuffix(t, "[u/r/d]:") {
		return hijackEvent, false
	}
	if pagerPattern.MatchString(t) {
		return pageEvent, false
	}
	if promptPattern.MatchString(t) {
		return promptEvent, strings.Contains(t, "<SAFE>")
	}
	lower := strings.ToLower(t)
	if strings.HasSuffix(lower, "login:") || strings.HasSuffix(lower, "password:") || strings.HasPrefix(lower, "password for ") && strings.HasSuffix(lower, ":") {
		return loginEvent, false
	}
	return lineEvent, false
}

// decoder interprets a bounded horizontal terminal line. Unsupported cursor
// movement is rejected, never guessed. Text stays private to the protocol layer.
type decoder struct {
	bytes          int64
	line           []byte
	cursor         int
	mode           byte
	escape         []byte
	stringEscape   bool
	maxLine        int
	lastFragment   string
	lineNumber     uint64
	savedLine      uint64
	savedCursor    int
	hasSavedCursor bool
}

func newDecoder(maxLine int) *decoder { return &decoder{maxLine: maxLine} }
func (d *decoder) resetLine() {
	clear(d.line)
	d.line = nil
	d.cursor = 0
	d.lastFragment = ""
	d.lineNumber++
}
func (d *decoder) event() event {
	text := string(d.line)
	kind, safe := classify(text)
	return event{kind: kind, text: text, safe: safe, throughBytes: d.bytes}
}
func (d *decoder) feed(data []byte) ([]event, error) {
	out := []event{}
	for _, b := range data {
		d.bytes++
		if d.mode == 's' {
			d.escape = append(d.escape, b)
			if len(d.escape) > 512 {
				return nil, failure(BoundsExceeded)
			}
			if b == 7 || d.stringEscape && b == '\\' {
				d.mode = 0
				clear(d.escape)
				d.escape = nil
				d.stringEscape = false
				continue
			}
			d.stringEscape = b == 27
			continue
		}
		if d.mode == 'e' {
			switch b {
			case '7': // DECSC: bounded current-line cursor save.
				d.savedLine, d.savedCursor, d.hasSavedCursor = d.lineNumber, d.cursor, true
				d.mode = 0
			case '8': // DECRC: reject unmodelled cross-line restoration.
				if !d.hasSavedCursor || d.savedLine != d.lineNumber {
					return nil, failure(ProtocolUnknown)
				}
				d.cursor = d.savedCursor
				d.mode = 0
			case 'c': // RIS: reset terminal presentation, not router rollback.
				d.resetLine()
				d.mode = 0
				clear(d.escape)
				d.escape = nil
				d.stringEscape = false
				d.hasSavedCursor = false
				d.savedCursor = 0
				d.savedLine = 0
			case 'Z':
				d.mode = 0
				out = append(out, event{kind: deviceEvent, throughBytes: d.bytes})
			case '[':
				d.mode = 'c'
				d.escape = nil
			case ']', 'P', '^', '_':
				d.mode = 's'
				d.escape = nil
			default:
				return nil, failure(ProtocolUnknown)
			}
			continue
		}
		if d.mode == 'c' {
			d.escape = append(d.escape, b)
			if len(d.escape) > 64 {
				return nil, failure(BoundsExceeded)
			}
			if b >= 0x40 && b <= 0x7e {
				if err := d.csi(d.escape, &out); err != nil {
					return nil, err
				}
				d.mode = 0
				d.escape = nil
			}
			continue
		}
		switch b {
		case 27:
			d.mode = 'e'
		case '\r':
			d.cursor = 0
		case '\n':
			e := d.event()
			if e.kind == lineEvent || strings.TrimSpace(e.text) != d.lastFragment {
				out = append(out, e)
			}
			d.resetLine()
		case 8:
			if d.cursor > 0 {
				d.cursor--
			}
		case 7: // bell has no text or authority.
		case '\t':
			if err := d.put(' '); err != nil {
				return nil, err
			}
		default:
			if b < 32 || b == 127 {
				return nil, failure(ProtocolUnknown)
			}
			if err := d.put(b); err != nil {
				return nil, err
			}
		}
	}
	if d.mode == 0 {
		e := d.event()
		if e.kind != lineEvent && strings.TrimSpace(e.text) != d.lastFragment {
			out = append(out, e)
			d.lastFragment = strings.TrimSpace(e.text)
		}
	}
	return out, nil
}
func (d *decoder) put(b byte) error {
	if d.cursor >= d.maxLine {
		return failure(BoundsExceeded)
	}
	for len(d.line) <= d.cursor {
		d.line = append(d.line, ' ')
	}
	d.line[d.cursor] = b
	d.cursor++
	return nil
}
func (d *decoder) csi(raw []byte, out *[]event) error {
	final := raw[len(raw)-1]
	params := string(raw[:len(raw)-1])
	if final == 'm' || (final == 'h' || final == 'l') && (params == "?25" || params == "?7") {
		return nil
	}
	if final == 'n' && params == "6" {
		*out = append(*out, event{kind: cursorEvent, throughBytes: d.bytes})
		return nil
	}
	if final == 'c' && (params == "" || params == "0") {
		*out = append(*out, event{kind: deviceEvent, throughBytes: d.bytes})
		return nil
	}
	// CHR 7.24.6 emits this exact movement before a fresh prompt. We do not
	// model a screen: accept it only at a genuinely empty line, never merely
	// after CR moved the cursor over existing text or a partial frame marker.
	if final == 'B' && params == "9999" {
		if len(d.line) != 0 || d.cursor != 0 {
			return failure(ProtocolUnknown)
		}
		d.resetLine()
		d.hasSavedCursor = false
		d.savedCursor = 0
		d.savedLine = 0
		return nil
	}
	// Exact native Home spellings must be handled before the single-number
	// parser. Drop the old presentation; never emit it as an output line.
	if final == 'H' && (params == "" || params == "1;1") {
		d.resetLine()
		d.hasSavedCursor = false
		d.savedCursor = 0
		d.savedLine = 0
		return nil
	}
	// ED from Home/an empty line is sufficient for the observed logout path.
	// CR alone over nonempty text does not authorize discarding that text.
	if final == 'J' && (params == "" || params == "0") {
		if len(d.line) != 0 || d.cursor != 0 {
			return failure(ProtocolUnknown)
		}
		d.resetLine()
		return nil
	}
	n := 1
	if params != "" {
		var err error
		n, err = strconv.Atoi(params)
		if err != nil || n < 0 || n > d.maxLine {
			return failure(ProtocolUnknown)
		}
	}
	switch final {
	case 'K':
		if params == "" || params == "0" {
			if d.cursor < len(d.line) {
				clear(d.line[d.cursor:])
				d.line = d.line[:d.cursor]
			}
		}
		if params == "2" {
			clear(d.line)
			d.line = nil
			d.cursor = 0
		}
		if params != "" && params != "0" && params != "2" {
			return failure(ProtocolUnknown)
		}
	case 'G':
		if n < 1 {
			return failure(ProtocolUnknown)
		}
		d.cursor = n - 1
	case 'C':
		d.cursor += n
	case 'D':
		d.cursor -= n
		if d.cursor < 0 {
			d.cursor = 0
		}
	case 'J':
		if params != "2" {
			return failure(ProtocolUnknown)
		}
		d.resetLine()
	case 'H', 'f':
		if params != "" {
			return failure(ProtocolUnknown)
		}
		d.resetLine()
	default:
		return failure(ProtocolUnknown)
	}
	if d.cursor >= d.maxLine {
		return failure(BoundsExceeded)
	}
	return nil
}
