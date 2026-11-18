package app

import (
	"errors"
	"maps"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/moedove/ixp-manager/internal/migrate"
)

type LegacyPatchBatch struct {
	ID             string `json:"id"`
	ImportedAt     string `json:"importedAt"`
	ImportedBy     string `json:"importedBy"`
	SourceTimezone string `json:"sourceTimezone"`
	Panels         int    `json:"panels"`
	ReviewPanels   int    `json:"reviewPanels"`
	ReviewPorts    int    `json:"reviewPorts"`
	Histories      int    `json:"histories"`
	Attachments    int    `json:"attachments"`
}
type LegacyPatchPanel struct {
	Source   migrate.PatchPanelSource `json:"source"`
	BatchID  string                   `json:"batchId"`
	NativeID string                   `json:"nativeId,omitempty"`
	Review   []string                 `json:"review"`
}
type LegacyPatchPort struct {
	Source        migrate.PatchPortSource `json:"source"`
	BatchID       string                  `json:"batchId"`
	NativeID      string                  `json:"nativeId,omitempty"`
	PortCreatedAt string                  `json:"portCreatedAt,omitempty"`
	AllocationID  string                  `json:"allocationId,omitempty"`
	Review        string                  `json:"review,omitempty"`
}
type LegacyPatchSnapshot struct {
	Source  migrate.PatchHistorySource `json:"source"`
	BatchID string                     `json:"batchId"`
}
type LegacyPatchAttachment struct {
	Source          migrate.PatchFileSource `json:"source"`
	BatchID         string                  `json:"batchId"`
	DocumentID      string                  `json:"documentId"`
	DirectoryID     string                  `json:"directoryId"`
	NativePortID    string                  `json:"nativePortId,omitempty"`
	PortCreatedAt   string                  `json:"portCreatedAt,omitempty"`
	AllocationID    string                  `json:"allocationId,omitempty"`
	MemberID        string                  `json:"memberId,omitempty"`
	MemberCreatedAt string                  `json:"memberCreatedAt,omitempty"`
	Review          string                  `json:"review,omitempty"`
}
type LegacyPatchingState struct {
	Batches     []LegacyPatchBatch      `json:"batches"`
	Panels      []LegacyPatchPanel      `json:"panels"`
	Ports       []LegacyPatchPort       `json:"ports"`
	Histories   []LegacyPatchSnapshot   `json:"histories"`
	Attachments []LegacyPatchAttachment `json:"attachments"`
}

