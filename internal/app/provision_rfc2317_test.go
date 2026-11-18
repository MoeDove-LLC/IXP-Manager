package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/moedove/ixp-manager/internal/provision"
)

const pairTestParent = "2.0.192.in-addr.arpa."
const pairTestChild = "128-131." + pairTestParent
const pairTestSource = `; Parent fixture
$ORIGIN 2.0.192.in-addr.arpa.
$TTL 3600
@ IN SOA ns.parent.example.test. hostmaster.parent.example.test. ( 41 3600 600 604800 300 )
@ IN NS ns.parent.example.test.
10 IN PTR unrelated-private-parent-record.example.test.
`

func makePairRelease(t *testing.T, source string, previousParent, parent, child uint32, previousChild *uint32) ([]byte, provision.RFC2317MergedBundle) {
	t.Helper()
	first, last := uint16(128), uint16(131)
	servers := []string{"ns1.example.test.", "ns2.example.test."}
	input := provision.RFC2317Input{
		Version: 1, First: &first, Last: &last, ChildZone: pairTestChild, NewChildZone: previousChild == nil, PreviousChildSerial: previousChild,
		Parent: provision.RFC2317ParentReview{Zone: pairTestParent, ObservedSerial: &previousParent, SnapshotSHA256: digest(source), TTL: 3600,
			Delegation: provision.RFC2317DelegationClaim{Owner: pairTestChild, State: "absent"}},
		DNS:  provision.DNSOptions{Prefix: "192.0.2.128/30", Serial: child, PrimaryNS: servers[0], NameServers: servers, ResponsibleMailbox: "hostmaster.example.test."},
		PTRs: []provision.RFC2317PTR{{Address: "192.0.2.129", Name: "paired-customer.example.test."}},
	}
	if previousChild != nil {
		input.Parent.Delegation.State = "ns"
		input.Parent.Delegation.NameServers = servers
	}
	for offset := first; offset <= last; offset++ {
		owner := provision.RFC2317OwnerClaim{Owner: fmt.Sprintf("%d.%s", offset, pairTestParent), State: "absent"}
		if previousChild != nil {
			owner.State, owner.Target = "cname", fmt.Sprintf("%d.%s", offset, pairTestChild)
		}
		input.Parent.Owners = append(input.Parent.Owners, owner)
	}
	review, err := provision.RenderRFC2317(input)
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := provision.PrepareRFC2317Merge(review, []byte(source), parent)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := provision.EncodeRFC2317Merged(bundle)
	if err != nil {
		t.Fatal(err)
	}
	return raw, bundle
}

func requestPairImport(c *client, raw []byte) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "/api/provisioning/rfc2317-import", bytes.NewReader(raw))
	r.RemoteAddr = "127.0.0.1:10000"
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-CSRF-Token", c.csrf)
	r.AddCookie(c.cookie)
	w := httptest.NewRecorder()
	c.a.ServeHTTP(w, r)
	return w
}

func importRFC2317TestRelease(t *testing.T, c *client, raw []byte) Record {
	t.Helper()
	w := requestPairImport(c, raw)
	if w.Code != 201 {
		t.Fatalf("import: %d %s", w.Code, w.Body.String())
	}
	var revision Record
	if err := json.Unmarshal(w.Body.Bytes(), &revision); err != nil {
		t.Fatal(err)
	}
	return revision
}

