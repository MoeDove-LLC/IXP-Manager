package app

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/moedove/ixp-manager/internal/core"
)

type coreAdoption struct {
	ID   string `json:"id"`
	ETag string `json:"etag"`
}
type coreMutation struct {
	Bundle           core.Bundle       `json:"bundle"`
	ExpectedRevision string            `json:"expectedRevision,omitempty"`
	PortETags        map[string]string `json:"portEtags,omitempty"`
	AdoptLinks       []coreAdoption    `json:"adoptLinks,omitempty"`
}

func (a *App) coreHMAC(kind string, v any) string {
	raw, _ := json.Marshal(v)
	mac := hmac.New(sha256.New, a.store.securityKey)
	mac.Write([]byte("core-review-v1\x00" + kind + "\x00"))
	mac.Write(raw)
	return hex.EncodeToString(mac.Sum(nil))
}

func corePortDependencies(s *State, id string) Record {
	p := find(s, "ports", id)
	uses := []any{}
	for _, b := range s.CoreBundles {
		if contains(corePorts(b), id) {
			uses = append(uses, b)
		}
	}
	for _, r := range s.Resources["core-links"] {
		if str(r, "portAId") == id || str(r, "portBId") == id {
			uses = append(uses, r)
		}
	}
	for _, r := range s.Resources["connections"] {
		ps, _ := connectionPortIDs(r)
		if contains(ps, id) {
			uses = append(uses, r)
		}
	}
	for _, r := range s.Resources["patch-ports"] {
		if str(r, "switchPortId") == id {
			uses = append(uses, r)
		}
	}
	return Record{"port": p, "switch": find(s, "switches", str(p, "switchId")), "uses": uses}
}
func (a *App) corePortETag(s *State, id string) string {
	return a.coreHMAC("port", corePortDependencies(s, id))
}
func (a *App) coreRevision(s *State, b core.Bundle) string {
	deps := []Record{}
	for _, id := range corePorts(b) {
		deps = append(deps, corePortDependencies(s, id))
	}
	return a.coreHMAC("bundle", Record{"bundle": b, "dependencies": deps})
}
func (a *App) coreView(s *State, b core.Bundle) Record {
	raw, _ := json.Marshal(b)
	out := Record{}
	_ = json.Unmarshal(raw, &out)
	out["revision"] = a.coreRevision(s, b)
	out["derived"], out["warnings"] = coreDerived(s, b)
	return out
}

// Recheck account, live browser session or API token under the same store lock
// as a read or mutation. Token bindings come from its current stored version.
func (a *App) coreActor(s *State, user Record, session Session, write bool) (Record, []string, error) {
	roles := []string{"admin", "operator", "viewer"}
	if write {
		roles = []string{"admin", "operator"}
	}
	actor, err := a.atlasActor(s, user, session, roles...)
	if err != nil {
		return nil, nil, errors.New("core access changed; authenticate again")
	}
	if session.API {
		if write {
			return nil, nil, errors.New("core mutations require a staff browser session")
		}
		for _, t := range s.Security[str(actor, "id")].Tokens {
			if t.ID == session.TokenID && time.Now().Before(t.ExpiresAt) && contains(t.Scopes, "core:read") && len(t.SwitchIDs) > 0 {
				return actor, append([]string(nil), t.SwitchIDs...), nil
			}
		}
		return nil, nil, errors.New("core:read token scope required")
	}
	return actor, nil, nil
}
func coreVisible(b core.Bundle, switchIDs []string) bool {
	return len(switchIDs) == 0 || (contains(switchIDs, b.SideA.SwitchID) && contains(switchIDs, b.SideB.SwitchID))
}
func coreFlatVisible(s *State, l Record, scope []string) bool {
	return len(scope) == 0 || (contains(scope, str(find(s, "ports", str(l, "portAId")), "switchId")) && contains(scope, str(find(s, "ports", str(l, "portBId")), "switchId")))
}