func legacyPatchAttachment(s *State, documentID string) *LegacyPatchAttachment {
	if s.LegacyPatching == nil {
		return nil
	}
	for i := range s.LegacyPatching.Attachments {
		a := &s.LegacyPatching.Attachments[i]
		if a.DocumentID == documentID {
			return a
		}
	}
	return nil
}
func legacyPatchDirectory(s *State, directoryID string) *LegacyPatchAttachment {
	if s.LegacyPatching == nil {
		return nil
	}
	for i := range s.LegacyPatching.Attachments {
		a := &s.LegacyPatching.Attachments[i]
		if a.DirectoryID == directoryID {
			return a
		}
	}
	return nil
}
func legacyPatchBindingCurrent(s *State, a *LegacyPatchAttachment) bool {
	if a == nil || a.Source.Kind != "current" || a.Source.Private || a.NativePortID == "" || a.MemberID == "" || a.AllocationID == "" || a.PortCreatedAt == "" || a.MemberCreatedAt == "" {
		return false
	}
	p := find(s, "patch-ports", a.NativePortID)
	m := find(s, "members", a.MemberID)
	return p != nil && m != nil && str(p, "createdAt") == a.PortCreatedAt && str(m, "createdAt") == a.MemberCreatedAt && str(p, "memberId") == a.MemberID && str(p, "allocationId") == a.AllocationID && patchAllocated(p)
}
func legacyPatchVisible(s *State, a *LegacyPatchAttachment, user Record) bool {
	role := str(user, "role")
	if role == "" {
		return false
	}
	if role == "admin" || role == "operator" || role == "viewer" {
		return true
	}
	return role == "member" && legacyPatchBindingCurrent(s, a) && str(user, "memberId") == a.MemberID
}
func documentReadable(s *State, d Document, user Record) bool {
	if f := legacyDocstoreFile(s, d.ID); f != nil {
		return legacyDocstoreVisible(s, f, user)
	}
	if a := legacyPatchAttachment(s, d.ID); a != nil {
		return legacyPatchVisible(s, a, user)
	}
	return documentVisible(d.Visibility, d.MemberID, user)
}
func directoryReadable(s *State, d Directory, user Record) bool {
	if f := legacyDocstoreDirectory(s, d.ID); f != nil {
		return legacyDocstoreVisible(s, f, user)
	}
	if a := legacyPatchDirectory(s, d.ID); a != nil {
		return legacyPatchVisible(s, a, user)
	}
	return documentVisible(d.Visibility, d.MemberID, user)
}
func legacyDocumentView(s *State, d Document) Record {
	out := documentView(d)
	legacyDocstoreView(s, d, out)
	if a := legacyPatchAttachment(s, d.ID); a != nil {
		out["legacyPatch"], out["readOnly"] = true, true
		out["legacyKind"], out["legacySourceId"] = a.Source.Kind, a.Source.ID
		out["originalName"], out["sourceUploadedAt"] = a.Source.Name, a.Source.Facts["uploaded_at"]
		out["allocationAccessActive"] = legacyPatchBindingCurrent(s, a)
		if legacyPatchBindingCurrent(s, a) {
			out["visibility"], out["memberId"] = "member", a.MemberID
		}
	}
	return out
}
func legacyDirectoryView(s *State, d Directory) Record {
	out := Record{"id": d.ID, "name": d.Name, "description": d.Description, "parentId": d.ParentID, "memberId": d.MemberID, "visibility": d.Visibility, "createdAt": d.CreatedAt, "updatedAt": d.UpdatedAt}
	if f := legacyDocstoreDirectory(s, d.ID); f != nil {
		out["legacyDocstore"], out["readOnly"], out["visibility"] = true, true, legacyDocstoreScope(s, f)
		if f.Source.Kind == "customer" && legacyDocstoreMemberCurrent(s, f) {
			out["memberId"] = f.Source.MemberID
		}
	}
	if a := legacyPatchDirectory(s, d.ID); a != nil {
		out["legacyPatch"], out["readOnly"] = true, true
		if legacyPatchBindingCurrent(s, a) {
			out["visibility"], out["memberId"] = "member", a.MemberID
		}
	}
	return out
}

