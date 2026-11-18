package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func deliveryHealthProfile(id, kind string) Record {
	return Record{"id": id, "name": "Profile " + id, "revision": strings.Repeat("a", 22), "options": Record{"kind": kind, "families": []int{4, 6}}}
}
func deliveryHealthFixture(t *testing.T, kind string) (*App, State, time.Time) {
	t.Helper()
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	a := &App{store: store}
	if err = a.initEncryptedArtifactDir("provisioning"); err != nil {
		t.Fatal(err)
	}
	s := State{Version: 1, Resources: map[string][]Record{provisionProfileKind: {deliveryHealthProfile("profile-a", kind)}, "locations": {{"id": "location-a", "name": "Location"}}, "switches": {{"id": "switch-a", "name": "Monitored switch", "locationId": "location-a", "managementIp": "192.0.2.10", "monitor": true}}}, Settings: Record{}}
	return a, s, time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
}
func deliveryHealthRevision(t *testing.T, a *App, s *State, id, created string) ProvisionRevision {
	t.Helper()
	profile := s.Resources[provisionProfileKind][0]
	options, err := parseProvisionOptions(profile["options"])
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := a.renderProvisioning(s, str(profile, "id"), 0)
	if err != nil {
		t.Fatal(err)
	}
	d := ProvisionRevision{ID: id, ProfileID: str(profile, "id"), ProfileName: str(profile, "name"), ProfileRevision: str(profile, "revision"), Options: options, Kind: artifact.Kind, Filename: artifact.Filename, Status: "approved", Checksum: digest(artifact.Content), RenderChecksum: provisionArtifactDigest(artifact), CreatedAt: created, ApprovedBy: "admin", ValidationLevel: artifact.ValidationLevel, Complete: artifact.Complete, TargetCount: artifact.TargetCount, Notices: artifact.Notices}
	a.store.mu.Lock()
	metadata, err := a.writeEncryptedArtifactLocked("provisioning", id, d.Checksum, artifact.Content)
	a.store.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	d.ArtifactVersion, d.ArtifactSize, d.ArtifactChecksum = metadata.Version, metadata.Size, metadata.Checksum
	if err = validateProvisionRevision(d); err != nil {
		t.Fatal(err)
	}
	return d
}
func deliveryHealthOne(t *testing.T, a *App, s *State, now time.Time) monitoringDeliveryHealthItem {
	t.Helper()
	report := a.monitoringDeliveryReport(s, "", 20, now)
	if len(report.Items) != 1 || report.NextAfter != "" || report.PullStaleAfterSeconds != 86400 || !report.CheckedAt.Equal(now) {
		t.Fatal("unexpected report envelope", report)
	}
	return report.Items[0]
}

func TestMonitoringDeliveryHealthScopePaginationAndMissing(t *testing.T) {
	s := State{Resources: map[string][]Record{provisionProfileKind: {deliveryHealthProfile("z", "smokeping"), deliveryHealthProfile("b", "nagios"), deliveryHealthProfile("a", "nagios"), deliveryHealthProfile("dns", "dns-arpa"), deliveryHealthProfile("core", "switch-core"), deliveryHealthProfile("as112", "as112-dns")}}}
	// Only configured profiles appear; history for a deleted profile is not a
	// new configured target and a newer draft is not an approved version.
	s.ProvisionRevisions = []ProvisionRevision{{ID: "orphan", ProfileID: "deleted", Kind: "nagios", Status: "approved"}, {ID: "draft", ProfileID: "a", Kind: "nagios", Status: "draft"}}
	a := &App{}
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	before, _ := json.Marshal(s)
	first := a.monitoringDeliveryReport(&s, "", 2, now)
	if len(first.Items) != 2 || first.Items[0].ProfileID != "a" || first.Items[1].ProfileID != "b" || first.NextAfter != "b" {
		t.Fatal("wrong stable page", first)
	}
	for _, item := range first.Items {
		if item.ApprovalStatus != "missing" || item.PullStatus != "not-observed" || item.GeneratedAt != nil || item.LastPulledAt != nil || item.LastDownloadedAt != nil || item.TargetCount != nil {
			t.Fatal("missing approval invented metadata", item)
		}
	}
	raw, _ := json.Marshal(first)
	for _, fragment := range []string{`"generatedAt":null`, `"lastPulledAt":null`, `"lastDownloadedAt":null`} {
		if !bytes.Contains(raw, []byte(fragment)) {
			t.Fatal("nullable contract missing", fragment)
		}
	}
	second := a.monitoringDeliveryReport(&s, first.NextAfter, 2, now)
	if len(second.Items) != 1 || second.Items[0].ProfileID != "z" || second.NextAfter != "" {
		t.Fatal("wrong last page", second)
	}
	if gap := a.monitoringDeliveryReport(&s, "c", 2, now); len(gap.Items) != 1 || gap.Items[0].ProfileID != "z" {
		t.Fatal("cursor required an existing row")
	}
	if end := a.monitoringDeliveryReport(&s, "z", 2, now); len(end.Items) != 0 || end.NextAfter != "" {
		t.Fatal("terminal cursor repeated items")
	}
	after, _ := json.Marshal(s)
	if !bytes.Equal(before, after) {
		t.Fatal("pagination reordered source snapshot")
	}
	s.Resources[provisionProfileKind] = nil
	for index := 0; index < 30; index++ {
		s.Resources[provisionProfileKind] = append(s.Resources[provisionProfileKind], deliveryHealthProfile(fmt.Sprintf("profile-%02d", index), "nagios"))
	}
	if report := a.monitoringDeliveryReport(&s, "", 0, now); len(report.Items) != 20 || report.NextAfter != "profile-19" {
		t.Fatal("default page bound lost")
	}
	if report := a.monitoringDeliveryReport(&s, "", 10000, now); len(report.Items) != 25 || report.NextAfter != "profile-24" {
		t.Fatal("maximum page bound lost")
	}
}

