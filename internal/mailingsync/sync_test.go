package mailingsync

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
)

type fixture struct {
	mu            sync.Mutex
	t             *testing.T
	server        *httptest.Server
	c             Config
	export        Export
	members       map[string]Member
	writes        int
	gets          int
	lost          bool
	beforeLoss    bool
	readFailure   bool
	failReadback  bool
	sourceStatus  int
	targetStatus  int
	sourceDrift   bool
	version       string
	wrongList     bool
	createdAt     string
	redirect      string
	notifications int
	adminNotify   bool
	adminSetting  any
	adminDrift    bool
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{t: t, members: map[string]Member{}, version: "GNU Mailman 3.3.10 (Tom Sawyer)", createdAt: "2026-11-14T00:00:00"}
	f.export = Export{Version: 1, List: List{"ml_1", "members", "members@lists.example.test", 1, "2026-11-15T00:00:00Z"}, Subscribed: []string{"add@example.test", "stay@example.test"}, Unsubscribed: []string{"remove@example.test"}, GeneratedAt: "2026-11-15T00:00:00Z"}
	f.export.Checksum = f.export.checksum()
	f.members["stay@example.test"] = Member{fmt.Sprintf("%032x", 1), "members.lists.example.test", "stay@example.test", "member", "as_address"}
	f.members["remove@example.test"] = Member{fmt.Sprintf("%032x", 2), "members.lists.example.test", "remove@example.test", "member", "as_address"}
	f.members["foreign@example.test"] = Member{fmt.Sprintf("%032x", 3), "members.lists.example.test", "foreign@example.test", "member", "as_address"}
	f.server = httptest.NewTLSServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.server.Close)
	dir := t.TempDir()
	write := func(name string, b []byte) string {
		p := filepath.Join(dir, name)
		if e := os.WriteFile(p, b, 0600); e != nil {
			t.Fatal(e)
		}
		return p
	}
	ca := write("ca.pem", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: f.server.Certificate().Raw}))
	f.c = Config{Version: 1, Source: Source{URL: f.server.URL, TokenFile: write("token", []byte("source-dummy")), CAFile: ca, ListID: f.export.List.ID, ListKey: f.export.List.Key, ListEmail: f.export.List.Email, ListCreatedAt: f.export.List.CreatedAt}, Target: Target{URL: f.server.URL + "/3.1", Username: "restadmin", PasswordFile: write("password", []byte("target-dummy")), CAFile: ca, ListID: "members.lists.example.test", ListEmail: f.export.List.Email}}
	return f
}
func (f *fixture) handle(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gets++
	reply := func(v any) { w.Header().Set("Content-Type", "application/json"); json.NewEncoder(w).Encode(v) }
	if strings.HasPrefix(r.URL.Path, "/api/") {
		if r.Method != "GET" || r.Header.Get("Authorization") != "Bearer source-dummy" || r.URL.Path != "/api/mailing-lists/ml_1/export" {
			f.t.Error("invalid source request")
			w.WriteHeader(403)
			return
		}
		if f.sourceStatus != 0 {
			w.WriteHeader(f.sourceStatus)
			return
		}
		f.export.Checksum = f.export.checksum()
		reply(f.export)
		return
	}
	u, p, ok := r.BasicAuth()
	if !ok || u != "restadmin" || p != "target-dummy" {
		f.t.Error("target auth missing or source credential leaked")
		w.WriteHeader(403)
		return
	}
	if f.redirect != "" {
		http.Redirect(w, r, f.redirect, 307)
		return
	}
	if f.targetStatus != 0 {
		w.WriteHeader(f.targetStatus)
		return
	}
	switch {
	case r.Method == "GET" && r.URL.Path == "/3.1/lists/members.lists.example.test/config/admin_notify_mchanges":
		if f.adminSetting != nil {
			reply(f.adminSetting)
		} else {
			reply(map[string]bool{"admin_notify_mchanges": f.adminNotify})
		}
	case r.Method == "GET" && r.URL.Path == "/3.1/lists/members.lists.example.test/config/created_at":
		reply(map[string]any{"created_at": f.createdAt})
	case r.Method == "GET" && r.URL.Path == "/3.1/system/versions":
		reply(map[string]any{"mailman_version": f.version, "api_version": "3.1"})
	case r.Method == "GET" && r.URL.Path == "/3.1/lists/members.lists.example.test":
		id := f.c.Target.ListID
		if f.wrongList {
			id = "other.example.test"
		}
		reply(map[string]any{"list_id": id, "fqdn_listname": f.c.Target.ListEmail})
	case r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/3.1/lists/members.lists.example.test/member/"):
		if f.readFailure {
			w.WriteHeader(503)
			return
		}
		address := strings.TrimPrefix(r.URL.Path, "/3.1/lists/members.lists.example.test/member/")
		m, ok := f.members[address]
		if !ok {
			w.WriteHeader(404)
			return
		}
		reply(m)
	case r.Method == "POST" && r.URL.Path == "/3.1/members":
		var b map[string]any
		json.NewDecoder(r.Body).Decode(&b)
		if _, unsupported := b["admin_notify_mchanges"]; unsupported {
			f.t.Error("stock Mailman rejects per-request administrator-notice override")
			w.WriteHeader(400)
			return
		}
		if len(b) != 8 || b["list_id"] != f.c.Target.ListID || b["role"] != "member" || b["pre_verified"] != true || b["pre_confirmed"] != true || b["pre_approved"] != true || b["send_welcome_message"] != false || b["invitation"] != false || f.adminNotify {
			f.notifications++
			f.t.Error("notification suppression contract missing")
		}
		f.writes++
		if !f.beforeLoss {
			address, _ := b["subscriber"].(string)
			f.members[address] = Member{fmt.Sprintf("%032x", 100+f.writes), f.c.Target.ListID, address, "member", "as_address"}
		}
		if f.sourceDrift {
			f.export.List.Revision++
		}
		if f.adminDrift {
			f.adminNotify = true
		}
		if f.failReadback {
			f.readFailure = true
		}
		if f.lost {
			conn, _, _ := w.(http.Hijacker).Hijack()
			conn.Close()
			return
		}
		w.WriteHeader(201)
	case r.Method == "DELETE" && r.URL.Path == "/3.1/lists/members.lists.example.test/roster/member":
		var b map[string][]string
		json.NewDecoder(r.Body).Decode(&b)
		if len(b) != 1 || len(b["emails"]) != 1 || !slices.Contains(f.export.Unsubscribed, b["emails"][0]) {
			f.t.Error("delete exceeded exact explicit intention")
		}
		f.writes++
		delete(f.members, b["emails"][0])
		reply(map[string]bool{b["emails"][0]: true})
	default:
		f.t.Error("unexpected Mailman request", r.Method, r.URL.Path)
		w.WriteHeader(404)
	}
}

