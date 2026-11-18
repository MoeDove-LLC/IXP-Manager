package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// Unlike the generic domain-fixture client, this sends exactly the review token
// the caller captured. Missing/stale tags are deliberately never refreshed.
func requestResourceRevision(c *client, method, path string, body any, match string) *httptest.ResponseRecorder {
	var buffer bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buffer).Encode(body); err != nil {
			c.t.Fatal(err)
		}
	}
	r := httptest.NewRequest(method, path, &buffer)
	r.RemoteAddr = "127.0.0.1:10000"
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	if c.cookie != nil {
		r.AddCookie(c.cookie)
	}
	r.Header.Set("X-CSRF-Token", c.csrf)
	if match != "" {
		r.Header.Set("If-Match", match)
	}
	w := httptest.NewRecorder()
	c.a.ServeHTTP(w, r)
	return w
}

func TestResourceRevisionRequiresAnExactReviewedVersion(t *testing.T) {
	c, _ := setupClient(t)
	created := c.create("locations", Record{"name": "Original", "city": "Shanghai"})
	path := "/api/resources/locations/" + str(created, "id")
	read := requestResourceRevision(c, "GET", path, nil, "")
	if read.Code != 200 || read.Header().Get("ETag") != fmt.Sprintf("%q", str(created, "_etag")) || !validResourceETag(str(created, "_etag")) {
		t.Fatal("read did not expose its exact strong revision")
	}
	if w := requestResourceRevision(c, "PUT", path, Record{"name": "Unreviewed"}, ""); w.Code != 428 {
		t.Fatal("missing version was accepted", w.Code)
	}
	for _, tag := range []string{"*", `W/"` + str(created, "_etag") + `"`, str(created, "_etag"), `"` + strings.Repeat("a", 64) + `","` + str(created, "_etag") + `"`} {
		if w := requestResourceRevision(c, "PUT", path, Record{"name": "Malformed"}, tag); w.Code != 400 {
			t.Fatal("non-exact precondition accepted", w.Code)
		}
	}
	first := requestResourceRevision(c, "PUT", path, Record{"name": "New visible value", "city": "Shanghai"}, read.Header().Get("ETag"))
	if first.Code != 200 || first.Header().Get("ETag") == read.Header().Get("ETag") {
		t.Fatal("valid update failed to advance version", first.Code, first.Body.String())
	}
	for _, method := range []string{"PUT", "DELETE"} {
		w := requestResourceRevision(c, method, path, Record{"name": "Overwrite"}, read.Header().Get("ETag"))
		if w.Code != 412 || strings.Contains(w.Body.String(), "New visible value") {
			t.Fatal("stale mutation accepted or conflict response leaked current data", method, w.Code)
		}
	}
	var current Record
	if err := json.Unmarshal(first.Body.Bytes(), &current); err != nil {
		t.Fatal(err)
	}
	echo := Record{"name": "Body precondition", "_etag": current["_etag"]}
	w := requestResourceRevision(c, "PUT", path, echo, "")
	if w.Code != 200 {
		t.Fatal("explicit body precondition failed", w.Code, w.Body.String())
	}
	state := c.a.store.Snapshot()
	if find(&state, "locations", str(created, "id"))["_etag"] != nil {
		t.Fatal("transport revision was persisted as resource data")
	}
	exported := c.request("GET", "/api/export", nil)
	if strings.Contains(exported.Body.String(), `"_etag"`) {
		t.Fatal("transport revisions contaminated data exports")
	}
	wrong := c.create("locations", Record{"name": "Other"})
	if w := requestResourceRevision(c, "DELETE", path, nil, fmt.Sprintf("%q", str(wrong, "_etag"))); w.Code != 412 {
		t.Fatal("revision from another record accepted")
	}
}

