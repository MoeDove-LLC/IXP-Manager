package app

import (
	"errors"
	"net/http"
	"regexp"
	"sort"
	"time"

	"github.com/moedove/ixp-manager/internal/sflow"
)

var deliveryNoncePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)

type receiverApprovalCache struct {
	Version  uint64
	Valid    bool
	Bindings map[receiverSampleBinding]bool
}
type receiverSampleBinding struct {
	ExporterID, SwitchID, ConnectionID, VLANID, MAC, Direction string
	IfIndex                                                    uint32
}
type deliveryAllowance struct {
	Revision string
	Samples  int
}
type pendingDelivery struct {
	TokenOwner, ExporterID, SwitchID, MappingRevision, DatagramDigest string
	ExpiresAt                                                         time.Time
	Allowed                                                           map[string]deliveryAllowance
	ReportHash                                                        string
}
type deliveryObservation struct {
	ReceiptRevision    string
	AuthorizationCount uint64
	LastAuthorizedAt   time.Time
	LastReceiptAt      time.Time
	Outcome            string
	Datagrams, Bytes   int
}

func (a *App) canSFlowForward(s *State, user Record, session Session, switchID string) bool {
	if !session.API {
		return false
	}
	actor, _, err := a.sflowActor(s, user, session, true)
	if err != nil || actor == nil {
		return false
	}
	for _, t := range s.Security[str(actor, "id")].Tokens {
		if t.ID == session.TokenID && time.Now().Before(t.ExpiresAt) && contains(t.Scopes, "sflow:forward") && contains(t.SwitchIDs, switchID) {
			return true
		}
	}
	return false
}
func deliveryOwner(session Session) string { return digest(session.UserID + "\x00" + session.TokenID) }
func (a *App) deliveryView(id, revision string) Record {
	a.deliveryMu.Lock()
	defer a.deliveryMu.Unlock()
	o, ok := a.deliveryStatus[id]
	if !ok {
		return Record{"authorizationCount": 0, "deliveryState": "unknown", "evidencePersistence": "current-process"}
	}
	state := o.Outcome
	if state == "" {
		state = "authorization-only"
	}
	return Record{"authorizationCount": o.AuthorizationCount, "lastAuthorizedAt": optionalTime(o.LastAuthorizedAt), "lastReceiptAt": optionalTime(o.LastReceiptAt), "deliveryState": state, "receiptDatagrams": o.Datagrams, "receiptBytes": o.Bytes, "receiptRevision": o.ReceiptRevision, "receiptForCurrentRevision": o.ReceiptRevision == revision, "evidencePersistence": "current-process", "notice": "这是collector本地socket操作回执，不证明远端收到；重启后未知。"}
}

