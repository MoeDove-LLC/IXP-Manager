package app

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/moedove/ixp-manager/internal/notify"
)

type patchRecipientSource struct {
	Header   string `json:"header"`
	Email    string `json:"email"`
	SourceID string `json:"sourceId"`
}
type patchNotificationDraft struct {
	Type             string                 `json:"type"`
	Revision         string                 `json:"revision"`
	To               []string               `json:"to"`
	Cc               []string               `json:"cc"`
	Bcc              []string               `json:"bcc"`
	RecipientSources []patchRecipientSource `json:"recipientSources,omitempty"`
	Subject          string                 `json:"subject"`
	Text             string                 `json:"text"`
	LOAID            string                 `json:"loaId"`
	DocumentIDs      []string               `json:"documentIds"`
}
type patchRecipientOption struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Email string `json:"email"`
	Kind  string `json:"kind"`
}
type patchNotificationContext struct {
	PortID           string                 `json:"portId"`
	PortIDs          []string               `json:"portIds"`
	MemberID         string                 `json:"memberId"`
	MemberName       string                 `json:"memberName"`
	Revision         string                 `json:"revision"`
	Type             string                 `json:"type"`
	DeliveryEnabled  bool                   `json:"deliveryEnabled"`
	From             string                 `json:"from"`
	RecipientOptions []patchRecipientOption `json:"recipientOptions"`
	LOAs             []Record               `json:"loas"`
	Documents        []Record               `json:"documents"`
	Draft            patchNotificationDraft `json:"draft"`
	BlockingReasons  []string               `json:"blockingReasons"`
	Limits           Record                 `json:"limits"`
}

// Receipts bind encrypted snapshots to the current allocation and author.
// Neither editable text nor CC/BCC addresses are duplicated into State/audit.
type PatchNotificationReceipt struct {
	ID              string            `json:"id"`
	OutboxID        string            `json:"outboxId"`
	PortID          string            `json:"portId"`
	Type            string            `json:"type"`
	Actor           NoteEntityBinding `json:"actor"`
	Generation      uint64            `json:"generation"`
	ContextRevision string            `json:"contextRevision"`
	ReviewDigest    string            `json:"reviewDigest"`
	RequestDigest   string            `json:"requestDigest"`
	SessionBinding  string            `json:"sessionBinding"`
	CreatedAt       time.Time         `json:"createdAt"`
	RecipientCount  int               `json:"recipientCount"`
	BCCCount        int               `json:"bccCount"`
}
type PatchNotificationState struct {
	Version  int                        `json:"version"`
	Receipts []PatchNotificationReceipt `json:"receipts,omitempty"`
}