func TestConcurrentResourceWritersHaveExactlyOneWinner(t *testing.T) {
	c, _ := setupClient(t)
	row := c.create("locations", Record{"name": "Base", "city": "Original"})
	path := "/api/resources/locations/" + str(row, "id")
	tag := fmt.Sprintf("%q", str(row, "_etag"))
	start := make(chan struct{})
	results := make(chan int, 2)
	var wg sync.WaitGroup
	for _, name := range []string{"First", "Second"} {
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			<-start
			results <- requestResourceRevision(c, "PUT", path, Record{"name": name, "city": "Original"}, tag).Code
		}(name)
	}
	close(start)
	wg.Wait()
	close(results)
	counts := map[int]int{}
	for code := range results {
		counts[code]++
	}
	if counts[200] != 1 || counts[412] != 1 {
		t.Fatal("concurrent changes were not serialized by reviewed version", counts)
	}
	current := c.ok("GET", path, nil)
	if current["name"] != "First" && current["name"] != "Second" {
		t.Fatal("winning mutation was lost")
	}
	// Explicit reconciliation with the newly reviewed record succeeds.
	if w := requestResourceRevision(c, "PUT", path, Record{"name": current["name"], "city": "Reviewed merge"}, fmt.Sprintf("%q", str(current, "_etag"))); w.Code != 200 {
		t.Fatal("reviewed retry failed", w.Code)
	}
}

func TestResourceVersionIncludesHiddenStateWithoutRevealingIt(t *testing.T) {
	c, _ := setupClient(t)
	user := c.create("users", Record{"name": "Viewer", "email": "etag-viewer@example.net", "role": "viewer", "password": "very-private-test-password"})
	old := str(user, "_etag")
	id := str(user, "id")
	snapshot := c.a.store.Snapshot()
	private := str(find(&snapshot, "users", id), "passwordHash")
	response := c.request("GET", "/api/resources/users/"+id, nil)
	if strings.Contains(response.Body.String(), private) || strings.Contains(response.Body.String(), "very-private-test-password") || strings.Contains(response.Body.String(), "passwordHash") {
		t.Fatal("private account fields escaped in a revision response")
	}
	if err := c.a.store.Update(func(s *State) error {
		find(s, "users", id)["passwordHash"] = "private-fixture-hash-changed"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	latest := c.ok("GET", "/api/resources/users/"+id, nil)
	if latest["_etag"] == old {
		t.Fatal("hidden account data change was not part of the version")
	}
	record := Record{"id": "same-id", "name": "same fields"}
	if c.a.resourceETag("locations", record) == c.a.resourceETag("infrastructures", record) {
		t.Fatal("resource versions were not domain-separated")
	}
}

func TestResourceWriterRechecksCapturedAuthority(t *testing.T) {
	c, _ := setupClient(t)
	r := httptest.NewRequest(http.MethodGet, "/api/session", nil)
	r.AddCookie(c.cookie)
	user, session, ok := c.a.authenticate(r)
	if !ok {
		t.Fatal("fixture session unavailable")
	}
	state := c.a.store.Snapshot()
	if status, err := resourceWriter(&state, user, session, "locations"); err != nil || status != 0 {
		t.Fatal("valid authority denied", status, err)
	}
	find(&state, "users", session.UserID)["role"] = "viewer"
	if status, err := resourceWriter(&state, user, session, "locations"); err == nil || status != 403 {
		t.Fatal("earlier role used after authorization changed")
	}
	state = c.a.store.Snapshot()
	security := state.Security[session.UserID]
	security.Generation++
	state.Security[session.UserID] = security
	if status, err := resourceWriter(&state, user, session, "locations"); err == nil || status != 401 {
		t.Fatal("security-generation invalidation ignored")
	}
	state = c.a.store.Snapshot()
	session.API = true
	session.TokenID = "revoked-token"
	session.Expires = time.Time{}
	if status, err := resourceWriter(&state, user, session, "locations"); err == nil || status != 401 {
		t.Fatal("revoked token finished a captured write")
	}
}