func corePortBlocked(s *State, id, bundleID string) string {
	p := find(s, "ports", id)
	if p == nil {
		return "physical port not found"
	}
	if str(p, "memberId") != "" {
		return "assigned to a member"
	}
	for _, c := range s.Resources["connections"] {
		ids, _ := connectionPortIDs(c)
		if contains(ids, id) {
			return "used by a member connection"
		}
	}
	for _, p := range s.Resources["patch-ports"] {
		if str(p, "switchPortId") == id && str(p, "memberId") != "" {
			return "allocated to a member patch circuit"
		}
	}
	for _, b := range s.CoreBundles {
		if b.ID != bundleID && contains(corePorts(b), id) {
			return "used by another core bundle"
		}
	}
	for _, l := range s.Resources["core-links"] {
		if str(l, "portAId") == id || str(l, "portBId") == id {
			return "unbundled core link: explicit adoption required"
		}
	}
	return ""
}

func (a *App) coreInventory(s *State, scope []string, bundleID string) Record {
	switches, ports, members := []Record{}, []Record{}, []Record{}
	for _, sw := range s.Resources["switches"] {
		if len(scope) > 0 && !contains(scope, str(sw, "id")) {
			continue
		}
		switches = append(switches, clean(sw))
	}
	for _, p := range s.Resources["ports"] {
		if len(scope) > 0 && !contains(scope, str(p, "switchId")) {
			continue
		}
		v := clean(p)
		id := str(p, "id")
		reason := corePortBlocked(s, id, bundleID)
		v["_etag"] = a.corePortETag(s, id)
		v["available"] = reason == ""
		v["blockedReason"] = reason
		v["adoptable"] = strings.HasPrefix(reason, "unbundled core link:")
		ports = append(ports, v)
	}
	for _, m := range s.Resources["members"] {
		if len(scope) > 0 {
			visible := false
			for _, b := range s.CoreBundles {
				if coreVisible(b, scope) && (b.SideA.OwnerMemberID == str(m, "id") || b.SideB.OwnerMemberID == str(m, "id")) {
					visible = true
					break
				}
			}
			if !visible {
				continue
			}
		}
		members = append(members, Record{"id": str(m, "id"), "name": str(m, "name"), "asn": m["asn"], "type": m["type"]})
	}
	return Record{"switches": switches, "ports": ports, "members": members}
}

