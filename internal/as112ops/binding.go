package as112ops

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"slices"

	"github.com/moedove/ixp-manager/internal/routing"
)

// InstanceBinding changes only local execution authority, never an artifact or
// its original plan checksum. HostIdentity is scoped to this random instance ID;
// the machine identifier itself is never persisted or returned.
type InstanceBinding struct {
	Version        int         `json:"version"`
	InstanceID     string      `json:"instanceId"`
	Host           TargetPin   `json:"host"`
	HostIdentity   string      `json:"hostIdentity,omitempty"`
	PlanHosts      []TargetPin `json:"planHosts"`
	ReviewChecksum string      `json:"reviewChecksum,omitempty"`
}

func validTarget(p TargetPin) bool {
	return len(p.BootID) == 36 && len(p.NetworkNamespace) >= 3 && len(p.NetworkNamespace) <= 64
}
func validateBinding(b *InstanceBinding) error {
	if b == nil {
		return nil
	}
	if b.Version != 1 || !validHash(b.InstanceID) || !validTarget(b.Host) || b.HostIdentity != "" && !validHash(b.HostIdentity) || b.ReviewChecksum != "" && !validHash(b.ReviewChecksum) || len(b.PlanHosts) == 0 || len(b.PlanHosts) > 128 {
		return errors.New("invalid local instance binding")
	}
	for i, h := range b.PlanHosts {
		if !validTarget(h) || slices.Contains(b.PlanHosts[:i], h) {
			return errors.New("invalid original plan host bindings")
		}
	}
	return nil
}
func copyBinding(b *InstanceBinding) *InstanceBinding {
	if b == nil {
		return nil
	}
	out := *b
	out.PlanHosts = slices.Clone(b.PlanHosts)
	return &out
}
func boundTarget(p Plan, s Status) TargetPin {
	if s.Binding != nil {
		return s.Binding.Host
	}
	return p.Host
}
func scopeHostIdentity(instance, machine string) string {
	return routing.Checksum("ixp-as112/local-host/v1\x00" + instance + "\x00" + machine)
}
func newBinding(p Plan) (*InstanceBinding, error) {
	var id [32]byte
	if _, e := rand.Read(id[:]); e != nil {
		return nil, e
	}
	b := &InstanceBinding{Version: 1, InstanceID: hex.EncodeToString(id[:]), Host: p.Host, PlanHosts: []TargetPin{p.Host}}
	b.HostIdentity = captureHostIdentity(b.InstanceID)
	return b, nil
}
func verifyTarget(p Plan, s Status) error {
	actual, e := CaptureTarget()
	if e != nil || actual != boundTarget(p, s) {
		return errors.New("AS112 host or network namespace differs from approved execution binding")
	}
	if s.Binding != nil {
		if e = validateBinding(s.Binding); e != nil {
			return e
		}
		if !slices.Contains(s.Binding.PlanHosts, p.Host) {
			return errors.New("plan origin is absent from the local instance binding")
		}
		if s.Binding.HostIdentity != "" && captureHostIdentity(s.Binding.InstanceID) != s.Binding.HostIdentity {
			return errors.New("AS112 stable local host identity changed or unavailable")
		}
	}
	return nil
}
func (c *Controller) acceptsPlanHost(host TargetPin) bool {
	return host == c.plan.Host || c.state.Binding != nil && slices.Contains(c.state.Binding.PlanHosts, host)
}
