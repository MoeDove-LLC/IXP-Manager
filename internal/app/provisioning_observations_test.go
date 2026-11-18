package app

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func provisionObservationRevision(t *testing.T, store *Store, id string) ProvisionRevision {
	t.Helper()
	for _, revision := range store.Snapshot().ProvisionRevisions {
		if revision.ID == id {
			return revision
		}
	}
	t.Fatal("provision revision not found")
	return ProvisionRevision{}
}

func provisionObservationStoreVersion(store *Store) uint64 {
	store.mu.RLock()
	defer store.mu.RUnlock()
	return store.revision
}

func TestProvisionResponseObservationsAuthorizedAndDurable(t *testing.T) {
	c, dir, _ := credentialFixture(t)
	nagios := createMonitorProfile(c, "Nagios", "nagios")
	smokeping := createMonitorProfile(c, "Smokeping", "smokeping")
	firstDraft := c.ok(http.MethodPost, "/api/provisioning", Record{"profileId": nagios["id"]})
	secondDraft := c.ok(http.MethodPost, "/api/provisioning", Record{"profileId": smokeping["id"]})
	firstID, secondID := str(firstDraft, "id"), str(secondDraft, "id")
	if w := c.request(http.MethodGet, "/api/provisioning/"+firstID+"/config", nil); w.Code != http.StatusConflict {
		t.Fatalf("draft was served: HTTP %d", w.Code)
	}
	if d := provisionObservationRevision(t, c.a.store, firstID); d.LastPulledAt != nil || d.LastDownloadedAt != nil {
		t.Fatal("request for an unapproved revision recorded a successful response")
	}
	firstPath, secondPath := approveProvision(c, firstDraft), approveProvision(c, secondDraft)
	raw := str(c.ok(http.MethodPost, "/api/account/tokens", Record{
		"name": "Nagios pull", "currentPassword": "very-long-test-password", "scopes": []string{"provisioning:read", "provisioning:pull"},
		"profileIds": []string{str(nagios, "id")},
	}), "token")
	if w := bearerRequest(c.a, raw, http.MethodGet, secondPath+"/config", nil); w.Code != http.StatusForbidden {
		t.Fatalf("profile binding was bypassed: HTTP %d", w.Code)
	}
	if d := provisionObservationRevision(t, c.a.store, secondID); d.LastPulledAt != nil || d.LastDownloadedAt != nil {
		t.Fatal("foreign-profile request recorded a successful response")
	}
	if w := bearerRequest(c.a, raw, http.MethodGet, firstPath+"/config", nil); w.Code != http.StatusOK || w.Header().Get("X-Config-SHA256") != digest(w.Body.String()) {
		t.Fatalf("approved bound pull failed: HTTP %d", w.Code)
	}
	first := provisionObservationRevision(t, c.a.store, firstID)
	if first.LastPulledAt == nil || first.LastDownloadedAt != nil {
		t.Fatal("bound pull did not record its own observation")
	}
	for _, path := range []string{firstPath, secondPath} {
		if w := c.request(http.MethodGet, path+"/config", nil); w.Code != http.StatusOK || w.Header().Get("X-Config-SHA256") != digest(w.Body.String()) {
			t.Fatalf("staff download failed: HTTP %d", w.Code)
		}
	}
	first = provisionObservationRevision(t, c.a.store, firstID)
	second := provisionObservationRevision(t, c.a.store, secondID)
	if first.LastPulledAt == nil || first.LastDownloadedAt == nil || second.LastPulledAt != nil || second.LastDownloadedAt == nil {
		t.Fatal("staff downloads were not kept separate from API pulls and other revisions")
	}
	if got := c.ok(http.MethodGet, firstPath, nil); got["lastPulledAt"] != nil || got["lastDownloadedAt"] != nil || got["checksum"] != firstDraft["checksum"] {
		t.Fatal("observations changed the provisioning detail or reviewed identifier")
	}
	version := provisionObservationStoreVersion(c.a.store)
	if w := bearerRequest(c.a, raw, http.MethodGet, firstPath+"/config", nil); w.Code != http.StatusOK {
		t.Fatal("throttling blocked a valid download")
	}
	if w := c.request(http.MethodGet, firstPath+"/config", nil); w.Code != http.StatusOK {
		t.Fatal("throttling blocked a valid browser download")
	}
	boundary := first.LastPulledAt.Add(provisionResponseObservationInterval)
	c.a.recordProvisionResponse(first, true, boundary.Add(-time.Nanosecond))
	if got := provisionObservationStoreVersion(c.a.store); got != version {
		t.Fatal("repeat serve wrote durable state before the five-minute interval")
	}
	c.a.recordProvisionResponse(first, true, boundary)
	firstAfter := provisionObservationRevision(t, c.a.store, firstID)
	if firstAfter.LastPulledAt == nil || !firstAfter.LastPulledAt.Equal(boundary) || firstAfter.LastDownloadedAt == nil ||
		!firstAfter.LastDownloadedAt.Equal(*first.LastDownloadedAt) || provisionObservationStoreVersion(c.a.store) != version+1 {
		t.Fatal("interval boundary did not update exactly the selected observation")
	}
	if err := c.a.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	if err := ValidateProvisioningArtifacts(reopened); err != nil {
		t.Fatalf("offline provisioning validation rejected the durable observations: %v", err)
	}
	if got := provisionObservationRevision(t, reopened, firstID); got.LastPulledAt == nil || !got.LastPulledAt.Equal(boundary) ||
		got.LastDownloadedAt == nil || !got.LastDownloadedAt.Equal(*first.LastDownloadedAt) {
		t.Fatal("observation times did not survive durable reopen")
	}
}