func TestMonitoringDeliveryHealthCurrentUsesActualArtifactAndRenderer(t *testing.T) {
	for _, kind := range []string{"nagios", "smokeping"} {
		t.Run(kind, func(t *testing.T) {
			a, s, now := deliveryHealthFixture(t, kind)
			revision := deliveryHealthRevision(t, a, &s, "revision-a", now.Add(-time.Hour).Format(time.RFC3339Nano))
			s.ProvisionRevisions = []ProvisionRevision{revision}
			before, _ := json.Marshal(s)
			stateBefore, _ := os.ReadFile(a.store.path)
			item := deliveryHealthOne(t, a, &s, now)
			if item.Kind != kind || item.ApprovalStatus != "current" || item.RevisionID != revision.ID || item.GeneratedAt == nil || *item.GeneratedAt != revision.CreatedAt || item.TargetCount == nil || *item.TargetCount != 1 || item.PullStatus != "not-observed" {
				t.Fatal("readable deterministic version not recognized", item)
			}
			after, _ := json.Marshal(s)
			stateAfter, _ := os.ReadFile(a.store.path)
			if !bytes.Equal(before, after) || !bytes.Equal(stateBefore, stateAfter) {
				t.Fatal("health query changed snapshot or persisted state")
			}
			raw, _ := json.Marshal(item)
			for _, secret := range []string{revision.Checksum, revision.RenderChecksum, revision.ArtifactChecksum, "define host", "192.0.2.10", "Monitored switch", a.store.path, "checksum", "config", "lease"} {
				if bytes.Contains(raw, []byte(secret)) {
					t.Fatal("report leaked artifact or inventory detail", secret)
				}
			}
		})
	}
}

func TestMonitoringDeliveryHealthNewestApprovedDoesNotFallBack(t *testing.T) {
	a, s, now := deliveryHealthFixture(t, "nagios")
	old := deliveryHealthRevision(t, a, &s, "revision-z-old", "2026-11-18T12:00:00Z")
	pulled := now.Add(-time.Minute)
	old.LastPulledAt = &pulled
	newer := deliveryHealthRevision(t, a, &s, "revision-a", "2026-11-18T07:30:00-05:00")
	tied := deliveryHealthRevision(t, a, &s, "revision-z", "2026-11-18T13:30:00+01:00")
	draft := tied
	draft.ID = "draft"
	draft.Status = "draft"
	draft.CreatedAt = "2027-01-01T00:00:00Z"
	s.ProvisionRevisions = []ProvisionRevision{old, newer, draft, tied}
	item := deliveryHealthOne(t, a, &s, now)
	if item.RevisionID != tied.ID || item.ApprovalStatus != "current" || item.PullStatus != "not-observed" || item.LastPulledAt != nil {
		t.Fatal("head used lexical time, insertion order, draft, or another version's pull", item)
	}
	s.ProvisionRevisions[0], s.ProvisionRevisions[3] = s.ProvisionRevisions[3], s.ProvisionRevisions[0]
	if again := deliveryHealthOne(t, a, &s, now); !reflect.DeepEqual(item, again) {
		t.Fatal("history order changed head")
	}
	s.ProvisionRevisions[0].Pruned = true
	item = deliveryHealthOne(t, a, &s, now)
	if item.RevisionID != tied.ID || item.ApprovalStatus != "unavailable" {
		t.Fatal("pruned latest approval fell back to older readable file")
	}
	s.ProvisionRevisions[0].Pruned = false
	if err := os.Remove(filepath.Join(a.encryptedArtifactDir("provisioning"), tied.ID+".enc")); err != nil {
		t.Fatal(err)
	}
	item = deliveryHealthOne(t, a, &s, now)
	if item.RevisionID != tied.ID || item.ApprovalStatus != "unavailable" {
		t.Fatal("missing latest approval fell back")
	}
}