func TestMailmanSubscriptionAdminNoticePrerequisite(t *testing.T) {
	for _, kind := range []string{"enabled", "missing", "null", "string", "mid-apply", "no-new-subscriptions"} {
		t.Run(kind, func(t *testing.T) {
			f := newFixture(t)
			switch kind {
			case "enabled":
				f.adminNotify = true
			case "missing":
				f.adminSetting = map[string]any{}
			case "null":
				f.adminSetting = map[string]any{"admin_notify_mchanges": nil}
			case "string":
				f.adminSetting = map[string]any{"admin_notify_mchanges": "false"}
			case "mid-apply":
				f.export.Subscribed = []string{"add@example.test", "new@example.test"}
				f.adminDrift = true
			case "no-new-subscriptions":
				f.export.Subscribed = []string{"stay@example.test"}
				f.adminNotify = true
				// Even an unreadable setting must not block these safe actions.
				f.adminSetting = map[string]any{}
			}
			c := f.client()
			p := f.preview(c)
			s, e := c.Apply(context.Background(), p, filepath.Join(t.TempDir(), "journal"), p.Checksum, true)
			if kind == "no-new-subscriptions" {
				if e != nil || !s.RecordedComplete || f.writes != 1 {
					t.Fatal("safe unchanged/unsubscribe actions blocked", s, e)
				}
				return
			}
			if e == nil || !strings.Contains(e.Error(), "admin_notify_mchanges=false") || s.Unknown != 0 || f.notifications != 0 {
				t.Fatal("subscription prerequisite failed", s, e)
			}
			wanted := 0
			if kind == "mid-apply" {
				wanted = 1
			}
			if f.writes != wanted {
				t.Fatal("notification setting did not gate every subscription", f.writes)
			}
		})
	}
}
func (f *fixture) client() *Client {
	c, e := NewClient(f.c)
	if e != nil {
		f.t.Fatal(e)
	}
	return c
}
func (f *fixture) preview(c *Client) Plan {
	p, e := c.Preview(context.Background())
	if e != nil {
		f.t.Fatal(e)
	}
	return p
}

func TestMailmanPreviewApplyAndReplay(t *testing.T) {
	f := newFixture(t)
	c := f.client()
	p := f.preview(c)
	if f.writes != 0 || p.Summary(nil).Add != 1 || p.Summary(nil).Remove != 1 || p.Summary(nil).Unchanged != 1 {
		t.Fatal("preview mutated or miscounted")
	}
	j := filepath.Join(t.TempDir(), "journal")
	s, e := c.Apply(context.Background(), p, j, p.Checksum, true)
	if e != nil || !s.RecordedComplete || s.Completed != 3 || f.writes != 2 || f.notifications != 0 {
		t.Fatal("apply contract", s, e)
	}
	if _, ok := f.members["foreign@example.test"]; !ok {
		t.Fatal("unmanaged external member removed")
	}
	if _, e = c.Apply(context.Background(), p, j, p.Checksum, true); e != nil || f.writes != 2 {
		t.Fatal("completed operation replayed", e)
	}
	b, _ := Encode(p)
	if strings.Contains(string(b), "source-dummy") || strings.Contains(string(b), "target-dummy") {
		t.Fatal("plan leaked credentials")
	}
}

