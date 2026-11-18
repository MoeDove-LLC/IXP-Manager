package mailingsync

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func watchFixture(t *testing.T, f *fixture, maximum int) (string, WatchPolicy) {
	t.Helper()
	p := f.preview(f.client())
	dir := filepath.Join(t.TempDir(), "worker")
	policy, err := AuthorizeWatch(context.Background(), f.c, p, dir, p.Checksum, WatchOptions{true, true, maximum, 5})
	if err != nil {
		t.Fatal(err)
	}
	return dir, policy
}
func openWatchFixture(t *testing.T, f *fixture, dir string) *Watcher {
	t.Helper()
	w, err := OpenWatch(f.c, dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { w.Close() })
	return w
}
func setWatchConsent(f *fixture, subscribed, unsubscribed []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.export.Subscribed = subscribed
	f.export.Unsubscribed = unsubscribed
	f.export.List.Revision++
}

func TestWatchConsentRoundsRestartAndRetention(t *testing.T) {
	f := newFixture(t)
	dir, policy := watchFixture(t, f, 2)
	w := openWatchFixture(t, f, dir)
	if other, err := OpenWatch(f.c, dir); err == nil {
		other.Close()
		t.Fatal("concurrent worker acquired history")
	}
	ctx := context.Background()
	s, err := w.Tick(ctx)
	if err != nil || s.Round != 1 || s.Active || !s.Summary.RecordedComplete || f.writes != 2 {
		t.Fatal(s, err, f.writes)
	}
	old, _ := ReadPrivate(filepath.Join(w.roundDir(1), "plan.json"), maxDocument)
	if s, err = w.Tick(ctx); err != nil || s.Round != 1 || f.writes != 2 {
		t.Fatal("unchanged poll created/replayed a round", s, err)
	}
	w.Close()
	w = openWatchFixture(t, f, dir)
	setWatchConsent(f, []string{"stay@example.test"}, []string{"add@example.test"})
	if s, err = w.Tick(ctx); err != nil || s.Round != 2 || f.writes != 3 {
		t.Fatal("new self consent not applied on next round", s, err)
	}
	setWatchConsent(f, []string{"add@example.test", "stay@example.test"}, []string{})
	if s, err = w.Tick(ctx); err != nil || s.Round != 3 || f.writes != 4 {
		t.Fatal(s, err)
	}
	now, _ := ReadPrivate(filepath.Join(w.roundDir(1), "plan.json"), maxDocument)
	if string(old) != string(now) {
		t.Fatal("old plan was rewritten")
	}
	setWatchConsent(f, []string{"new@example.test"}, []string{})
	f.mu.Lock()
	f.lost, f.beforeLoss = true, true
	f.mu.Unlock()
	if s, err = w.Tick(ctx); err == nil || !s.Active || s.Summary.Unknown != 1 {
		t.Fatal(s, err)
	}
	if s, err = w.Prune(1, policy.Checksum); err != nil || s.PrunedThrough != 2 || s.Round != 4 || !s.Active {
		t.Fatal("prune failed to preserve active round", s, err)
	}
	for _, p := range []string{"approved-plan.json", "policy.json", "round-00000000000000000003/plan.json", "round-00000000000000000004/journal.json"} {
		if _, err := ReadPrivate(filepath.Join(dir, p), maxDocument); err != nil {
			t.Fatal(p, err)
		}
	}
	if _, err := os.Stat(w.roundDir(1)); !os.IsNotExist(err) {
		t.Fatal("closed history not pruned")
	}
	w.Close()
	w = openWatchFixture(t, f, dir)
	if s, err = w.Tick(ctx); err == nil || s.Summary.Unknown != 1 || f.writes != 5 {
		t.Fatal("restart bypassed retained unresolved round", s, err)
	}
}