func TestMonitoringDeliveryHealthDriftAndUnavailableAreDistinct(t *testing.T) {
	for name, tc := range map[string]struct {
		change func(*testing.T, *App, *State)
		want   string
	}{
		"profile-revision": {func(_ *testing.T, _ *App, s *State) {
			s.Resources[provisionProfileKind][0]["revision"] = strings.Repeat("b", 22)
		}, "stale"},
		"inventory":         {func(_ *testing.T, _ *App, s *State) { s.Resources["switches"][0]["managementIp"] = "192.0.2.11" }, "stale"},
		"invalid-inventory": {func(_ *testing.T, _ *App, s *State) { s.Resources["switches"][0]["managementIp"] = "SECRET-RAW-ERROR" }, "stale"},
		"invalid-options": {func(_ *testing.T, _ *App, s *State) {
			s.Resources[provisionProfileKind][0]["options"] = Record{"kind": "nagios", "families": []int{7}}
		}, "stale"},
		"kind-changed": {func(_ *testing.T, _ *App, s *State) {
			s.Resources[provisionProfileKind][0]["options"] = Record{"kind": "smokeping"}
		}, "missing"},
		"corrupted-file": {func(t *testing.T, a *App, s *State) {
			if err := os.WriteFile(filepath.Join(a.encryptedArtifactDir("provisioning"), s.ProvisionRevisions[0].ID+".enc"), []byte("SECRET-RAW-ERROR"), 0600); err != nil {
				t.Fatal(err)
			}
		}, "unavailable"},
		"symlink-file": {func(t *testing.T, a *App, s *State) {
			path := filepath.Join(a.encryptedArtifactDir("provisioning"), s.ProvisionRevisions[0].ID+".enc")
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(a.store.path, path); err != nil {
				t.Fatal(err)
			}
		}, "unavailable"},
		"drift-plus-pruned": {func(_ *testing.T, _ *App, s *State) {
			s.Resources["switches"][0]["managementIp"] = "192.0.2.11"
			s.ProvisionRevisions[0].Pruned = true
		}, "unavailable"},
		"damaged-metadata": {func(_ *testing.T, _ *App, s *State) { s.ProvisionRevisions[0].CreatedAt = "SECRET-RAW-ERROR" }, "unavailable"},
	} {
		t.Run(name, func(t *testing.T) {
			a, s, now := deliveryHealthFixture(t, "nagios")
			s.ProvisionRevisions = []ProvisionRevision{deliveryHealthRevision(t, a, &s, "revision", now.Format(time.RFC3339Nano))}
			tc.change(t, a, &s)
			item := deliveryHealthOne(t, a, &s, now)
			if item.ApprovalStatus != tc.want {
				t.Fatalf("status %s want %s", item.ApprovalStatus, tc.want)
			}
			raw, _ := json.Marshal(item)
			if bytes.Contains(raw, []byte("SECRET-RAW-ERROR")) {
				t.Fatal("raw diagnostic leaked")
			}
		})
	}
}

