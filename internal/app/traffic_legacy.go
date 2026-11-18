package app

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/moedove/ixp-manager/internal/traffic"
)

type LegacyImportApproval struct {
	ID         string    `json:"id"`
	Checksum   string    `json:"checksum"`
	ApprovedBy string    `json:"approvedBy"`
	ApprovedAt time.Time `json:"approvedAt"`
}

func validateLegacyApprovals(s *State) error {
	if len(s.LegacyImports) > traffic.LegacyMaxBatches {
		return errors.New("legacy approval limit exceeded")
	}
	seen := map[string]bool{}
	for _, r := range s.LegacyImports {
		if !identifier.MatchString(r.ID) || !externalDigest(r.Checksum) || r.ApprovedBy == "" || r.ApprovedAt.IsZero() || seen[r.ID] {
			return errors.New("invalid legacy approval identity")
		}
		seen[r.ID] = true
	}
	return nil
}

func (a *App) initLegacyTraffic() error {
	store, err := traffic.OpenLegacy(filepath.Join(filepath.Dir(a.store.path), "legacy-traffic"))
	if err != nil {
		return err
	}
	a.legacyTraffic = store
	return nil
}
func legacyApproval(s *State, id string) *LegacyImportApproval {
	for i := range s.LegacyImports {
		if s.LegacyImports[i].ID == id {
			return &s.LegacyImports[i]
		}
	}
	return nil
}
func (a *App) legacyMemberAlias(member, revision, id string) string {
	return "legacy-" + a.coreHMAC("legacy-member-batch", Record{"member": member, "revision": revision, "id": id})[:40]
}
func decodeLegacy(w http.ResponseWriter, r *http.Request, value any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 32<<20+65536)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(value); err != nil {
		problem(w, 400, "invalid bounded legacy JSON request")
		return false
	}
	if d.Decode(new(any)) != io.EOF {
		problem(w, 400, "one legacy request document required")
		return false
	}
	return true
}
func (a *App) validateLegacyOwnerClaims(s *State, b traffic.LegacyBatch) error {
	for _, row := range b.Rows {
		if row.StaffOnly {
			continue
		}
		if row.MemberID == "" || row.MemberRevision != a.trafficMemberRevision(s, row.MemberID) || row.OwnerEvidence == nil || row.Window.Start == nil || row.Window.End == nil {
			return errors.New("member-visible legacy rows require complete historical evidence and a current member entity")
		}
	}
	return nil
}
func (a *App) legacyTrafficRoutes(w http.ResponseWriter, r *http.Request, user Record, session Session) bool {
	if !strings.HasPrefix(r.URL.Path, "/api/traffic/legacy/") {
		return false
	}
	if a.legacyTraffic == nil {
		problem(w, 503, "legacy traffic store unavailable")
		return true
	}
	a.legacyMu.Lock()
	defer a.legacyMu.Unlock()
	s := a.store.Snapshot()
	actor, err := a.trafficActor(&s, user, session)
	if err != nil {
		problem(w, 403, "legacy traffic access denied")
		return true
	}
	member := str(actor, "role") == "member"
	path := strings.TrimPrefix(r.URL.Path, "/api/traffic/legacy/")
	if path == "catalog" && r.Method == "GET" {
		catalog, err := a.legacyTraffic.List()
		if err != nil {
			problem(w, 503, "legacy catalog unavailable")
			return true
		}
		items := []Record{}
		for _, meta := range catalog.Batches {
			data, _ := json.Marshal(meta)
			view := Record{}
			_ = json.Unmarshal(data, &view)
			approval := legacyApproval(&s, meta.ID)
			reviewed := approval != nil && approval.Checksum == meta.Checksum
			view["reviewed"] = reviewed
			if member {
				if !reviewed {
					continue
				}
				revision := a.trafficMemberRevision(&s, str(actor, "memberId"))
				result, e := a.legacyTraffic.Query(traffic.LegacyQuery{BatchID: meta.ID, MemberID: str(actor, "memberId"), MemberRevision: revision, Limit: 1})
				if e != nil {
					problem(w, 503, "legacy scope unavailable")
					return true
				}
				if result.TotalRows == 0 {
					continue
				}
				view = Record{"id": a.legacyMemberAlias(str(actor, "memberId"), revision, meta.ID), "format": meta.Format, "source": Record{"name": "已核对历史统计", "timezone": meta.Source.Timezone, "backend": meta.Source.Backend, "table": meta.Source.Table}, "rowCount": result.TotalRows, "staffOnlyRows": 0, "importedAt": meta.ImportedAt, "reviewed": true}
			}
			items = append(items, view)
		}
		out := Record{"sourceKind": "legacy-report", "batches": items, "capabilities": Record{"canPreview": !session.API && (str(actor, "role") == "admin" || str(actor, "role") == "operator"), "canImport": !session.API && str(actor, "role") == "admin", "canDelete": !session.API && str(actor, "role") == "admin"}, "notices": []string{"旧滑窗、采样日总量和RRD速率保留原单位/窗口；不合计不同批次或重叠窗口。", "缺历史归属证据的行仅工作人员可见，不按当前端口所有者回填。"}}
		if !member {
			out["bytes"], out["maxBytes"], out["maxBatches"] = catalog.Bytes, catalog.MaxBytes, catalog.MaxBatches
		}
		respond(w, 200, out)
		return true
	}
	if path == "query" && r.Method == "GET" {
		v := r.URL.Query()
		q := traffic.LegacyQuery{BatchID: v.Get("batchId"), SourceKind: v.Get("sourceKind"), EntityKind: v.Get("entityKind"), EntityID: v.Get("entityId"), Period: v.Get("period"), Category: v.Get("category"), OriginalDayFrom: v.Get("originalDayFrom"), OriginalDayTo: v.Get("originalDayTo"), Limit: 100}
		for _, field := range []struct {
			name string
			dest *int
		}{{"family", &q.Family}, {"offset", &q.Offset}, {"limit", &q.Limit}} {
			if raw := v.Get(field.name); raw != "" {
				value, e := strconv.Atoi(raw)
				if e != nil {
					problem(w, 400, "invalid legacy numeric filter")
					return true
				}
				*field.dest = value
			}
		}
		for _, field := range []struct {
			name string
			dest **time.Time
		}{{"windowFrom", &q.WindowFrom}, {"windowTo", &q.WindowTo}} {
			if raw := v.Get(field.name); raw != "" {
				at, e := time.Parse(time.RFC3339, raw)
				if e != nil {
					problem(w, 400, "legacy window filter must be RFC3339")
					return true
				}
				*field.dest = &at
			}
		}
		publicID := q.BatchID
		if member {
			q.MemberID = str(actor, "memberId")
			q.MemberRevision = a.trafficMemberRevision(&s, q.MemberID)
			q.BatchID = ""
			for _, approval := range s.LegacyImports {
				if a.legacyMemberAlias(q.MemberID, q.MemberRevision, approval.ID) == publicID {
					batch, e := a.legacyTraffic.Get(approval.ID)
					if e == nil && batch.Checksum == approval.Checksum {
						q.BatchID = approval.ID
					}
					break
				}
			}
			if q.BatchID == "" {
				problem(w, 404, "legacy batch not found")
				return true
			}
		}
		result, e := a.legacyTraffic.Query(q)
		if e != nil {
			problem(w, 422, e.Error())
			return true
		}
		latest := a.store.Snapshot()
		current, e := a.trafficActor(&latest, user, session)
		if e != nil || str(current, "role") != str(actor, "role") || member && (str(current, "memberId") != q.MemberID || a.trafficMemberRevision(&latest, q.MemberID) != q.MemberRevision) {
			problem(w, 403, "legacy history access changed")
			return true
		}
		if member {
			approval := legacyApproval(&latest, q.BatchID)
			if approval == nil {
				problem(w, 404, "legacy batch no longer published")
				return true
			}
			result.BatchID = publicID
			for i := range result.Rows {
				result.Rows[i].OwnerEvidence = nil
				result.Rows[i].MemberRevision = ""
				result.Rows[i].Window.Evidence = ""
				result.Rows[i].Notices = append(result.Rows[i].Notices, "历史归属已由管理员核对；不公开证明材料中的内部说明。")
			}
		}
		respond(w, 200, result)
		return true
	}
	if session.API || member || str(actor, "role") == "viewer" {
		problem(w, 403, "legacy changes require a staff browser session")
		return true
	}
	if (path == "preview" || path == "import") && r.Method == "POST" {
		var body struct {
			Input                traffic.LegacyInput `json:"input"`
			ExpectedChecksum     string              `json:"expectedChecksum"`
			AcknowledgeWindows   bool                `json:"acknowledgeWindows"`
			AcknowledgeOwnership bool                `json:"acknowledgeOwnership"`
			CurrentPassword      string              `json:"currentPassword"`
			Code                 string              `json:"code"`
		}
		if !decodeLegacy(w, r, &body) {
			return true
		}
		batch, e := traffic.ParseLegacy(body.Input)
		if e != nil {
			problem(w, 422, e.Error())
			return true
		}
		if path == "preview" {
			staff := 0
			for _, row := range batch.Rows {
				if row.StaffOnly {
					staff++
				}
			}
			rows := batch.Rows
			if len(rows) > 100 {
				rows = rows[:100]
			}
			respond(w, 200, Record{"batchId": batch.ID, "checksum": batch.Checksum, "source": batch.Source, "format": batch.Format, "rowCount": len(batch.Rows), "staffOnlyRows": staff, "rows": rows, "notices": batch.Notices})
			return true
		}
		if str(actor, "role") != "admin" {
			problem(w, 403, "administrator import required")
			return true
		}
		if a.limited(r) {
			problem(w, 429, "too many authentication attempts")
			return true
		}
		if !body.AcknowledgeWindows || body.ExpectedChecksum != batch.Checksum {
			problem(w, 409, "review the exact legacy windows and checksum before importing")
			return true
		}
		memberRows := false
		for _, row := range batch.Rows {
			memberRows = memberRows || !row.StaffOnly
		}
		if memberRows && !body.AcknowledgeOwnership {
			problem(w, 400, "explicit historical ownership disclosure acknowledgement required")
			return true
		}
		e = a.reauth(str(actor, "id"), body.CurrentPassword, body.Code, func(s *State, _ *SecurityUser) error {
			u, err := a.atlasActor(s, user, session, "admin")
			if err != nil {
				return err
			}
			if err := a.validateLegacyOwnerClaims(s, batch); err != nil {
				return err
			}
			if err := a.bindObservationKey(s); err != nil {
				return err
			}
			auditDetails(s, str(u, "id"), "legacy-import-review", "legacy-traffic", batch.ID, Record{"checksum": batch.Checksum, "memberRows": memberRows})
			return nil
		})
		if e != nil {
			problem(w, 403, e.Error())
			return true
		}
		_, existingErr := a.legacyTraffic.Get(batch.ID)
		meta, e := a.legacyTraffic.Put(batch, body.ExpectedChecksum)
		if e != nil {
			problem(w, 422, e.Error())
			return true
		}
		e = a.store.Update(func(s *State) error {
			u, err := a.atlasActor(s, user, session, "admin")
			if err != nil {
				return err
			}
			if err := a.validateLegacyOwnerClaims(s, batch); err != nil {
				return err
			}
			entry := LegacyImportApproval{ID: batch.ID, Checksum: batch.Checksum, ApprovedBy: str(u, "id"), ApprovedAt: time.Now().UTC()}
			if old := legacyApproval(s, batch.ID); old != nil {
				*old = entry
			} else {
				s.LegacyImports = append(s.LegacyImports, entry)
			}
			audit(s, str(u, "id"), "legacy-import", "legacy-traffic", batch.ID)
			return nil
		})
		if e != nil {
			if existingErr != nil {
				_ = a.legacyTraffic.Delete(batch.ID, batch.Checksum)
			}
			problem(w, 409, "legacy import authority changed; no member publication was committed")
			return true
		}
		respond(w, 201, meta)
		return true
	}
	if strings.HasPrefix(path, "batches/") && r.Method == "DELETE" {
		if str(actor, "role") != "admin" {
			problem(w, 403, "administrator required")
			return true
		}
		id := strings.TrimPrefix(path, "batches/")
		var body struct {
			ExpectedChecksum string `json:"expectedChecksum"`
			Reason           string `json:"reason"`
			Acknowledge      bool   `json:"acknowledge"`
		}
		if !decode(w, r, &body) {
			return true
		}
		if !body.Acknowledge || strings.TrimSpace(body.Reason) == "" || len(body.Reason) > 1000 {
			problem(w, 400, "acknowledgement and retention reason required")
			return true
		}
		batch, e := a.legacyTraffic.Get(id)
		if e != nil {
			problem(w, 404, "legacy batch not found")
			return true
		}
		if batch.Checksum != body.ExpectedChecksum {
			problem(w, 409, "legacy batch changed")
			return true
		}
		e = a.store.Update(func(s *State) error {
			u, err := a.atlasActor(s, user, session, "admin")
			if err != nil {
				return err
			}
			for i, v := range s.LegacyImports {
				if v.ID == id {
					s.LegacyImports = append(s.LegacyImports[:i], s.LegacyImports[i+1:]...)
					break
				}
			}
			auditDetails(s, str(u, "id"), "legacy-unpublish", "legacy-traffic", id, Record{"reason": body.Reason, "checksum": batch.Checksum})
			return nil
		})
		if e != nil {
			problem(w, 403, "retention authority changed")
			return true
		}
		if e := a.legacyTraffic.Delete(id, body.ExpectedChecksum); e != nil {
			problem(w, 500, "member publication revoked but legacy file cleanup failed; retry retention")
			return true
		}
		respond(w, 200, Record{"ok": true})
		return true
	}
	problem(w, 405, "legacy method not allowed")
	return true
}
