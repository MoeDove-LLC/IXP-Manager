package main

import (
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"github.com/moedove/ixp-manager/internal/routeros"
	"github.com/moedove/ixp-manager/internal/routeros/control"
	"github.com/moedove/ixp-manager/internal/routing"
)

type peerIntent struct {
	ID              string                   `json:"id"`
	ASN             uint32                   `json:"asn"`
	Address         string                   `json:"address"`
	MaxPrefixes     int                      `json:"maxPrefixes"`
	AllowedPrefixes []string                 `json:"allowedPrefixes,omitempty"`
	AllowedRoutes   []routing.PrefixOrigin   `json:"allowedRoutes,omitempty"`
	Authentication  *routeros.Authentication `json:"authentication,omitempty"`
}
type planIntent struct {
	ASN                         uint32                    `json:"asn"`
	RouterID                    string                    `json:"routerId"`
	LocalIPv4                   string                    `json:"localIpv4,omitempty"`
	LocalIPv6                   string                    `json:"localIpv6,omitempty"`
	Peers                       []peerIntent              `json:"peers"`
	Options                     routeros.PlanOptions      `json:"options"`
	Credentials                 map[string]credentialWire `json:"credentials,omitempty"`
	BaselineCredentials         map[string]credentialWire `json:"baselineCredentials,omitempty"`
	TimeoutSeconds              int                       `json:"timeoutSeconds"`
	ExclusiveAccessAcknowledged bool                      `json:"exclusiveAccessAcknowledged"`
}

// writePrivateNew publishes complete bytes atomically and never replaces an
// existing file. The destination directory must be owned and non-writable by
// another user; the generated stage may contain explicitly supplied secrets.
func writePrivateNew(path string, data []byte) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || len(data) > maxRequestBytes {
		return errors.New("output requires a bounded absolute clean path")
	}
	parent := filepath.Dir(path)
	resolved, e := filepath.EvalSymlinks(parent)
	if e != nil || resolved != parent {
		return errors.New("output directory must exist without symlinks")
	}
	info, e := os.Stat(parent)
	if e != nil || !info.IsDir() || info.Mode().Perm()&0022 != 0 {
		return errors.New("output directory must not be writable by other users")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) {
		return errors.New("output directory owner mismatch")
	}
	f, e := os.CreateTemp(parent, ".routeros-plan-")
	if e != nil {
		return errors.New("private temporary output unavailable")
	}
	name := f.Name()
	defer os.Remove(name)
	if _, e = f.Write(data); e != nil {
		f.Close()
		return errors.New("private output write failed")
	}
	if e = f.Sync(); e != nil {
		f.Close()
		return errors.New("private output sync failed")
	}
	if f.Close() != nil {
		return errors.New("private output close failed")
	}
	if os.Link(name, path) != nil {
		return errors.New("private output already exists or cannot be published")
	}
	if os.Remove(name) != nil {
		return errors.New("output published but temporary link remains; inspect permissions before use")
	}
	dir, e := os.Open(parent)
	if e != nil {
		return errors.New("output published but directory sync unavailable")
	}
	defer dir.Close()
	if dir.Sync() != nil {
		return errors.New("output published but directory sync failed")
	}
	return nil
}

func intendedAuthentication(old routeros.Authentication, explicit *routeros.Authentication, supplied credentialWire, present bool) (routeros.Authentication, error) {
	auth := old
	if explicit != nil {
		auth = *explicit
	}
	if present {
		if supplied.Password == "" || explicit != nil && (!auth.Enabled || auth.Revision != supplied.Revision) {
			return routeros.Authentication{}, errors.New("new credential conflicts with explicit authentication intent")
		}
		if explicit == nil {
			auth = routeros.Authentication{Enabled: true, Revision: supplied.Revision}
		}
	}
	return auth, nil
}