func TestWatchUnknownLateCommitRequiresExplicitResume(t *testing.T) {
	f := newFixture(t)
	setWatchConsent(f, []string{"add@example.test"}, []string{})
	dir, policy := watchFixture(t, f, 1)
	w := openWatchFixture(t, f, dir)
	f.mu.Lock()
	f.lost, f.beforeLoss = true, true
	f.mu.Unlock()
	ctx := context.Background()
	s, err := w.Tick(ctx)
	if err == nil || !s.Halted || s.Summary.Unknown != 1 || f.writes != 1 {
		t.Fatal(s, err)
	}
	w.Close()
	w = openWatchFixture(t, f, dir)
	setWatchConsent(f, []string{}, []string{"add@example.test"})
	f.mu.Lock()
	f.lost, f.beforeLoss = false, false
	f.mu.Unlock()
	if _, err = w.Tick(ctx); err == nil || f.writes != 1 {
		t.Fatal("opposite new consent bypassed uncertain request")
	}
	beforeObservation := w.Status().LastObservedAt
	f.mu.Lock()
	f.version = "unreviewed target"
	f.mu.Unlock()
	if s, err = w.Reconcile(ctx); err == nil || s.LastObservedAt != beforeObservation || !strings.HasPrefix(s.Reason, "reconcile-stopped:") {
		t.Fatal("failed readback invented a new persisted observation", s, err)
	}
	f.mu.Lock()
	f.version = "GNU Mailman 3.3.10 (Tom Sawyer)"
	f.mu.Unlock()
	if s, err = w.Reconcile(ctx); err != nil || s.Summary.NotObserved != 1 || f.writes != 1 {
		t.Fatal(s, err)
	}
	if _, err = w.Resume(ctx, policy.Checksum); err == nil {
		t.Fatal("not-observed treated as safe retry")
	}
	f.mu.Lock()
	f.members["add@example.test"] = Member{fmt.Sprintf("%032x", 101), f.c.Target.ListID, "add@example.test", "member", "as_address"}
	f.mu.Unlock()
	if s, err = w.Reconcile(ctx); err != nil || !s.Halted || s.Summary.Unknown != 0 || s.Summary.NotObserved != 0 || f.writes != 1 {
		t.Fatal(s, err)
	}
	if _, err = w.Tick(ctx); err == nil {
		t.Fatal("late observation silently resumed automation")
	}
	if _, err = w.Resume(ctx, strings.Repeat("0", 64)); err == nil {
		t.Fatal("unapproved resume")
	}
	if s, err = w.Resume(ctx, policy.Checksum); err != nil || s.Halted || f.writes != 1 {
		t.Fatal(s, err)
	}
	if s, err = w.Tick(ctx); err != nil || s.Round != 2 || f.writes != 2 {
		t.Fatal("new consent not applied after safe explicit recovery", s, err)
	}
	if _, exists := f.members["add@example.test"]; exists {
		t.Fatal("late subscription not withdrawn")
	}
}

func TestWatchAuthorityIdentityNotificationsAndLimitStop(t *testing.T) {
	for _, kind := range []string{"source-authority", "target-authority", "target-identity", "notification-prerequisite", "change-limit", "credentials"} {
		t.Run(kind, func(t *testing.T) {
			f := newFixture(t)
			maximum := 2
			if kind == "change-limit" {
				maximum = 1
			}
			dir, _ := watchFixture(t, f, maximum)
			w := openWatchFixture(t, f, dir)
			switch kind {
			case "source-authority":
				f.sourceStatus = 403
			case "target-authority":
				f.targetStatus = 401
			case "target-identity":
				f.createdAt = "2026-11-20T00:00:00"
			case "notification-prerequisite":
				f.adminNotify = true
			case "credentials":
				if err := os.Remove(f.c.Target.PasswordFile); err != nil {
					t.Fatal(err)
				}
			}
			s, err := w.Tick(context.Background())
			if err == nil || !s.Halted || !strings.HasPrefix(s.Reason, kind+":") || s.Round != 0 || f.writes != 0 {
				t.Fatal(s, err)
			}
			w.Close()
			w = openWatchFixture(t, f, dir)
			if _, err = w.Tick(context.Background()); err == nil || f.writes != 0 {
				t.Fatal("restart cleared persistent halt")
			}
		})
	}
}

