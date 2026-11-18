package app

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestFixedJobHealthSeparatesDueTimeAndLease(t *testing.T) {
	now := time.Date(2026, time.November, 18, 8, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name string
		job  Job
		want string
	}{
		{"future", Job{Status: "idle", NextRunAt: now.Add(time.Second)}, "scheduled"},
		{"equal", Job{Status: "idle", NextRunAt: now}, "due"},
		{"due-boundary", Job{Status: "idle", NextRunAt: now.Add(-schedulerStallAfter)}, "due"},
		{"overdue", Job{Status: "idle", NextRunAt: now.Add(-schedulerStallAfter - time.Nanosecond)}, "overdue"},
		{"live-lease", Job{Status: "running", NextRunAt: now.Add(-time.Hour), LeaseUntil: now.Add(time.Nanosecond)}, "running"},
		{"expired-lease", Job{Status: "running", LeaseUntil: now}, "lease-expired"},
		{"missing-lease", Job{Status: "running"}, "lease-expired"},
		{"unknown-status", Job{Status: "unknown-old-state", NextRunAt: now.Add(-time.Hour)}, "unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := jobHealth(tc.job, now); got != tc.want {
				t.Errorf("wanted %s, got %s", tc.want, got)
			}
		})
	}
}

func TestJobsAPIShowsAdminSchedulerAndReadOnlyLeaseHealth(t *testing.T) {
	admin, _ := setupClient(t)
	now := time.Now().UTC()
	if err := admin.a.store.Update(func(s *State) error {
		s.Jobs = []Job{
			{Kind: "cleanup", Status: "running", Lease: "private-lease-token", LeaseUntil: now.Add(-time.Second), NextRunAt: now.Add(-time.Hour), LastError: "private-job-output"},
			{Kind: "token-expiry", Status: "idle", NextRunAt: now.Add(-time.Hour), LastRunAt: now.Add(-2 * time.Hour)},
			{Kind: "private-background-experiment", Status: "running", LeaseUntil: now},
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	baseline := admin.a.store.Snapshot().Jobs
	reply := admin.request(http.MethodGet, "/api/jobs", nil)
	if reply.Code != http.StatusOK || reply.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("unexpected health response: %d", reply.Code)
	}
	for _, sensitive := range []string{"private-lease-token", "private-job-output", "private-background-experiment"} {
		if strings.Contains(reply.Body.String(), sensitive) {
			t.Fatal("scheduler response exposed private data")
		}
	}
	var observation struct {
		Scheduler schedulerHealthView `json:"scheduler"`
		CheckedAt time.Time           `json:"checkedAt"`
		Items     []struct {
			Kind       string     `json:"kind"`
			Status     string     `json:"status"`
			Health     string     `json:"health"`
			LeaseUntil *time.Time `json:"leaseUntil"`
			LastRunAt  *time.Time `json:"lastRunAt"`
		} `json:"items"`
	}
	if err := json.Unmarshal(reply.Body.Bytes(), &observation); err != nil {
		t.Fatal(err)
	}
	if observation.Scheduler.Status != "not-started" || observation.CheckedAt.Before(now) || len(observation.Items) != 2 {
		t.Fatalf("unexpected scheduler observation: %+v", observation)
	}
	if item := observation.Items[0]; item.Kind != "cleanup" || item.Status != "running" || item.Health != "lease-expired" || item.LeaseUntil == nil {
		t.Fatalf("wrong expired lease projection: %+v", item)
	}
	if item := observation.Items[1]; item.Kind != "token-expiry" || item.Status != "idle" || item.Health != "overdue" || item.LastRunAt == nil || item.LeaseUntil != nil {
		t.Fatalf("wrong overdue job projection: %+v", item)
	}
	if !reflect.DeepEqual(baseline, admin.a.store.Snapshot().Jobs) {
		t.Fatal("health observation changed persisted jobs")
	}
	guest := &client{a: admin.a, t: t}
	if got := guest.request(http.MethodGet, "/api/jobs", nil).Code; got != http.StatusUnauthorized {
		t.Fatal("anonymous scheduler response", got)
	}
	const viewerEmail, viewerPassword = "scheduler-viewer@example.test", "scheduler-viewer-fixture"
	admin.create("users", Record{"name": "Scheduler viewer", "email": viewerEmail, "role": "viewer", "password": viewerPassword})
	viewer := docLoginClient(t, admin.a, viewerEmail, viewerPassword)
	if got := viewer.request(http.MethodGet, "/api/jobs", nil).Code; got != http.StatusForbidden {
		t.Fatal("viewer scheduler response", got)
	}
	issued := admin.ok(http.MethodPost, "/api/account/tokens", Record{"name": "scheduler-fixture", "currentPassword": "very-long-test-password"})
	if got := bearerRequest(admin.a, str(issued, "token"), http.MethodGet, "/api/jobs", nil).Code; got != http.StatusForbidden {
		t.Fatal("bearer scheduler response", got)
	}
}

func TestJobsManualRunReleasesOnlyAnExpiredLease(t *testing.T) {
	admin, _ := setupClient(t)
	now := time.Now().UTC()
	setLease := func(until time.Time) {
		t.Helper()
		if err := admin.a.store.Update(func(s *State) error {
			s.Jobs[0] = Job{Kind: "token-expiry", Status: "running", Lease: "previous-worker", LeaseUntil: until, NextRunAt: now.Add(time.Hour)}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	setLease(now.Add(time.Minute))
	if reply := admin.request(http.MethodPost, "/api/jobs/token-expiry/run", nil); reply.Code != http.StatusConflict {
		t.Fatal("an active job lease became requeueable", reply.Code)
	}
	setLease(now.Add(-time.Second))
	admin.ok(http.MethodPost, "/api/jobs/token-expiry/run", nil)
	job := admin.a.store.Snapshot().Jobs[0]
	if job.Status != "idle" || job.Lease != "" || !job.LeaseUntil.IsZero() || job.NextRunAt.Before(now) || job.NextRunAt.After(time.Now()) {
		t.Fatalf("resubmission did not clear previous lease and make job due: %+v", job)
	}
}

func TestFixedJobDispatchHonorsTheSameEligibilityDuringClaim(t *testing.T) {
	admin, _ := setupClient(t)
	now := time.Now().UTC()
	if err := admin.a.store.Update(func(s *State) error {
		s.Jobs = []Job{
			{Kind: "token-expiry", Status: "unknown-old-state", NextRunAt: now.Add(-time.Hour)},
			{Kind: "cleanup", Status: "idle", NextRunAt: now},
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := admin.a.runDueJob(now); err != nil {
		t.Fatal(err)
	}
	jobs := admin.a.store.Snapshot().Jobs
	if jobs[0].Status != "unknown-old-state" || !jobs[0].LastRunAt.IsZero() || !jobs[1].LastRunAt.Equal(now) {
		t.Fatalf("worker should skip invalid state and execute the eligible job: %+v", jobs)
	}
}