func patchNotificationType(kind string) bool {
	return contains([]string{"connect", "cease", "info", "loa"}, kind)
}
func patchNotificationStatus(kind, status string) bool {
	switch kind {
	case "connect":
		return status == "reserved" || status == "awaiting-connect"
	case "cease":
		return status == "connected" || status == "awaiting-cease"
	case "info", "loa":
		return contains([]string{"reserved", "awaiting-connect", "connected", "awaiting-cease", "ceased"}, status)
	}
	return false
}
func patchMailName(name string) string {
	name = strings.ReplaceAll(strings.ReplaceAll(name, "/", "_"), "\\", "_")
	if len(name) > 200 {
		return "patch-attachment"
	}
	return name
}
func patchNotificationDocuments(s *State, ports []Record, member string) []Document {
	seen := map[string]bool{}
	out := []Document{}
	memberUser := Record{"role": "member", "memberId": member}
	for _, port := range ports {
		ids, _ := port["documentIds"].([]any)
		ids = append([]any{}, ids...)
		for _, legacy := range legacyPatchPortAttachments(s, port, memberUser) {
			ids = append(ids, str(legacy, "id"))
		}
		for _, raw := range ids {
			id, _ := raw.(string)
			d := documentByID(s, id)
			if d == nil || seen[id] || !documentReadable(s, *d, memberUser) || d.MemberID != "" && d.MemberID != member {
				continue
			}
			// Imported private/member-admin-only documents never gain mail access.
			seen[id] = true
			out = append(out, *d)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
func (a *App) patchNotificationContext(s *State, id, kind string, now time.Time) (patchNotificationContext, error) {
	c := patchNotificationContext{PortID: id, Type: kind, DeliveryEnabled: a.mailConfig.Enabled, From: a.mailConfig.From, RecipientOptions: []patchRecipientOption{}, LOAs: []Record{}, Documents: []Record{}, BlockingReasons: []string{}, Limits: Record{"maxRecipients": notify.MaxRecipients, "maxDocuments": 3, "maxAttachmentBytes": notify.MaxAttachmentBytes, "maxTextBytes": 16384}}
	if !patchNotificationType(kind) {
		return c, errors.New("notification type is invalid")
	}
	ports, err := patchGroup(s, find(s, "patch-ports", id))
	if err != nil {
		return c, errors.New("patch allocation is unavailable")
	}
	c.PortIDs = patchIDs(ports)
	c.MemberID = str(ports[0], "memberId")
	member := find(s, "members", c.MemberID)
	c.MemberName = str(member, "name")
	if member == nil || !validNoteEntity(noteEntity(member)) || ((kind == "connect" || kind == "loa") && str(member, "status") != "active") || str(ports[0], "allocationId") == "" {
		c.BlockingReasons = append(c.BlockingReasons, "allocation-unavailable")
	}
	for _, p := range ports {
		if !patchAllocated(p) || str(p, "memberId") != c.MemberID || str(p, "allocationId") != str(ports[0], "allocationId") {
			c.BlockingReasons = append(c.BlockingReasons, "allocation-unavailable")
			break
		}
	}
	if !patchNotificationStatus(kind, patchStatus(ports[0])) {
		c.BlockingReasons = append(c.BlockingReasons, "state-not-applicable")
	}
	add := func(id, label, address, kind string) {
		address = strings.ToLower(strings.TrimSpace(address))
		if _, valid := noteMailbox(address); valid {
			c.RecipientOptions = append(c.RecipientOptions, patchRecipientOption{id, label, address, kind})
		}
	}
	add("member:noc", "成员 NOC", str(member, "nocEmail"), "member")
	add("member:email", "成员邮箱", str(member, "email"), "member")
	contacts := []Record{}
	for _, contact := range noteSortedRecords(s, "contacts") {
		if str(contact, "memberId") == c.MemberID {
			contacts = append(contacts, contact)
			add("contact:"+str(contact, "id"), str(contact, "name"), str(contact, "email"), "contact")
		}
	}
	add("operations:support", "交换中心支持", str(s.Settings, "supportEmail"), "operations")
	loas := []LOA{}
	for _, letter := range s.LOAs {
		if letter.MemberID == c.MemberID && letter.AllocationID == str(ports[0], "allocationId") && loaStatus(s, letter, now) == "issued" {
			loas = append(loas, letter)
		}
	}
	sort.Slice(loas, func(i, j int) bool { return loas[i].ID < loas[j].ID })
	for _, l := range loas {
		c.LOAs = append(c.LOAs, Record{"id": l.ID, "label": "LOA " + l.ID, "expiresAt": l.ExpiresAt, "size": l.PDFSize})
	}
	documents := patchNotificationDocuments(s, ports, c.MemberID)
	for _, d := range documents {
		c.Documents = append(c.Documents, Record{"id": d.ID, "name": patchMailName(d.Name), "mediaType": d.MediaType, "size": d.Size})
	}
	data := patchLOAData(s, ports)
	panel := find(s, "patch-panels", str(ports[0], "panelId"))
	rack := find(s, "racks", str(panel, "rackId"))
	location := find(s, "locations", str(rack, "locationId"))
	physical := []Record{}
	switches := []Record{}
	for _, port := range ports {
		p := find(s, "ports", str(port, "switchPortId"))
		if p != nil {
			physical = append(physical, p)
			switches = append(switches, find(s, "switches", str(p, "switchId")))
		}
	}
	c.Revision = a.coreHMAC("patch-notification-context", Record{"type": kind, "ports": ports, "member": member, "contacts": contacts, "data": data, "panel": panel, "rack": rack, "location": location, "physical": physical, "switches": switches, "loas": loas, "documents": documents, "sources": c.RecipientOptions, "from": a.mailConfig.From, "origin": a.mailConfig.BaseURL})
	draft := patchNotificationDraft{Type: kind, Revision: c.Revision, To: []string{}, Cc: []string{}, Bcc: []string{}, DocumentIDs: []string{}}
	for _, option := range c.RecipientOptions {
		header := ""
		if option.ID == "member:noc" || option.ID == "member:email" && len(draft.To) == 0 {
			if len(draft.To) == 0 {
				draft.To = []string{option.Email}
				header = "to"
			}
		}
		if option.ID == "operations:support" && !contains(draft.To, option.Email) {
			draft.Bcc = []string{option.Email}
			header = "bcc"
		}
		if header != "" {
			draft.RecipientSources = append(draft.RecipientSources, patchRecipientSource{header, option.Email, option.ID})
		}
	}
	titles := map[string]string{"connect": "交叉连接接通请求", "cease": "交叉连接停用请求", "info": "交叉连接资料", "loa": "交叉连接 LOA"}
	draft.Subject = titles[kind] + " - " + data.ExchangeName + " / " + data.PanelName
	draft.Text = fmt.Sprintf("%s\n\n成员：%s\n机房：%s\n机柜：%s\n配线架：%s\n端口：%s\n线路编号：%s\n当前登记状态：%s\n\n", titles[kind], data.MemberName, data.Location, data.RackName, data.PanelName, strings.Join(data.Ports, ", "), data.CircuitID, patchStatus(ports[0]))
	switch kind {
	case "connect":
		draft.Text += "请协调机房完成本次交叉连接，并在完成后回复确认。"
	case "cease":
		draft.Text += "请协调机房停用本次交叉连接，并在完成后回复确认。"
	case "loa":
		draft.Text += "请查收本次分配当前有效的 LOA PDF 附件。"
	default:
		draft.Text += "以下附件如有选择，仅包含本次明确审阅的当前资料。"
	}
	draft.Text += "\n\n这是一封人工确认的通知，不证明物理线路已改变。\n"
	if (kind == "connect" || kind == "loa") && len(loas) > 0 {
		draft.LOAID = loas[0].ID
	}
	if kind == "loa" && draft.LOAID == "" {
		c.BlockingReasons = append(c.BlockingReasons, "loa-required")
	}
	if !a.mailConfig.Enabled {
		c.BlockingReasons = append(c.BlockingReasons, "delivery-disabled")
	}
	c.Draft = draft
	return c, nil
}

func prunePatchNotifications(s *State) {
	if s.PatchNotifications == nil {
		return
	}
	present := map[string]bool{}
	for _, e := range s.Outbox.Entries {
		present[e.ID] = true
	}
	out := s.PatchNotifications.Receipts[:0]
	for _, r := range s.PatchNotifications.Receipts {
		if present[r.OutboxID] {
			out = append(out, r)
		}
	}
	s.PatchNotifications.Receipts = out
}
func validatePatchNotifications(s *State) error {
	state := s.PatchNotifications
	if state == nil {
		for _, e := range s.Outbox.Entries {
			if strings.HasPrefix(e.Key, "patch:") {
				return errors.New("patch notification binding is missing")
			}
		}
		return nil
	}
	if state.Version != 1 || len(state.Receipts) > notify.MaxEntries {
		return errors.New("invalid patch notification state")
	}
	seen, entries := map[string]bool{}, map[string]notify.Entry{}
	for _, e := range s.Outbox.Entries {
		entries[e.ID] = e
	}
	for _, r := range state.Receipts {
		e, ok := entries[r.OutboxID]
		if !ok || seen[r.ID] || !identifier.MatchString(r.ID) || !identifier.MatchString(r.PortID) || !patchNotificationType(r.Type) || !validNoteEntity(r.Actor) || !externalDigest(r.ContextRevision) || !externalDigest(r.ReviewDigest) || !externalDigest(r.RequestDigest) || !externalDigest(r.SessionBinding) || r.CreatedAt.IsZero() || r.RecipientCount < 1 || r.RecipientCount > notify.MaxRecipients || r.BCCCount < 0 || r.BCCCount >= r.RecipientCount || e.Key != "patch:"+r.ID {
			return errors.New("invalid patch notification receipt")
		}
		seen[r.ID] = true
	}
	for _, e := range s.Outbox.Entries {
		if strings.HasPrefix(e.Key, "patch:") {
			found := false
			for _, r := range state.Receipts {
				found = found || r.OutboxID == e.ID
			}
			if !found {
				return errors.New("patch notification binding is missing")
			}
		}
	}
	return nil
}
func (a *App) patchMailApplicable(s *State, e notify.Entry, now time.Time) bool {
	if s.PatchNotifications == nil {
		return false
	}
	for _, r := range s.PatchNotifications.Receipts {
		if r.OutboxID != e.ID {
			continue
		}
		actor := find(s, "users", r.Actor.ID)
		if e.Key != "patch:"+r.ID || !noteEntityCurrent(s, "users", r.Actor) || str(actor, "role") != "admin" || s.Security[r.Actor.ID].Generation != r.Generation {
			return false
		}
		context, err := a.patchNotificationContext(s, r.PortID, r.Type, now)
		if err != nil || context.Revision != r.ContextRevision {
			return false
		}
		for _, code := range context.BlockingReasons {
			if code != "delivery-disabled" {
				return false
			}
		}
		return true
	}
	return false
}
func (a *App) patchRetryApplicable(s *State, id string) error {
	for _, e := range s.Outbox.Entries {
		if e.ID == id && strings.HasPrefix(e.Key, "patch:") && !a.patchMailApplicable(s, e, time.Now()) {
			return errors.New("patch allocation, sender authority or selected source changed; delivery cannot be retried")
		}
	}
	return nil
}
