package app

import (
	"context"
	"net/http"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/moedove/ixp-manager/internal/notify"
)

type schedulerBlockingSender struct {
	entered  chan struct{}
	canceled chan struct{}
	release  chan struct{}
}

func (s *schedulerBlockingSender) Send(ctx context.Context, _ notify.Message) error {
	close(s.entered)
	<-ctx.Done()
	close(s.canceled)
	<-s.release
	return ctx.Err()
}

func TestSchedulerHealthBlockedCycleAndCancellation(t *testing.T) {
	c, _ := setupClient(t)
	enableTestMail(c.a)
	sender := &schedulerBlockingSender{entered: make(chan struct{}), canceled: make(chan struct{}), release: make(chan struct{}, 1)}
	c.a.mailer = sender
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer close(sender.release)
	fresh := c.a.schedulerHealth(time.Now())
	if fresh.Status != "not-started" || fresh.StartedAt != nil || fresh.LastCompletedAt != nil || fresh.CycleStartedAt != nil ||
		fresh.StoppedAt != nil || fresh.CurrentPhase != "" || fresh.FailedPhases == nil || len(fresh.FailedPhases) != 0 || fresh.StallAfterSeconds != 600 {
		t.Fatalf("unexpected new scheduler state: %+v", fresh)
	}
	if w := c.request(http.MethodPost, "/api/forgot-password", Record{"email": "admin@example.net"}); w.Code != http.StatusAccepted {
		t.Fatalf("failed to queue local fixture mail: HTTP %d", w.Code)
	}
	c.a.StartBackground(ctx)
	select {
	case <-sender.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("background worker did not enter mail phase")
	}
	active := c.a.schedulerHealth(time.Now())
	if active.Status != "starting" || active.StartedAt == nil || active.CycleStartedAt == nil || active.CurrentPhase != schedulerMailPhase || active.LastCompletedAt != nil || active.StoppedAt != nil {
		t.Fatalf("blocked first cycle was classified as completed: %+v", active)
	}
	boundary := active.StartedAt.Add(schedulerStallAfter)
	if got := c.a.schedulerHealth(boundary); got.Status != "starting" || got.CurrentPhase != schedulerMailPhase {
		t.Fatalf("strict stall boundary: %+v", got)
	}
	if got := c.a.schedulerHealth(boundary.Add(time.Nanosecond)); got.Status != "stalled" || got.LastCompletedAt != nil {
		t.Fatalf("blocked worker was not observable as stalled: %+v", got)
	}
	if w := c.request(http.MethodGet, "/healthz", nil); w.Code != http.StatusOK {
		t.Fatalf("stalled scheduler changed HTTP liveness: HTTP %d", w.Code)
	}

	cancel()
	select {
	case <-sender.canceled:
	case <-time.After(3 * time.Second):
		t.Fatal("parent context did not propagate to the worker")
	}
	if got := c.a.schedulerHealth(time.Now()); got.Status != "stopping" || got.CurrentPhase != schedulerMailPhase || got.StoppedAt != nil {
		t.Fatalf("canceled but blocked worker reported stopped: %+v", got)
	}
	if got := c.a.schedulerHealth(boundary.Add(time.Nanosecond)); got.Status != "stalled" || got.CurrentPhase != schedulerMailPhase {
		t.Fatalf("blocked shutdown no longer showed its stalled phase: %+v", got)
	}
	sender.release <- struct{}{}
	select {
	case <-c.a.backgroundDone:
	case <-time.After(3 * time.Second):
		t.Fatal("worker did not exit once its blocking phase returned")
	}
	stopped := c.a.schedulerHealth(time.Now())
	if stopped.Status != "stopped" || stopped.CycleStartedAt != nil || stopped.CurrentPhase != "" || stopped.LastCompletedAt == nil ||
		stopped.StoppedAt == nil || stopped.StoppedAt.Before(*stopped.LastCompletedAt) || stopped.LastCompletedAt.Before(*active.CycleStartedAt) {
		t.Fatalf("worker did not publish exit after its actual cycle completion: %+v", stopped)
	}
}

func TestSchedulerHealthReportsPhaseFailureAndSubsequentCompletion(t *testing.T) {
	c, dir := setupClient(t)
	c.a.store.mu.Lock()
	original := c.a.store.path
	c.a.store.path = filepath.Join(dir, "missing", "state.json")
	c.a.store.mu.Unlock()
	c.a.StartBackground(context.Background())
	degraded := waitSchedulerStatus(t, c.a, "degraded")
	if degraded.LastCompletedAt == nil || !slices.Equal(degraded.FailedPhases, []schedulerPhase{schedulerJobsPhase}) {
		t.Fatalf("durable jobs failure was not reported by phase: %+v", degraded)
	}

	c.a.store.mu.Lock()
	c.a.store.path = original
	c.a.store.mu.Unlock()
	resumed := waitSchedulerStatus(t, c.a, "running")
	if resumed.LastCompletedAt == nil || !resumed.LastCompletedAt.After(*degraded.LastCompletedAt) || len(resumed.FailedPhases) != 0 {
		t.Fatalf("healthy next cycle did not clear previous failures: %+v", resumed)
	}
	if len(degraded.FailedPhases) != 1 || degraded.FailedPhases[0] != schedulerJobsPhase {
		t.Fatal("new cycle mutated a previous scheduler sample")
	}
}

func waitSchedulerStatus(t *testing.T, a *App, status string) schedulerHealthView {
	t.Helper()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	timeout := time.NewTimer(5 * time.Second)
	defer timeout.Stop()
	for {
		got := a.schedulerHealth(time.Now())
		if got.Status == status {
			return got
		}
		select {
		case <-ticker.C:
		case <-timeout.C:
			t.Fatalf("scheduler status is %q, want %q: %+v", got.Status, status, got)
		}
	}
}

func TestSchedulerHealthStallAgeUsesLastCompletedCycle(t *testing.T) {
	started := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	lastCompleted := started.Add(3 * time.Minute)
	state := schedulerHealthState{
		startedAt: started, lastCompletedAt: lastCompleted,
		cycleStartedAt: lastCompleted.Add(2 * time.Second), currentPhase: schedulerAtlasPhase,
	}
	boundary := lastCompleted.Add(schedulerStallAfter)
	if got := state.status(boundary); got != "running" {
		t.Fatalf("completed-cycle stall boundary: got %q", got)
	}
	if got := state.status(boundary.Add(time.Nanosecond)); got != "stalled" {
		t.Fatalf("current blocked phase suppressed stall: got %q", got)
	}
}
