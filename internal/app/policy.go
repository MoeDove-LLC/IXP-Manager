package app

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/moedove/ixp-manager/internal/policy"
	"github.com/moedove/ixp-manager/internal/routing"
)

const maxPolicyBytes int64 = 128 << 20

type PolicySnapshot struct {
	ID         string    `json:"id"`
	Kind       string    `json:"kind"`
	CreatedAt  time.Time `json:"createdAt"`
	Checksum   string    `json:"checksum"`
	Size       int64     `json:"size"`
	Source     string    `json:"source"`
	FetchedAt  time.Time `json:"fetchedAt"`
	ExpiresAt  time.Time `json:"expiresAt"`
	EntryCount int       `json:"entryCount"`
	ASSet      string    `json:"asSet,omitempty"`
}

func init() {
	Schemas["routers"]["rpkiPolicy"] = enum("off", "require-valid")
	Schemas["routers"]["rpkiHost"] = Field{Type: "ip"}
	Schemas["routers"]["rpkiPort"] = num(1, 65535, false)
	Schemas["routers"]["rpkiExpireSeconds"] = num(60, 86400, false)
}
func (a *App) policyDir() string { return filepath.Join(filepath.Dir(a.store.path), "policies") }
func (a *App) initPolicy() error {
	if err := os.MkdirAll(a.policyDir(), 0700); err != nil {
		return err
	}
	info, err := os.Lstat(a.policyDir())
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("policy directory must be a regular directory")
	}
	if err = os.Chmod(a.policyDir(), 0700); err != nil {
		return err
	}
	state := a.store.Snapshot()
	seen := map[string]bool{}
	for _, snapshot := range state.PolicySnapshots {
		if !identifier.MatchString(snapshot.ID) || seen[snapshot.ID] || !digestPattern.MatchString(snapshot.Checksum) || (snapshot.Kind != "irr" && snapshot.Kind != "rpki") || snapshot.Size < 1 || snapshot.Size > maxPolicyBytes {
			return fmt.Errorf("invalid policy snapshot metadata")
		}
		seen[snapshot.ID] = true
		f, err := a.openPolicy(snapshot)
		if err != nil {
			return err
		}
		h := sha256.New()
		_, readErr := io.Copy(h, f)
		f.Close()
		if readErr != nil || hex.EncodeToString(h.Sum(nil)) != snapshot.Checksum {
			return fmt.Errorf("policy snapshot checksum mismatch")
		}
	}
	return nil
}
func (a *App) openPolicy(s PolicySnapshot) (*os.File, error) {
	if !identifier.MatchString(s.ID) {
		return nil, fmt.Errorf("invalid snapshot ID")
	}
	root, err := os.OpenRoot(a.policyDir())
	if err != nil {
		return nil, err
	}
	defer root.Close()
	f, err := root.OpenFile(s.ID+".json", os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, fmt.Errorf("policy snapshot content unavailable")
	}
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() != s.Size {
		f.Close()
		return nil, fmt.Errorf("policy snapshot size or type mismatch")
	}
	return f, nil
}
func policyByID(s *State, id string) *PolicySnapshot {
	for i := range s.PolicySnapshots {
		if s.PolicySnapshots[i].ID == id {
			return &s.PolicySnapshots[i]
		}
	}
	return nil
}
func (a *App) loadPolicy(s PolicySnapshot) (*policy.IRRSnapshot, *policy.ROASnapshot, error) {
	f, err := a.openPolicy(s)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, maxPolicyBytes+1))
	if err != nil || routing.Checksum(string(raw)) != s.Checksum {
		return nil, nil, fmt.Errorf("policy snapshot checksum mismatch")
	}
	if s.Kind == "irr" {
		var v policy.IRRSnapshot
		if err = json.Unmarshal(raw, &v); err != nil {
			return nil, nil, err
		}
		return &v, nil, nil
	}
	var v policy.ROASnapshot
	if err = json.Unmarshal(raw, &v); err != nil {
		return nil, nil, err
	}
	return nil, &v, nil
}
func decodePolicyJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	if strings.Split(r.Header.Get("Content-Type"), ";")[0] != "application/json" {
		problem(w, 415, "Content-Type must be application/json")
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxPolicyBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		problem(w, 400, "invalid policy JSON: "+err.Error())
		return false
	}
	if decoder.Decode(new(any)) != io.EOF {
		problem(w, 400, "one JSON object required")
		return false
	}
	return true
}
func (a *App) policyRoutes(w http.ResponseWriter, r *http.Request, user Record) {
	role := str(user, "role")
	if role == "member" {
		problem(w, 403, "staff access required")
		return
	}
	mutate := role == "admin" || role == "operator"
	path := strings.TrimPrefix(r.URL.Path, "/api/policy/")
	if path == "snapshots" && r.Method == "GET" {
		items := a.store.Snapshot().PolicySnapshots
		if items == nil {
			items = []PolicySnapshot{}
		}
		respond(w, 200, Record{"items": items})
		return
	}
	if path == "snapshots" && r.Method == "POST" {
		if !mutate {
			problem(w, 403, "policy editing requires administrator or operator")
			return
		}
		var body struct {
			Kind     string          `json:"kind"`
			Snapshot json.RawMessage `json:"snapshot"`
		}
		if !decodePolicyJSON(w, r, &body) {
			return
		}
		now := time.Now().UTC()
		meta := PolicySnapshot{ID: token(16), Kind: body.Kind, CreatedAt: now}
		var canonical []byte
		var err error
		decode := func(target any) error {
			d := json.NewDecoder(strings.NewReader(string(body.Snapshot)))
			d.DisallowUnknownFields()
			if e := d.Decode(target); e != nil {
				return e
			}
			if d.Decode(new(any)) != io.EOF {
				return fmt.Errorf("invalid snapshot JSON")
			}
			return nil
		}
		switch body.Kind {
		case "irr":
			var v policy.IRRSnapshot
			if err = decode(&v); err == nil {
				err = policy.ValidateIRRSnapshot(v, now)
			}
			if err == nil {
				meta.Source = v.Source
				meta.FetchedAt = v.FetchedAt
				meta.ExpiresAt = v.ExpiresAt
				meta.EntryCount = len(v.Routes)
				meta.ASSet = v.ASSet
				canonical, err = json.Marshal(v)
			}
		case "rpki":
			var v policy.ROASnapshot
			if err = decode(&v); err == nil {
				_, err = policy.NewROAIndex(v, now)
			}
			if err == nil {
				meta.Source = v.Source
				meta.FetchedAt = v.FetchedAt
				meta.ExpiresAt = v.ExpiresAt
				meta.EntryCount = len(v.ROAs)
				canonical, err = json.Marshal(v)
			}
		default:
			err = fmt.Errorf("snapshot kind must be irr or rpki")
		}
		if err != nil {
			problem(w, 422, err.Error())
			return
		}
		meta.Size = int64(len(canonical))
		meta.Checksum = routing.Checksum(string(canonical))
		f, err := os.CreateTemp(a.policyDir(), ".policy-")
		if err != nil {
			problem(w, 500, "cannot store snapshot")
			return
		}
		defer os.Remove(f.Name())
		if _, err = f.Write(canonical); err == nil {
			err = f.Sync()
		}
		closeErr := f.Close()
		if err == nil {
			err = closeErr
		}
		if err != nil {
			problem(w, 500, "cannot persist snapshot")
			return
		}
		destination := filepath.Join(a.policyDir(), meta.ID+".json")
		if err = os.Link(f.Name(), destination); err != nil {
			problem(w, 500, "cannot publish snapshot")
			return
		}
		dir, err := os.Open(a.policyDir())
		if err == nil {
			err = dir.Sync()
			dir.Close()
		}
		if err != nil {
			os.Remove(destination)
			problem(w, 500, "cannot sync snapshot")
			return
		}
		err = a.store.Update(func(s *State) error {
			if len(s.PolicySnapshots) >= 16 {
				return fmt.Errorf("delete an older policy snapshot before importing another")
			}
			var total int64
			for _, v := range s.PolicySnapshots {
				total += v.Size
			}
			if total+meta.Size > 512<<20 {
				return fmt.Errorf("policy snapshot storage limit exceeded")
			}
			s.PolicySnapshots = append(s.PolicySnapshots, meta)
			audit(s, str(user, "id"), "import-policy", "policy", meta.ID)
			return nil
		})
		if err != nil {
			os.Remove(destination)
			problem(w, 409, err.Error())
			return
		}
		respond(w, 201, meta)
		return
	}
	if strings.HasPrefix(path, "snapshots/") && r.Method == "DELETE" {
		if !mutate {
			problem(w, 403, "policy editing requires staff write access")
			return
		}
		id := strings.TrimPrefix(path, "snapshots/")
		if !identifier.MatchString(id) {
			problem(w, 404, "snapshot not found")
			return
		}
		err := a.store.Update(func(s *State) error {
			if policyByID(s, id) == nil {
				return fmt.Errorf("snapshot not found")
			}
			s.PolicySnapshots = slices.DeleteFunc(s.PolicySnapshots, func(v PolicySnapshot) bool { return v.ID == id })
			audit(s, str(user, "id"), "delete-policy", "policy", id)
			return nil
		})
		if err != nil {
			problem(w, 404, err.Error())
			return
		}
		a.removePolicyArtifact(id)
		respond(w, 200, Record{"ok": true})
		return
	}
	if r.Method != "POST" || !slices.Contains([]string{"simulate", "candidates", "apply"}, path) {
		problem(w, 404, "policy endpoint not found")
		return
	}
	if path == "apply" && !mutate {
		problem(w, 403, "policy editing requires staff write access")
		return
	}
	var body struct {
		PeerID              string         `json:"peerId"`
		Routes              []policy.Route `json:"routes"`
		IRRID               string         `json:"irrSnapshotId"`
		RPKIID              string         `json:"rpkiSnapshotId"`
		RequireIRR          bool           `json:"requireIrr"`
		RejectNotFound      bool           `json:"rejectRpkiNotFound"`
		IncludeASSetOrigins bool           `json:"includeASSetOrigins"`
		BaseChecksum        string         `json:"baseChecksum"`
	}
	if !decode(w, r, &body) {
		return
	}
	s := a.store.Snapshot()
	peer := find(&s, "peers", body.PeerID)
	if peer == nil {
		problem(w, 404, "peer not found")
		return
	}
	var irr *policy.IRRSnapshot
	var rpki *policy.ROASnapshot
	for _, selected := range []struct{ id, kind string }{{body.IRRID, "irr"}, {body.RPKIID, "rpki"}} {
		if selected.id == "" {
			continue
		}
		meta := policyByID(&s, selected.id)
		if meta == nil || meta.Kind != selected.kind {
			problem(w, 404, "policy snapshot not found")
			return
		}
		i, v, err := a.loadPolicy(*meta)
		if err != nil {
			problem(w, 422, err.Error())
			return
		}
		if i != nil {
			irr = i
		}
		if v != nil {
			rpki = v
		}
	}
	if path == "simulate" {
		if len(body.Routes) > 1000 {
			problem(w, 400, "simulate at most 1000 routes at a time")
			return
		}
		allowed := []string{}
		for _, v := range peer["allowedPrefixes"].([]any) {
			allowed = append(allowed, v.(string))
		}
		bindings := []policy.Route{}
		if values, ok := peer["allowedRoutes"].([]any); ok {
			for _, v := range values {
				m := v.(map[string]any)
				bindings = append(bindings, policy.Route{Prefix: m["prefix"].(string), OriginASN: uint32(m["originAsn"].(float64))})
			}
		}
		decisions, err := policy.Simulate(policy.Request{AllowedRoutes: bindings, PeerASN: uint32(number(peer, "asn")), Routes: body.Routes, AllowedPrefixes: allowed, IRR: irr, RPKI: rpki, RequireIRR: body.RequireIRR, RejectRPKINotFound: body.RejectNotFound}, time.Now())
		if err != nil {
			problem(w, 422, err.Error())
			return
		}
		respond(w, 200, Record{"peerId": body.PeerID, "decisions": decisions})
		return
	}
	if irr == nil {
		problem(w, 400, "select an IRR snapshot for a reviewed proposal")
		return
	}
	bindings, err := candidateBindings(*irr, peer, body.IncludeASSetOrigins)
	prefixes := bindingPrefixes(bindings)
	if err != nil {
		problem(w, 422, err.Error())
		return
	}
	if path == "candidates" {
		added, removed := prefixChanges(peer, prefixes)
		respond(w, 200, Record{"peerId": body.PeerID, "baseChecksum": peerReviewChecksum(peer), "prefixes": prefixes, "allowedRoutes": bindings, "added": added, "removed": removed, "source": irr.Source})
		return
	}
	var result Record
	err = a.store.Update(func(s *State) error {
		current := find(s, "peers", body.PeerID)
		if current == nil || peerReviewChecksum(current) != body.BaseChecksum {
			return fmt.Errorf("peer policy changed; review a new proposal")
		}
		if policyByID(s, body.IRRID) == nil {
			return fmt.Errorf("policy snapshot was removed")
		}
		bindings, err := candidateBindings(*irr, current, body.IncludeASSetOrigins)
		prefixes := bindingPrefixes(bindings)
		if err != nil {
			return err
		}
		values := make([]any, len(prefixes))
		for i, v := range prefixes {
			values[i] = v
		}
		current["allowedPrefixes"] = values
		rules := make([]any, len(bindings))
		for i, v := range bindings {
			rules[i] = map[string]any{"prefix": v.Prefix, "originAsn": float64(v.OriginASN)}
		}
		current["allowedRoutes"] = rules
		current["updatedAt"] = time.Now().UTC().Format(time.RFC3339Nano)
		if err = validateAll(s); err != nil {
			return err
		}
		audit(s, str(user, "id"), "apply-policy-proposal", "peers", body.PeerID)
		result = clean(current)
		return nil
	})
	if err != nil {
		problem(w, 409, err.Error())
		return
	}
	respond(w, 200, result)
}
func peerCandidate(s policy.IRRSnapshot, p Record) ([]string, error) {
	prefixes, err := policy.CandidatePrefixes(s, uint32(number(p, "asn")), time.Now())
	if err != nil {
		return nil, err
	}
	address, err := netip.ParseAddr(str(p, "address"))
	if err != nil {
		return nil, err
	}
	out := []string{}
	for _, raw := range prefixes {
		prefix, err := netip.ParsePrefix(raw)
		if err != nil || prefix.Bits() == 0 || !prefix.Addr().IsGlobalUnicast() {
			return nil, fmt.Errorf("IRR candidate contains a prefix unsuitable for the managed routing profile")
		}
		if prefix.Addr().Is4() == address.Is4() {
			out = append(out, raw)
		}
	}
	sort.Strings(out)
	out = slices.Compact(out)
	if len(out) > 10000 {
		return nil, fmt.Errorf("candidate exceeds the 10000-prefix managed peer limit")
	}
	return out, nil
}
func peerReviewChecksum(p Record) string {
	snapshot := Record{}
	for _, key := range []string{"id", "asn", "address", "routerId", "memberId", "enabled", "maxPrefixes", "allowedPrefixes", "allowedRoutes"} {
		snapshot[key] = p[key]
	}
	b, _ := json.Marshal(snapshot)
	return routing.Checksum(string(b))
}
func prefixChanges(p Record, next []string) ([]string, []string) {
	old := []string{}
	for _, v := range p["allowedPrefixes"].([]any) {
		old = append(old, v.(string))
	}
	added, removed := []string{}, []string{}
	for _, v := range next {
		if !slices.Contains(old, v) {
			added = append(added, v)
		}
	}
	for _, v := range old {
		if !slices.Contains(next, v) {
			removed = append(removed, v)
		}
	}
	return added, removed
}

func (a *App) removePolicyArtifact(id string) {
	if !identifier.MatchString(id) {
		return
	}
	a.store.mu.RLock()
	defer a.store.mu.RUnlock()
	if a.store.closed {
		return
	}
	_ = os.Remove(filepath.Join(a.policyDir(), id+".json"))
}