func TestRFC2317PairImportBoundsAndSecrets(t *testing.T) {
	c, dir := setupClient(t)
	raw, _ := makePairRelease(t, pairTestSource, 41, 42, 17, nil)
	viewer := c.create("users", Record{"name": "Pair Viewer", "email": "pair-viewer@example.net", "role": "viewer", "password": "viewer-pair-long-password"})
	reader := loginClient(t, c.a, Record{"email": viewer["email"], "password": "viewer-pair-long-password"})
	if w := requestPairImport(reader, raw); w.Code != 403 {
		t.Fatalf("viewer imported a paired DNS release: %d", w.Code)
	}
	if w := requestPairImport(c, append(bytes.Clone(raw), ' ')); w.Code != 422 {
		t.Fatalf("noncanonical file returned %d", w.Code)
	}
	revision := importRFC2317TestRelease(t, c, raw)
	path := "/api/provisioning/" + str(revision, "id")
	metadata, _ := revision["rfc2317"].(map[string]any)
	if str(revision, "profileId") != rfc2317ProfileID || str(revision, "kind") != provision.RFC2317MergeKind || metadata["parentSerial"] != float64(42) || metadata["childSerial"] != float64(17) {
		t.Fatal("paired revision metadata was changed")
	}
	if w := requestPairImport(c, raw); w.Code != 409 {
		t.Fatalf("same pair reused its issued serials: %d", w.Code)
	}
	if w := c.request("GET", path+"/config", nil); w.Code != 409 {
		t.Fatal("draft released the whole encrypted parent")
	}
	preview := reader.ok("GET", path, nil)
	for _, marker := range []string{"paired-customer.example.test.", "128.2.0.192.in-addr.arpa. 3600 IN CNAME"} {
		if !strings.Contains(str(preview, "config"), marker) {
			t.Fatal("safe preview omitted reviewed records", marker)
		}
	}
	if strings.Contains(str(preview, "config"), "unrelated-private-parent-record") || strings.Contains(str(preview, "config"), digest(pairTestSource)) || strings.Contains(str(preview, "checksum"), digest(string(raw))) || preview["redacted"] != true {
		t.Fatal("preview exposed an unrelated parent record or raw bundle identity")
	}
	if w := reader.request("GET", path+"/diff", nil); w.Code != 405 || strings.Contains(w.Body.String(), "unrelated-private-parent-record") {
		t.Fatal("generic diff exposed the raw source")
	}
	stored, err := os.ReadFile(filepath.Join(dir, "provisioning", str(revision, "id")+".enc"))
	if err != nil || bytes.Contains(stored, []byte("unrelated-private-parent-record")) {
		t.Fatal("source parent was not stored exclusively in the encrypted artifact", err)
	}
	state := c.a.store.Snapshot()
	if len(state.ProvisionRevisions) != 1 || state.ProvisionSerials[pairTestParent] != 42 || state.ProvisionSerials[pairTestChild] != 17 || len(state.ProvisionApprovedSerials) != 0 {
		t.Fatal("an invalid or duplicate upload mutated paired reservation state")
	}
}

func TestRFC2317BothHeadsProtectAtomicApproval(t *testing.T) {
	_, bundle := makePairRelease(t, pairTestSource, 41, 42, 17, nil)
	for _, changed := range []string{pairTestParent, pairTestChild} {
		t.Run(changed, func(t *testing.T) {
			pair := rfc2317Revision(bundle)
			state := State{ProvisionSerials: map[string]uint32{pairTestParent: 42, pairTestChild: 17}, ProvisionApprovedSerials: map[string]uint32{pairTestParent: 41}}
			if err := pair.bindApprovalHeads(&state); err != nil {
				t.Fatal(err)
			}
			state.ProvisionApprovedSerials[changed] = state.ProvisionSerials[changed]
			if err := approveProvisionSerial(&state, ProvisionRevision{Kind: provision.RFC2317MergeKind, RFC2317: &pair}); err == nil {
				t.Fatal("one changed zone approval did not invalidate the pair")
			}
			if changed == pairTestChild && state.ProvisionApprovedSerials[pairTestParent] != 41 {
				t.Fatal("parent approval changed before checking the child")
			}
			if _, exists := state.ProvisionApprovedSerials[pairTestChild]; changed == pairTestParent && exists {
				t.Fatal("child approval changed before checking the parent")
			}
		})
	}
	state := State{ProvisionSerials: map[string]uint32{pairTestParent: 41, pairTestChild: 17}}
	pair := rfc2317Revision(bundle)
	if err := reserveRFC2317Serials(&state, ProvisionRevision{Kind: provision.RFC2317MergeKind, RFC2317: &pair}); err == nil || state.ProvisionSerials[pairTestParent] != 41 {
		t.Fatal("a rejected child reservation partially advanced the parent")
	}
}

