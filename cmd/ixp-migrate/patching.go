package main

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/moedove/ixp-manager/internal/migrate"
)

func (c *converter) patchFacts(table string, r row, fields []string) (map[string]string, error) {
	out := map[string]string{}
	for _, field := range fields {
		c.used[table][strconv.FormatInt(idOf(r), 10)+":"+field] = true
		v, exists := r[field]
		if !exists || v == nil {
			continue
		}
		switch v := v.(type) {
		case string:
			out[field] = v // Preserve notes, filenames and source dates exactly.
		case json.Number:
			out[field] = v.String()
		case bool:
			out[field] = strconv.FormatBool(v)
		case int:
			out[field] = strconv.Itoa(v)
		case int64:
			out[field] = strconv.FormatInt(v, 10)
		case float64:
			out[field] = strconv.FormatFloat(v, 'g', -1, 64)
		default:
			return nil, fmt.Errorf("%s id %d: %s must be a scalar source value", table, idOf(r), field)
		}
	}
	return out, nil
}
func patchSourceRef(kind string, v any) string {
	n, e := integer(v)
	if e != nil || n < 1 {
		return ""
	}
	return migrate.PatchID(kind, n)
}
func (c *converter) patching() error {
	if len(c.source.Tables["patch_panel"])+len(c.source.Tables["patch_panel_port"])+len(c.source.Tables["patch_panel_port_history"])+len(c.source.Tables["patch_panel_port_file"])+len(c.source.Tables["patch_panel_port_history_file"]) == 0 {
		return nil
	}
	p := &migrate.PatchingImport{Version: 1, SourceTimezone: "unknown", Panels: []migrate.PatchPanelSource{}, Ports: []migrate.PatchPortSource{}, Histories: []migrate.PatchHistorySource{}, Files: []migrate.PatchFileSource{}}
	panelFields := []string{"id", "cabinet_id", "name", "colo_reference", "cable_type", "connector_type", "installation_date", "port_prefix", "active", "chargeable", "colo_pp_type", "location_notes", "u_position", "mounted_at", "created_at", "updated_at"}
	portFields := []string{"id", "switch_port_id", "patch_panel_id", "customer_id", "state", "notes", "assigned_at", "connected_at", "cease_requested_at", "ceased_at", "last_state_change", "internal_use", "chargeable", "duplex_master_id", "number", "colo_circuit_ref", "ticket_ref", "private_notes", "owned_by", "description", "colo_billing_ref", "created_at", "updated_at"}
	historyFields := []string{"id", "patch_panel_port_id", "state", "notes", "assigned_at", "connected_at", "cease_requested_at", "ceased_at", "internal_use", "chargeable", "customer", "switchport", "duplex_master_id", "number", "colo_circuit_ref", "ticket_ref", "private_notes", "owned_by", "description", "colo_billing_ref", "cust_id", "created_at", "updated_at"}
	for _, r := range c.source.Tables["patch_panel"] {
		facts, e := c.patchFacts("patch_panel", r, panelFields)
		if e != nil {
			return e
		}
		p.Panels = append(p.Panels, migrate.PatchPanelSource{ID: migrate.PatchID("patch-panels", idOf(r)), LegacyID: idOf(r), RackID: c.ref("patch_panel", r, "cabinet_id", "racks"), Name: facts["name"], Prefix: facts["port_prefix"], Facts: facts})
	}
	for _, r := range c.source.Tables["patch_panel_port"] {
		facts, e := c.patchFacts("patch_panel_port", r, portFields)
		if e != nil {
			return e
		}
		// A legacy public LOA bearer code never enters an import or its facts.
		c.used["patch_panel_port"][strconv.FormatInt(idOf(r), 10)+":loa_code"] = true
		if r["loa_code"] != nil && r["loa_code"] != "" {
			c.warn("patch_panel_port", r, "old LOA bearer authorization excluded; issue a new reviewed LOA in the new system")
		}
		p.Ports = append(p.Ports, migrate.PatchPortSource{ID: migrate.PatchID("patch-ports", idOf(r)), LegacyID: idOf(r), PanelID: patchSourceRef("patch-panels", r["patch_panel_id"]), MemberID: c.ref("patch_panel_port", r, "customer_id", "members"), SwitchPortID: c.ref("patch_panel_port", r, "switch_port_id", "ports"), DuplexMasterID: patchSourceRef("patch-ports", r["duplex_master_id"]), Number: int(c.num("patch_panel_port", r, "number")), State: int(c.num("patch_panel_port", r, "state")), Facts: facts})
		if p.Ports[len(p.Ports)-1].State == 999 {
			c.warn("patch_panel_port", r, "Other state requires staff review; its entire panel must not become available inventory")
		}
	}
	for _, r := range c.source.Tables["patch_panel_port_history"] {
		facts, e := c.patchFacts("patch_panel_port_history", r, historyFields)
		if e != nil {
			return e
		}
		p.Histories = append(p.Histories, migrate.PatchHistorySource{ID: migrate.PatchID("patch-history", idOf(r)), LegacyID: idOf(r), PortID: patchSourceRef("patch-ports", r["patch_panel_port_id"]), DuplexMasterID: patchSourceRef("patch-history", r["duplex_master_id"]), Number: int(c.num("patch_panel_port_history", r, "number")), State: int(c.num("patch_panel_port_history", r, "state")), Facts: facts})
	}
	for _, kind := range []string{"current", "history"} {
		table, parent, target := "patch_panel_port_file", "patch_panel_port_id", "patch-ports"
		if kind == "history" {
			table, parent, target = "patch_panel_port_history_file", "patch_panel_port_history_id", "patch-history"
		}
		fields := []string{"id", parent, "name", "type", "uploaded_at", "uploaded_by", "size", "is_private", "storage_location", "created_at", "updated_at"}
		for _, r := range c.source.Tables[table] {
			facts, e := c.patchFacts(table, r, fields)
			if e != nil {
				return e
			}
			private := true
			switch facts["is_private"] {
			case "0", "false":
				private = false
			case "1", "true":
			default:
				c.warn(table, r, "missing or unknown privacy flag retained as staff-only")
			}
			size, e := integer(r["size"])
			if e != nil {
				return fmt.Errorf("%s id %d: attachment size must be an integer", table, idOf(r))
			}
			p.Files = append(p.Files, migrate.PatchFileSource{ID: migrate.PatchFileID(kind, idOf(r)), LegacyID: idOf(r), Kind: kind, ParentID: patchSourceRef(target, r[parent]), Name: facts["name"], Private: private, StorageKey: facts["storage_location"], Size: size, Entry: migrate.PatchFileEntry(kind, idOf(r)), Facts: facts})
		}
	}
	if err := migrate.ValidatePatching(p, false); err != nil {
		return err
	}
	c.patchReviewWarnings(p)
	c.result.Patching = p
	return nil
}

