package control

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/netip"
	"sort"
	"strings"

	"github.com/moedove/ixp-manager/internal/routeros"
	"github.com/moedove/ixp-manager/internal/routeros/api"
)

type objectState struct {
	Resource         api.Resource `json:"resource"`
	ID               string       `json:"id"`
	Attributes       api.Record   `json:"attributes"`
	CredentialKnown  bool         `json:"credentialKnown"`
	CredentialDigest string       `json:"credentialDigest,omitempty"`
}
type deviceState struct {
	Objects        map[string]objectState `json:"objects"`
	ExternalDigest string                 `json:"externalDigest"`
	Chains         map[string][]string    `json:"chains"`
}
type mutationSpec struct {
	Resource         api.Resource `json:"resource"`
	Action           api.Action   `json:"action"`
	ID               string       `json:"id,omitempty"`
	Property         string       `json:"property,omitempty"`
	Attributes       api.Record   `json:"attributes"`
	CredentialDigest string       `json:"credentialDigest,omitempty"`
}
type stateDelta struct {
	Key    string       `json:"key"`
	Object *objectState `json:"object,omitempty"`
	Remove bool         `json:"remove"`
}

func mac(key []byte, domain string, v any) string {
	data, _ := json.Marshal(v)
	h := hmac.New(sha256.New, key)
	h.Write([]byte(domain))
	h.Write([]byte{0})
	h.Write(data)
	return hex.EncodeToString(h.Sum(nil))
}
func credentialMAC(key []byte, id string, value []byte) string {
	h := hmac.New(sha256.New, key)
	h.Write([]byte("credential\x00" + id + "\x00"))
	h.Write(value)
	return hex.EncodeToString(h.Sum(nil))
}
func stateMAC(key []byte, s deviceState) string { return mac(key, "state", s) }
func objectKey(resource api.Resource, id string) string {
	return string(resource) + "/" + strings.ToUpper(id)
}
func copyRecord(row api.Record) api.Record {
	out := api.Record{}
	for k, v := range row {
		out[k] = v
	}
	return out
}
func canonicalValue(resource api.Resource, field, value string) string {
	switch field {
	case "fib", "disabled", "dynamic", "invalid", "inactive", "connect", "listen", "multihop", "output.remove-private-as", "output.as-override":
		switch value {
		case "yes":
			value = "true"
		case "no":
			value = "false"
		}
	}
	if (resource == api.BGPInstance || resource == api.BGPConnection) && field == "vrf" && value == "" {
		return "main"
	}
	if field == "remote.address" || field == "local.address" || field == "router-id" {
		if a, e := netip.ParseAddr(value); e == nil {
			return a.String()
		}
		if p, e := netip.ParsePrefix(value); e == nil && p.Bits() == p.Addr().BitLen() {
			return p.Addr().String()
		}
	}
	return value
}
func canonicalRecord(resource api.Resource, row api.Record) (api.Record, error) {
	if _, err := routeros.ObservedAttributesChecksum(resource, row); err != nil {
		return nil, failure("invalid-observed-attributes")
	}
	out := api.Record{}
	for k, v := range row {
		if k == ".id" || k == "used" || (k == "inactive" && resource != api.FilterRule) {
			continue
		}
		out[k] = canonicalValue(resource, k, v)
	}
	return out, nil
}
func projectObservation(key []byte, target routeros.Target, o Observation) (deviceState, error) {
	if o.Target != target || !o.CrossChannelIdentityVerified || o.Resolver == nil || o.ObservedAt.IsZero() {
		return deviceState{}, failure("unverified-observation")
	}
	owner := routeros.OwnerName(target.RouterID)
	s := deviceState{Objects: map[string]objectState{}, Chains: map[string][]string{}}
	external := []objectState{}
	externalChains := map[string][]string{}
	menus := []struct {
		r    api.Resource
		rows []api.Record
	}{{api.RoutingTable, o.Inventory.Tables}, {api.BGPInstance, o.Inventory.Instances}, {api.BGPConnection, o.Inventory.Connections}, {api.FilterRule, o.Inventory.FilterRules}}
	total := 0
	for _, menu := range menus {
		if o.Inventory.Capabilities[menu.r] != api.Supported {
			return deviceState{}, failure("incomplete-observation")
		}
		ids := map[string]bool{}
		for _, row := range menu.rows {
			total++
			if total > routeros.MaxPrepareOperations+20000 {
				return deviceState{}, failure("observation-row-limit")
			}
			id := strings.ToUpper(row[".id"])
			if !validObjectID(id) || ids[id] {
				return deviceState{}, failure("ambiguous-observed-id")
			}
			ids[id] = true
			attrs, err := canonicalRecord(menu.r, row)
			if err != nil {
				return deviceState{}, err
			}
			owned := attrs["name"] == owner || strings.HasPrefix(attrs["name"], owner+"_") || strings.HasPrefix(attrs["chain"], owner+"_") || strings.HasPrefix(attrs["comment"], owner+":")
			obj := objectState{Resource: menu.r, ID: id, Attributes: attrs}
			if !owned {
				external = append(external, obj)
				if menu.r == api.FilterRule {
					externalChains[attrs["chain"]] = append(externalChains[attrs["chain"]], id)
				}
				continue
			}
			if menu.r == api.BGPConnection {
				credential, exists := o.Credentials[id]
				if exists && credential.Known {
					value := credential.Value.CopyBytes()
					// A zero opaque SecretValue is not a verified empty value.
					if !credential.Matches(value) {
						clear(value)
						return deviceState{}, failure("unverified-credential-value")
					}
					obj.CredentialKnown = true
					obj.CredentialDigest = credentialMAC(key, id, value)
					clear(value)
				}
			}
			s.Objects[objectKey(menu.r, id)] = obj
			if menu.r == api.FilterRule {
				s.Chains[attrs["chain"]] = append(s.Chains[attrs["chain"]], id)
			}
		}
	}
	sort.Slice(external, func(i, j int) bool {
		return objectKey(external[i].Resource, external[i].ID) < objectKey(external[j].Resource, external[j].ID)
	})
	// Unmanaged attributes are not persisted, even if their comments contain
	// unrelated private data. A keyed digest detects changes without guessing.
	s.ExternalDigest = mac(key, "external", struct {
		Objects []objectState
		Chains  map[string][]string
	}{external, externalChains})
	return s, nil
}
func validObjectID(v string) bool {
	if len(v) < 2 || len(v) > 17 || v[0] != '*' {
		return false
	}
	for _, c := range v[1:] {
		if !(c >= '0' && c <= '9' || c >= 'A' && c <= 'F') {
			return false
		}
	}
	return true
}
func specFor(key []byte, m api.Mutation) mutationSpec {
	s := mutationSpec{Resource: m.Resource, Action: m.Action, ID: m.ID, Property: m.Property, Attributes: api.Record{}}
	for _, a := range m.Attributes {
		if a.Name == "tcp-md5-key" {
			s.CredentialDigest = credentialMAC(key, m.ID, []byte(a.Value))
			continue
		}
		s.Attributes[a.Name] = canonicalValue(m.Resource, a.Name, a.Value)
	}
	return s
}
func cloneState(s deviceState) deviceState {
	out := deviceState{Objects: make(map[string]objectState, len(s.Objects)), ExternalDigest: s.ExternalDigest}
	for k, v := range s.Objects {
		out.Objects[k] = v
	}
	if s.Chains != nil {
		out.Chains = map[string][]string{}
		for chain, ids := range s.Chains {
			out.Chains[chain] = append([]string(nil), ids...)
		}
	}
	return out
}
func applyDelta(s *deviceState, d stateDelta) {
	old, existed := s.Objects[d.Key]
	removeOrder := func(chain, id string) {
		ids := s.Chains[chain]
		next := []string{}
		for _, v := range ids {
			if v != id {
				next = append(next, v)
			}
		}
		if len(next) == 0 {
			delete(s.Chains, chain)
		} else {
			s.Chains[chain] = next
		}
	}
	if existed && old.Resource == api.FilterRule && (d.Remove || d.Object != nil && d.Object.Attributes["chain"] != old.Attributes["chain"]) {
		removeOrder(old.Attributes["chain"], old.ID)
	}
	if d.Object != nil && d.Object.Resource == api.FilterRule && (!existed || old.Attributes["chain"] != d.Object.Attributes["chain"]) {
		if s.Chains == nil {
			s.Chains = map[string][]string{}
		}
		chain := d.Object.Attributes["chain"]
		s.Chains[chain] = append(s.Chains[chain], d.Object.ID)
	}
	if d.Remove {
		delete(s.Objects, d.Key)
	} else if d.Object != nil {
		s.Objects[d.Key] = *d.Object
	}
}