func TestMailmanSourceAndTargetDrift(t *testing.T) {
	for _, kind := range []string{"source", "member", "list", "list-recreated", "version", "address-mode", "mid-apply"} {
		t.Run(kind, func(t *testing.T) {
			f := newFixture(t)
			c := f.client()
			p := f.preview(c)
			switch kind {
			case "source":
				f.export.List.Revision++
			case "member":
				f.members["add@example.test"] = Member{fmt.Sprintf("%032x", 8), f.c.Target.ListID, "add@example.test", "member", "as_address"}
			case "list":
				f.wrongList = true
			case "list-recreated":
				f.createdAt = "2026-11-15T01:00:00"
			case "version":
				f.version = "GNU Mailman 3.3.11"
			case "address-mode":
				m := f.members["stay@example.test"]
				m.Mode = "as_user"
				f.members[m.Email] = m
			case "mid-apply":
				f.sourceDrift = true
			}
			_, e := c.Apply(context.Background(), p, filepath.Join(t.TempDir(), "journal"), p.Checksum, true)
			if e == nil {
				t.Fatal("drift accepted")
			}
			limit := 0
			if kind == "mid-apply" || kind == "address-mode" {
				limit = 1
			}
			if f.writes > limit {
				t.Fatal("drift allowed subsequent mutation", f.writes)
			}
		})
	}
}

func TestMailmanLostResponseReadbackNeverRetries(t *testing.T) {
	for _, committed := range []bool{true, false} {
		t.Run(fmt.Sprint(committed), func(t *testing.T) {
			f := newFixture(t)
			c := f.client()
			p := f.preview(c)
			f.lost = true
			f.beforeLoss = !committed
			j := filepath.Join(t.TempDir(), "journal")
			s, e := c.Apply(context.Background(), p, j, p.Checksum, true)
			if e == nil || f.writes != 1 {
				t.Fatal("lost response silently continued or replayed", e)
			}
			if committed {
				if s.Completed != 1 || s.Unknown != 0 {
					t.Fatal("committed readback not recorded", s)
				}
			} else {
				if s.Unknown != 1 {
					t.Fatal("unknown write not recorded", s)
				}
				if _, e = c.Apply(context.Background(), p, j, p.Checksum, true); e == nil || f.writes != 1 {
					t.Fatal("uncertain write retried")
				}
				if _, e = c.Reconcile(context.Background(), p, j, p.Checksum); e != nil || f.writes != 1 {
					t.Fatal("reconcile wrote", e)
				}
				if _, e = c.Apply(context.Background(), p, j, p.Checksum, true); e == nil || f.writes != 1 {
					t.Fatal("not-observed state automatically retried")
				}
				// A timed-out server request can commit after our first readback.
				// Another read-only reconcile can observe it without a second POST.
				f.members["add@example.test"] = Member{fmt.Sprintf("%032x", 99), f.c.Target.ListID, "add@example.test", "member", "as_address"}
				if s, e = c.Reconcile(context.Background(), p, j, p.Checksum); e != nil || s.Completed != 1 || s.NotObserved != 0 || f.writes != 1 {
					t.Fatal("late commit was retried or not observed", s, e)
				}
			}
		})
	}
}

func TestMailmanPersistedIntentAndReadFailure(t *testing.T) {
	f := newFixture(t)
	c := f.client()
	p := f.preview(c)
	jpath := filepath.Join(t.TempDir(), "journal")
	j, e := journal(jpath, p, true)
	if e != nil {
		t.Fatal(e)
	}
	j.Results[0].State = "intent"
	if e = saveJournal(jpath, j); e != nil {
		t.Fatal(e)
	}
	if _, e = c.Apply(context.Background(), p, jpath, p.Checksum, true); e == nil || f.writes != 0 {
		t.Fatal("crash intent replayed")
	}
	f.readFailure = true
	if _, e = c.Reconcile(context.Background(), p, jpath, p.Checksum); e == nil || f.writes != 0 {
		t.Fatal("failed readback invented completion")
	}
	j, e = journal(jpath, p, false)
	if e != nil || j.Results[0].State != "intent" {
		t.Fatal("pending intent lost")
	}
}

