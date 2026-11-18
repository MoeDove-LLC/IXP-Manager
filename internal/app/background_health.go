package app

import (
	"context"
	"time"
)

const schedulerStallAfter = 10 * time.Minute

type schedulerPhase string

const (
	schedulerJobsPhase         schedulerPhase = "jobs"
	schedulerMailPhase         schedulerPhase = "mail"
	schedulerAtlasPhase        schedulerPhase = "atlas"
	schedulerLookingGlassPhase schedulerPhase = "looking-glass"
)

// schedulerHealthState is process-local and guarded by backgroundMu.
type schedulerHealthState struct {
	startedAt       time.Time
	lastCompletedAt time.Time
	cycleStartedAt  time.Time
	stoppedAt       time.Time
	currentPhase    schedulerPhase
	failedPhases    []schedulerPhase
	canceled        <-chan struct{}
}

type schedulerHealthView struct {
	Status            string           `json:"status"`
	StartedAt         *time.Time       `json:"startedAt"`
	LastCompletedAt   *time.Time       `json:"lastCompletedAt"`
	CycleStartedAt    *time.Time       `json:"cycleStartedAt"`
	StoppedAt         *time.Time       `json:"stoppedAt"`
	CurrentPhase      schedulerPhase   `json:"currentPhase"`
	FailedPhases      []schedulerPhase `json:"failedPhases"`
	StallAfterSeconds int              `json:"stallAfterSeconds"`
}

func (a *App) runBackgroundCycle(ctx context.Context, startedAt time.Time) {
	a.backgroundMu.Lock()
	a.backgroundHealth.cycleStartedAt = startedAt
	a.backgroundHealth.currentPhase = schedulerJobsPhase
	a.backgroundMu.Unlock()

	now := startedAt.UTC()
	var failed []schedulerPhase
	if a.runDueJob(now) != nil {
		failed = append(failed, schedulerJobsPhase)
	}
	a.setSchedulerPhase(schedulerMailPhase)
	if a.deliverOne(ctx, now) != nil {
		failed = append(failed, schedulerMailPhase)
	}
	a.setSchedulerPhase(schedulerAtlasPhase)
	if a.atlasTick(ctx, now) != nil {
		failed = append(failed, schedulerAtlasPhase)
	}
	a.setSchedulerPhase(schedulerLookingGlassPhase)
	if a.sweepLookingGlass(now) != nil {
		failed = append(failed, schedulerLookingGlassPhase)
	}

	a.backgroundMu.Lock()
	a.backgroundHealth.lastCompletedAt = time.Now()
	a.backgroundHealth.cycleStartedAt = time.Time{}
	a.backgroundHealth.currentPhase = ""
	a.backgroundHealth.failedPhases = failed
	a.backgroundMu.Unlock()
}

func (a *App) setSchedulerPhase(phase schedulerPhase) {
	a.backgroundMu.Lock()
	a.backgroundHealth.currentPhase = phase
	a.backgroundMu.Unlock()
}

func schedulerTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	utc := t.UTC()
	return &utc
}

func (a *App) schedulerHealth(now time.Time) schedulerHealthView {
	a.backgroundMu.Lock()
	state := a.backgroundHealth
	a.backgroundMu.Unlock()
	failed := append([]schedulerPhase{}, state.failedPhases...)
	return schedulerHealthView{
		Status: state.status(now), StartedAt: schedulerTime(state.startedAt), LastCompletedAt: schedulerTime(state.lastCompletedAt),
		CycleStartedAt: schedulerTime(state.cycleStartedAt), StoppedAt: schedulerTime(state.stoppedAt), CurrentPhase: state.currentPhase,
		FailedPhases: failed, StallAfterSeconds: int(schedulerStallAfter / time.Second),
	}
}

func (state schedulerHealthState) status(now time.Time) string {
	if state.startedAt.IsZero() {
		return "not-started"
	}
	if !state.stoppedAt.IsZero() {
		return "stopped"
	}
	lastProgress := state.lastCompletedAt
	if lastProgress.IsZero() {
		lastProgress = state.startedAt
	}
	if now.Sub(lastProgress) > schedulerStallAfter {
		return "stalled"
	}
	select {
	case <-state.canceled:
		return "stopping"
	default:
	}
	if state.lastCompletedAt.IsZero() {
		return "starting"
	}
	if len(state.failedPhases) != 0 {
		return "degraded"
	}
	return "running"
}