// CredentialAttributesChecksum covers every normalized non-secret connection
// property used by the controller. ID and exact name are additionally checked
// by the live reader; no password enters this checksum.
func CredentialAttributesChecksum(row api.Record) (string, error) {
	attrs, err := canonicalRecord(api.BGPConnection, row)
	if err != nil {
		return "", err
	}
	data, _ := json.Marshal(struct {
		Domain     string
		Attributes api.Record
	}{"routeros-credential-authority-v1", attrs})
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), nil
}
func authorityFor(s deviceState) []CredentialAuthority {
	out := []CredentialAuthority{}
	for _, obj := range s.Objects {
		if obj.Resource != api.BGPConnection {
			continue
		}
		row := copyRecord(obj.Attributes)
		row[".id"] = obj.ID
		digest, err := CredentialAttributesChecksum(row)
		if err != nil {
			continue
		}
		out = append(out, CredentialAuthority{ID: obj.ID, Name: obj.Attributes["name"], AttributesChecksum: digest})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
func completeCredentials(s deviceState) bool {
	for _, o := range s.Objects {
		if o.Resource == api.BGPConnection && !o.CredentialKnown {
			return false
		}
	}
	return true
}
func newObjectMismatch(key []byte, owner string, obj objectState, spec mutationSpec) string {
	if obj.Resource != spec.Resource {
		return "resource"
	}
	required := api.Record{}
	switch spec.Resource {
	case api.RoutingTable:
		required = api.Record{"name": owner, "fib": "false", "disabled": "false", "dynamic": "false", "invalid": "false", "comment": ""}
	case api.BGPInstance:
		required = api.Record{"name": owner, "vrf": "main", "disabled": "false", "comment": ""}
	case api.FilterRule:
		required = api.Record{"disabled": "false", "inactive": "false"}
	case api.BGPConnection:
		required = api.Record{"as": "", "templates": "", "disabled": "true", "dynamic": "false", "routing-table": owner, "vrf": "main", "connect": "true", "listen": "true", "multihop": "false", "output.remove-private-as": "false", "output.as-override": "false", "comment": "", "input.filter-nlri": "", "output.filter-select": "", "output.network": "", "output.redistribute": "", "input.limit-process-routes-ipv4": "", "input.limit-process-routes-ipv6": ""}
		if obj.CredentialKnown && obj.CredentialDigest != credentialMAC(key, obj.ID, nil) {
			return "credential-state"
		}
	default:
		return "resource"
	}
	for k, v := range spec.Attributes {
		required[k] = v
	}
	keys := make([]string, 0, len(required))
	for k := range required {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		v := required[k]
		actual, ok := obj.Attributes[k]
		if !ok {
			return "missing-" + k
		}
		if actual != v {
			return "value-" + k
		}
	}
	keys = keys[:0]
	for k := range obj.Attributes {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if _, ok := required[k]; !ok {
			return "unexpected-" + k
		}
	}
	return ""
}

// verifyMutation predicts the exact allowed change. It does not accept a
// driver's desired=true/baseline=true assertion or an ACK as state evidence.
func verifyMutation(key []byte, owner string, before, after deviceState, spec mutationSpec, returnedID string) (stateDelta, error) {
	if before.ExternalDigest != after.ExternalDigest {
		return stateDelta{}, failure("external-state-changed")
	}
	expected := cloneState(before)
	delta := stateDelta{}
	switch spec.Action {
	case api.Add:
		if len(after.Objects) != len(before.Objects)+1 {
			return delta, failure("add-readback-not-exact")
		}
		for k, obj := range after.Objects {
			if _, ok := before.Objects[k]; ok {
				continue
			}
			if delta.Object != nil {
				return stateDelta{}, failure("added-object-count-mismatch")
			}
			if mismatch := newObjectMismatch(key, owner, obj, spec); mismatch != "" {
				return stateDelta{}, failure("added-object-" + mismatch)
			}
			if returnedID != "" && !strings.EqualFold(returnedID, obj.ID) {
				return stateDelta{}, failure("add-id-readback-mismatch")
			}
			copy := obj
			copy.Attributes = copyRecord(obj.Attributes)
			delta = stateDelta{Key: k, Object: &copy}
		}
		if delta.Object == nil {
			return delta, failure("added-object-not-observed")
		}
	case api.Set, api.Unset, api.Enable, api.Disable, api.Remove:
		k := objectKey(spec.Resource, spec.ID)
		obj, ok := before.Objects[k]
		if !ok {
			return delta, failure("mutation-object-not-owned")
		}
		delta.Key = k
		if spec.Action == api.Remove {
			delta.Remove = true
			break
		}
		obj.Attributes = copyRecord(obj.Attributes)
		switch spec.Action {
		case api.Set:
			for name, value := range spec.Attributes {
				obj.Attributes[name] = value
			}
			if spec.CredentialDigest != "" {
				obj.CredentialKnown = true
				obj.CredentialDigest = spec.CredentialDigest
			}
		case api.Unset:
			switch spec.Property {
			case "input.limit-process-routes-ipv4", "input.limit-process-routes-ipv6":
				obj.Attributes[spec.Property] = ""
			case "fib":
				obj.Attributes["fib"] = "false"
			default:
				return stateDelta{}, failure("unsupported-unset-property")
			}
		case api.Enable:
			obj.Attributes["disabled"] = "false"
		case api.Disable:
			obj.Attributes["disabled"] = "true"
		}
		delta.Object = &obj
	default:
		return delta, failure("unsupported-state-action")
	}
	applyDelta(&expected, delta)
	if stateMAC(key, expected) != stateMAC(key, after) {
		return stateDelta{}, failure("mutation-readback-mismatch")
	}
	return delta, nil
}