func (a *App) coreAdopt(s *State, b core.Bundle, adoptions []coreAdoption) ([]Record, error) {
	if len(adoptions) > core.MaxLinks {
		return nil, fmt.Errorf("too many links to adopt")
	}
	seen := map[string]bool{}
	adopted := []Record{}
	for _, ad := range adoptions {
		old := find(s, "core-links", ad.ID)
		if old == nil || seen[ad.ID] {
			return nil, fmt.Errorf("adoption references a missing or duplicate unbundled link")
		}
		seen[ad.ID] = true
		if ad.ETag == "" || ad.ETag != a.resourceETag("core-links", old) {
			return nil, errCoreConflict
		}
		found := false
		for _, l := range b.Links {
			a, z := str(old, "portAId"), str(old, "portBId")
			if (l.SideA.PortID == a && l.SideB.PortID == z) || (l.SideA.PortID == z && l.SideB.PortID == a) {
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("adopted link must remain as an explicit endpoint pair in the new bundle")
		}
		if (str(old, "type") == "layer2") != (b.Type == core.L2LAG) {
			return nil, fmt.Errorf("adoption cannot silently change the old link's layer; review and edit its inventory first")
		}
		adopted = append(adopted, old)
	}
	kept := []Record{}
	for _, r := range s.Resources["core-links"] {
		if !seen[str(r, "id")] {
			kept = append(kept, r)
		}
	}
	s.Resources["core-links"] = kept
	return adopted, nil
}

var errCoreConflict = errors.New("core inventory changed; reload and review the draft before saving")

func coreCheckMetadata(candidate core.Bundle, old *core.Bundle) error {
	if old == nil {
		if candidate.BindingRevision != "" || candidate.CreatedAt != "" || candidate.UpdatedAt != "" {
			return fmt.Errorf("core metadata is server managed")
		}
		return nil
	}
	if (candidate.BindingRevision != "" && candidate.BindingRevision != old.BindingRevision) || (candidate.CreatedAt != "" && candidate.CreatedAt != old.CreatedAt) || (candidate.UpdatedAt != "" && candidate.UpdatedAt != old.UpdatedAt) {
		return fmt.Errorf("core metadata cannot be overwritten")
	}
	return nil
}

func (a *App) coreRoutes(w http.ResponseWriter, r *http.Request, user Record, session Session) {
	path := strings.TrimPrefix(r.URL.Path, "/api/core")
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if r.Method == "GET" {
		a.store.mu.RLock()
		defer a.store.mu.RUnlock()
		if a.store.closed {
			problem(w, 503, "data store is closed")
			return
		}
		s := &a.store.state
		actor, scope, err := a.coreActor(s, user, session, false)
		if err != nil {
			problem(w, 403, err.Error())
			return
		}
		switch {
		case path == "" || path == "/":
			items, flat := []Record{}, []Record{}
			for _, b := range s.CoreBundles {
				if coreVisible(b, scope) {
					items = append(items, a.coreView(s, b))
				}
			}
			for _, l := range s.Resources["core-links"] {
				if coreFlatVisible(s, l, scope) {
					flat = append(flat, a.resourceView(s, "core-links", clean(l)))
				}
			}
			respond(w, 200, Record{"items": items, "unbundledLinks": flat, "capabilities": Record{"types": []string{core.ECMP, core.L2LAG, core.L3LAG}, "maxLinks": core.MaxLinks, "maxBundles": core.MaxBundles, "inventoryOnly": true, "physicalObservation": true, "lacpObservation": false, "bgpObservation": false, "bfdObservation": false, "canWrite": !session.API && (str(actor, "role") == "admin" || str(actor, "role") == "operator")}})
		case path == "/inventory":
			id := r.URL.Query().Get("bundleId")
			if id != "" {
				b := coreBundleByID(s, id)
				if b == nil || !coreVisible(*b, scope) {
					problem(w, 404, "core bundle not found")
					return
				}
			}
			respond(w, 200, a.coreInventory(s, scope, id))
		case len(parts) == 2 && parts[0] == "bundles":
			b := coreBundleByID(s, parts[1])
			if b == nil || !coreVisible(*b, scope) {
				problem(w, 404, "core bundle not found")
				return
			}
			respond(w, 200, a.coreView(s, *b))
		default:
			problem(w, 404, "core endpoint not found")
		}
		return
	}
	preview := path == "/preview" && r.Method == "POST"
	create := path == "/bundles" && r.Method == "POST"
	update := len(parts) == 2 && parts[0] == "bundles" && r.Method == "PUT"
	remove := len(parts) == 2 && parts[0] == "bundles" && r.Method == "DELETE"
	if !preview && !create && !update && !remove {
		problem(w, 404, "core action not found")
		return
	}
	if session.API {
		problem(w, 403, "core changes require a staff browser session")
		return
	}
	var input coreMutation
	var deletion struct {
		ExpectedRevision string `json:"expectedRevision"`
		Reason           string `json:"reason"`
	}
	if remove {
		if !decode(w, r, &deletion) {
			return
		}
		if !validResourceETag(deletion.ExpectedRevision) {
			problem(w, 428, "expectedRevision from the reviewed bundle is required")
			return
		}
		if len(strings.TrimSpace(deletion.Reason)) < 1 || len(deletion.Reason) > 1024 {
			problem(w, 422, "a deletion reason of 1..1024 bytes is required")
			return
		}
	} else {
		if !decode(w, r, &input) {
			return
		}
		if len(input.PortETags) > core.MaxLinks*4 {
			problem(w, 422, "too many port review tokens")
			return
		}
		if update && !validResourceETag(input.ExpectedRevision) {
			problem(w, 428, "expectedRevision from the reviewed bundle is required")
			return
		}
	}
	status := 422
	var result Record
	work := func(s *State) error {
		actor, _, err := a.coreActor(s, user, session, true)
		if err != nil {
			status = 403
			return err
		}
		var old *core.Bundle
		if update || remove {
			old = coreBundleByID(s, parts[1])
			if old == nil {
				status = 404
				return fmt.Errorf("core bundle not found")
			}
			expected := input.ExpectedRevision
			if remove {
				expected = deletion.ExpectedRevision
			}
			if expected != a.coreRevision(s, *old) {
				status = 409
				return errCoreConflict
			}
		}
		if remove {
			prior := *old
			kept := []core.Bundle{}
			for _, b := range s.CoreBundles {
				if b.ID != prior.ID {
					kept = append(kept, b)
				}
			}
			s.CoreBundles = kept
			stampCorePorts(s, &prior, nil, time.Now().UTC().Format(time.RFC3339Nano))
			if err := validateAll(s); err != nil {
				return err
			}
			auditDetails(s, str(actor, "id"), "delete", "core-bundles", prior.ID, Record{"reason": deletion.Reason, "bundle": prior})
			result = Record{"ok": true, "inventoryOnly": true}
			return nil
		}
		b := coreNormalize(input.Bundle)
		if update && b.ID != old.ID {
			return fmt.Errorf("bundle ID must match the URL")
		}
		if preview {
			old = coreBundleByID(s, b.ID)
		} else if create && coreBundleByID(s, b.ID) != nil {
			status = 409
			return fmt.Errorf("core bundle ID already exists")
		}
		if err := coreCheckMetadata(b, old); err != nil {
			return err
		}
		if err := core.Validate(b); err != nil {
			return err
		}
		if !preview {
			required := corePorts(b)
			if old != nil {
				required = append(required, corePorts(*old)...)
			}
			for _, id := range required {
				tag := input.PortETags[id]
				if tag == "" {
					status = 428
					return fmt.Errorf("review all old and new physical ports using /api/core/inventory")
				}
				if tag != a.corePortETag(s, id) {
					status = 409
					return errCoreConflict
				}
			}
		}
		adopted, err := a.coreAdopt(s, b, input.AdoptLinks)
		if err != nil {
			if errors.Is(err, errCoreConflict) {
				status = 409
			}
			return err
		}
		var prior *core.Bundle
		if old != nil {
			copy := *old
			prior = &copy
		}
		now := time.Now().UTC().Format(time.RFC3339Nano)
		b.BindingRevision = token(16)
		b.CreatedAt = now
		b.UpdatedAt = now
		if prior != nil {
			b.CreatedAt = prior.CreatedAt
		}
		if old == nil {
			s.CoreBundles = append(s.CoreBundles, b)
		} else {
			*old = b
		}
		// Preview works on a detached state. Include the complete intended port
		// reservation there, while retaining actual port tokens until save.
		for _, id := range corePorts(b) {
			p := find(s, "ports", id)
			if p == nil {
				return fmt.Errorf("core port not found")
			}
			p["coreReserved"] = true
		}
		if !preview {
			stampCorePorts(s, prior, &b, now)
		}
		if err := validateAll(s); err != nil {
			return err
		}
		if preview {
			out := b
			if prior == nil {
				out.BindingRevision = ""
				out.CreatedAt = ""
				out.UpdatedAt = ""
			} else {
				out.BindingRevision = prior.BindingRevision
				out.CreatedAt = prior.CreatedAt
				out.UpdatedAt = prior.UpdatedAt
			}
			d, warnings := coreDerived(s, b)
			result = Record{"bundle": out, "derived": d, "warnings": warnings}
			return nil
		}
		auditDetails(s, str(actor, "id"), "save", "core-bundles", b.ID, Record{"bindingRevision": b.BindingRevision, "adoptedLinks": adopted})
		result = a.coreView(s, b)
		return nil
	}
	var err error
	if preview {
		a.store.mu.RLock()
		if a.store.closed {
			err = errors.New("data store is closed")
			status = 503
		} else {
			s := cloneState(a.store.state)
			err = work(&s)
		}
		a.store.mu.RUnlock()
	} else {
		err = a.store.Update(work)
	}
	if err != nil {
		problem(w, status, err.Error())
		return
	}
	if create {
		respond(w, 201, result)
	} else {
		respond(w, 200, result)
	}
}
