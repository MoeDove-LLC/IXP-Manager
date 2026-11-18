package app

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/moedove/ixp-manager/internal/loa"
)

// PatchHistory is append-only. Snapshot records are copies, never pointers to
// current inventory; historical tenant ownership cannot follow a reassignment.
type PatchHistory struct {
	ID           string   `json:"id"`
	PortIDs      []string `json:"portIds"`
	AllocationID string   `json:"allocationId,omitempty"`
	MemberID     string   `json:"memberId,omitempty"`
	Action       string   `json:"action"`
	ActorID      string   `json:"actorId"`
	Reason       string   `json:"reason"`
	EffectiveAt  string   `json:"effectiveAt"`
	RecordedAt   string   `json:"recordedAt"`
	Before       []Record `json:"before"`
	After        []Record `json:"after"`
}
type LOA struct {
	ReservationChecksum string   `json:"reservationChecksum"`
	ID                  string   `json:"id"`
	PortIDs             []string `json:"portIds"`
	AllocationID        string   `json:"allocationId"`
	MemberID            string   `json:"memberId"`
	CreatedBy           string   `json:"createdBy"`
	CreatedAt           string   `json:"createdAt"`
	ExpiresAt           string   `json:"expiresAt"`
	RevokedAt           string   `json:"revokedAt,omitempty"`
	RevokedBy           string   `json:"revokedBy,omitempty"`
	RevocationReason    string   `json:"revocationReason,omitempty"`
	Snapshot            loa.Data `json:"snapshot"`
	PDFBlobID           string   `json:"pdfBlobId"`
	HTMLBlobID          string   `json:"htmlBlobId"`
	PDFSize             int64    `json:"pdfSize"`
	HTMLSize            int64    `json:"htmlSize"`
	Checksum            string   `json:"checksum"`
	HTMLChecksum        string   `json:"htmlChecksum"`
}

var patchProtected = []string{"status", "memberId", "circuitId", "switchPortId", "duplexPeerId", "allocationId", "assignedAt", "connectedAt", "ceaseRequestedAt", "ceasedAt", "lastEffectiveAt", "documentIds"}
var patchStatuses = map[string]bool{"available": true, "prewired": true, "reserved": true, "awaiting-connect": true, "connected": true, "awaiting-cease": true, "ceased": true, "broken": true}