func validateLegacyPatching(s *State) error {
	l := s.LegacyPatching
	if l == nil {
		return nil
	}
	if len(l.Batches) > 4096 || len(l.Panels)+len(l.Ports)+len(l.Histories)+len(l.Attachments) > migrate.PatchRecordLimit {
		return errors.New("legacy patch archive exceeds limit")
	}
	docs := map[string]Document{}
	dirs := map[string]Directory{}
	for _, d := range s.Documents {
		docs[d.ID] = d
	}
	for _, d := range s.Directories {
		dirs[d.ID] = d
	}
	batchIDs := map[string]bool{}
	portSources := map[string]LegacyPatchPort{}
	sourceIDs := map[string]bool{}
	protectedDirs := map[string]string{}
	for _, b := range l.Batches {
		if !digestPattern.MatchString(b.ID) || batchIDs[b.ID] || b.ImportedAt == "" || b.ImportedBy == "" {
			return errors.New("invalid legacy patch receipt")
		}
		batchIDs[b.ID] = true
		if _, err := time.Parse(time.RFC3339Nano, b.ImportedAt); err != nil {
			return errors.New("invalid legacy import timestamp")
		}
		if err := migrate.ValidatePatchTimezone(b.SourceTimezone); err != nil {
			return err
		}
	}
	for _, v := range l.Panels {
		if sourceIDs[v.Source.ID] || !batchIDs[v.BatchID] {
			return errors.New("invalid legacy panel identity")
		}
		sourceIDs[v.Source.ID] = true
		if v.NativeID != "" && (v.NativeID != v.Source.ID || len(v.Review) > 0) {
			return errors.New("legacy review panel must not claim allocatable inventory")
		}
		if err := migrate.ValidatePatching(&migrate.PatchingImport{Version: 1, Panels: []migrate.PatchPanelSource{v.Source}}, false); err != nil {
			return err
		}
	}
	for _, v := range l.Ports {
		if sourceIDs[v.Source.ID] || !batchIDs[v.BatchID] {
			return errors.New("invalid legacy port identity")
		}
		sourceIDs[v.Source.ID] = true
		if v.NativeID != "" && v.NativeID != v.Source.ID {
			return errors.New("legacy current-port identity changed")
		}
		if err := migrate.ValidatePatching(&migrate.PatchingImport{Version: 1, Ports: []migrate.PatchPortSource{v.Source}}, false); err != nil {
			return err
		}
		portSources[v.Source.ID] = v
	}
	for _, v := range l.Histories {
		if sourceIDs[v.Source.ID] || !batchIDs[v.BatchID] {
			return errors.New("invalid legacy snapshot identity")
		}
		sourceIDs[v.Source.ID] = true
		if err := migrate.ValidatePatching(&migrate.PatchingImport{Version: 1, Histories: []migrate.PatchHistorySource{v.Source}}, false); err != nil {
			return err
		}
	}
	for _, a := range l.Attachments {
		if sourceIDs[a.Source.ID] || !batchIDs[a.BatchID] || a.DocumentID != a.Source.ID || protectedDirs[a.DirectoryID] != "" {
			return errors.New("invalid legacy attachment identity")
		}
		sourceIDs[a.Source.ID] = true
		if a.NativePortID != "" {
			parent, ok := portSources[a.Source.ParentID]
			if a.Source.Kind != "current" || !ok || parent.NativeID != a.NativePortID || parent.PortCreatedAt != a.PortCreatedAt {
				return errors.New("legacy attachment physical binding changed")
			}
			if a.MemberID != "" && (a.Source.Private || parent.Source.MemberID != a.MemberID || a.AllocationID == "" || a.AllocationID != parent.AllocationID || a.MemberCreatedAt == "") {
				return errors.New("legacy attachment tenant grant differs from original direct-port ownership")
			}
		} else if a.MemberID != "" || a.AllocationID != "" || a.PortCreatedAt != "" || a.MemberCreatedAt != "" {
			return errors.New("unresolved legacy files cannot grant tenant access")
		}
		if a.Source.Kind == "history" && (a.NativePortID != "" || a.MemberID != "") {
			return errors.New("historical attachments must remain staff-only")
		}
		d, ok := docs[a.DocumentID]
		folder, folderOK := dirs[a.DirectoryID]
		if !ok || !folderOK || d.DirectoryID != folder.ID || d.Visibility != "staff" || d.MemberID != "" || folder.Visibility != "staff" || folder.MemberID != "" || folder.ParentID != "" || d.Checksum != a.Source.Checksum || d.Size != a.Source.Size || d.Version != 1 || len(d.Revisions) != 0 {
			return errors.New("legacy attachment snapshots and their managed permissions are immutable")
		}
		if err := migrate.ValidatePatching(&migrate.PatchingImport{Version: 1, Files: []migrate.PatchFileSource{a.Source}}, true); err != nil {
			return err
		}
		protectedDirs[folder.ID] = d.ID
	}
	for _, d := range s.Documents {
		if id := protectedDirs[d.DirectoryID]; id != "" && id != d.ID {
			return errors.New("legacy attachment directories are read-only")
		}
	}
	for _, d := range s.Directories {
		if protectedDirs[d.ParentID] != "" {
			return errors.New("legacy attachment directories cannot contain additional children")
		}
	}
	return nil
}

var legacyPatchStatuses = map[int]string{1: "available", 2: "awaiting-connect", 3: "connected", 4: "awaiting-cease", 5: "ceased", 6: "broken", 7: "reserved", 8: "prewired"}

func legacyPatchFactRef(facts map[string]string, key string) bool {
	v := facts[key]
	return v != "" && v != "0" && v != "null"
}

