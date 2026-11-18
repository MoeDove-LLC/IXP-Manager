package live

import (
	"strings"

	"github.com/moedove/ixp-manager/internal/routeros"
	"github.com/moedove/ixp-manager/internal/routeros/api"
	"github.com/moedove/ixp-manager/internal/routeros/control"
)

// These grants come only from the supervisor's authenticated private journal.
// They are checked against this actual complete observation, and remain scoped
// to this one read: they never become persistent name-based cache authority.
func (r *Runtime) credentialAuthority(inv api.Inventory, known routeros.KnownState, grants []control.CredentialAuthority) (map[string]string, error) {
	allowed := map[string]string{}
	if len(grants) == 0 {
		return allowed, nil
	}
	if len(grants) > r.config.MaxObjects || len(known.Peers) > r.config.MaxObjects {
		return nil, fail("credential-authority-bound")
	}
	names := map[string]bool{}
	for _, peer := range known.Peers {
		name, e := routeros.ManagedConnectionName(r.target.RouterID, peer.ID)
		if e != nil || names[name] {
			return nil, fail("credential-authority-manifest-invalid")
		}
		names[name] = true
	}
	rows := map[string]api.Record{}
	for _, row := range inv.Connections {
		id := strings.ToUpper(row[".id"])
		if !nativeID.MatchString(id) || rows[id] != nil {
			return nil, fail("credential-authority-inventory-ambiguous")
		}
		rows[id] = row
	}
	seenNames := map[string]bool{}
	owner := routeros.OwnerName(r.target.RouterID)
	for _, grant := range grants {
		id := strings.ToUpper(grant.ID)
		if !nativeID.MatchString(grant.ID) || grant.ID != id || !digestPattern.MatchString(grant.AttributesChecksum) || allowed[id] != "" || seenNames[grant.Name] || !names[grant.Name] {
			return nil, fail("credential-authority-invalid")
		}
		row := rows[id]
		if row == nil || row["name"] != grant.Name || row["instance"] != owner || row["routing-table"] != owner || row["local.role"] != "ebgp-rs" {
			return nil, fail("credential-authority-object-mismatch")
		}
		checksum, e := control.CredentialAttributesChecksum(row)
		if e != nil || checksum != grant.AttributesChecksum {
			return nil, fail("credential-authority-attributes-mismatch")
		}
		allowed[id] = grant.Name
		seenNames[grant.Name] = true
	}
	return allowed, nil
}