func TestRFC2317NativePairedApprovalCAS(t *testing.T) {
	binary := os.Getenv("NAMED_CHECKZONE_BIN")
	if binary == "" {
		t.Skip("NAMED_CHECKZONE_BIN is required for the native whole-zone checks")
	}
	c, dir := setupClient(t)
	c.a.namedCheckzone = binary
	firstRaw, firstBundle := makePairRelease(t, pairTestSource, 41, 42, 17, nil)
	staleRaw, _ := makePairRelease(t, pairTestSource, 41, 43, 18, nil)
	first := importRFC2317TestRelease(t, c, firstRaw)
	stale := importRFC2317TestRelease(t, c, staleRaw)
	path := "/api/provisioning/" + str(first, "id")
	stalePath := "/api/provisioning/" + str(stale, "id")
	if w := c.request("POST", path+"/approve", Record{"checksum": first["checksum"]}); w.Code != 409 {
		t.Fatal("pair approved without native validation")
	}
	c.ok("POST", stalePath+"/validate", nil)
	approveProvision(c, first)
	if w := c.request("POST", stalePath+"/approve", Record{"checksum": stale["checksum"]}); w.Code != 409 {
		t.Fatal("stale validated pair replaced the current pair")
	}
	state := c.a.store.Snapshot()
	if state.ProvisionApprovedSerials[pairTestParent] != 42 || state.ProvisionApprovedSerials[pairTestChild] != 17 {
		t.Fatal("rejected approval changed one of the heads")
	}
	download := c.request("GET", path+"/config", nil)
	if download.Code != 200 || !bytes.Equal(firstRaw, download.Body.Bytes()) || download.Header().Get("X-Config-SHA256") != digest(string(firstRaw)) || download.Header().Get("X-Provisioning-Revision") != first["id"] {
		t.Fatal("approved download did not return the exactly bound pair")
	}
	if w := c.request("POST", path+"/prune", Record{"checksum": first["checksum"], "acknowledge": true}); w.Code != 409 {
		t.Fatal("currently approved paired artifact was pruned")
	}
	if err := approveProvisionSerial(&state, ProvisionRevision{Kind: "dns-arpa", Zone: pairTestParent, Options: provision.Options{DNS: provision.DNSOptions{Serial: 43}}}); err == nil || !strings.Contains(err.Error(), "preserve its delegation") {
		t.Fatal("direct full parent approval could overwrite an active delegation", err)
	}
	source := firstBundle.Files[1].Content
	previousChild := uint32(17)
	wrong, _ := makePairRelease(t, source+"; altered parent bytes\n", 42, 44, 19, &previousChild)
	if w := requestPairImport(c, wrong); w.Code != 409 || !strings.Contains(w.Body.String(), "same SOA serial") {
		t.Fatal("same-serial different parent bytes passed the approval baseline check")
	}
	nextRaw, _ := makePairRelease(t, source, 42, 44, 19, &previousChild)
	next := importRFC2317TestRelease(t, c, nextRaw)
	nextPath := approveProvision(c, next)
	nextPreview := str(c.ok("GET", nextPath, nil), "config")
	if !strings.Contains(nextPreview, "Parent SOA 42 -> 44") || strings.Contains(nextPreview, " IN CNAME ") {
		t.Fatal("existing parent records were presented as new changes")
	}
	if w := c.request("GET", path+"/config", nil); w.Code != 409 {
		t.Fatal("superseded parent/child pair remained downloadable")
	}
	c.ok("POST", path+"/prune", Record{"checksum": first["checksum"], "acknowledge": true, "reason": "Replaced by the next pair"})
	if err := c.a.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := New(Config{DataDir: dir, NamedCheckzone: binary})
	if err != nil {
		t.Fatal("paired revisions could not be restored from the encrypted store", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	c = loginClient(t, reopened, Record{"email": "admin@example.net", "password": "very-long-test-password"})
	if w := c.request("GET", nextPath+"/config", nil); w.Code != 200 || !bytes.Equal(nextRaw, w.Body.Bytes()) {
		t.Fatal("store reopen lost the approved pair")
	}
}
