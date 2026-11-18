package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"github.com/moedove/ixp-manager/internal/provision"
)

const provisionProfileKind = "provisioning-profiles"

func init() {
	Schemas[provisionProfileKind] = Schema{"name": textField(true), "options": {Type: "provision-options", Required: true}, "revision": textField(true), "dnsNewZone": {Type: "boolean"}, "dnsSerialFloor": num(0, math.MaxUint32, false)}
}

func parseProvisionOptions(raw any) (provision.Options, error) {
	var options provision.Options
	b, err := json.Marshal(raw)
	if err != nil || len(b) > 65536 {
		return options, errors.New("configuration profile options exceed 64 KiB")
	}
	decoder := json.NewDecoder(bytes.NewReader(b))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&options); err != nil {
		return options, errors.New("invalid typed configuration profile options")
	}
	if options.DNS.Serial != 0 {
		return options, errors.New("DNS serial is assigned by the server when generating a revision")
	}
	if options.Kind == "dns-arpa" {
		options.DNS.Serial = 1
	}
	options, err = provision.NormalizeOptions(options)
	if err != nil {
		return options, err
	}
	options.DNS.Serial = 0
	return options, nil
}

func optionsRecord(options provision.Options) Record {
	b, _ := json.Marshal(options)
	var result Record
	_ = json.Unmarshal(b, &result)
	return result
}

func validateProvisionProfile(s *State, record Record) error {
	if !credentialRevision.MatchString(str(record, "revision")) {
		return errors.New("invalid configuration profile revision")
	}
	options, err := parseProvisionOptions(record["options"])
	if err != nil {
		return err
	}
	if options.Kind == "as112-dns" {
		router := find(s, "routers", options.RouterID)
		if router == nil || routerRole(router) != "as112" || !validNoteEntity(noteEntity(router)) {
			return errors.New("AS112 DNS profile requires its current AS112 router entity")
		}
	}
	if options.VLANID != "" && find(s, "vlans", options.VLANID) == nil {
		return errors.New("configuration profile VLAN does not exist")
	}
	if options.InfrastructureID != "" && find(s, "infrastructures", options.InfrastructureID) == nil {
		return errors.New("configuration profile infrastructure does not exist")
	}
	if options.Kind == "switch-core" && find(s, "switches", options.SwitchID) == nil {
		return errors.New("configuration profile switch does not exist")
	}
	if options.VLANID != "" && options.InfrastructureID != "" && str(find(s, "vlans", options.VLANID), "infrastructureId") != options.InfrastructureID {
		return errors.New("configuration profile VLAN and infrastructure do not match")
	}
	if options.Kind == "dns-arpa" {
		if _, _, err := dnsProfileBaseline(record); err != nil {
			return err
		}
		if options.VLANID == "" {
			return errors.New("DNS profiles require an explicit VLAN and one of its exact prefixes")
		}
		prefix, err := netip.ParsePrefix(options.DNS.Prefix)
		prefixes, e := vlanPrefixes(find(s, "vlans", options.VLANID))
		found := false
		for _, candidate := range prefixes {
			if candidate == prefix {
				found = true
			}
		}
		if err != nil || e != nil || !found {
			return errors.New("DNS prefix must exactly match a configured VLAN prefix; partial authoritative zones are forbidden")
		}
		zone, err := provision.ReverseZone(options.DNS.Prefix)
		if err != nil {
			return err
		}
		if _, _, err := provisionSerialReference(s, record, zone); err != nil {
			return err
		}
		for _, other := range s.Resources[provisionProfileKind] {
			if str(other, "id") == str(record, "id") {
				continue
			}
			old, err := parseProvisionOptions(other["options"])
			if err != nil {
				return err
			}
			if old.Kind == "dns-arpa" {
				otherZone, err := provision.ReverseZone(old.DNS.Prefix)
				if err != nil || otherZone == zone {
					return errors.New("an authoritative reverse zone can have only one profile; independent DNS views are not configured")
				}
			}
		}
	} else if record["dnsNewZone"] != nil || record["dnsSerialFloor"] != nil {
		return errors.New("DNS serial settings only apply to reverse-zone profiles")
	}
	record["options"] = optionsRecord(options)
	return nil
}

func dnsProfileBaseline(profile Record) (bool, uint32, error) {
	isNew := profile["dnsNewZone"] == true
	raw, present := profile["dnsSerialFloor"]
	if isNew {
		if present && raw != nil {
			return false, 0, errors.New("choose a new zone or an existing SOA serial, not both")
		}
		return true, 0, nil
	}
	value, ok := raw.(float64)
	if !present || !ok || math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || value > math.MaxUint32 || value != math.Trunc(value) {
		return false, 0, errors.New("an existing DNS zone requires its current SOA serial; explicitly select new zone only if it has never been published")
	}
	return false, uint32(value), nil
}

// Resource exports preserve the latest comparable explicit/issued baseline;
// this is not a complete private backup or proof of what a server installed.
func carryProvisionSerialFloors(s *State) error {
	if err := validateProvisionSerials(s); err != nil {
		return err
	}
	for _, profile := range s.Resources[provisionProfileKind] {
		options, err := parseProvisionOptions(profile["options"])
		if err != nil {
			return err
		}
		if options.Kind != "dns-arpa" {
			continue
		}
		zone, err := provision.ReverseZone(options.DNS.Prefix)
		if err != nil {
			return err
		}
		baseline, known, err := provisionSerialReference(s, profile, zone)
		if err != nil {
			return err
		}
		if known {
			profile["dnsNewZone"] = false
			profile["dnsSerialFloor"] = float64(baseline)
		}
	}
	return nil
}