func TestMailmanUncertainCommitAndSourceRevocation(t *testing.T) {
	f := newFixture(t)
	c := f.client()
	p := f.preview(c)
	f.sourceStatus = 403
	j := filepath.Join(t.TempDir(), "journal")
	if _, e := c.Apply(context.Background(), p, j, p.Checksum, true); e == nil || f.writes != 0 {
		t.Fatal("revoked source authority allowed mutation")
	}
	f.sourceStatus = 0
	f.failReadback = true
	s, e := c.Apply(context.Background(), p, j, p.Checksum, true)
	if e == nil || s.Unknown != 1 || f.writes != 1 {
		t.Fatal("failed readback fabricated success", s, e)
	}
	f.readFailure = false
	if s, e = c.Reconcile(context.Background(), p, j, p.Checksum); e != nil || s.Completed != 1 || s.Unknown != 0 || f.writes != 1 {
		t.Fatal("read-only recovery repeated mutation", s, e)
	}
}

func TestMailmanRecordedSummaryAndCapturedEntity(t *testing.T) {
	f := newFixture(t)
	c := f.client()
	p := f.preview(c)
	if f.c.Target.ListCreatedAt != "" || p.Target.ListCreatedAt != f.createdAt {
		t.Fatal("preview did not capture target creation identity")
	}
	j := filepath.Join(t.TempDir(), "journal")
	s, e := c.Apply(context.Background(), p, j, p.Checksum, true)
	if e != nil || !s.RecordedComplete || s.LastObservedAt == "" || s.NoIntentions {
		t.Fatal("recorded observation missing", s, e)
	}
	f.sourceStatus = 403
	s, e = c.Apply(context.Background(), p, j, p.Checksum, true)
	if e == nil || !s.RecordedComplete || s.LastObservedAt == "" {
		t.Fatal("failed fresh check lost historical observation semantics", s, e)
	}
	raw, _ := Encode(s)
	if strings.Contains(string(raw), "externalApplied") {
		t.Fatal("historical summary claimed live application")
	}
	f.sourceStatus = 0
	f.export.Subscribed = []string{}
	f.export.Unsubscribed = []string{}
	empty := f.preview(c)
	s, e = c.Apply(context.Background(), empty, filepath.Join(t.TempDir(), "empty"), empty.Checksum, true)
	if e != nil || s.RecordedComplete || !s.NoIntentions || s.LastObservedAt != "" {
		t.Fatal("empty plan fabricated completed observation", s, e)
	}
}

func TestMailmanTLSRedirectAndExplicitAcknowledgement(t *testing.T) {
	f := newFixture(t)
	c := f.client()
	p := f.preview(c)
	j := filepath.Join(t.TempDir(), "journal")
	before := f.gets
	if _, e := c.Apply(context.Background(), p, j, p.Checksum, false); e == nil || f.gets != before {
		t.Fatal("ack failure contacted target")
	}
	if _, e := c.Apply(context.Background(), p, j, strings.Repeat("0", 64), true); e == nil || f.gets != before {
		t.Fatal("wrong checksum contacted target")
	}
	f.redirect = f.server.URL + "/wrong"
	if _, e := c.Preview(context.Background()); e == nil {
		t.Fatal("redirect accepted")
	}
	f.redirect = ""
	bad := f.c
	bad.Target.CAFile = ""
	badClient, e := NewClient(bad)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = badClient.Preview(context.Background()); e == nil {
		t.Fatal("untrusted server accepted")
	}
}

func TestMailmanPrivateContractsAndFiles(t *testing.T) {
	f := newFixture(t)
	c := f.client()
	p := f.preview(c)
	raw, _ := Encode(f.c)
	if _, e := DecodeConfig(append([]byte(`{"version":1,`), raw[1:]...)); e == nil {
		t.Fatal("duplicate config key accepted")
	}
	if _, e := DecodeConfig(append(raw, []byte(` {}`)...)); e == nil {
		t.Fatal("trailing JSON accepted")
	}
	p.Actions[0].Email = "outside@example.test"
	p.Checksum = planSum(p)
	if p.validate(f.c) == nil {
		t.Fatal("plan introduced an unexported address")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "private")
	if e := WriteNew(path, []byte("secret")); e != nil {
		t.Fatal(e)
	}
	if e := WriteNew(path, []byte("replacement")); e == nil {
		t.Fatal("existing output overwritten")
	}
	if e := os.Chmod(path, 0644); e != nil {
		t.Fatal(e)
	}
	if _, e := ReadPrivate(path, 100); e == nil {
		t.Fatal("public credential accepted")
	}
	link := filepath.Join(dir, "link")
	os.Symlink(path, link)
	if _, e := ReadPrivate(link, 100); e == nil {
		t.Fatal("symlink input accepted")
	}
	release, e := lock(filepath.Join(dir, "j"))
	if e != nil {
		t.Fatal(e)
	}
	defer release()
	if _, e = lock(filepath.Join(dir, "j")); e == nil {
		t.Fatal("concurrent journal lock acquired")
	}
}