func patchStatus(port Record) string {
	status := str(port, "status")
	if status == "" {
		return "available"
	}
	return status
}
func patchAllocated(port Record) bool {
	switch patchStatus(port) {
	case "reserved", "awaiting-connect", "connected", "awaiting-cease", "ceased":
		return true
	}
	return false
}
func patchFree(port Record) bool {
	return patchStatus(port) == "available" || patchStatus(port) == "prewired"
}
func patchHash(value any) string {
	b, _ := json.Marshal(value)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func patchCopies(records []Record) []Record {
	b, _ := json.Marshal(records)
	out := []Record{}
	_ = json.Unmarshal(b, &out)
	return out
}
func patchIDs(ports []Record) []string {
	ids := make([]string, 0, len(ports))
	for _, port := range ports {
		ids = append(ids, str(port, "id"))
	}
	sort.Strings(ids)
	return ids
}
func patchGroup(s *State, port Record) ([]Record, error) {
	if port == nil {
		return nil, fmt.Errorf("patch port not found")
	}
	ports := []Record{port}
	if peer := str(port, "duplexPeerId"); peer != "" {
		other := find(s, "patch-ports", peer)
		if other == nil || str(other, "duplexPeerId") != str(port, "id") || str(other, "panelId") != str(port, "panelId") || str(other, "id") == str(port, "id") {
			return nil, fmt.Errorf("duplex pairing is inconsistent")
		}
		ports = append(ports, other)
	}
	sort.Slice(ports, func(i, j int) bool { return str(ports[i], "id") < str(ports[j], "id") })
	return ports, nil
}
func patchRevision(ports []Record) string { return patchHash(ports) }
func patchText(value string, max int) bool {
	return strings.TrimSpace(value) != "" && len(value) <= max && !strings.ContainsAny(value, "\x00\r")
}

// normalizeLegacyPatching preserves old allocations without inventing dates or
// switch bindings. The stable ID changes only after a real release/reservation.
func normalizeLegacyPatching(s *State) error {
	for _, port := range s.Resources["patch-ports"] {
		if str(port, "status") == "" {
			port["status"] = "available"
		}
		if patchAllocated(port) && str(port, "allocationId") == "" {
			ports, err := patchGroup(s, port)
			if err != nil {
				return err
			}
			allocation := "legacy_" + patchHash(Record{"ports": patchIDs(ports), "member": str(port, "memberId")})[:24]
			for _, p := range ports {
				p["allocationId"] = allocation
			}
		}
	}
	return nil
}
func guardPatchResourceMutation(s *State, kind, method string, old, body Record) error {
	if kind != "patch-ports" {
		return nil
	}
	if method == "DELETE" {
		if old == nil {
			return nil
		}
		if patchAllocated(old) || str(old, "duplexPeerId") != "" || str(old, "switchPortId") != "" {
			return fmt.Errorf("release/unpair the port before deleting inventory")
		}

		return nil
	}
	if old == nil {
		status := patchStatus(body)
		if status != "available" && status != "prewired" {
			return fmt.Errorf("new inventory must start available or prewired; use lifecycle actions to allocate")
		}
		for _, key := range patchProtected {
			if key == "status" || (key == "switchPortId" && status == "prewired") {
				continue
			}
			v, exists := body[key]
			if exists && v != nil && v != "" {
				return fmt.Errorf("%s is controlled by the lifecycle workflow", key)
			}
		}
		if status == "prewired" && str(body, "switchPortId") == "" {
			return fmt.Errorf("prewired inventory requires a switch port")
		}
		body["status"] = status
		return nil
	}
	for _, key := range patchProtected {
		incoming, exists := body[key]
		previous, present := old[key]
		if exists && patchHash(incoming) != patchHash(previous) {
			return fmt.Errorf("%s can only change through the lifecycle workflow", key)
		}
		if present {
			body[key] = previous
		} else {
			delete(body, key)
		}
	}
	if patchAllocated(old) && (str(body, "panelId") != str(old, "panelId") || number(body, "number") != number(old, "number")) {
		return fmt.Errorf("allocated patch ports cannot move to another panel or number")
	}
	return nil
}
func recordPatchResourceChange(s *State, user Record, method string, before, after Record) error {
	ids := []string{}
	if before != nil {
		ids = append(ids, str(before, "id"))
	} else if after != nil {
		ids = append(ids, str(after, "id"))
	}
	if len(ids) == 0 {
		return nil
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	previous := []Record{}
	next := []Record{}
	if before != nil {
		previous = patchCopies([]Record{before})
	}
	if after != nil {
		next = patchCopies([]Record{after})
	}
	s.PatchHistory = append(s.PatchHistory, PatchHistory{ID: token(16), PortIDs: ids, AllocationID: str(after, "allocationId"), MemberID: str(after, "memberId"), Action: "inventory-" + strings.ToLower(method), ActorID: str(user, "id"), Reason: "Inventory metadata changed", EffectiveAt: now, RecordedAt: now, Before: previous, After: next})
	return nil
}

func validatePatching(s *State) error {
	bound := map[string]string{}
	for _, port := range s.Resources["patch-ports"] {
		status := patchStatus(port)
		if !patchStatuses[status] {
			return fmt.Errorf("unsupported patch port state")
		}
		ports, err := patchGroup(s, port)
		if err != nil {
			return err
		}
		groupKey := strings.Join(patchIDs(ports), ",")
		if len(ports) == 2 {
			other := ports[0]
			if str(other, "id") == str(port, "id") {
				other = ports[1]
			}
			for _, field := range []string{"status", "memberId", "allocationId", "switchPortId", "circuitId"} {
				if str(port, field) != str(other, field) {
					return fmt.Errorf("duplex ports must share %s", field)
				}
			}
			if patchHash(port["documentIds"]) != patchHash(other["documentIds"]) {
				return fmt.Errorf("duplex attachments must agree")
			}
		}
		member := str(port, "memberId")
		if patchAllocated(port) {
			if str(port, "allocationId") == "" {
				return fmt.Errorf("allocated patch port requires allocationId")
			}
			if member == "" && status != "reserved" {
				return fmt.Errorf("allocated patch port requires a member")
			}
		} else if member != "" || str(port, "allocationId") != "" {
			return fmt.Errorf("free/broken patch port cannot retain a tenant allocation")
		}
		if member != "" && find(s, "members", member) == nil {
			return fmt.Errorf("patch port references missing member")
		}
		if switchID := str(port, "switchPortId"); switchID != "" {
			physical := find(s, "ports", switchID)
			if physical == nil {
				return fmt.Errorf("patch binding references missing switch port")
			}
			if existing := bound[switchID]; existing != "" && existing != groupKey {
				return fmt.Errorf("switch port already bound to a different patch allocation")
			}
			bound[switchID] = groupKey
			if member != "" {
				if owner := str(physical, "memberId"); owner != "" && owner != member {
					return fmt.Errorf("switch port belongs to another member")
				}
				for _, connection := range s.Resources["connections"] {
					ids, e := connectionPortIDs(connection)
					if e == nil && slices.Contains(ids, switchID) && str(connection, "memberId") != member {
						return fmt.Errorf("switch port carries another member's connection")
					}
				}
			}
		} else if status == "prewired" {
			return fmt.Errorf("prewired state requires a physical switch port")
		}
		if err = validatePatchDocuments(s, port); err != nil {
			return err
		}
		for _, field := range []string{"assignedAt", "connectedAt", "ceaseRequestedAt", "ceasedAt", "lastEffectiveAt"} {
			if value := str(port, field); value != "" {
				if _, e := time.Parse(time.RFC3339Nano, value); e != nil {
					return fmt.Errorf("invalid lifecycle date %s", field)
				}
			}
		}
	}
	historyIDs := map[string]bool{}
	for _, entry := range s.PatchHistory {
		if entry.ID == "" || historyIDs[entry.ID] || len(entry.PortIDs) < 1 || len(entry.PortIDs) > 2 || !patchText(entry.Reason, 2000) {
			return fmt.Errorf("invalid immutable patch history")
		}
		historyIDs[entry.ID] = true
	}
	loaIDs := map[string]bool{}
	for _, letter := range s.LOAs {
		if letter.ID == "" || loaIDs[letter.ID] || letter.AllocationID == "" || letter.MemberID == "" || len(letter.PortIDs) < 1 || len(letter.PortIDs) > 2 || !blobName.MatchString(letter.PDFBlobID) || !blobName.MatchString(letter.HTMLBlobID) || !digestPattern.MatchString(letter.Checksum) || !digestPattern.MatchString(letter.HTMLChecksum) || letter.PDFSize < 1 || letter.PDFSize > 4<<20 || letter.HTMLSize < 1 || letter.HTMLSize > 1<<20 {
			return fmt.Errorf("invalid immutable LOA artifact metadata")
		}
		loaIDs[letter.ID] = true
		if letter.Snapshot.ID != letter.ID || letter.Snapshot.AllocationID != letter.AllocationID || letter.Snapshot.IssuedAt != letter.CreatedAt || letter.Snapshot.ExpiresAt != letter.ExpiresAt {
			return fmt.Errorf("LOA content snapshot identity mismatch")
		}
		created, e := time.Parse(time.RFC3339Nano, letter.CreatedAt)
		expires, ee := time.Parse(time.RFC3339Nano, letter.ExpiresAt)
		if e != nil || ee != nil || !expires.After(created) || expires.Sub(created) > 30*24*time.Hour {
			return fmt.Errorf("invalid LOA expiry")
		}
	}
	return nil
}
func validatePatchDocuments(s *State, port Record) error {
	value, exists := port["documentIds"]
	if !exists {
		return nil
	}
	ids, ok := value.([]any)
	if !ok || len(ids) > 20 {
		return fmt.Errorf("patch attachments must contain at most20 document IDs")
	}
	seen := map[string]bool{}
	for _, value := range ids {
		id, ok := value.(string)
		if !ok || seen[id] {
			return fmt.Errorf("patch attachment IDs must be distinct strings")
		}
		seen[id] = true
		document := documentByID(s, id)
		if document == nil {
			return fmt.Errorf("patch attachment document is missing")
		}
		member := str(port, "memberId")
		if document.Visibility != "staff" && document.Visibility != "member" {
			return fmt.Errorf("patch attachments must use staff/member document scope")
		}
		if document.MemberID != "" && document.MemberID != member {
			return fmt.Errorf("attachment belongs to another tenant")
		}
		if document.Visibility == "member" && member == "" {
			return fmt.Errorf("member attachments require a current tenant")
		}
	}
	return nil
}
func patchBytesHash(data []byte) string { h := sha256.Sum256(data); return hex.EncodeToString(h[:]) }

func patchAvailableActions(port Record, user Record) []string {
	if role := str(user, "role"); role != "admin" && role != "operator" {
		return []string{}
	}
	switch patchStatus(port) {
	case "available", "prewired":
		actions := []string{"reserve", "prewire", "mark-broken"}
		if patchStatus(port) == "prewired" {
			actions = append(actions, "release")
		}
		if str(port, "duplexPeerId") == "" {
			return append(actions, "pair")
		}
		if str(port, "switchPortId") == "" {
			return append(actions, "unpair")
		}
		return actions
	case "reserved":
		if str(port, "memberId") == "" {
			return []string{"release"}
		}
		return []string{"request-connect", "connect", "release"}
	case "awaiting-connect":
		return []string{"connect", "release"}
	case "connected":
		return []string{"request-cease", "cease"}
	case "awaiting-cease":
		return []string{"cease"}
	case "ceased":
		return []string{"release"}
	case "broken":
		return []string{"repair"}
	}
	return []string{}
}
func patchPortView(s *State, port, user Record) Record {
	fields := []string{"id", "name", "panelId", "number", "status", "memberId", "circuitId", "switchPortId", "duplexPeerId", "allocationId", "assignedAt", "connectedAt", "ceaseRequestedAt", "ceasedAt", "lastEffectiveAt"}
	out := Record{}
	for _, field := range fields {
		if value, ok := port[field]; ok {
			out[field] = value
		}
	}
	if str(user, "role") != "member" {
		out["notes"] = str(port, "notes")
	}
	panel := find(s, "patch-panels", str(port, "panelId"))
	rack := find(s, "racks", str(panel, "rackId"))
	location := find(s, "locations", str(rack, "locationId"))
	physical := find(s, "ports", str(port, "switchPortId"))
	sw := find(s, "switches", str(physical, "switchId"))
	member := find(s, "members", str(port, "memberId"))
	out["panelName"] = str(panel, "name")
	out["rackName"] = str(rack, "name")
	out["locationName"] = str(location, "name")
	out["switchPortName"] = str(physical, "name")
	out["switchName"] = str(sw, "name")
	out["memberName"] = str(member, "name")
	ports, _ := patchGroup(s, port)
	out["revision"] = patchRevision(ports)
	out["availableActions"] = patchAvailableActions(port, user)
	attachments := []Record{}
	ids, _ := port["documentIds"].([]any)
	for _, raw := range ids {
		id, _ := raw.(string)
		d := documentByID(s, id)
		if d != nil && documentReadable(s, *d, user) && (d.MemberID == "" || d.MemberID == str(port, "memberId")) {
			attachments = append(attachments, Record{"id": d.ID, "name": d.Name, "visibility": d.Visibility, "checksum": d.Checksum, "version": d.Version})
		}
	}
	out["attachments"] = attachments
	out["attachments"] = append(attachments, legacyPatchPortAttachments(s, port, user)...)
	return out
}
func visiblePatchPorts(s *State, user Record) []Record {
	out := []Record{}
	for _, port := range s.Resources["patch-ports"] {
		if str(user, "role") == "member" && (!patchAllocated(port) || str(port, "memberId") != str(user, "memberId")) {
			continue
		}
		out = append(out, patchPortView(s, port, user))
	}
	return out
}

type patchTransition struct {
	DuplexPeerRevision string    `json:"duplexPeerRevision"`
	Action             string    `json:"action"`
	Revision           string    `json:"revision"`
	Reason             string    `json:"reason"`
	EffectiveAt        string    `json:"effectiveAt"`
	MemberID           string    `json:"memberId"`
	DuplexPeerID       string    `json:"duplexPeerId"`
	SwitchPortID       string    `json:"switchPortId"`
	CircuitID          string    `json:"circuitId"`
	DocumentIDs        *[]string `json:"documentIds"`
	KeepPrewired       bool      `json:"keepPrewired"`
}

func transitionPatch(s *State, user Record, id string, input patchTransition, now time.Time) ([]Record, error) {
	port := find(s, "patch-ports", id)
	ports, err := patchGroup(s, port)
	if err != nil {
		return nil, err
	}
	if !slices.Contains(patchAvailableActions(port, user), input.Action) {
		return nil, fmt.Errorf("transition is not allowed in the current state")
	}
	if input.Revision == "" || input.Revision != patchRevision(ports) {
		return nil, fmt.Errorf("patch allocation changed; refresh before retrying")
	}
	if !patchText(input.Reason, 2000) {
		return nil, fmt.Errorf("reason is required (maximum2000 bytes)")
	}
	effective := now.UTC()
	if input.EffectiveAt != "" {
		effective, err = time.Parse(time.RFC3339Nano, input.EffectiveAt)
		if err != nil {
			return nil, fmt.Errorf("effectiveAt must be RFC3339")
		}
	}
	if effective.After(now.Add(time.Minute)) {
		return nil, fmt.Errorf("future effective dates are not allowed for completed transitions")
	}
	before := patchCopies(ports)
	if input.DuplexPeerID != "" {
		if input.Action != "reserve" && input.Action != "prewire" && input.Action != "pair" {
			return nil, fmt.Errorf("pairing is only allowed before allocation")
		}
		if len(ports) != 1 || input.DuplexPeerID == id {
			return nil, fmt.Errorf("port already paired or self-pair requested")
		}
		other := find(s, "patch-ports", input.DuplexPeerID)
		if other == nil || str(other, "panelId") != str(port, "panelId") || !patchFree(other) || str(other, "duplexPeerId") != "" {
			return nil, fmt.Errorf("duplex partner must be an unpaired free port on the same panel")
		}
		if input.DuplexPeerRevision == "" || input.DuplexPeerRevision != patchRevision([]Record{other}) {
			return nil, fmt.Errorf("duplex partner changed; refresh its revision before pairing")
		}
		if str(other, "switchPortId") != "" && str(port, "switchPortId") != "" && str(other, "switchPortId") != str(port, "switchPortId") {
			return nil, fmt.Errorf("duplex ports have conflicting physical bindings")
		}
		ports = append(ports, other)
		before = patchCopies(ports)
		port["duplexPeerId"] = str(other, "id")
		other["duplexPeerId"] = id
	}
	if input.Action == "pair" && len(ports) != 2 {
		return nil, fmt.Errorf("duplexPeerId is required")
	}
	for _, p := range ports {
		if previous := str(p, "lastEffectiveAt"); previous != "" {
			last, e := time.Parse(time.RFC3339Nano, previous)
			if e != nil || effective.Before(last) {
				return nil, fmt.Errorf("effective date precedes the previous transition")
			}
		}
	}
	if input.MemberID != "" && input.Action != "reserve" && input.MemberID != str(port, "memberId") {
		return nil, fmt.Errorf("member reassignment requires release and a new reservation")
	}
	if input.SwitchPortID != "" && input.SwitchPortID != str(port, "switchPortId") && !slices.Contains([]string{"reserve", "prewire", "connect", "pair"}, input.Action) {
		return nil, fmt.Errorf("physical binding cannot change during this transition")
	}
	if input.CircuitID != "" && input.CircuitID != str(port, "circuitId") && !slices.Contains([]string{"reserve", "prewire", "connect", "pair"}, input.Action) {
		return nil, fmt.Errorf("circuit cannot change during this transition")
	}
	member := str(port, "memberId")
	binding := str(port, "switchPortId")
	circuit := str(port, "circuitId")
	if input.SwitchPortID != "" {
		binding = input.SwitchPortID
	}
	if input.CircuitID != "" {
		if len(input.CircuitID) > 256 || strings.ContainsAny(input.CircuitID, "\x00\r\n") {
			return nil, fmt.Errorf("invalid circuit ID")
		}
		circuit = input.CircuitID
	}
	if binding == "" && len(ports) == 2 {
		binding = str(ports[1], "switchPortId")
	}
	switch input.Action {
	case "reserve":
		m := find(s, "members", input.MemberID)
		if m == nil || (str(m, "status") == "suspended" || str(m, "status") == "closed") {
			return nil, fmt.Errorf("reserve requires an existing non-suspended member")
		}
		member = input.MemberID
		allocation := token(16)
		for _, p := range ports {
			p["status"] = "reserved"
			p["memberId"] = member
			p["allocationId"] = allocation
			p["assignedAt"] = effective.UTC().Format(time.RFC3339Nano)
		}
	case "prewire":
		if binding == "" {
			return nil, fmt.Errorf("prewire requires switchPortId")
		}
		for _, p := range ports {
			p["status"] = "prewired"
		}
	case "pair":
		status := "available"
		if binding != "" {
			status = "prewired"
		}
		for _, p := range ports {
			p["status"] = status
		}
	case "unpair":
		for _, p := range ports {
			delete(p, "duplexPeerId")
		}
		if binding != "" {
			return nil, fmt.Errorf("release physical prewire binding before unpairing")
		}
	case "request-connect":
		for _, p := range ports {
			p["status"] = "awaiting-connect"
		}
	case "connect":
		if member == "" || binding == "" || circuit == "" {
			return nil, fmt.Errorf("connect requires member, physical switch binding and circuit ID")
		}
		for _, p := range ports {
			p["status"] = "connected"
			p["connectedAt"] = effective.UTC().Format(time.RFC3339Nano)
		}
	case "request-cease":
		for _, p := range ports {
			p["status"] = "awaiting-cease"
			p["ceaseRequestedAt"] = effective.UTC().Format(time.RFC3339Nano)
		}
	case "cease":
		for _, p := range ports {
			p["status"] = "ceased"
			p["ceasedAt"] = effective.UTC().Format(time.RFC3339Nano)
		}
	case "release":
		for _, p := range ports {
			for _, key := range []string{"memberId", "allocationId", "circuitId", "assignedAt", "connectedAt", "ceaseRequestedAt", "ceasedAt", "documentIds"} {
				delete(p, key)
			}
			p["status"] = "available"
			if input.KeepPrewired {
				if binding == "" {
					return nil, fmt.Errorf("retaining prewire requires a physical binding")
				}
				p["status"] = "prewired"
			} else {
				delete(p, "switchPortId")
				delete(p, "duplexPeerId")
			}
		}
		member = ""
		circuit = ""
		if !input.KeepPrewired {
			binding = ""
		}
		input.DocumentIDs = nil
	case "mark-broken":
		for _, p := range ports {
			p["status"] = "broken"
		}
	case "repair":
		for _, p := range ports {
			p["status"] = "available"
			if binding != "" {
				p["status"] = "prewired"
			}
		}
	}
	for _, p := range ports {
		if binding != "" {
			p["switchPortId"] = binding
		}
		if circuit != "" {
			p["circuitId"] = circuit
		}
		if input.DocumentIDs != nil {
			ids := []any{}
			for _, id := range *input.DocumentIDs {
				ids = append(ids, id)
			}
			p["documentIds"] = ids
		}
		p["updatedAt"] = now.UTC().Format(time.RFC3339Nano)
		p["lastEffectiveAt"] = effective.UTC().Format(time.RFC3339Nano)
	}
	if err = validateAll(s); err != nil {
		return nil, err
	}
	if err = validatePatching(s); err != nil {
		return nil, err
	}
	historyMember := member
	allocation := str(port, "allocationId")
	if input.Action == "release" {
		historyMember = str(before[0], "memberId")
		allocation = str(before[0], "allocationId")
	}
	s.PatchHistory = append(s.PatchHistory, PatchHistory{ID: token(16), PortIDs: patchIDs(ports), AllocationID: allocation, MemberID: historyMember, Action: input.Action, ActorID: str(user, "id"), Reason: input.Reason, EffectiveAt: effective.UTC().Format(time.RFC3339Nano), RecordedAt: now.UTC().Format(time.RFC3339Nano), Before: before, After: patchCopies(ports)})
	audit(s, str(user, "id"), input.Action, "patch-ports", id)
	return ports, nil
}

func patchLOAData(s *State, ports []Record) loa.Data {
	p := ports[0]
	member := find(s, "members", str(p, "memberId"))
	panel := find(s, "patch-panels", str(p, "panelId"))
	rack := find(s, "racks", str(panel, "rackId"))
	location := find(s, "locations", str(rack, "locationId"))
	physical := find(s, "ports", str(p, "switchPortId"))
	sw := find(s, "switches", str(physical, "switchId"))
	data := loa.Data{ExchangeName: str(s.Settings, "name"), MemberName: str(member, "name"), MemberASN: uint32(number(member, "asn")), PanelName: str(panel, "name"), Location: strings.TrimSpace(str(location, "name") + " " + str(location, "city") + " " + str(location, "address")), RackName: str(rack, "name"), CircuitID: str(p, "circuitId"), AllocationID: str(p, "allocationId"), Ports: []string{}}
	if physical != nil {
		data.SwitchPort = str(sw, "name") + " / " + str(physical, "name")
	}
	for _, port := range ports {
		data.Ports = append(data.Ports, fmt.Sprintf("%s (#%g)", str(port, "name"), number(port, "number")))
	}
	return data
}
func patchReservationChecksum(s *State, ports []Record) string {
	return patchHash(struct {
		Data loa.Data `json:"data"`
		IDs  []string `json:"portIds"`
	}{patchLOAData(s, ports), patchIDs(ports)})
}
func loaByID(s *State, id string) *LOA {
	for i := range s.LOAs {
		if s.LOAs[i].ID == id {
			return &s.LOAs[i]
		}
	}
	return nil
}
func loaStatus(s *State, letter LOA, now time.Time) string {
	if letter.RevokedAt != "" {
		return "revoked"
	}
	expires, err := time.Parse(time.RFC3339Nano, letter.ExpiresAt)
	if err != nil {
		return "stale"
	}
	if !now.Before(expires) {
		return "expired"
	}
	if len(letter.PortIDs) == 0 {
		return "stale"
	}
	ports, err := patchGroup(s, find(s, "patch-ports", letter.PortIDs[0]))
	if err != nil || !slices.Equal(patchIDs(ports), letter.PortIDs) {
		return "stale"
	}
	for _, p := range ports {
		if str(p, "memberId") != letter.MemberID || str(p, "allocationId") != letter.AllocationID || (patchStatus(p) != "reserved" && patchStatus(p) != "awaiting-connect") {
			return "stale"
		}
	}
	member := find(s, "members", letter.MemberID)
	if member == nil || str(member, "status") == "suspended" || str(member, "status") == "closed" {
		return "stale"
	}
	if patchReservationChecksum(s, ports) != letter.ReservationChecksum {
		return "stale"
	}
	return "issued"
}
func loaView(s *State, letter LOA, now time.Time) Record {
	return Record{"id": letter.ID, "portIds": letter.PortIDs, "portNames": letter.Snapshot.Ports, "memberId": letter.MemberID, "memberName": letter.Snapshot.MemberName, "allocationId": letter.AllocationID, "createdAt": letter.CreatedAt, "expiresAt": letter.ExpiresAt, "revokedAt": letter.RevokedAt, "status": loaStatus(s, letter, now), "checksum": letter.Checksum, "htmlChecksum": letter.HTMLChecksum}
}
func loaUsage(s *State) int64 {
	total := int64(0)
	for _, letter := range s.LOAs {
		total += letter.PDFSize + letter.HTMLSize
	}
	return total
}

// patching handles authenticated lifecycle routes. The global HTTP layer owns
// session/token authorization and CSRF; this handler also enforces tenant scope.
func (a *App) patching(w http.ResponseWriter, r *http.Request, user Record) bool {
	if !strings.HasPrefix(r.URL.Path, "/api/patching") {
		return false
	}
	parts := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/patching"), "/"), "/")
	if len(parts) == 1 && parts[0] == "legacy" {
		a.legacyPatchList(w, r, user)
		return true
	}
	if !slices.Contains([]string{"admin", "operator", "viewer", "member"}, str(user, "role")) {
		problem(w, 403, "authenticated account required")
		return true
	}
	staff := str(user, "role") != "member"
	write := str(user, "role") == "admin" || str(user, "role") == "operator"
	if r.Method != "GET" && !write {
		problem(w, 403, "staff operator access required")
		return true
	}
	s := a.store.Snapshot()
	now := time.Now().UTC()
	if len(parts) == 1 && parts[0] == "ports" && r.Method == "GET" {
		respond(w, 200, Record{"items": visiblePatchPorts(&s, user)})
		return true
	}
	if len(parts) == 1 && parts[0] == "history" && r.Method == "GET" {
		if !staff {
			problem(w, 403, "patch history is staff-only")
			return true
		}
		items := []PatchHistory{}
		filter := r.URL.Query().Get("portId")
		for _, entry := range s.PatchHistory {
			if filter == "" || slices.Contains(entry.PortIDs, filter) {
				items = append(items, entry)
			}
		}
		respond(w, 200, Record{"items": items})
		return true
	}
	if len(parts) == 1 && parts[0] == "loas" && r.Method == "GET" {
		items := []Record{}
		for _, letter := range s.LOAs {
			if !staff && (letter.MemberID != str(user, "memberId") || loaStatus(&s, letter, now) != "issued") {
				continue
			}
			items = append(items, loaView(&s, letter, now))
		}
		respond(w, 200, Record{"items": items})
		return true
	}
	if len(parts) == 3 && parts[0] == "ports" && parts[2] == "transition" && r.Method == "POST" {
		var input patchTransition
		if !decode(w, r, &input) {
			return true
		}
		views := []Record{}
		err := a.store.Update(func(s *State) error {
			ports, err := transitionPatch(s, user, parts[1], input, now)
			if err != nil {
				return err
			}
			for _, port := range ports {
				views = append(views, patchPortView(s, port, user))
			}
			return nil
		})
		if err != nil {
			problem(w, 409, err.Error())
		} else {
			respond(w, 200, Record{"items": views})
		}
		return true
	}
	if len(parts) == 3 && parts[0] == "ports" && parts[2] == "loas" && r.Method == "POST" {
		a.issueLOA(w, r, user, parts[1], now)
		return true
	}
	if len(parts) == 3 && parts[0] == "loas" && parts[2] == "revoke" && r.Method == "POST" {
		var input struct {
			Reason string `json:"reason"`
		}
		if !decode(w, r, &input) {
			return true
		}
		if !patchText(input.Reason, 2000) {
			problem(w, 400, "revocation reason required")
			return true
		}
		var view Record
		err := a.store.Update(func(s *State) error {
			letter := loaByID(s, parts[1])
			if letter == nil {
				return fmt.Errorf("LOA not found")
			}
			if letter.RevokedAt != "" {
				return fmt.Errorf("LOA already revoked")
			}
			letter.RevokedAt = now.Format(time.RFC3339Nano)
			letter.RevokedBy = str(user, "id")
			letter.RevocationReason = input.Reason
			s.PatchHistory = append(s.PatchHistory, PatchHistory{ID: token(16), PortIDs: append([]string(nil), letter.PortIDs...), AllocationID: letter.AllocationID, MemberID: letter.MemberID, Action: "revoke-loa", ActorID: str(user, "id"), Reason: input.Reason, EffectiveAt: now.Format(time.RFC3339Nano), RecordedAt: now.Format(time.RFC3339Nano), Before: []Record{}, After: []Record{}})
			audit(s, str(user, "id"), "revoke", "loas", letter.ID)
			view = loaView(s, *letter, now)
			return nil
		})
		if err != nil {
			problem(w, 409, err.Error())
		} else {
			respond(w, 200, view)
		}
		return true
	}
	if len(parts) == 3 && parts[0] == "loas" && parts[2] == "download" && r.Method == "GET" {
		letter := loaByID(&s, parts[1])
		if letter == nil || (!staff && letter.MemberID != str(user, "memberId")) {
			problem(w, 404, "LOA not found")
			return true
		}
		if loaStatus(&s, *letter, now) != "issued" {
			problem(w, 409, "LOA is no longer current or valid")
			return true
		}
		format := r.URL.Query().Get("format")
		if format == "" {
			format = "pdf"
		}
		var data []byte
		var err error
		var checksum string
		switch format {
		case "pdf":
			data, err = readLOABlob(a, letter.PDFBlobID, letter.PDFSize, letter.Checksum)
			checksum = letter.Checksum
		case "html":
			data, err = readLOABlob(a, letter.HTMLBlobID, letter.HTMLSize, letter.HTMLChecksum)
			checksum = letter.HTMLChecksum
		default:
			problem(w, 400, "format must be pdf or html")
			return true
		}
		if err != nil {
			problem(w, 500, "LOA artifact integrity check failed")
			return true
		}
		w.Header().Set("X-Content-SHA256", checksum)
		if format == "pdf" {
			if !bytes.HasPrefix(data, []byte("%PDF-")) {
				problem(w, 500, "LOA PDF is corrupt")
				return true
			}
			w.Header().Set("Content-Type", "application/pdf")
			w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="LOA-%s.pdf"`, letter.ID))
		} else {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Content-Disposition", fmt.Sprintf(`inline; filename="LOA-%s.html"`, letter.ID))
			w.Header().Set("Content-Security-Policy", "default-src 'none'; img-src data:; style-src 'unsafe-inline'; base-uri 'none'; frame-ancestors 'none'; sandbox")
		}
		_, _ = w.Write(data)
		return true
	}
	problem(w, 404, "patching endpoint not found")
	return true
}
func (a *App) issueLOA(w http.ResponseWriter, r *http.Request, user Record, id string, now time.Time) {
	var input struct {
		Revision  string `json:"revision"`
		ExpiresAt string `json:"expiresAt"`
		Reason    string `json:"reason"`
	}
	if !decode(w, r, &input) {
		return
	}
	expires, err := time.Parse(time.RFC3339Nano, input.ExpiresAt)
	if err != nil || !expires.After(now) || expires.Sub(now) > 30*24*time.Hour {
		problem(w, 400, "LOA expiry must be future and within30 days")
		return
	}
	if !patchText(input.Reason, 2000) {
		problem(w, 400, "LOA issuance reason required")
		return
	}
	s := a.store.Snapshot()
	ports, err := patchGroup(&s, find(&s, "patch-ports", id))
	if err != nil {
		problem(w, 404, err.Error())
		return
	}
	if input.Revision == "" || patchRevision(ports) != input.Revision {
		problem(w, 409, "patch allocation changed; refresh before issuing LOA")
		return
	}
	for _, port := range ports {
		if str(port, "memberId") == "" || (patchStatus(port) != "reserved" && patchStatus(port) != "awaiting-connect") {
			problem(w, 409, "LOA requires a current member reservation awaiting connection")
			return
		}
	}
	letter := LOA{ID: token(16), PortIDs: patchIDs(ports), MemberID: str(ports[0], "memberId"), AllocationID: str(ports[0], "allocationId"), CreatedBy: str(user, "id"), CreatedAt: now.Format(time.RFC3339Nano), ExpiresAt: expires.UTC().Format(time.RFC3339Nano), ReservationChecksum: patchReservationChecksum(&s, ports)}
	letter.Snapshot = patchLOAData(&s, ports)
	letter.Snapshot.ID = letter.ID
	letter.Snapshot.IssuedAt = letter.CreatedAt
	letter.Snapshot.ExpiresAt = letter.ExpiresAt
	letter.Snapshot.Reason = input.Reason
	pdf, html, err := loa.Generate(letter.Snapshot)
	if err != nil {
		problem(w, 422, err.Error())
		return
	}
	pdfBlob, err := a.writeBlob(bytes.NewReader(pdf))
	if err != nil {
		problem(w, 500, "failed to store LOA PDF")
		return
	}
	htmlBlob, err := a.writeBlob(strings.NewReader(html))
	if err != nil {
		a.discardBlob(pdfBlob.BlobID)
		problem(w, 500, "failed to store LOA HTML")
		return
	}
	committed := false
	defer func() {
		if !committed {
			a.discardBlob(pdfBlob.BlobID)
			a.discardBlob(htmlBlob.BlobID)
		}
	}()
	letter.PDFBlobID = pdfBlob.BlobID
	letter.HTMLBlobID = htmlBlob.BlobID
	letter.PDFSize = pdfBlob.Size
	letter.HTMLSize = htmlBlob.Size
	letter.Checksum = pdfBlob.Checksum
	letter.HTMLChecksum = htmlBlob.Checksum
	var view Record
	err = a.store.Update(func(s *State) error {
		commitNow := time.Now().UTC()
		if !expires.After(commitNow) {
			return fmt.Errorf("LOA expired before issuance completed")
		}
		current, e := patchGroup(s, find(s, "patch-ports", id))
		if e != nil {
			return e
		}
		if patchRevision(current) != input.Revision || patchReservationChecksum(s, current) != letter.ReservationChecksum {
			return fmt.Errorf("reservation changed during document rendering")
		}
		for _, existing := range s.LOAs {
			if existing.AllocationID == letter.AllocationID && loaStatus(s, existing, commitNow) == "issued" {
				return fmt.Errorf("a valid LOA already exists for this reservation; revoke it before reissuing")
			}
		}
		if blobUsage(s)+letter.PDFSize+letter.HTMLSize > a.documentQuota {
			return fmt.Errorf("document quota exceeded")
		}
		s.LOAs = append(s.LOAs, letter)
		s.PatchHistory = append(s.PatchHistory, PatchHistory{ID: token(16), PortIDs: letter.PortIDs, AllocationID: letter.AllocationID, MemberID: letter.MemberID, Action: "issue-loa", ActorID: str(user, "id"), Reason: input.Reason, EffectiveAt: letter.CreatedAt, RecordedAt: letter.CreatedAt, Before: patchCopies(current), After: patchCopies(current)})
		if e = validatePatching(s); e != nil {
			return e
		}
		audit(s, str(user, "id"), "issue", "loas", letter.ID)
		view = loaView(s, letter, commitNow)
		return nil
	})
	if err != nil {
		problem(w, 409, err.Error())
	} else {
		committed = true
		respond(w, 201, view)
	}
}

func readLOABlob(a *App, id string, size int64, checksum string) ([]byte, error) {
	if size < 1 || size > 4<<20 {
		return nil, fmt.Errorf("invalid artifact size")
	}
	file, err := a.openBlob(id)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, size+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) != size || patchBytesHash(data) != checksum {
		return nil, fmt.Errorf("artifact checksum mismatch")
	}
	return data, nil
}
