package app

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	provider "github.com/moedove/ixp-manager/internal/integrations"
)

type ExternalDiff struct {
	Field        string `json:"field"`
	Before       any    `json:"before"`
	After        any    `json:"after"`
	ApplyAllowed bool   `json:"applyAllowed"`
	Reason       string `json:"reason,omitempty"`
}
type ExternalProposal struct {
	SnapshotPruned   bool           `json:"snapshotPruned,omitempty"`
	SnapshotPrunedAt time.Time      `json:"snapshotPrunedAt,omitempty"`
	SnapshotPrunedBy string         `json:"snapshotPrunedBy,omitempty"`
	DismissedBy      string         `json:"dismissedBy,omitempty"`
	DismissedAt      time.Time      `json:"dismissedAt,omitempty"`
	ResolutionReason string         `json:"resolutionReason,omitempty"`
	ProviderOrigin   string         `json:"providerOrigin,omitempty"`
	ID               string         `json:"id"`
	Provider         string         `json:"provider"`
	Source           string         `json:"source"`
	MemberID         string         `json:"memberId"`
	MemberName       string         `json:"memberName"`
	ConnectionID     string         `json:"connectionId,omitempty"`
	ASN              uint32         `json:"asn"`
	Status           string         `json:"status"`
	CreatedAt        time.Time      `json:"createdAt"`
	FetchedAt        time.Time      `json:"fetchedAt"`
	SnapshotChecksum string         `json:"snapshotChecksum"`
	Checksum         string         `json:"checksum"`
	BaseChecksum     string         `json:"baseChecksum"`
	Diff             []ExternalDiff `json:"diff"`
	AppliedFields    []string       `json:"appliedFields,omitempty"`
	AppliedBy        string         `json:"appliedBy,omitempty"`
	AppliedAt        time.Time      `json:"appliedAt,omitempty"`
}

func (a *App) integrationDir() string {
	return filepath.Join(filepath.Dir(a.store.path), "integrations", "snapshots")
}
func (a *App) initIntegrations(path string) error {
	config, err := provider.LoadConfig(path)
	if err != nil {
		return err
	}
	client, err := provider.NewClient(config)
	if err != nil {
		return err
	}
	if client == nil {
		a.peeringDB = nil
	} else {
		a.peeringDB = client
	}
	if err = os.MkdirAll(a.integrationDir(), 0700); err != nil {
		return err
	}
	state := a.store.Snapshot()
	if err = ValidateExternalArtifacts(filepath.Dir(a.store.path), state); err != nil {
		return err
	}
	// Resume already-authorized pruning after a crash between its durable marker
	// and file removal. Offline validation remains strictly read-only.
	a.store.mu.Lock()
	defer a.store.mu.Unlock()
	for _, p := range state.ExternalProposals {
		if p.SnapshotPruned {
			if _, err := a.removePrunedArtifact(p); err != nil {
				return err
			}
		}
	}
	return nil
}