// Called with sflowMu held, after the sampled store accepted this new event.
// Approval and allocation are checked again after that durable write; an
// intervening change may record observations but can never authorize delivery.
func (a *App) authorizeSFlowDelivery(user Record, session Session, b sflow.Batch, flows []sflow.AttributedFlow) (*sflow.DeliveryEnvelope, error) {
	a.store.mu.RLock()
	defer a.store.mu.RUnlock()
	s := &a.store.state
	if a.store.closed || !a.canSFlowForward(s, user, session, b.SwitchID) {
		return nil, errors.New("forwarding authority changed")
	}
	e := find(s, sflowExporterKind, b.ExporterID)
	if e == nil || str(e, "switchId") != b.SwitchID {
		return nil, errors.New("exporter changed")
	}
	current, err := a.flowMapping(s, e)
	if err != nil || current.Revision != b.MappingRevision {
		return nil, errors.New("mapping changed before forwarding authorization")
	}
	now := time.Now().UTC()
	envelope := &sflow.DeliveryEnvelope{Version: 1, RequestNonce: b.RequestNonce, DatagramDigest: b.DatagramDigest, ExporterID: b.ExporterID, SwitchID: b.SwitchID, MappingRevision: b.MappingRevision, IssuedAt: now, ExpiresAt: now.Add(sflow.MaxDeliveryTTL), Deliveries: []sflow.Delivery{}}
	if a.receiverApprovals == nil {
		a.receiverApprovals = map[string]receiverApprovalCache{}
	}
	for _, id := range sortedReceiverIDs(s) {
		r := receiverByID(s, id)
		if r == nil || !r.Spec.Enabled || r.Status != "approved" || b.ReceivedAt.Before(r.ApprovedAt) {
			continue
		}
		cached, ok := a.receiverApprovals[id]
		if !ok || cached.Version != a.store.revision {
			plan, sum, e := a.receiverPlan(s, *r, now)
			cached = receiverApprovalCache{Version: a.store.revision, Valid: e == nil && sum == r.ApprovedChecksum, Bindings: map[receiverSampleBinding]bool{}}
			if cached.Valid {
				for _, binding := range plan.Bindings {
					cached.Bindings[receiverSampleBinding{binding.ExporterID, binding.SwitchID, binding.ConnectionID, binding.VLANID, binding.MAC, binding.Direction, binding.IfIndex}] = true
				}
			}
			a.receiverApprovals[id] = cached
		}
		if !cached.Valid {
			continue
		}
		indices := []int{}
		for i, f := range flows {
			if i >= len(b.Datagram.Flows) || f.UnattributedReason != "" || !contains(r.Spec.VLANIDs, f.VLANID) {
				continue
			}
			// Statistical attribution may use a unique physical ingress owner.
			// Header disclosure additionally requires the exact reviewed
			// exporter, physical interface and registered MAC tuple. A known
			// destination MAC alone cannot add unreviewed remote exporters.
			raw := b.Datagram.Flows[i]
			source := f.SourceMemberID == r.Spec.MemberID && f.SourceMemberRevision == r.MemberRevision && contains(r.Spec.ConnectionIDs, f.SourceConnectionID) && raw.Input.Format == 0 && cached.Bindings[receiverSampleBinding{b.ExporterID, b.SwitchID, f.SourceConnectionID, f.VLANID, canonicalDeviceMAC(raw.SourceMAC), "source", raw.Input.Value}]
			destination := f.DestinationMemberID == r.Spec.MemberID && f.DestinationMemberRevision == r.MemberRevision && contains(r.Spec.ConnectionIDs, f.DestinationConnectionID) && raw.Output.Format == 0 && cached.Bindings[receiverSampleBinding{b.ExporterID, b.SwitchID, f.DestinationConnectionID, f.VLANID, canonicalDeviceMAC(raw.DestinationMAC), "destination", raw.Output.Value}]
			if source || destination {
				indices = append(indices, i)
			}
		}
		if len(indices) == 0 {
			continue
		}
		if len(envelope.Deliveries) >= sflow.MaxDeliveries {
			return nil, errors.New("eligible receivers exceed per-packet forwarding bound")
		}
		envelope.Deliveries = append(envelope.Deliveries, sflow.Delivery{ReceiverID: id, ReceiverRevision: r.Spec.Revision, Address: r.Spec.Address, Port: r.Spec.Port, FlowIndices: indices, HeaderDataDisclosure: true})
	}
	a.deliveryMu.Lock()
	defer a.deliveryMu.Unlock()
	if a.pendingDeliveries == nil {
		a.pendingDeliveries = map[string]pendingDelivery{}
	}
	for nonce, p := range a.pendingDeliveries {
		if !now.Before(p.ExpiresAt) {
			delete(a.pendingDeliveries, nonce)
		}
	}
	if _, exists := a.pendingDeliveries[b.RequestNonce]; exists {
		return nil, errors.New("delivery request nonce was already used")
	}
	if len(a.pendingDeliveries) >= 8192 {
		return nil, errors.New("delivery authorization capacity reached")
	}
	if a.deliveryStatus == nil {
		a.deliveryStatus = map[string]deliveryObservation{}
	}
	grant := pendingDelivery{TokenOwner: deliveryOwner(session), ExporterID: b.ExporterID, SwitchID: b.SwitchID, MappingRevision: b.MappingRevision, DatagramDigest: b.DatagramDigest, ExpiresAt: now.Add(30 * time.Second), Allowed: map[string]deliveryAllowance{}}
	for _, d := range envelope.Deliveries {
		grant.Allowed[d.ReceiverID] = deliveryAllowance{Revision: d.ReceiverRevision, Samples: len(d.FlowIndices)}
		o := a.deliveryStatus[d.ReceiverID]
		o.AuthorizationCount++
		o.LastAuthorizedAt = now
		a.deliveryStatus[d.ReceiverID] = o
	}
	if len(envelope.Deliveries) > 0 {
		a.pendingDeliveries[b.RequestNonce] = grant
	}
	return envelope, nil
}