func validateProvisionProfiles(s *State) error {
	if len(s.Resources[provisionProfileKind]) > 256 {
		return errors.New("at most 256 configuration profiles are supported")
	}
	for _, profile := range s.Resources[provisionProfileKind] {
		if err := validateProvisionProfile(s, profile); err != nil {
			return err
		}
	}
	return nil
}

func (a *App) provisionProfiles(w http.ResponseWriter, r *http.Request, user Record, session Session) {
	if str(user, "role") == "member" {
		problem(w, 403, "staff access required")
		return
	}
	base := "/api/resources/" + provisionProfileKind
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, base), "/")
	id := ""
	if len(parts) == 2 && parts[0] == "" {
		id = parts[1]
	} else if len(parts) != 1 || parts[0] != "" {
		problem(w, 404, "configuration profile not found")
		return
	}
	if r.Method == "GET" {
		s := a.store.Snapshot()
		if id == "" {
			items := s.Resources[provisionProfileKind]
			if items == nil {
				items = []Record{}
			}
			respond(w, 200, Record{"items": items})
			return
		}
		if profile := find(&s, provisionProfileKind, id); profile != nil {
			respond(w, 200, clean(profile))
		} else {
			problem(w, 404, "configuration profile not found")
		}
		return
	}
	role := str(user, "role")
	if session.API || (role != "admin" && role != "operator") {
		problem(w, 403, "configuration profiles require an administrator or operator browser session")
		return
	}
	if (r.Method != "POST" && r.Method != "PUT" && r.Method != "DELETE") || (r.Method == "POST" && id != "") || (r.Method != "POST" && id == "") {
		problem(w, 405, "invalid configuration profile operation")
		return
	}
	var body struct {
		Name             string            `json:"name"`
		Options          provision.Options `json:"options"`
		ExpectedRevision string            `json:"expectedRevision"`
		DNSNewZone       *bool             `json:"dnsNewZone,omitempty"`
		DNSSerialFloor   *uint32           `json:"dnsSerialFloor,omitempty"`
	}
	if !decode(w, r, &body) {
		return
	}
	var result Record
	status := 400
	err := a.store.Update(func(s *State) error {
		if _, err := a.atlasActor(s, user, session, "admin", "operator"); err != nil {
			status = 401
			return errors.New("configuration profile authority changed; sign in again")
		}
		old := find(s, provisionProfileKind, id)
		if r.Method != "POST" {
			if old == nil {
				status = 404
				return errors.New("configuration profile not found")
			}
			if body.ExpectedRevision == "" || str(old, "revision") != body.ExpectedRevision {
				status = 409
				return errors.New("configuration profile changed; refresh before trying again")
			}
		}
		if r.Method == "DELETE" {
			for i, candidate := range s.Resources[provisionProfileKind] {
				if str(candidate, "id") == id {
					s.Resources[provisionProfileKind] = append(s.Resources[provisionProfileKind][:i], s.Resources[provisionProfileKind][i+1:]...)
					break
				}
			}
			audit(s, str(user, "id"), "delete", provisionProfileKind, id)
			result = Record{"ok": true}
			return nil
		}
		if old == nil && len(s.Resources[provisionProfileKind]) >= 256 {
			return errors.New("configuration profile quota reached")
		}
		now := time.Now().UTC().Format(time.RFC3339Nano)
		if old == nil {
			id = token(16)
		}
		result = Record{"id": id, "name": strings.TrimSpace(body.Name), "options": optionsRecord(body.Options), "revision": token(16), "createdAt": now, "updatedAt": now}
		if body.DNSNewZone != nil {
			result["dnsNewZone"] = *body.DNSNewZone
		}
		if body.DNSSerialFloor != nil {
			result["dnsSerialFloor"] = float64(*body.DNSSerialFloor)
		}
		if old != nil {
			result["createdAt"] = old["createdAt"]
		}
		if err := validate(s, provisionProfileKind, result); err != nil {
			return err
		}
		if old == nil {
			s.Resources[provisionProfileKind] = append(s.Resources[provisionProfileKind], result)
		} else {
			for i, candidate := range s.Resources[provisionProfileKind] {
				if str(candidate, "id") == id {
					s.Resources[provisionProfileKind][i] = result
					break
				}
			}
		}
		audit(s, str(user, "id"), strings.ToLower(r.Method), provisionProfileKind, id)
		return nil
	})
	if err != nil {
		problem(w, status, err.Error())
		return
	}
	if r.Method == "POST" {
		respond(w, 201, result)
	} else {
		respond(w, 200, result)
	}
}

func (a *App) renderProvisioning(s *State, profileID string, serial uint32) (provision.Artifact, error) {
	profile := find(s, provisionProfileKind, profileID)
	if profile == nil {
		return provision.Artifact{}, errors.New("configuration profile not found")
	}
	options, err := parseProvisionOptions(profile["options"])
	if err != nil {
		return provision.Artifact{}, err
	}
	if options.Kind == "dns-arpa" {
		options.DNS.Serial = serial
	}
	input, err := provisionInput(s, options)
	if err != nil {
		return provision.Artifact{}, err
	}
	artifact, err := provision.Render(input)
	if err != nil {
		return provision.Artifact{}, fmt.Errorf("configuration generation failed: %w", err)
	}
	return artifact, nil
}