// ValidateExternalArtifacts is read-only, suitable for offline backup restore
// validation before replacing the active data directory.
func ValidateExternalArtifacts(dir string, state State) error {
	a := &App{store: &Store{path: filepath.Join(dir, "state.json")}}
	seen := map[string]bool{}
	for _, p := range state.ExternalProposals {
		if !identifier.MatchString(p.ID) || seen[p.ID] || p.Provider != "peeringdb" || (p.Source != "provider-fetch" && p.Source != "offline-import") || (p.Status != "pending" && p.Status != "applied" && p.Status != "dismissed") {
			return errors.New("invalid external proposal metadata")
		}
		seen[p.ID] = true
		if !externalDigest(p.Checksum) || !externalDigest(p.BaseChecksum) || !externalDigest(p.SnapshotChecksum) || proposalChecksum(p) != p.Checksum {
			return errors.New("external proposal checksum invalid")
		}
		if p.SnapshotPruned {
			if p.Status == "pending" || p.SnapshotPrunedAt.IsZero() || p.SnapshotPrunedBy == "" {
				return errors.New("invalid external snapshot pruning marker")
			}
			continue
		}
		snapshot, err := a.readExternalSnapshot(p)
		if err != nil {
			return err
		}
		if snapshot.Network.ASN != p.ASN || !snapshot.FetchedAt.Equal(p.FetchedAt) || proposalChecksum(p) != p.Checksum {
			return errors.New("external proposal checksum or binding mismatch")
		}
	}
	return nil
}
func externalDigest(s string) bool {
	if len(s) != 64 || strings.ToLower(s) != s {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}
func proposalChecksum(p ExternalProposal) string {
	raw, _ := json.Marshal(struct {
		Provider, Source, ProviderOrigin, MemberID, ConnectionID string
		ASN                                                      uint32
		BaseChecksum, SnapshotChecksum                           string
		Diff                                                     []ExternalDiff
	}{p.Provider, p.Source, p.ProviderOrigin, p.MemberID, p.ConnectionID, p.ASN, p.BaseChecksum, p.SnapshotChecksum, p.Diff})
	return provider.Checksum(raw)
}
func externalBase(s *State, memberID, connectionID string) (string, error) {
	member := find(s, "members", memberID)
	if member == nil || number(member, "asn") <= 0 {
		return "", errors.New("a peering member with an ASN is required")
	}
	var connection Record
	if connectionID != "" {
		connection = find(s, "connections", connectionID)
		if connection == nil || str(connection, "memberId") != memberID {
			return "", errors.New("connection does not belong to member")
		}
	}
	vlan := find(s, "vlans", str(connection, "vlanId"))
	infrastructure := find(s, "infrastructures", fabricID(vlan))
	raw, _ := json.Marshal(Record{"member": member, "connection": connection, "vlan": vlan, "infrastructure": infrastructure})
	return provider.Checksum(raw), nil
}
func optionalExternalID(r Record, key string) any {
	if n := number(r, key); n > 0 {
		return n
	}
	return nil
}
func externalDiff(s *State, memberID, connectionID string, snapshot provider.Snapshot) ([]ExternalDiff, error) {
	member := find(s, "members", memberID)
	if member == nil || number(member, "asn") != float64(snapshot.Network.ASN) {
		return nil, errors.New("snapshot ASN does not match authoritative member ASN")
	}
	out := []ExternalDiff{}
	add := func(field string, before, after any, allowed bool, reason string) {
		a, _ := json.Marshal(before)
		b, _ := json.Marshal(after)
		if string(a) != string(b) {
			out = append(out, ExternalDiff{Field: field, Before: before, After: after, ApplyAllowed: allowed, Reason: reason})
		}
	}
	add("member.name", str(member, "name"), snapshot.Network.Name, true, "")
	if snapshot.Network.Website != "" {
		add("member.website", str(member, "website"), snapshot.Network.Website, true, "")
	}
	add("member.peeringdbId", optionalExternalID(member, "peeringdbId"), float64(snapshot.Network.ID), true, "")
	if snapshot.Network.IRRASSet != "" {
		add("member.irrASSet", str(member, "irrASSet"), snapshot.Network.IRRASSet, false, "IRR 策略需要通过路由策略工作流单独审核")
	}
	if connectionID != "" {
		connection := find(s, "connections", connectionID)
		if connection == nil || str(connection, "memberId") != memberID {
			return nil, errors.New("connection does not belong to member")
		}
		matches := []provider.NetIXLAN{}
		vlan := find(s, "vlans", str(connection, "vlanId"))
		expectedIX := number(find(s, "infrastructures", fabricID(vlan)), "peeringdbId")
		for _, link := range snapshot.NetIXLANs {
			if expectedIX > 0 && float64(link.IXID) != expectedIX {
				continue
			}
			if (str(connection, "ipv4") != "" && str(connection, "ipv4") == link.IPv4) || (str(connection, "ipv6") != "" && str(connection, "ipv6") == link.IPv6) {
				matches = append(matches, link)
			}
		}
		if len(matches) == 1 {
			link := matches[0]
			add("connection.peeringdbNetixlanId", optionalExternalID(connection, "peeringdbNetixlanId"), float64(link.ID), true, "")
			add("connection.ipv4", str(connection, "ipv4"), link.IPv4, false, "本地地址分配是权威数据，不从外部覆盖")
			add("connection.ipv6", str(connection, "ipv6"), link.IPv6, false, "本地地址分配是权威数据，不从外部覆盖")
			var localSpeed, externalSpeed any
			if speed, known := connectionCapacity(s, connection); known {
				localSpeed = speed
			}
			if link.Speed > 0 {
				externalSpeed = float64(link.Speed)
			}
			add("connection.speed", localSpeed, externalSpeed, false, "端口/LAG 容量由本地物理资源决定；空值表示未知")
		} else {
			reason := "没有匹配本地地址的外部连接"
			if len(matches) > 1 {
				reason = "多个外部连接匹配，不能自动关联"
			}
			out = append(out, ExternalDiff{Field: "connection.peeringdbNetixlanId", Before: optionalExternalID(connection, "peeringdbNetixlanId"), After: nil, ApplyAllowed: false, Reason: reason})
		}
	}
	return out, nil
}

// writeExternalSnapshot is called only inside Store.Update, which serializes
// quota checks, artifact creation and the referencing metadata transaction.
func (a *App) writeExternalSnapshot(id string, snapshot provider.Snapshot) (string, error) {
	if a.store.closed {
		return "", errors.New("control store is closed")
	}
	if !identifier.MatchString(id) {
		return "", errors.New("invalid proposal identifier")
	}
	raw, checksum, err := provider.Canonical(snapshot)
	if err != nil {
		return "", err
	}
	if len(raw) > provider.MaxSnapshotBytes {
		return "", errors.New("canonical snapshot exceeds 8 MiB")
	}
	entries, err := os.ReadDir(a.integrationDir())
	if err != nil {
		return "", err
	}
	var used int64
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".json") {
			info, err := entry.Info()
			if err != nil || !info.Mode().IsRegular() {
				return "", errors.New("unexpected snapshot filesystem entry")
			}
			used += info.Size()
		}
	}
	if used+int64(len(raw)) > 128<<20 {
		return "", errors.New("external snapshot storage exceeds 128 MiB capacity")
	}
	path := filepath.Join(a.integrationDir(), id+".json")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return "", err
	}
	success := false
	defer func() {
		file.Close()
		if !success {
			os.Remove(path)
		}
	}()
	if _, err = file.Write(raw); err != nil {
		return "", err
	}
	if err = file.Sync(); err != nil {
		return "", err
	}
	if err = file.Close(); err != nil {
		return "", err
	}
	dir, err := os.Open(a.integrationDir())
	if err != nil {
		return "", err
	}
	err = dir.Sync()
	dir.Close()
	if err != nil {
		return "", err
	}
	success = true
	return checksum, nil
}
func (a *App) readExternalSnapshot(p ExternalProposal) (provider.Snapshot, error) {
	var snapshot provider.Snapshot
	if !identifier.MatchString(p.ID) {
		return snapshot, errors.New("invalid persisted proposal identifier")
	}
	path := filepath.Join(a.integrationDir(), p.ID+".json")
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > provider.MaxSnapshotBytes {
		return snapshot, errors.New("external snapshot missing, oversized or not a regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return snapshot, err
	}
	raw, err := io.ReadAll(io.LimitReader(file, provider.MaxSnapshotBytes+1))
	file.Close()
	if err != nil || len(raw) > provider.MaxSnapshotBytes || provider.Checksum(raw) != p.SnapshotChecksum {
		return snapshot, errors.New("external snapshot checksum mismatch")
	}
	if err = json.Unmarshal(raw, &snapshot); err != nil {
		return snapshot, err
	}
	return provider.Normalize(snapshot, snapshot.FetchedAt)
}
func proposalMetadata(p ExternalProposal) Record {
	raw, _ := json.Marshal(p)
	var view Record
	json.Unmarshal(raw, &view)
	view["snapshotUnavailable"] = p.SnapshotPruned
	view["snapshotPrunedAt"] = optionalTime(p.SnapshotPrunedAt)
	view["appliedAt"] = optionalTime(p.AppliedAt)
	view["dismissedAt"] = optionalTime(p.DismissedAt)
	view["sourceNote"] = "仅供资料核对，外部数据不会自动替代本地网络分配"
	if p.Source == "offline-import" {
		view["sourceNote"] = "离线快照仅校验内容摘要与结构，摘要不证明来源；必须人工核对"
	}
	return view
}
func (a *App) proposalView(p ExternalProposal) (Record, error) {
	if p.SnapshotPruned {
		return proposalMetadata(p), nil
	}
	snapshot, err := a.readExternalSnapshot(p)
	if err != nil {
		return nil, err
	}
	view := proposalMetadata(p)
	view["snapshot"] = snapshot
	return view, nil
}
func (a *App) createExternalProposal(s State, memberID, connectionID, source string, snapshot provider.Snapshot, actor Record) (ExternalProposal, error) {
	var p ExternalProposal
	base, err := externalBase(&s, memberID, connectionID)
	if err != nil {
		return p, err
	}
	diff, err := externalDiff(&s, memberID, connectionID, snapshot)
	if err != nil {
		return p, err
	}
	p = ExternalProposal{ID: token(16), Provider: "peeringdb", Source: source, MemberID: memberID, MemberName: str(find(&s, "members", memberID), "name"), ConnectionID: connectionID, ASN: snapshot.Network.ASN, Status: "pending", CreatedAt: time.Now().UTC(), FetchedAt: snapshot.FetchedAt, BaseChecksum: base, Diff: diff}
	createdArtifact := false
	err = a.store.Update(func(current *State) error {
		role := str(find(current, "users", str(actor, "id")), "role")
		if role != "admin" && role != "operator" {
			return errors.New("staff permission changed")
		}
		if len(current.ExternalProposals) >= 2000 {
			return errors.New("external proposal history capacity reached")
		}
		latest, err := externalBase(current, memberID, connectionID)
		if err != nil || latest != base {
			return errors.New("local data changed while fetching; create a new proposal")
		}
		p.SnapshotChecksum, err = a.writeExternalSnapshot(p.ID, snapshot)
		if err != nil {
			return err
		}
		createdArtifact = true
		if source == "provider-fetch" {
			p.ProviderOrigin = a.peeringDB.Origin()
		}
		p.Checksum = proposalChecksum(p)
		current.ExternalProposals = append(current.ExternalProposals, p)
		audit(current, str(actor, "id"), "external-proposal", "members", memberID)
		return nil
	})
	if err != nil && createdArtifact {
		os.Remove(filepath.Join(a.integrationDir(), p.ID+".json"))
	}
	return p, err
}
func (a *App) integrations(w http.ResponseWriter, r *http.Request, user Record, session Session) {
	role := str(user, "role")
	if role == "member" {
		problem(w, 403, "staff access required")
		return
	}
	path := r.URL.Path
	if path == "/api/integrations" && r.Method == "GET" {
		respond(w, 200, Record{"providers": []Record{{"id": "peeringdb", "label": "PeeringDB", "enabled": a.peeringDB != nil, "readOnly": true}}})
		return
	}
	if path == "/api/integrations/proposals" && r.Method == "GET" {
		offset, limit := 0, 100
		var err error
		if raw := r.URL.Query().Get("offset"); raw != "" {
			offset, err = strconv.Atoi(raw)
			if err != nil || offset < 0 {
				problem(w, 400, "invalid offset")
				return
			}
		}
		if raw := r.URL.Query().Get("limit"); raw != "" {
			limit, err = strconv.Atoi(raw)
			if err != nil || limit < 1 || limit > 100 {
				problem(w, 400, "limit must be 1..100")
				return
			}
		}
		state := a.store.Snapshot()
		items := []Record{}
		for index := len(state.ExternalProposals) - 1 - offset; index >= 0 && len(items) < limit; index-- {
			items = append(items, proposalMetadata(state.ExternalProposals[index]))
		}
		respond(w, 200, Record{"items": items, "hasMore": offset+len(items) < len(state.ExternalProposals)})
		return
	}
	if r.Method == "GET" && strings.HasPrefix(path, "/api/integrations/proposals/") {
		id := strings.TrimPrefix(path, "/api/integrations/proposals/")
		state := a.store.Snapshot()
		for _, p := range state.ExternalProposals {
			if p.ID == id {
				view, err := a.proposalView(p)
				if err != nil {
					problem(w, 500, "stored provider snapshot failed integrity validation")
					return
				}
				respond(w, 200, view)
				return
			}
		}
		problem(w, 404, "proposal not found")
		return
	}
	if strings.HasPrefix(path, "/api/integrations/proposals/") && (r.Method == "DELETE" || (r.Method == "POST" && strings.HasSuffix(path, "/prune"))) {
		a.externalRetention(w, r, user, session)
		return
	}
	if r.Method != "POST" || (role != "admin" && role != "operator") {
		problem(w, 403, "staff write access required")
		return
	}
	if path == "/api/integrations/peeringdb/propose" || path == "/api/integrations/proposals/import" {
		if a.limited(r) {
			problem(w, 429, "too many attempts; retry later")
			return
		}
		var body struct {
			MemberID     string `json:"memberId"`
			ConnectionID string `json:"connectionId"`
			IXID         int64  `json:"ixId"`
			FacilityID   int64  `json:"facilityId"`
			Document     string `json:"document"`
			Checksum     string `json:"checksum"`
		}
		if !decode(w, r, &body) {
			return
		}
		state := a.store.Snapshot()
		if _, err := externalBase(&state, body.MemberID, body.ConnectionID); err != nil {
			problem(w, 400, err.Error())
			return
		}
		if body.ConnectionID != "" {
			connection := find(&state, "connections", body.ConnectionID)
			vlan := find(&state, "vlans", str(connection, "vlanId"))
			expectedIX := int64(number(find(&state, "infrastructures", fabricID(vlan)), "peeringdbId"))
			if expectedIX > 0 {
				if body.IXID > 0 && body.IXID != expectedIX {
					problem(w, 400, "exchange ID differs from the connection fabric mapping")
					return
				}
				body.IXID = expectedIX
			}
		}
		var snapshot provider.Snapshot
		var err error
		source := "provider-fetch"
		if path == "/api/integrations/proposals/import" {
			source = "offline-import"
			if len(body.Document) > 6<<20 {
				problem(w, 400, "snapshot import document exceeds 6 MiB")
				return
			}
			if provider.Checksum([]byte(body.Document)) != body.Checksum {
				problem(w, 400, "snapshot document checksum mismatch")
				return
			}
			snapshot, err = provider.Parse([]byte(body.Document), time.Now())
		} else {
			if a.peeringDB == nil {
				problem(w, 503, "PeeringDB provider is disabled in local configuration")
				return
			}
			snapshot, err = a.peeringDB.Fetch(r.Context(), uint32(number(find(&state, "members", body.MemberID), "asn")), body.IXID, body.FacilityID)
		}
		if err == nil {
			snapshot, err = provider.Normalize(snapshot, time.Now())
		}
		if err != nil {
			problem(w, 422, err.Error())
			return
		}
		p, err := a.createExternalProposal(state, body.MemberID, body.ConnectionID, source, snapshot, user)
		if err != nil {
			problem(w, 409, err.Error())
			return
		}
		view, err := a.proposalView(p)
		if err != nil {
			problem(w, 500, "could not read saved snapshot")
			return
		}
		respond(w, 201, view)
		return
	}
	parts := strings.Split(strings.TrimPrefix(path, "/api/integrations/proposals/"), "/")
	if len(parts) != 2 || (parts[1] != "apply" && parts[1] != "dismiss") {
		problem(w, 404, "integration action not found")
		return
	}
	if role != "admin" || session.API {
		problem(w, 403, "browser administrator review is required to apply external changes")
		return
	}
	var body struct {
		Checksum     string   `json:"checksum"`
		BaseChecksum string   `json:"baseChecksum"`
		Fields       []string `json:"fields"`
		Reason       string   `json:"reason"`
	}
	if !decode(w, r, &body) {
		return
	}
	if parts[1] == "apply" && len(body.Fields) == 0 {
		problem(w, 400, "choose at least one reviewed field")
		return
	}
	if len(body.Reason) > 1000 || strings.ContainsRune(body.Reason, 0) {
		problem(w, 400, "review reason exceeds limit")
		return
	}
	var applied ExternalProposal
	err := a.store.Update(func(s *State) error {
		if str(find(s, "users", str(user, "id")), "role") != "admin" {
			return errors.New("administrator permission changed")
		}
		for i := range s.ExternalProposals {
			p := &s.ExternalProposals[i]
			if p.ID != parts[0] {
				continue
			}
			if parts[1] == "dismiss" {
				if p.Status != "pending" || p.Checksum != body.Checksum || proposalChecksum(*p) != p.Checksum {
					return errors.New("proposal status or review checksum changed")
				}
				p.Status = "dismissed"
				p.DismissedBy = str(user, "id")
				p.DismissedAt = time.Now().UTC()
				p.ResolutionReason = strings.TrimSpace(body.Reason)
				applied = *p
				audit(s, str(user, "id"), "dismiss-external-proposal", "members", p.MemberID)
				return nil
			}
			if p.Status != "pending" || p.Checksum != body.Checksum || proposalChecksum(*p) != p.Checksum || p.BaseChecksum != body.BaseChecksum {
				return errors.New("proposal status or reviewed checksum changed")
			}
			base, err := externalBase(s, p.MemberID, p.ConnectionID)
			if err != nil || base != p.BaseChecksum {
				return errors.New("authoritative local data changed; create a new proposal")
			}
			snapshot, err := a.readExternalSnapshot(*p)
			if err != nil {
				return err
			}
			if _, err = provider.Normalize(snapshot, time.Now()); err != nil {
				return errors.New("external snapshot is stale; fetch a new proposal")
			}
			freshDiff, err := externalDiff(s, p.MemberID, p.ConnectionID, snapshot)
			if err != nil {
				return err
			}
			if !reflect.DeepEqual(normalizeJSON(freshDiff), normalizeJSON(p.Diff)) {
				return errors.New("stored diff does not match immutable snapshot")
			}
			allowed := map[string]ExternalDiff{}
			for _, d := range p.Diff {
				if d.ApplyAllowed {
					allowed[d.Field] = d
				}
			}
			seen := map[string]bool{}
			for _, field := range body.Fields {
				d, ok := allowed[field]
				if !ok || seen[field] {
					return errors.New("field is protected, absent from diff or repeated")
				}
				seen[field] = true
				switch field {
				case "member.name", "member.website", "member.peeringdbId":
					member := find(s, "members", p.MemberID)
					member[strings.TrimPrefix(field, "member.")] = d.After
					member["updatedAt"] = time.Now().UTC().Format(time.RFC3339Nano)
				case "connection.peeringdbNetixlanId":
					connection := find(s, "connections", p.ConnectionID)
					connection["peeringdbNetixlanId"] = d.After
					connection["updatedAt"] = time.Now().UTC().Format(time.RFC3339Nano)
				default:
					return errors.New("unsupported apply field")
				}
			}
			if err = validateAll(s); err != nil {
				return err
			}
			p.Status = "applied"
			p.AppliedFields = append([]string(nil), body.Fields...)
			sort.Strings(p.AppliedFields)
			p.AppliedBy = str(user, "id")
			p.AppliedAt = time.Now().UTC()
			applied = *p
			audit(s, str(user, "id"), "apply-external-profile", "members", p.MemberID)
			return nil
		}
		return errors.New("proposal not found")
	})
	if err != nil {
		problem(w, 409, err.Error())
		return
	}
	if applied.Status == "dismissed" {
		respond(w, 200, proposalMetadata(applied))
		return
	}
	view, err := a.proposalView(applied)
	if err != nil {
		problem(w, 500, "could not read applied snapshot")
		return
	}
	respond(w, 200, view)
}
func normalizeJSON(v any) any {
	raw, _ := json.Marshal(v)
	var normalized any
	json.Unmarshal(raw, &normalized)
	return normalized
}

// removePrunedArtifact requires the control-store mutex. It never removes a
// pending or unknown file, and only unlinks bytes matching the retained digest.
func (a *App) removePrunedArtifact(p ExternalProposal) (int64, error) {
	if !p.SnapshotPruned || p.Status == "pending" || !identifier.MatchString(p.ID) {
		return 0, errors.New("snapshot is not eligible for pruning")
	}
	path := filepath.Join(a.integrationDir(), p.ID+".json")
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil || !info.Mode().IsRegular() {
		return 0, errors.New("pruned snapshot is not a regular file")
	}
	if _, err = a.readExternalSnapshot(p); err != nil {
		return 0, errors.New("snapshot bytes changed; restore or inspect before pruning")
	}
	if err = os.Remove(path); err != nil && !os.IsNotExist(err) {
		return 0, err
	}
	dir, err := os.Open(a.integrationDir())
	if err != nil {
		return 0, err
	}
	err = dir.Sync()
	dir.Close()
	return info.Size(), err
}
func (a *App) externalRetention(w http.ResponseWriter, r *http.Request, user Record, session Session) {
	if str(user, "role") != "admin" || session.API {
		problem(w, 403, "browser administrator required for snapshot retention")
		return
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/integrations/proposals/"), "/")
	prune := r.Method == "POST" && len(parts) == 2 && parts[1] == "prune"
	remove := r.Method == "DELETE" && len(parts) == 1
	if (!prune && !remove) || !identifier.MatchString(parts[0]) {
		problem(w, 404, "retention action not found")
		return
	}
	var body struct {
		Checksum    string `json:"checksum"`
		Acknowledge bool   `json:"acknowledge"`
		Reason      string `json:"reason"`
	}
	if !decode(w, r, &body) {
		return
	}
	body.Reason = strings.TrimSpace(body.Reason)
	if !body.Acknowledge || len(body.Reason) > 1000 || strings.ContainsRune(body.Reason, 0) || (remove && body.Reason == "") {
		problem(w, 400, "explicit acknowledgement is required; deletion also needs a reason of 1..1000 bytes")
		return
	}
	var saved ExternalProposal
	var freed int64
	err := a.store.Update(func(s *State) error {
		if str(find(s, "users", str(user, "id")), "role") != "admin" {
			return errors.New("administrator permission changed")
		}
		for i := range s.ExternalProposals {
			p := &s.ExternalProposals[i]
			if p.ID != parts[0] {
				continue
			}
			if p.Status == "pending" || (p.Status != "applied" && p.Status != "dismissed") || p.Checksum != body.Checksum || proposalChecksum(*p) != p.Checksum {
				return errors.New("only a terminal proposal with its current review checksum can be retained/pruned")
			}
			if prune {
				if !p.SnapshotPruned {
					if _, err := a.readExternalSnapshot(*p); err != nil {
						return err
					}
					p.SnapshotPruned = true
					p.SnapshotPrunedAt = time.Now().UTC()
					p.SnapshotPrunedBy = str(user, "id")
					details := proposalMetadata(*p)
					details["retentionReason"] = body.Reason
					auditDetails(s, str(user, "id"), "prune-external-snapshot", "members", p.MemberID, details)
				}
				saved = *p
				return nil
			}
			if !p.SnapshotPruned {
				return errors.New("prune the terminal raw snapshot before deleting metadata")
			}
			var err error
			freed, err = a.removePrunedArtifact(*p)
			if err != nil {
				return err
			}
			details := proposalMetadata(*p)
			details["retentionReason"] = body.Reason
			auditDetails(s, str(user, "id"), "delete-external-proposal", "members", p.MemberID, details)
			saved = *p
			s.ExternalProposals = append(s.ExternalProposals[:i], s.ExternalProposals[i+1:]...)
			return nil
		}
		return errors.New("proposal not found")
	})
	if err != nil {
		problem(w, 409, err.Error())
		return
	}
	if prune {
		a.store.mu.Lock()
		if a.store.closed {
			err = errors.New("store closed before snapshot removal; restart will resume")
		} else {
			freed, err = a.removePrunedArtifact(saved)
		}
		a.store.mu.Unlock()
		if err != nil {
			problem(w, 500, "pruning was recorded but file removal is incomplete; retry or restart after resolving storage errors")
			return
		}
		view := proposalMetadata(saved)
		view["freedBytes"] = freed
		respond(w, 200, view)
		return
	}
	respond(w, 200, Record{"ok": true, "freedBytes": freed, "auditPreserved": true})
}
