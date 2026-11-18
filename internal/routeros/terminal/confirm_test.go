package terminal

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestConfirm7246RequiresExactReleaseAndFreshNoOwnerWithoutCommitClaim(t *testing.T) {
	c, f := newFixture(t, nil)
	if _, err := c.EnterSafeMode(context.Background()); err != nil {
		t.Fatal(err)
	}
	result, err := c.ConfirmSafeMode(context.Background())
	if err != nil || !result.Requested || !result.Released || result.CommitVerified || result.Evidence.Mode != Released || result.Evidence.Owner != OwnerNone || !result.Evidence.OwnerQueryAvailable || result.Evidence.Closed || result.Evidence.CommitVerified || result.Evidence.RollbackVerified || result.Evidence.NativeValidated {
		t.Fatal("release evidence was missing or promoted to commit proof", result, err)
	}
	if _, err := c.ConfirmSafeMode(context.Background()); codeOf(err) != StateUnknown {
		t.Fatal("second confirmation toggled Safe Mode", err)
	}
	f.mu.Lock()
	controls := append([]byte(nil), f.controls...)
	commands := append([]string(nil), f.commands...)
	f.mu.Unlock()
	if string(controls) != "\x18\x18" || len(commands) != 4 || !strings.Contains(commands[2], "/safe-mode/print") || !strings.Contains(commands[3], "/safe-mode/print") {
		t.Fatal("release lacked fresh owner reads or sent repeated control", controls)
	}
	for _, command := range commands {
		if strings.Contains(command, "/quit") {
			t.Fatal("release used quit")
		}
	}
	if evidence, err := c.OwnerEvidence(context.Background()); err != nil || evidence.Owner != OwnerNone || evidence.Mode != Ready {
		t.Fatal("released session could not perform a bounded read", err)
	}
}

func TestConfirmNeverTogglesWithoutCurrentOwner(t *testing.T) {
	for _, change := range []func(*fixture){func(f *fixture) { f.ownerMenu = false }, func(f *fixture) { f.ownerMismatch = true }} {
		c, f := newFixture(t, change)
		if _, err := c.EnterSafeMode(context.Background()); err != nil {
			t.Fatal(err)
		}
		result, err := c.ConfirmSafeMode(context.Background())
		if err == nil || result.Requested || result.Released || result.CommitVerified {
			t.Fatal("unverified ownership allowed a release toggle")
		}
		f.mu.Lock()
		controls := append([]byte(nil), f.controls...)
		f.mu.Unlock()
		if string(controls) != "\x18" {
			t.Fatal("owner check failed after a release toggle")
		}
	}
}

func TestConfirmRejectsUnknownTakenLossSafePromptDisconnectAndPostOwner(t *testing.T) {
	cases := map[string]func(*fixture){
		"generic success":   func(f *fixture) { f.releaseMessage = "Success!" },
		"old documentation": func(f *fixture) { f.releaseMessage = "[Safe Mode released]" },
		"taken":             func(f *fixture) { f.releaseMessage = "Taking Safe Mode session... Success!" },
		"unknown":           func(f *fixture) { f.releaseMessage = fixtureSecret },
		"duplicate ack":     func(f *fixture) { f.releaseMessage += "\r\n" + f.releaseMessage },
		"explicit loss":     func(f *fixture) { f.releaseMessage = "[Safe mode released by another user]" },
		"other owner prompt": func(f *fixture) {
			f.releaseMessage = "Hijacking Safe Mode from private-owner - unroll/release/don't take it [u/r/d]:"
		},
		"safe prompt":            func(f *fixture) { f.releaseSafePrompt = true },
		"no prompt":              func(f *fixture) { f.releaseNoPrompt = true },
		"disconnect":             func(f *fixture) { f.releaseEOF = true },
		"post owner changed":     func(f *fixture) { f.releaseOwnerOther = true },
		"post owner unavailable": func(f *fixture) { f.releaseOwnerUnavailable = true },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			c, f := newFixture(t, change)
			if _, err := c.EnterSafeMode(context.Background()); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			result, err := c.ConfirmSafeMode(ctx)
			cancel()
			if err == nil || result.Released || result.CommitVerified || !c.Snapshot().Closed {
				t.Fatal("ambiguous release was reported as verified", result, err)
			}
			encoded, _ := json.Marshal(result)
			if strings.Contains(string(encoded), fixtureSecret) || strings.Contains(err.Error(), fixtureSecret) {
				t.Fatal("release error exposed terminal output")
			}
			if again, err := c.ConfirmSafeMode(context.Background()); err == nil || again.Requested || again.Released {
				t.Fatal("ambiguous release was retried")
			}
			f.mu.Lock()
			controls := append([]byte(nil), f.controls...)
			f.mu.Unlock()
			if string(controls) != "\x18\x18" {
				t.Fatal("release sent a takeover response or repeated toggle", controls)
			}
		})
	}
}

func TestConcurrentConfirmCallsReleaseOnlyOnce(t *testing.T) {
	c, f := newFixture(t, nil)
	if _, err := c.EnterSafeMode(context.Background()); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan Confirmation, 8)
	failures := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := c.ConfirmSafeMode(context.Background())
			results <- result
			failures <- err
		}()
	}
	wg.Wait()
	close(results)
	close(failures)
	released := 0
	for result := range results {
		if result.Released {
			released++
		}
	}
	if released != 1 {
		t.Fatal("concurrent confirmations did not have exactly one release")
	}
	for err := range failures {
		if err != nil && codeOf(err) != StateUnknown {
			t.Fatal("confirmation interleaved terminal frames", err)
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if string(f.controls) != "\x18\x18" {
		t.Fatal("concurrent confirmation repeated Ctrl-X")
	}
}

func TestCancelledConfirmDoesNotWriteAndLostOwnerCannotConfirm(t *testing.T) {
	c, f := newFixture(t, nil)
	if _, err := c.EnterSafeMode(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if result, err := c.ConfirmSafeMode(ctx); codeOf(err) != Cancelled || result.Requested {
		t.Fatal("cancelled confirmation was sent", err)
	}
	go f.write("\r\n[Safe mode released by another user]\r\n[admin@CHR] > ")
	select {
	case <-c.done:
	case <-time.After(time.Second):
		t.Fatal("fixture loss was not observed")
	}
	if result, err := c.ConfirmSafeMode(context.Background()); codeOf(err) != OwnershipLost || result.Requested || result.Released {
		t.Fatal("lost owner was allowed to release", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if string(f.controls) != "\x18" {
		t.Fatal("cancelled/lost confirmation wrote control")
	}
}