func buildStageIntent(review control.Review, input planIntent) (stageWire, error) {
	if review.KnownState.Target != review.Snapshot.Target || review.ObservedAt.IsZero() || input.TimeoutSeconds < 10 || input.TimeoutSeconds > 300 {
		return stageWire{}, errors.New("review or confirmation timeout invalid")
	}
	old := map[string]routeros.Authentication{}
	for _, connection := range review.Snapshot.Connections {
		old[connection.Config.PeerID] = connection.Config.Authentication
	}
	desired := routeros.Desired{ASN: input.ASN, RouterID: input.RouterID, LocalIPv4: input.LocalIPv4, LocalIPv6: input.LocalIPv6}
	peerIDs := map[string]bool{}
	for _, peer := range input.Peers {
		peerIDs[peer.ID] = true
		supplied, present := input.Credentials[peer.ID]
		auth, err := intendedAuthentication(old[peer.ID], peer.Authentication, supplied, present)
		if err != nil {
			return stageWire{}, err
		}
		desired.Peers = append(desired.Peers, routeros.Peer{ID: peer.ID, ASN: peer.ASN, Address: peer.Address, MaxPrefixes: peer.MaxPrefixes, AllowedPrefixes: peer.AllowedPrefixes, AllowedRoutes: peer.AllowedRoutes, Authentication: auth})
	}
	for id := range input.Credentials {
		if !peerIDs[id] {
			return stageWire{}, errors.New("new credential has no desired peer")
		}
	}
	plan, err := routeros.BuildReferencedPlan(review.Snapshot.Target, desired, review.Snapshot, input.Options)
	if err != nil {
		return stageWire{}, err
	}
	if _, err = routeros.NewAPIBridge(plan, credentials(input.Credentials)); err != nil {
		return stageWire{}, errors.New("required new credential values or revisions are missing")
	}
	return stageWire{Plan: plan, KnownState: review.KnownState, Credentials: input.Credentials, BaselineCredentials: input.BaselineCredentials, TimeoutSeconds: input.TimeoutSeconds, ExclusiveAccessAcknowledged: input.ExclusiveAccessAcknowledged}, nil
}

func planCommand(args []string, out io.Writer) error {
	flags := flag.NewFlagSet("plan", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	reviewPath := flags.String("review", "", "private JSON response produced by review")
	intentPath := flags.String("file", "", "private desired configuration intent")
	handoffPath := flags.String("handoff", "", "credential-free browser planning handoff for the reviewed inventory router")
	credentialPath := flags.String("credentials", "", "optional private 0600 credentials/baselineCredentials bundle for handoff planning")
	timeout := flags.Int("timeout", 60, "handoff confirmation window in seconds (10..300)")
	exclusive := flags.Bool("exclusive-access", false, "explicitly acknowledge the actual exclusive maintenance window for the handoff")
	identity := flags.Bool("allow-identity-change", false, "explicitly authorize the handoff's local ASN/router-ID change")
	outputPath := flags.String("output", "", "new private stage file")
	if flags.Parse(args) != nil || flags.NArg() != 0 || *reviewPath == "" || *outputPath == "" || (*intentPath == "") == (*handoffPath == "") {
		return errors.New("plan requires --review, exactly one of --file/--handoff, and --output")
	}
	supplied := map[string]bool{}
	flags.Visit(func(f *flag.Flag) { supplied[f.Name] = true })
	if supplied["file"] && supplied["handoff"] {
		return errors.New("--file and --handoff are mutually exclusive")
	}
	if *handoffPath == "" && (supplied["credentials"] || supplied["timeout"] || supplied["exclusive-access"] || supplied["allow-identity-change"] || supplied["handoff"]) {
		return errors.New("--credentials, --timeout, --exclusive-access and --allow-identity-change require --handoff")
	}
	raw, err := privateRead(*reviewPath)
	if err != nil {
		return err
	}
	defer clear(raw)
	var reviewed response
	if strictDecode(raw, &reviewed) != nil || !reviewed.OK || reviewed.Review == nil {
		return errors.New("private review response invalid")
	}
	var input planIntent
	if *handoffPath != "" {
		handoffData, err := planningHandoffRead(*handoffPath)
		if err != nil {
			return err
		}
		defer clear(handoffData)
		var handoff routeros.PlanningHandoff
		if strictDecode(handoffData, &handoff) != nil || !planningHandoffShape(handoffData) {
			return errors.New("bounded credential-free handoff JSON invalid")
		}
		bundle, err := readHandoffCredentials(*credentialPath)
		if err != nil {
			return err
		}
		input, err = handoffPlanIntent(*reviewed.Review, handoff, bundle, *timeout, *exclusive, *identity)
		if err != nil {
			return err
		}
	} else {
		intentData, err := privateRead(*intentPath)
		if err != nil {
			return err
		}
		defer clear(intentData)
		if strictDecode(intentData, &input) != nil {
			return errors.New("private desired intent invalid")
		}
	}
	stage, err := buildStageIntent(*reviewed.Review, input)
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(stage)
	if err != nil {
		return errors.New("private stage encoding failed")
	}
	encoded = append(encoded, '\n')
	defer clear(encoded)
	if err = writePrivateNew(*outputPath, encoded); err != nil {
		return err
	}
	counts := map[routeros.Phase]int{}
	for _, operation := range stage.Plan.Operations {
		counts[operation.Phase]++
	}
	return json.NewEncoder(out).Encode(struct {
		PlanChecksum    string                 `json:"planChecksum"`
		Operations      map[routeros.Phase]int `json:"operations"`
		EstimatedUndo   int                    `json:"estimatedUndoActions"`
		NativeValidated bool                   `json:"nativeValidated"`
	}{stage.Plan.Checksum, counts, stage.Plan.Budget.EstimatedActions, false})
}