func TestMonitoringDeliveryHealthPullWindowAndBrowserDownload(t *testing.T) {
	a, s, now := deliveryHealthFixture(t, "smokeping")
	revision := deliveryHealthRevision(t, a, &s, "revision", now.Add(-48*time.Hour).Format(time.RFC3339Nano))
	s.ProvisionRevisions = []ProvisionRevision{revision}
	for _, tc := range []struct {
		name  string
		delta time.Duration
		set   bool
		want  string
	}{
		{"missing", 0, false, "not-observed"}, {"recent", time.Hour, true, "recent"}, {"exact-boundary", 24 * time.Hour, true, "recent"}, {"strictly-stale", 24*time.Hour + time.Nanosecond, true, "stale"}, {"just-recent", 24*time.Hour - time.Nanosecond, true, "recent"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s.ProvisionRevisions[0].LastPulledAt = nil
			if tc.set {
				stamp := now.Add(-tc.delta)
				s.ProvisionRevisions[0].LastPulledAt = &stamp
			}
			download := now
			s.ProvisionRevisions[0].LastDownloadedAt = &download
			item := deliveryHealthOne(t, a, &s, now)
			if item.PullStatus != tc.want || item.LastDownloadedAt == nil || !item.LastDownloadedAt.Equal(now) {
				t.Fatal("browser download or boundary changed pull observation", item)
			}
			if item.LastPulledAt != nil {
				*item.LastPulledAt = time.Time{}
				if s.ProvisionRevisions[0].LastPulledAt.IsZero() {
					t.Fatal("report aliased immutable timestamp")
				}
			}
		})
	}
	zero := time.Time{}
	s.ProvisionRevisions[0].LastPulledAt = &zero
	s.ProvisionRevisions[0].LastDownloadedAt = &zero
	item := deliveryHealthOne(t, a, &s, now)
	if item.PullStatus != "not-observed" || item.LastPulledAt != nil || item.LastDownloadedAt != nil {
		t.Fatal("zero value treated as observation")
	}
}

func TestMonitoringDeliveryHealthKindChangeDoesNotReuseObservations(t *testing.T) {
	a, s, now := deliveryHealthFixture(t, "nagios")
	old := deliveryHealthRevision(t, a, &s, "nagios-revision", now.Add(-time.Hour).Format(time.RFC3339Nano))
	old.LastPulledAt, old.LastDownloadedAt = &now, &now
	s.ProvisionRevisions = []ProvisionRevision{old}
	s.Resources[provisionProfileKind][0]["options"] = Record{"kind": "smokeping"}
	item := deliveryHealthOne(t, a, &s, now)
	if item.Kind != "smokeping" || item.ApprovalStatus != "missing" || item.RevisionID != "" || item.PullStatus != "not-observed" || item.LastPulledAt != nil || item.LastDownloadedAt != nil {
		t.Fatal("new monitoring kind inherited another kind's approval or delivery")
	}
	smoke := deliveryHealthRevision(t, a, &s, "smoke-revision", now.Add(-2*time.Hour).Format(time.RFC3339Nano))
	s.ProvisionRevisions = append(s.ProvisionRevisions, smoke)
	item = deliveryHealthOne(t, a, &s, now)
	if item.ApprovalStatus != "current" || item.RevisionID != smoke.ID || item.PullStatus != "not-observed" {
		t.Fatal("newer approval for different kind displaced current-kind head")
	}
}

func TestMonitoringDeliveryHealthDoesNotReadOffPageArtifacts(t *testing.T) {
	s := State{Resources: map[string][]Record{provisionProfileKind: {deliveryHealthProfile("a", "nagios"), deliveryHealthProfile("b", "smokeping")}}, ProvisionRevisions: []ProvisionRevision{{ID: "off-page", ProfileID: "b", Kind: "smokeping", Status: "approved", CreatedAt: "2026-11-18T12:00:00Z"}}}
	// There is intentionally no store: attempting an off-page artifact read
	// would panic instead of producing this bounded metadata-only first page.
	a := &App{}
	report := a.monitoringDeliveryReport(&s, "", 1, time.Now())
	if len(report.Items) != 1 || report.Items[0].ProfileID != "a" || report.NextAfter != "a" {
		t.Fatal("off-page selection changed result")
	}
}

func TestMonitoringDeliveryHealthCurrentEmptyTargetCountIsExplicit(t *testing.T) {
	a, s, now := deliveryHealthFixture(t, "nagios")
	s.Resources["switches"] = nil
	s.ProvisionRevisions = []ProvisionRevision{deliveryHealthRevision(t, a, &s, "empty-revision", now.Format(time.RFC3339Nano))}
	item := deliveryHealthOne(t, a, &s, now)
	raw, _ := json.Marshal(item)
	if item.ApprovalStatus != "current" || item.TargetCount == nil || *item.TargetCount != 0 || !bytes.Contains(raw, []byte(`"targetCount":0`)) {
		t.Fatal("zero target set omitted or presented as unavailable")
	}
}
