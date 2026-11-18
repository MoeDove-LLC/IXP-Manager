// Package as112ops owns a dedicated local AS112 DNS/BGP instance. File
// publication is atomic; DNS and BGP process changes are separate WAL steps.
package as112ops

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/moedove/ixp-manager/internal/as112"
	"github.com/moedove/ixp-manager/internal/routing"
)

type BinaryPin struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}
type Tools struct {
	Router         BinaryPin `json:"router"`
	PDNS           BinaryPin `json:"pdns"`
	NamedCheckzone BinaryPin `json:"namedCheckzone"`
}
type TargetPin struct {
	BootID           string `json:"bootId"`
	NetworkNamespace string `json:"networkNamespace"`
}
type PlanInput struct {
	Root, Platform, ActiveConfig, DNSBundle      string
	Tools                                        Tools
	Host                                         TargetPin
	HealthIntervalSeconds, RecoveryHealthyChecks int
}
type Plan struct {
	Version               int       `json:"version"`
	Root                  string    `json:"root"`
	Platform              string    `json:"platform"`
	RouterID              string    `json:"routerId"`
	RouterCreatedAt       string    `json:"routerCreatedAt"`
	ActiveConfig          string    `json:"activeConfig"`
	StandbyConfig         string    `json:"standbyConfig"`
	DNSBundle             string    `json:"dnsBundle"`
	ActiveChecksum        string    `json:"activeChecksum"`
	StandbyChecksum       string    `json:"standbyChecksum"`
	DNSChecksum           string    `json:"dnsChecksum"`
	Checksum              string    `json:"checksum"`
	Tools                 Tools     `json:"tools"`
	Host                  TargetPin `json:"host"`
	HealthIntervalSeconds int       `json:"healthIntervalSeconds"`
	RecoveryHealthyChecks int       `json:"recoveryHealthyChecks"`
}
type Approval struct {
	PlanChecksum, ActiveChecksum, StandbyChecksum, DNSChecksum string
	ExclusiveInstanceAcknowledged                              bool
}
type Process struct {
	PID              int    `json:"pid"`
	StartTime        string `json:"startTime"`
	ExecutableSHA256 string `json:"executableSha256"`
	Kind             string `json:"kind"`
	Mode             string `json:"mode"`
}
type Observation struct{ Processes []Process }
type Intent struct {
	Sequence  uint64    `json:"sequence"`
	Kind      string    `json:"kind"`
	StartedAt time.Time `json:"startedAt"`
}
type Status struct {
	Version       int              `json:"version"`
	PlanChecksum  string           `json:"planChecksum"`
	RouterID      string           `json:"routerId"`
	Phase         string           `json:"phase"`
	Sequence      uint64           `json:"sequence"`
	Pending       *Intent          `json:"pending,omitempty"`
	Installed     bool             `json:"installed"`
	DNS           *Process         `json:"dns,omitempty"`
	BGP           *Process         `json:"bgp,omitempty"`
	HealthyChecks int              `json:"healthyChecks"`
	LastHealthy   bool             `json:"lastHealthy"`
	UpdatedAt     time.Time        `json:"updatedAt"`
	Reason        string           `json:"reason,omitempty"`
	Binding       *InstanceBinding `json:"binding,omitempty"`
}

// Runtime may perform only these fixed actions against the plan's dedicated
// instance. Observation must include every observable process under the
// dedicated service UID using its private root. Privileged out-of-band writers
// are outside the continuous-exclusive-ownership contract.
// Stop accepts only a process generation created by this Runtime instance.
type Runtime interface {
	Validate(context.Context, Plan, string) error
	Observe(context.Context, Plan) (Observation, error)
	StartDNS(context.Context, Plan, string) (Process, error)
	StartBGP(context.Context, Plan, string, bool) (Process, error)
	Stop(context.Context, Process) error
	Health(context.Context, Plan) (as112.HealthResult, error)
	Close() error
}

