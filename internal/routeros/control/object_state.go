package control

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/moedove/ixp-manager/internal/routeros/api"
	"strings"
)

func ObjectAttributesChecksum(resource api.Resource, row api.Record) (string, error) {
	attrs, err := canonicalRecord(resource, row)
	if err != nil {
		return "", err
	}
	data, _ := json.Marshal(struct {
		Resource   api.Resource
		Attributes api.Record
	}{resource, attrs})
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// ExpectedObjectAttributes predicts only the approved in-place non-secret
// change. It is not a device observation or permission to issue that change.
func ExpectedObjectAttributes(before api.Record, m api.Mutation) (api.Record, error) {
	if api.ValidateMutation(m) != nil || !validObjectID(strings.ToUpper(m.ID)) || !strings.EqualFold(before[".id"], m.ID) {
		return nil, failure("object-mutation-invalid")
	}
	if m.Resource != api.BGPInstance && m.Resource != api.BGPConnection && m.Resource != api.FilterRule {
		return nil, failure("object-resource-not-permitted")
	}
	attrs, err := canonicalRecord(m.Resource, before)
	if err != nil {
		return nil, err
	}
	switch m.Action {
	case api.Set:
		for _, a := range m.Attributes {
			if a.Name == "tcp-md5-key" {
				continue
			}
			if a.Name == "chain" || a.Name == "comment" || a.Name == "name" && a.Value != attrs["name"] {
				return nil, failure("object-identity-change-not-permitted")
			}
			attrs[a.Name] = canonicalValue(m.Resource, a.Name, a.Value)
		}
	case api.Unset:
		if m.Resource != api.BGPConnection || m.Property != "input.limit-process-routes-ipv4" && m.Property != "input.limit-process-routes-ipv6" {
			return nil, failure("object-unset-not-permitted")
		}
		attrs[m.Property] = ""
	case api.Enable, api.Disable:
		if m.Resource != api.BGPConnection {
			return nil, failure("object-state-change-not-permitted")
		}
		attrs["disabled"] = "false"
		if m.Action == api.Disable {
			attrs["disabled"] = "true"
		}
	default:
		return nil, failure("object-action-not-permitted")
	}
	attrs[".id"] = strings.ToUpper(m.ID)
	if _, err = canonicalRecord(m.Resource, attrs); err != nil {
		return nil, err
	}
	return attrs, nil
}

func projectObject(key []byte, target api.Resource, id string, o ObjectObservation) (objectState, error) {
	if o.Resource != target || o.ID != id || o.Attributes[".id"] != id || !validObjectID(id) || !o.CrossChannelIdentityVerified || o.ObservedAt.IsZero() {
		return objectState{}, failure("unverified-object-observation")
	}
	attrs, err := canonicalRecord(target, o.Attributes)
	if err != nil {
		return objectState{}, err
	}
	obj := objectState{Resource: target, ID: id, Attributes: attrs}
	if target == api.BGPConnection {
		value := o.Credential.Value.CopyBytes()
		defer clear(value)
		if !o.Credential.Known || !o.Credential.Matches(value) {
			return objectState{}, failure("object-credential-unverified")
		}
		obj.CredentialKnown = true
		obj.CredentialDigest = credentialMAC(key, id, value)
	}
	return obj, nil
}