type provisionObservationWriter struct {
	*httptest.ResponseRecorder
	write func([]byte) (int, error)
	calls int
}

func (w *provisionObservationWriter) Write(data []byte) (int, error) {
	w.calls++
	return w.write(data)
}

func provisionObservationRequest(c *client, path string) *http.Request {
	request := httptest.NewRequest(http.MethodGet, path, nil)
	request.AddCookie(c.cookie)
	return request
}

func TestProvisionResponseObservationRequiresWholeSuccessfulWrite(t *testing.T) {
	c, dir, _ := credentialFixture(t)
	profile := createMonitorProfile(c, "Monitor", "nagios")
	draft := c.ok(http.MethodPost, "/api/provisioning", Record{"profileId": profile["id"]})
	id := str(draft, "id")
	path := approveProvision(c, draft) + "/config"
	store := c.a.store
	approved := provisionObservationRevision(t, store, id)
	artifactPath := filepath.Join(c.a.encryptedArtifactDir("provisioning"), id+".enc")
	encrypted, err := os.ReadFile(artifactPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(artifactPath, []byte("invalid envelope"), 0600); err != nil {
		t.Fatal(err)
	}
	if w := c.request(http.MethodGet, path, nil); w.Code != http.StatusInternalServerError {
		t.Fatalf("bad envelope was served: HTTP %d", w.Code)
	}
	if err := os.WriteFile(artifactPath, encrypted, 0600); err != nil {
		t.Fatal(err)
	}
	if d := provisionObservationRevision(t, store, id); d.LastDownloadedAt != nil {
		t.Fatal("integrity failure recorded a download")
	}
	for _, tc := range []struct {
		name  string
		short bool
		err   error
	}{
		{name: "short nil error", short: true},
		{name: "full with error", err: io.ErrClosedPipe},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := &provisionObservationWriter{ResponseRecorder: httptest.NewRecorder()}
			w.write = func(data []byte) (int, error) {
				if tc.short {
					data = data[:len(data)-1]
				}
				n, _ := w.ResponseRecorder.Write(data)
				return n, tc.err
			}
			version := provisionObservationStoreVersion(store)
			c.a.ServeHTTP(w, provisionObservationRequest(c, path))
			if w.calls != 1 || provisionObservationStoreVersion(store) != version || provisionObservationRevision(t, store, id).LastDownloadedAt != nil {
				t.Fatal("failed writer emitted a second body or recorded a successful download")
			}
		})
	}

	w := &provisionObservationWriter{ResponseRecorder: httptest.NewRecorder()}
	originalPath := store.path
	w.write = func(data []byte) (int, error) {
		store.mu.Lock()
		store.path = filepath.Join(dir, "missing", "state.json")
		store.mu.Unlock()
		return w.ResponseRecorder.Write(data)
	}
	version := provisionObservationStoreVersion(store)
	c.a.ServeHTTP(w, provisionObservationRequest(c, path))
	store.mu.Lock()
	store.path = originalPath
	store.mu.Unlock()
	if w.Code != http.StatusOK || w.calls != 1 || digest(w.Body.String()) != approved.Checksum || provisionObservationStoreVersion(store) != version ||
		provisionObservationRevision(t, store, id).LastDownloadedAt != nil {
		t.Fatal("failed observation commit changed the completed response or published an observation")
	}

	w = &provisionObservationWriter{ResponseRecorder: httptest.NewRecorder()}
	w.write = func(data []byte) (int, error) {
		if err := store.Update(func(s *State) error {
			s.ProvisionRevisions[0].ArtifactChecksum = digest("replacement envelope")
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		return w.ResponseRecorder.Write(data)
	}
	c.a.ServeHTTP(w, provisionObservationRequest(c, path))
	if w.Code != http.StatusOK || w.calls != 1 || digest(w.Body.String()) != approved.Checksum || provisionObservationRevision(t, store, id).LastDownloadedAt != nil {
		t.Fatal("completed writer attached a receipt to changed durable revision metadata")
	}
}

func TestProvisionResponseObservationConcurrentWritesCoalesce(t *testing.T) {
	c, _ := setupClient(t)
	profile := createMonitorProfile(c, "Monitor", "smokeping")
	draft := c.ok(http.MethodPost, "/api/provisioning", Record{"profileId": profile["id"]})
	path := approveProvision(c, draft) + "/config"
	store := c.a.store
	version := provisionObservationStoreVersion(store)
	entered, release := make(chan struct{}, 2), make(chan struct{})
	responses := make(chan *provisionObservationWriter, 2)
	for range 2 {
		go func() {
			w := &provisionObservationWriter{ResponseRecorder: httptest.NewRecorder()}
			w.write = func(data []byte) (int, error) {
				entered <- struct{}{}
				<-release
				return w.ResponseRecorder.Write(data)
			}
			c.a.ServeHTTP(w, provisionObservationRequest(c, path))
			responses <- w
		}()
	}
	timeout := time.NewTimer(5 * time.Second)
	defer timeout.Stop()
	for range 2 {
		select {
		case <-entered:
		case <-timeout.C:
			close(release)
			t.Fatal("concurrent successful responses did not both enter Write")
		}
	}
	close(release)
	for range 2 {
		select {
		case w := <-responses:
			if w.Code != http.StatusOK || w.calls != 1 || w.Header().Get("X-Config-SHA256") != digest(w.Body.String()) {
				t.Fatal("concurrent authorized download failed")
			}
		case <-timeout.C:
			t.Fatal("concurrent successful responses did not both return")
		}
	}
	if provisionObservationStoreVersion(store) != version+1 || provisionObservationRevision(t, store, str(draft, "id")).LastDownloadedAt == nil {
		t.Fatal("concurrent responses did not create exactly one durable observation")
	}
}

func TestProvisionResponseObservationSkipsOtherApprovedKinds(t *testing.T) {
	f := newCoreAPIFixture(t)
	bundle := f.bundle()
	bundle.SideA.MTU, bundle.SideB.MTU = 9000, 9000
	bundle = f.preview(t, bundle, nil)
	f.c.ok(http.MethodPost, "/api/core/bundles", coreMutation{Bundle: bundle, PortETags: f.tags(t)})
	profile := f.c.ok(http.MethodPost, "/api/resources/provisioning-profiles", Record{
		"name": "Switch plan", "options": Record{"kind": "switch-core", "switchId": f.swA},
	})
	draft := f.c.ok(http.MethodPost, "/api/provisioning", Record{"profileId": profile["id"]})
	path := approveProvision(f.c, draft) + "/config"
	store := f.c.a.store
	version := provisionObservationStoreVersion(store)
	if w := f.c.request(http.MethodGet, path, nil); w.Code != http.StatusOK || w.Header().Get("X-Provisioning-Kind") != "switch-core" {
		t.Fatalf("approved switch plan download failed: HTTP %d", w.Code)
	}
	d := provisionObservationRevision(t, store, str(draft, "id"))
	if provisionObservationStoreVersion(store) != version || d.LastPulledAt != nil || d.LastDownloadedAt != nil {
		t.Fatal("unrelated approved kind was recorded")
	}
}