func supported(platform string) bool {
	return platform == "bird2" || platform == "bird3" || platform == "frr"
}
func cleanAbs(path string) bool {
	return filepath.IsAbs(path) && filepath.Clean(path) == path && path != "/" && !strings.ContainsAny(path, "\x00\r\n")
}
func validHash(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
func digest(p Plan) string {
	p.Checksum = ""
	b, _ := json.Marshal(p)
	return routing.Checksum(string(b))
}
func BuildPlan(in PlanInput) (Plan, error) {
	if len(in.Host.BootID) != 36 || len(in.Host.NetworkNamespace) < 3 || len(in.Host.NetworkNamespace) > 64 {
		return Plan{}, errors.New("actual boot and network namespace target pin required")
	}
	if !supported(in.Platform) {
		return Plan{}, errors.New("automatic AS112 process supervision supports dedicated foreground BIRD 2/3 and FRR; OpenBGPD privsep and remote Junos need separate verified containment/lease adapters")
	}
	if !cleanAbs(in.Root) || len(in.Root) > 160 {
		return Plan{}, errors.New("AS112 root must be a short absolute dedicated path")
	}
	for _, pin := range []BinaryPin{in.Tools.Router, in.Tools.PDNS, in.Tools.NamedCheckzone} {
		if !cleanAbs(pin.Path) || !validHash(pin.SHA256) {
			return Plan{}, errors.New("fixed absolute native binary and SHA256 pins required")
		}
	}
	b, e := as112.Decode(in.DNSBundle)
	if e != nil {
		return Plan{}, e
	}
	standby, e := routing.AS112Standby(in.Platform, in.ActiveConfig, b)
	if e != nil {
		return Plan{}, e
	}
	if in.HealthIntervalSeconds == 0 {
		in.HealthIntervalSeconds = 5
	}
	if in.RecoveryHealthyChecks == 0 {
		in.RecoveryHealthyChecks = 2
	}
	if in.HealthIntervalSeconds < 1 || in.HealthIntervalSeconds > 30 || in.RecoveryHealthyChecks < 2 || in.RecoveryHealthyChecks > 10 {
		return Plan{}, errors.New("health interval must be 1..30 seconds and recovery threshold 2..10")
	}
	p := Plan{Version: 1, Root: in.Root, Platform: in.Platform, RouterID: b.RouterID, RouterCreatedAt: b.RouterCreatedAt, ActiveConfig: in.ActiveConfig, StandbyConfig: standby, DNSBundle: in.DNSBundle, ActiveChecksum: routing.Checksum(in.ActiveConfig), StandbyChecksum: routing.Checksum(standby), DNSChecksum: routing.Checksum(in.DNSBundle), Tools: in.Tools, Host: in.Host, HealthIntervalSeconds: in.HealthIntervalSeconds, RecoveryHealthyChecks: in.RecoveryHealthyChecks}
	p.Checksum = digest(p)
	return p, nil
}
func ValidatePlan(p Plan) error {
	want, e := BuildPlan(PlanInput{Root: p.Root, Platform: p.Platform, ActiveConfig: p.ActiveConfig, DNSBundle: p.DNSBundle, Tools: p.Tools, Host: p.Host, HealthIntervalSeconds: p.HealthIntervalSeconds, RecoveryHealthyChecks: p.RecoveryHealthyChecks})
	if e != nil {
		return e
	}
	a, _ := json.Marshal(p)
	b, _ := json.Marshal(want)
	if string(a) != string(b) {
		return errors.New("AS112 plan differs from exact generated artifact binding")
	}
	return nil
}
func EncodePlan(p Plan) ([]byte, error) {
	if e := ValidatePlan(p); e != nil {
		return nil, e
	}
	b, e := json.MarshalIndent(p, "", "  ")
	return append(b, '\n'), e
}
func DecodePlan(raw []byte) (Plan, error) {
	var p Plan
	if len(raw) > 40<<20 {
		return p, errors.New("AS112 plan too large")
	}
	d := json.NewDecoder(strings.NewReader(string(raw)))
	d.DisallowUnknownFields()
	if d.Decode(&p) != nil || d.Decode(new(any)) != io.EOF {
		return p, errors.New("invalid AS112 plan JSON")
	}
	if e := ValidatePlan(p); e != nil {
		return p, e
	}
	canonical, _ := EncodePlan(p)
	if string(canonical) != string(raw) {
		return p, errors.New("AS112 plan must retain canonical encoding")
	}
	return p, nil
}
