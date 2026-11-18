package terminal

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
)

func TestFloatingUndoCountRequiresOneCanonicalBoundedNumber(t *testing.T) {
	for _, count := range []int{0, 1, 99, 100} {
		c, f := newFixture(t, func(f *fixture) { f.historyCount = strconv.Itoa(count) })
		result, err := c.Query(context.Background(), FloatingUndoCount)
		if err != nil || !result.HistoryKnown || result.HistoryActions != count || result.ObservedLines != 1 {
			t.Fatal("canonical floating undo count was not parsed", result, err)
		}
		f.mu.Lock()
		commands := append([]string(nil), f.commands...)
		f.mu.Unlock()
		if len(commands) != 1 || !strings.Contains(commands[0], "/system/history/print count-only where floating-undo") {
			t.Fatal("history query used an unbounded inventory command")
		}
	}
}

func TestFloatingUndoMissingMalformedOrUnsupportedIsUnknown(t *testing.T) {
	for _, text := range []string{"", "00", "+1", "-1", "101", "1.0", " 1", "1 ", "1\r\n1", "count: 1", fixtureSecret} {
		c, _ := newFixture(t, func(f *fixture) { f.historyCount = text })
		result, err := c.Query(context.Background(), FloatingUndoCount)
		if codeOf(err) != ProtocolUnknown || result.HistoryKnown || result.HistoryActions != 0 {
			t.Fatal("unknown history count became a known number")
		}
		encoded, _ := json.Marshal(result)
		if strings.Contains(string(encoded), fixtureSecret) || strings.Contains(err.Error(), fixtureSecret) {
			t.Fatal("history count failure leaked raw output")
		}
	}
	c, _ := newFixture(t, func(f *fixture) { f.reject = true })
	result, err := c.Query(context.Background(), FloatingUndoCount)
	if codeOf(err) != RemoteRejected || result.HistoryKnown || result.HistoryActions != 0 {
		t.Fatal("unsupported count menu was treated as zero")
	}
}