func TestWatchPublishedRoundCrashRecoveryAndUnreferencedIntent(t *testing.T) {
	for _, pending := range []bool{false, true} {
		t.Run(fmt.Sprint(pending), func(t *testing.T) {
			f := newFixture(t)
			dir, _ := watchFixture(t, f, 2)
			w := openWatchFixture(t, f, dir)
			original := w.state
			_, p, _, err := w.preview(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if err = w.prepare(p); err != nil {
				t.Fatal(err)
			}
			if pending {
				_, j, err := w.round(1)
				if err != nil {
					t.Fatal(err)
				}
				j.Results[0] = Result{State: "intent"}
				if err = saveJournal(filepath.Join(w.roundDir(1), "journal.json"), j); err != nil {
					t.Fatal(err)
				}
			}
			if err = writeWatch(filepath.Join(dir, "state.json"), original, true); err != nil {
				t.Fatal(err)
			}
			w.Close()
			restarted, err := OpenWatch(f.c, dir)
			if pending {
				if err == nil {
					restarted.Close()
					t.Fatal("unreferenced attempted mutation bypassed history")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer restarted.Close()
			s, err := restarted.Tick(context.Background())
			if err != nil || s.Round != 1 || f.writes != 2 {
				t.Fatal("published round was not reused", s, err)
			}
		})
	}
}

func TestMailmanCheckpointFailureReportsOnlyDurableJournal(t *testing.T) {
	for _, mutation := range []bool{false, true} {
		t.Run(fmt.Sprint(mutation), func(t *testing.T) {
			f := newFixture(t)
			address := "stay@example.test"
			if mutation {
				address = "add@example.test"
			}
			setWatchConsent(f, []string{address}, []string{})
			c := f.client()
			p := f.preview(c)
			path := filepath.Join(t.TempDir(), "journal")
			f.server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				f.handle(w, r)
				if mutation && r.Method == "POST" || !mutation && strings.Contains(r.URL.Path, "/member/") {
					if err := os.Chmod(path, 0400); err != nil {
						t.Error(err)
					}
				}
			})
			s, err := c.Apply(context.Background(), p, path, p.Checksum, true)
			if err == nil || s.RecordedComplete || s.Completed != 0 {
				t.Fatal("unpersisted memory observation reported as durable", s, err)
			}
			if mutation && s.Unknown != 1 {
				t.Fatal("durable intent was hidden after readback checkpoint failure", s)
			}
			if err := os.Chmod(path, 0600); err != nil {
				t.Fatal(err)
			}
			durable, err := Status(p, path)
			if err != nil || durable != s {
				t.Fatal("error summary differs from durable journal", durable, s, err)
			}
		})
	}
}

func TestMailmanUnchangedObservationsUseOneCheckpoint(t *testing.T) {
	f := newFixture(t)
	f.export.Subscribed = []string{}
	f.export.Unsubscribed = []string{}
	for i := 0; i < 40; i++ {
		address := fmt.Sprintf("existing-%02d@example.test", i)
		f.export.Subscribed = append(f.export.Subscribed, address)
		f.members[address] = Member{fmt.Sprintf("%032x", 1000+i), f.c.Target.ListID, address, "member", "as_address"}
	}
	sort.Strings(f.export.Subscribed)
	c := f.client()
	p := f.preview(c)
	path := filepath.Join(t.TempDir(), "journal")
	f.server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/member/") {
			raw, err := ReadPrivate(path, maxDocument)
			if err != nil {
				t.Error(err)
			}
			var j Journal
			if err := json.Unmarshal(raw, &j); err != nil {
				t.Error(err)
			}
			for _, result := range j.Results {
				if result.State != "planned" {
					t.Error("read-only observation checkpointed before end of unchanged batch")
				}
			}
		}
		f.handle(w, r)
	})
	s, err := c.Apply(context.Background(), p, path, p.Checksum, true)
	if err != nil || s.Completed != 40 || !s.RecordedComplete || f.writes != 0 {
		t.Fatal(s, err)
	}
}