// Strict conversion must flag conditions that the importer can only retain as
// non-allocatable review records. It never fills missing physical port slots.
func (c *converter) patchReviewWarnings(p *migrate.PatchingImport) {
	ports := map[string]migrate.PatchPortSource{}
	panels := map[string]bool{}
	histories := map[string]bool{}
	byPanel := map[string][]migrate.PatchPortSource{}
	slaves := map[string][]migrate.PatchPortSource{}
	for _, v := range p.Ports {
		ports[v.ID] = v
		byPanel[v.PanelID] = append(byPanel[v.PanelID], v)
		if v.DuplexMasterID != "" {
			slaves[v.DuplexMasterID] = append(slaves[v.DuplexMasterID], v)
		}
	}
	for _, v := range p.Panels {
		panels[v.ID] = true
	}
	for _, v := range p.Histories {
		histories[v.ID] = true
	}
	for _, panel := range p.Panels {
		issues := []string{}
		seen := map[int]bool{}
		max := 0
		if strings.TrimSpace(panel.Name) == "" || panel.RackID == "" {
			issues = append(issues, "missing converted rack/name")
		}
		if len(byPanel[panel.ID]) == 0 {
			issues = append(issues, "no complete port inventory")
		}
		for _, port := range byPanel[panel.ID] {
			if port.Number < 1 || port.Number > 1024 || seen[port.Number] {
				issues = append(issues, "invalid or duplicate port number")
			}
			seen[port.Number] = true
			if port.Number > max {
				max = port.Number
			}
			if port.State < 1 || port.State > 8 {
				issues = append(issues, "unsupported port state")
			}
			master := port
			if port.DuplexMasterID != "" {
				m, ok := ports[port.DuplexMasterID]
				if !ok || m.PanelID != panel.ID || m.DuplexMasterID != "" || m.ID == port.ID {
					issues = append(issues, "unresolved duplex graph")
				} else {
					master = m
				}
				if (port.MemberID != "" && port.MemberID != master.MemberID) || (port.SwitchPortID != "" && port.SwitchPortID != master.SwitchPortID) || (port.State != 1 && port.State != master.State) {
					issues = append(issues, "slave has independent allocation")
				}
			}
			if len(slaves[port.ID]) > 1 {
				issues = append(issues, "multiple duplex slaves")
			}
			if master.State >= 2 && master.State <= 5 && master.MemberID == "" {
				issues = append(issues, "allocated/internal port without a resolved member")
			}
			if (master.State == 1 || master.State == 6 || master.State == 8) && master.MemberID != "" {
				issues = append(issues, "free/prewired/broken port still has a member")
			}
			if master.State == 8 && master.SwitchPortID == "" {
				issues = append(issues, "prewire without resolved switch port")
			}
			for key, value := range map[string]string{"customer_id": port.MemberID, "switch_port_id": port.SwitchPortID} {
				if raw := port.Facts[key]; raw != "" && raw != "0" && value == "" {
					issues = append(issues, "unresolved source foreign key")
				}
			}
		}
		if max != len(seen) {
			issues = append(issues, "missing physical port rows")
		}
		if len(issues) > 0 {
			c.warn("patch_panel", row{"id": panel.LegacyID}, "entire panel requires staff review, never auto-filled as available: "+strings.Join(issues, "; "))
		}
	}
	for _, port := range p.Ports {
		if !panels[port.PanelID] {
			c.warn("patch_panel_port", row{"id": port.LegacyID}, "parent panel absent; retained as an unallocated source record")
		}
	}
	for _, file := range p.Files {
		table := "patch_panel_port_file"
		parent, found := ports[file.ParentID]
		if file.Kind == "history" {
			table = "patch_panel_port_history_file"
			found = histories[file.ParentID]
		}
		if !found {
			c.warn(table, row{"id": file.LegacyID}, "original parent is absent; attachment retained staff-only")
		}
		if file.Kind == "current" && !file.Private && parent.MemberID == "" {
			c.warn(table, row{"id": file.LegacyID}, "raw referenced port has no confirmed customer; duplex inheritance never grants file access")
		}
	}
}