func (a *App) sflowDeliveryReport(w http.ResponseWriter, r *http.Request, user Record, session Session) {
	if r.Method != "POST" {
		problem(w, 405, "method not allowed")
		return
	}
	var report sflow.DeliveryReport
	if !decode(w, r, &report) {
		return
	}
	if report.Version != 1 || !deliveryNoncePattern.MatchString(report.RequestNonce) || !externalDigest(report.DatagramDigest) || len(report.Receipts) > sflow.MaxDeliveries || report.ObservedAt.IsZero() {
		problem(w, 422, "invalid delivery receipt")
		return
	}
	a.store.mu.RLock()
	defer a.store.mu.RUnlock()
	if a.store.closed || !a.canSFlowForward(&a.store.state, user, session, report.SwitchID) {
		problem(w, 403, "forwarding receipt scope changed")
		return
	}
	a.deliveryMu.Lock()
	defer a.deliveryMu.Unlock()
	grant, exists := a.pendingDeliveries[report.RequestNonce]
	if !exists || !time.Now().Before(grant.ExpiresAt) || grant.TokenOwner != deliveryOwner(session) || grant.ExporterID != report.ExporterID || grant.SwitchID != report.SwitchID || grant.MappingRevision != report.MappingRevision || grant.DatagramDigest != report.DatagramDigest {
		problem(w, 409, "delivery authorization unavailable; no UDP retry is permitted")
		return
	}
	hash := patchHash(report)
	if grant.ReportHash != "" {
		if grant.ReportHash == hash {
			respond(w, 200, Record{"ok": true, "duplicate": true})
			return
		}
		problem(w, 409, "conflicting delivery receipt")
		return
	}
	seen := map[string]bool{}
	for _, item := range report.Receipts {
		allow, ok := grant.Allowed[item.ReceiverID]
		if !ok || seen[item.ReceiverID] || allow.Revision != item.ReceiverRevision || item.Datagrams < 0 || item.Datagrams > allow.Samples || item.Bytes < 0 || item.Bytes > item.Datagrams*sflow.MaxRelayDatagramBytes {
			problemReceipt(w)
			return
		}
		seen[item.ReceiverID] = true
		switch item.Outcome {
		case "sent-to-socket":
			if item.Datagrams == 0 || item.Bytes == 0 {
				problemReceipt(w)
				return
			}
		case "partial-send-error":
			if item.Datagrams == 0 || item.Bytes == 0 {
				problemReceipt(w)
				return
			}
		case "blocked-policy", "encode-rejected", "expired", "rate-limited", "send-error":
			if item.Datagrams != 0 || item.Bytes != 0 {
				problemReceipt(w)
				return
			}
		default:
			problemReceipt(w)
			return
		}
	}
	if len(seen) != len(grant.Allowed) {
		problem(w, 422, "receipt must account for each authorized receiver")
		return
	}
	for _, item := range report.Receipts {
		o := a.deliveryStatus[item.ReceiverID]
		o.ReceiptRevision = item.ReceiverRevision
		o.LastReceiptAt = time.Now().UTC()
		o.Outcome = item.Outcome
		o.Datagrams = item.Datagrams
		o.Bytes = item.Bytes
		a.deliveryStatus[item.ReceiverID] = o
	}
	grant.ReportHash = hash
	a.pendingDeliveries[report.RequestNonce] = grant
	respond(w, 200, Record{"ok": true, "remoteArrivalVerified": false})
}
func problemReceipt(w http.ResponseWriter) {
	problem(w, 422, "receipt exceeds or contradicts its issued authorization")
}

func (a *App) receiverList(s *State, user Record, session Session, scope []string) []Record {
	items := []Record{}
	for _, r := range s.SFlowReceivers {
		if a.receiverVisible(s, r, user, scope) {
			items = append(items, a.receiverView(s, r, user, session))
		}
	}
	sort.Slice(items, func(i, j int) bool { return str(items[i], "name") < str(items[j], "name") })
	return items
}