// importLegacyPatching preserves snapshots rather than inventing old events.
// A panel is either represented completely or retained entirely for review.
func importLegacyPatching(s *State, user Record, p *migrate.PatchingImport, versions map[string]DocumentVersion, receipt string) (LegacyPatchBatch, error) {
	var result LegacyPatchBatch
	if err := migrate.ValidatePatching(p, len(p.Files) > 0); err != nil {
		return result, err
	}
	if s.LegacyPatching == nil {
		s.LegacyPatching = &LegacyPatchingState{Batches: []LegacyPatchBatch{}, Panels: []LegacyPatchPanel{}, Ports: []LegacyPatchPort{}, Histories: []LegacyPatchSnapshot{}, Attachments: []LegacyPatchAttachment{}}
	}
	l := s.LegacyPatching
	existing := map[string]bool{}
	for _, v := range l.Batches {
		if v.ID == receipt {
			return result, errors.New("this patch bundle was already imported")
		}
	}
	for _, v := range l.Panels {
		existing[v.Source.ID] = true
	}
	for _, v := range l.Ports {
		existing[v.Source.ID] = true
	}
	for _, v := range l.Histories {
		existing[v.Source.ID] = true
	}
	for _, v := range l.Attachments {
		existing[v.Source.ID] = true
	}
	for _, v := range p.Panels {
		if existing[v.ID] || find(s, "patch-panels", v.ID) != nil {
			return result, errors.New("legacy panel identity already exists")
		}
	}
	for _, v := range p.Ports {
		if existing[v.ID] || find(s, "patch-ports", v.ID) != nil {
			return result, errors.New("legacy port identity already exists")
		}
	}
	for _, v := range p.Histories {
		if existing[v.ID] {
			return result, errors.New("legacy snapshot identity already exists")
		}
	}
	for _, v := range p.Files {
		if existing[v.ID] || documentByID(s, v.ID) != nil {
			return result, errors.New("legacy attachment identity already exists")
		}
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	result = LegacyPatchBatch{ID: receipt, ImportedAt: now, ImportedBy: str(user, "id"), SourceTimezone: p.SourceTimezone, Histories: len(p.Histories), Attachments: len(p.Files)}
	portSources := map[string]migrate.PatchPortSource{}
	byPanel := map[string][]migrate.PatchPortSource{}
	switchClaims := map[string][]string{}
	for _, v := range p.Ports {
		portSources[v.ID] = v
		byPanel[v.PanelID] = append(byPanel[v.PanelID], v)
		if v.SwitchPortID != "" && v.DuplexMasterID == "" {
			switchClaims[v.SwitchPortID] = append(switchClaims[v.SwitchPortID], v.ID)
		}
	}
	created := map[string]Record{}
	for _, panel := range p.Panels {
		panel.Facts = maps.Clone(panel.Facts)
		entry := LegacyPatchPanel{Source: panel, BatchID: receipt, Review: []string{}}
		ports := byPanel[panel.ID]
		numbers := map[int]bool{}
		slaves := map[string][]migrate.PatchPortSource{}
		maxNumber := 0
		issue := func(text string) {
			if !contains(entry.Review, text) {
				entry.Review = append(entry.Review, text)
			}
		}
		if strings.TrimSpace(panel.Name) == "" || len(panel.Name) > 255 || find(s, "racks", panel.RackID) == nil {
			issue("面板名称或机柜引用无法映射")
		}
		if len(ports) == 0 {
			issue("没有完整的旧端口记录，禁止自动补成空闲端口")
		}
		for _, port := range ports {
			if port.Number < 1 || port.Number > 1024 || numbers[port.Number] {
				issue("端口编号重复或超出新系统边界")
			}
			numbers[port.Number] = true
			if port.Number > maxNumber {
				maxNumber = port.Number
			}
			if legacyPatchStatuses[port.State] == "" {
				issue("包含 Other 或未知状态的端口")
			}
			if port.DuplexMasterID != "" {
				slaves[port.DuplexMasterID] = append(slaves[port.DuplexMasterID], port)
				master, ok := portSources[port.DuplexMasterID]
				if !ok || master.PanelID != panel.ID || master.DuplexMasterID != "" || master.ID == port.ID {
					issue("双工主从引用不完整或跨面板")
				}
			}
			if legacyPatchFactRef(port.Facts, "customer_id") && port.MemberID == "" {
				issue("当前客户引用未迁移，不推断名称对应的成员")
			}
			if legacyPatchFactRef(port.Facts, "switch_port_id") && port.SwitchPortID == "" {
				issue("交换机端口引用未迁移")
			}
		}
		if maxNumber != len(numbers) {
			issue("旧端口编号存在缺行，整面板保留待核对")
		}
		for master, children := range slaves {
			if len(children) != 1 || portSources[master].PanelID != panel.ID {
				issue("旧主口拥有多个从口或未导出")
			}
		}
		candidate := []Record{}
		for _, port := range ports {
			master := port
			if port.DuplexMasterID != "" {
				if m, ok := portSources[port.DuplexMasterID]; ok {
					master = m
				}
			}
			if port.ID != master.ID && ((port.MemberID != "" && port.MemberID != master.MemberID) || (port.SwitchPortID != "" && port.SwitchPortID != master.SwitchPortID) || (port.State != 1 && port.State != master.State)) {
				issue("从口含独立分配信息，不能覆盖为主口归属")
			}
			status := legacyPatchStatuses[master.State]
			r := Record{"id": port.ID, "name": panel.Prefix + strconv.Itoa(port.Number), "panelId": panel.ID, "number": float64(port.Number), "status": status, "createdAt": now, "updatedAt": now}
			if master.MemberID != "" {
				if find(s, "members", master.MemberID) == nil {
					issue("当前成员不存在")
				}
				r["memberId"] = master.MemberID
			}
			if master.SwitchPortID != "" {
				physical := find(s, "ports", master.SwitchPortID)
				if physical == nil {
					issue("物理交换机端口不存在")
				} else if master.MemberID != "" && str(physical, "memberId") != "" && str(physical, "memberId") != master.MemberID {
					issue("物理端口属于另一成员")
				}
				r["switchPortId"] = master.SwitchPortID
				if len(switchClaims[master.SwitchPortID]) > 1 {
					issue("同一交换机端口被多个旧分配占用")
				}
			}
			if circuit := master.Facts["colo_circuit_ref"]; circuit != "" {
				r["circuitId"] = circuit
			}
			if patchAllocated(r) {
				if master.MemberID == "" && status != "reserved" {
					issue("旧内部或无成员占用无法表达，禁止伪造成员授权")
				}
				r["allocationId"] = "import-" + receipt[:16] + "-" + strconv.FormatInt(master.LegacyID, 10)
			} else if master.MemberID != "" {
				issue("空闲或损坏端口仍有客户，需明确核对")
			}
			if status == "prewired" && master.SwitchPortID == "" {
				issue("预布线缺少物理端口绑定")
			}
			if port.DuplexMasterID != "" {
				r["duplexPeerId"] = master.ID
			} else if len(slaves[port.ID]) == 1 {
				r["duplexPeerId"] = slaves[port.ID][0].ID
			}
			candidate = append(candidate, r)
		}
		if len(entry.Review) == 0 {
			native := Record{"id": panel.ID, "name": strings.TrimSpace(panel.Name), "rackId": panel.RackID, "portCount": float64(maxNumber), "createdAt": now, "updatedAt": now}
			s.Resources["patch-panels"] = append(s.Resources["patch-panels"], native)
			s.Resources["patch-ports"] = append(s.Resources["patch-ports"], candidate...)
			entry.NativeID = panel.ID
			result.Panels++
			for _, port := range candidate {
				created[str(port, "id")] = port
				if err := recordPatchResourceChange(s, user, "POST", nil, port); err != nil {
					return result, err
				}
			}
		} else {
			sort.Strings(entry.Review)
			result.ReviewPanels++
		}
		l.Panels = append(l.Panels, entry)
	}
	for _, port := range p.Ports {
		port.Facts = maps.Clone(port.Facts)
		entry := LegacyPatchPort{Source: port, BatchID: receipt}
		if native := created[port.ID]; native != nil {
			entry.NativeID = port.ID
			entry.PortCreatedAt = now
			entry.AllocationID = str(native, "allocationId")
		} else {
			entry.Review = "所属面板未完整迁移或未提供；未生成可分配端口"
			result.ReviewPorts++
		}
		l.Ports = append(l.Ports, entry)
	}
	for _, history := range p.Histories {
		history.Facts = maps.Clone(history.Facts)
		l.Histories = append(l.Histories, LegacyPatchSnapshot{Source: history, BatchID: receipt})
	}
	for _, file := range p.Files {
		file.Facts = maps.Clone(file.Facts)
		v, ok := versions[file.ID]
		if !ok || v.Checksum != file.Checksum || v.Size != file.Size {
			return result, errors.New("validated attachment content is missing")
		}
		attachment := LegacyPatchAttachment{Source: file, BatchID: receipt, DocumentID: file.ID, DirectoryID: "dir-" + file.ID}
		if file.Kind == "current" {
			if native := created[file.ParentID]; native != nil {
				attachment.NativePortID = file.ParentID
				attachment.PortCreatedAt = now
				// The raw referenced port controls old file access; a slave's null
				// customer must not inherit a new member grant from its master.
				raw := portSources[file.ParentID]
				if !file.Private && raw.MemberID != "" && raw.MemberID == str(native, "memberId") && patchAllocated(native) {
					attachment.MemberID = raw.MemberID
					attachment.MemberCreatedAt = str(find(s, "members", raw.MemberID), "createdAt")
					attachment.AllocationID = str(native, "allocationId")
				}
				if !file.Private && attachment.MemberID == "" {
					attachment.Review = "原附件的直接端口未能确认成员授权，仅工作人员可见"
				}
			} else {
				attachment.Review = "当前端口未进入可分配库存，附件仅工作人员可见"
			}
		}
		name := file.Name
		if !validDocumentName(name) {
			name = "附件-" + file.ID
			attachment.Review = "原文件名已保留在来源信息，列表使用安全名称"
		}
		folder := Directory{ID: attachment.DirectoryID, Name: "旧配线附件 " + file.ID, Visibility: "staff", CreatedAt: now, UpdatedAt: now}
		doc := Document{ID: file.ID, Name: name, Description: "旧配线附件快照；权限由原始隐私属性及当前分配绑定限定。", DirectoryID: folder.ID, Visibility: "staff", CreatedAt: now, UpdatedAt: now, CreatedBy: str(user, "id"), Version: 1, BlobID: v.BlobID, Size: v.Size, MediaType: v.MediaType, Checksum: v.Checksum}
		if directoryByID(s, folder.ID) != nil {
			return result, errors.New("legacy managed directory already exists")
		}
		s.Directories = append(s.Directories, folder)
		s.Documents = append(s.Documents, doc)
		l.Attachments = append(l.Attachments, attachment)
	}
	l.Batches = append(l.Batches, result)
	return result, validateLegacyPatching(s)
}

func legacyPatchPortAttachments(s *State, port, user Record) []Record {
	out := []Record{}
	if s.LegacyPatching == nil {
		return out
	}
	for _, a := range s.LegacyPatching.Attachments {
		if a.Source.Kind != "current" || a.NativePortID != str(port, "id") || a.PortCreatedAt != str(port, "createdAt") {
			continue
		}
		if !a.Source.Private && a.AllocationID != "" && !legacyPatchBindingCurrent(s, &a) {
			continue
		}
		d := documentByID(s, a.DocumentID)
		if d == nil || !documentReadable(s, *d, user) {
			continue
		}
		view := legacyDocumentView(s, *d)
		view["legacyPatch"] = true
		out = append(out, view)
	}
	return out
}
func (a *App) legacyPatchList(w http.ResponseWriter, r *http.Request, user Record) {
	if r.Method != "GET" {
		problem(w, 405, "method not allowed")
		return
	}
	if !contains([]string{"admin", "operator", "viewer"}, str(user, "role")) {
		problem(w, 403, "legacy patch snapshots are staff-only")
		return
	}
	s := a.store.Snapshot()
	if s.LegacyPatching == nil {
		respond(w, 200, LegacyPatchingState{Batches: []LegacyPatchBatch{}, Panels: []LegacyPatchPanel{}, Ports: []LegacyPatchPort{}, Histories: []LegacyPatchSnapshot{}, Attachments: []LegacyPatchAttachment{}})
		return
	}
	respond(w, 200, s.LegacyPatching)
}
