package app

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	devicesnmp "github.com/moedove/ixp-manager/internal/snmp"
	"github.com/moedove/ixp-manager/internal/telemetry"
	"github.com/moedove/ixp-manager/internal/traffic"
)

// DeviceDiscovery references an immutable read-only SNMP proposal, not a live
// device configuration. Applying a proposal only changes inventory mappings.
type DeviceMappingChange struct {
	PortID         string `json:"portId"`
	Created        bool   `json:"created"`
	BeforeIfIndex  uint32 `json:"beforeIfIndex"`
	AfterIfIndex   uint32 `json:"afterIfIndex"`
	BeforeIdentity string `json:"beforeIdentity"`
	AfterIdentity  string `json:"afterIdentity"`
	IntentChecksum string `json:"intentChecksum"`
}
type DeviceDiscovery struct {
	Size               int64                 `json:"size"`
	ReviewBaseChecksum string                `json:"reviewBaseChecksum,omitempty"`
	Changes            []DeviceMappingChange `json:"changes,omitempty"`
	DismissedAt        *time.Time            `json:"dismissedAt,omitempty"`
	DismissedBy        string                `json:"dismissedBy,omitempty"`
	ResolutionReason   string                `json:"resolutionReason,omitempty"`
	PrunedAt           *time.Time            `json:"prunedAt,omitempty"`
	PrunedBy           string                `json:"prunedBy,omitempty"`
	PruneReason        string                `json:"pruneReason,omitempty"`

	ID           string     `json:"id"`
	SwitchID     string     `json:"switchId"`
	ObservedAt   time.Time  `json:"observedAt"`
	Checksum     string     `json:"checksum"`
	Status       string     `json:"status"`
	CreatedAt    time.Time  `json:"createdAt"`
	AppliedAt    *time.Time `json:"appliedAt,omitempty"`
	AppliedBy    string     `json:"appliedBy,omitempty"`
	SnapshotFile string     `json:"snapshotFile"`
}

var discoveryFilePattern = regexp.MustCompile(`^discovery-[a-f0-9]{32}\.json$`)
var observationFilePattern = regexp.MustCompile(`^observation-[a-f0-9]{64}\.json$`)

const maxDeviceDiscoveries = 1024
const deviceDiscoveryQuota int64 = 128 << 20
const deviceObservationQuota int64 = 128 << 20

type devicePortOwner struct {
	MemberID       string    `json:"memberId"`
	IfIndex        uint32    `json:"ifIndex"`
	OwnershipSince time.Time `json:"ownershipSince"`
}
type deviceObservation struct {
	fileChecksum   string
	fileSize       int64
	LastSecurity   string                     `json:"lastSecurity"`
	PortOwners     map[string]devicePortOwner `json:"portOwners,omitempty"`
	Version        int                        `json:"version"`
	SwitchID       string                     `json:"switchId"`
	LastAttemptAt  time.Time                  `json:"lastAttemptAt"`
	ErrorCode      string                     `json:"errorCode,omitempty"`
	BootGeneration string                     `json:"bootGeneration"`
	LastSuccess    *devicesnmp.Snapshot       `json:"lastSuccess,omitempty"`
}

func (a *App) deviceDir() string { return filepath.Join(filepath.Dir(a.store.path), "devices") }
func deviceDigest(data []byte) string {
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}
func observationFile(switchID string) string {
	return "observation-" + deviceDigest([]byte(switchID)) + ".json"
}
func (a *App) readDeviceFile(name string, value any) ([]byte, error) {
	a.store.mu.RLock()
	defer a.store.mu.RUnlock()
	if a.store.closed {
		return nil, fmt.Errorf("device store is closed")
	}
	if !discoveryFilePattern.MatchString(name) && !observationFilePattern.MatchString(name) {
		return nil, fmt.Errorf("invalid device snapshot filename")
	}
	fd, err := syscall.Open(filepath.Join(a.deviceDir(), name), syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), name)
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > devicesnmp.MaxSnapshotBytes+(1<<20) {
		return nil, fmt.Errorf("device snapshot must be a bounded private regular file")
	}
	data, err := io.ReadAll(io.LimitReader(file, devicesnmp.MaxSnapshotBytes+(1<<20)+1))
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(value); err != nil {
		return nil, err
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return nil, fmt.Errorf("device file contains multiple JSON documents")
	}
	return data, nil
}
func (a *App) writeDeviceFile(name string, value any, immutable bool) (string, error) {
	a.store.mu.RLock()
	defer a.store.mu.RUnlock()
	if a.store.closed {
		return "", fmt.Errorf("device store is closed")
	}
	if !discoveryFilePattern.MatchString(name) && !observationFilePattern.MatchString(name) {
		return "", fmt.Errorf("invalid device snapshot filename")
	}
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	data = append(data, '\n')
	if len(data) > devicesnmp.MaxSnapshotBytes+(1<<20) {
		return "", fmt.Errorf("device snapshot exceeds file limit")
	}
	path := filepath.Join(a.deviceDir(), name)
	if info, e := os.Lstat(path); e == nil && !info.Mode().IsRegular() {
		return "", fmt.Errorf("device snapshot target is not a regular file")
	}
	if err := a.checkDeviceQuota(name, int64(len(data))); err != nil {
		return "", err
	}
	file, err := os.CreateTemp(a.deviceDir(), ".device-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if err = file.Chmod(0600); err != nil {
		return "", err
	}
	if _, err = file.Write(data); err != nil {
		return "", err
	}
	if err = file.Sync(); err != nil {
		return "", err
	}
	if err = file.Close(); err != nil {
		return "", err
	}
	if immutable {
		err = os.Link(file.Name(), path)
	} else {
		err = os.Rename(file.Name(), path)
	}
	if err != nil {
		return "", err
	}
	directory, err := os.Open(a.deviceDir())
	if err != nil {
		return "", err
	}
	defer directory.Close()
	if err = directory.Sync(); err != nil {
		return "", err
	}
	return deviceDigest(data), nil
}
func (a *App) initDevices() error {
	if err := os.MkdirAll(a.deviceDir(), 0700); err != nil {
		return err
	}
	info, err := os.Lstat(a.deviceDir())
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return fmt.Errorf("devices directory must be private and not a symlink")
	}
	s := a.store.Snapshot()
	if err = validateDevices(&s); err != nil {
		return err
	}
	referenced := map[string]bool{}
	for _, discovery := range s.DeviceDiscoveries {
		if discovery.PrunedAt != nil {
			continue
		}
		referenced[discovery.SnapshotFile] = true
		if _, err = a.loadDiscovery(discovery); err != nil {
			return err
		}
	}
	files, err := os.ReadDir(a.deviceDir())
	if err != nil {
		return err
	}
	observations := 0
	for _, file := range files {
		name := file.Name()
		if discoveryFilePattern.MatchString(name) && !referenced[name] {
			if err = os.Remove(filepath.Join(a.deviceDir(), name)); err != nil {
				return err
			}
			continue
		}
		if observationFilePattern.MatchString(name) {
			observations++
			if observations > 4096 {
				return fmt.Errorf("device observation file limit exceeded")
			}
			var value deviceObservation
			if _, err = a.readDeviceFile(name, &value); err != nil {
				return err
			}
			if value.Version != 1 || observationFile(value.SwitchID) != name {
				return fmt.Errorf("device observation identity mismatch")
			}
			if value.LastSuccess != nil {
				if err = devicesnmp.ValidateSnapshot(*value.LastSuccess, time.Now()); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
func validateDevices(s *State) error {
	if len(s.DeviceDiscoveries) > maxDeviceDiscoveries {
		return fmt.Errorf("device discovery metadata limit exceeded")
	}
	seen := map[string]bool{}
	for _, discovery := range s.DeviceDiscoveries {
		if !identifier.MatchString(discovery.ID) || seen[discovery.ID] || !identifier.MatchString(discovery.SwitchID) || !discoveryFilePattern.MatchString(discovery.SnapshotFile) || discovery.SnapshotFile != "discovery-"+discovery.ID+".json" || !digestPattern.MatchString(discovery.Checksum) || (discovery.Status != "pending" && discovery.Status != "applied" && discovery.Status != "dismissed") || discovery.CreatedAt.IsZero() || discovery.ObservedAt.IsZero() {
			return fmt.Errorf("invalid device discovery metadata")
		}
		if (discovery.Status == "applied" && (discovery.AppliedAt == nil || discovery.AppliedBy == "")) || (discovery.Status == "pending" && (discovery.AppliedAt != nil || discovery.AppliedBy != "")) {
			return fmt.Errorf("invalid discovery review state")
		}
		if discovery.Size < 0 || discovery.Size > devicesnmp.MaxSnapshotBytes+1 || (discovery.PrunedAt != nil && discovery.Status == "pending") || (discovery.Status == "dismissed" && (discovery.DismissedAt == nil || discovery.DismissedBy == "")) {
			return fmt.Errorf("invalid discovery retention metadata")
		}
		seen[discovery.ID] = true
	}
	indices := map[string]bool{}
	for _, port := range s.Resources["ports"] {
		index := number(port, "snmpIfIndex")
		if index == 0 {
			continue
		}
		if index < 1 || index > 2147483647 || index != float64(uint32(index)) {
			return fmt.Errorf("invalid SNMP interface index")
		}
		key := str(port, "switchId") + "/" + strconv.FormatUint(uint64(index), 10)
		if indices[key] {
			return fmt.Errorf("SNMP ifIndex already bound on switch")
		}
		indices[key] = true
	}
	return nil
}
func guardDeviceResourceMutation(s *State, kind, method string, old, body Record) error {
	if kind != "ports" || method == "DELETE" {
		return nil
	}
	for _, key := range []string{"snmpIfIndex", "snmpIfName", "snmpIfType", "snmpPhysicalAddress", "snmpEngineIdentity"} {
		previous, exists := old[key]
		if incoming, supplied := body[key]; supplied && (!exists || patchHash(incoming) != patchHash(previous)) {
			return fmt.Errorf("SNMP identity/mapping changes require reviewed discovery")
		}
		if exists {
			body[key] = previous
		} else {
			delete(body, key)
		}
	}
	return nil
}
func deviceIdentityMatches(port Record, observed devicesnmp.Interface, snapshot devicesnmp.Snapshot) bool {
	return str(port, "snmpIfName") != "" && str(port, "snmpIfName") == observed.Name && number(port, "snmpIfType") == float64(observed.Type) && canonicalDeviceMAC(str(port, "snmpPhysicalAddress")) == canonicalDeviceMAC(observed.PhysicalAddress) && str(port, "snmpEngineIdentity") == snapshot.EngineIdentity
}
func canonicalDeviceMAC(value string) string {
	if value == "" {
		return ""
	}
	mac, err := net.ParseMAC(value)
	if err != nil {
		return value
	}
	return mac.String()
}
func setDeviceAnchors(port Record, observed devicesnmp.Interface, snapshot devicesnmp.Snapshot) {
	port["snmpIfName"] = observed.Name
	port["snmpIfType"] = float64(observed.Type)
	port["snmpPhysicalAddress"] = canonicalDeviceMAC(observed.PhysicalAddress)
	port["snmpEngineIdentity"] = snapshot.EngineIdentity
}

func (a *App) loadDiscovery(meta DeviceDiscovery) (devicesnmp.Snapshot, error) {
	if meta.PrunedAt != nil {
		return devicesnmp.Snapshot{}, fmt.Errorf("raw discovery snapshot was explicitly pruned")
	}
	var snapshot devicesnmp.Snapshot
	data, err := a.readDeviceFile(meta.SnapshotFile, &snapshot)
	if err != nil {
		return snapshot, err
	}
	if deviceDigest(data) != meta.Checksum || snapshot.SwitchID != meta.SwitchID || !snapshot.ObservedAt.Equal(meta.ObservedAt) || snapshot.ErrorCode != "" {
		return snapshot, fmt.Errorf("device discovery integrity mismatch")
	}
	return snapshot, devicesnmp.ValidateSnapshot(snapshot, time.Now())
}
func discoveryByID(s *State, id string) *DeviceDiscovery {
	for i := range s.DeviceDiscoveries {
		if s.DeviceDiscoveries[i].ID == id {
			return &s.DeviceDiscoveries[i]
		}
	}
	return nil
}
func discoveryView(meta DeviceDiscovery) Record {
	changes := meta.Changes
	if changes == nil {
		changes = []DeviceMappingChange{}
	}
	return Record{"id": meta.ID, "switchId": meta.SwitchID, "observedAt": meta.ObservedAt, "checksum": meta.Checksum, "status": meta.Status, "createdAt": meta.CreatedAt, "appliedAt": meta.AppliedAt, "appliedBy": meta.AppliedBy, "size": meta.Size, "prunedAt": meta.PrunedAt, "dismissedAt": meta.DismissedAt, "resolutionReason": meta.ResolutionReason, "reviewBaseChecksum": meta.ReviewBaseChecksum, "changes": changes}
}

func deviceAllowed(session Session, switchID string) bool {
	return !session.API || contains(session.SwitchIDs, switchID)
}
func deviceMembers(s *State, portID string) []string {
	members := monitoringPortMembers(s, portID)
	if member := str(find(s, "ports", portID), "memberId"); member != "" && !contains(members, member) {
		members = append(members, member)
	}
	return members
}
func deviceOwnershipSince(s *State, port Record) time.Time { return observationOwnershipSince(s, port) }

func deviceBaseChecksum(s *State, switchID string) string {
	ports := []Record{}
	ids := map[string]bool{}
	for _, port := range s.Resources["ports"] {
		if str(port, "switchId") == switchID {
			ports = append(ports, port)
			ids[str(port, "id")] = true
		}
	}
	sort.Slice(ports, func(i, j int) bool { return str(ports[i], "id") < str(ports[j], "id") })
	connections := []Record{}
	patches := []Record{}
	for _, c := range s.Resources["connections"] {
		for _, id := range monitoringConnectionPorts(c) {
			if ids[id] {
				connections = append(connections, c)
				break
			}
		}
	}
	for _, p := range s.Resources["patch-ports"] {
		if ids[str(p, "switchPortId")] {
			patches = append(patches, p)
		}
	}
	sort.Slice(connections, func(i, j int) bool { return str(connections[i], "id") < str(connections[j], "id") })
	sort.Slice(patches, func(i, j int) bool { return str(patches[i], "id") < str(patches[j], "id") })
	return patchHash(Record{"switch": find(s, "switches", switchID), "ports": ports, "connections": connections, "patches": patches})
}

func (a *App) latestDevice(switchID string) (deviceObservation, error) {
	var observation deviceObservation
	raw, err := a.readDeviceFile(observationFile(switchID), &observation)
	if os.IsNotExist(err) {
		return deviceObservation{Version: 1, SwitchID: switchID}, nil
	}
	if err != nil {
		return observation, err
	}
	if observation.Version != 1 || observation.SwitchID != switchID {
		return observation, fmt.Errorf("device observation identity mismatch")
	}
	observation.fileChecksum = deviceDigest(raw)
	observation.fileSize = int64(len(raw))
	return observation, nil
}
func deviceCounterSource(switchID string) string { return "switch-" + deviceDigest([]byte(switchID)) }
func nextBootGeneration(previous deviceObservation, current devicesnmp.Snapshot) string {
	reset := previous.LastSuccess == nil || previous.BootGeneration == ""
	if previous.LastSuccess != nil {
		old := previous.LastSuccess
		before, _ := strconv.ParseUint(old.UptimeTicks, 10, 32)
		after, _ := strconv.ParseUint(current.UptimeTicks, 10, 32)
		if old.EngineEpoch != current.EngineEpoch || after < before {
			reset = true
		}
		elapsed := current.ObservedAt.Sub(old.ObservedAt).Seconds()
		if after >= before && elapsed > 10 && float64(after-before)/100 < elapsed-10 {
			reset = true
		}
	}
	if reset {
		return deviceDigest([]byte(current.SwitchID + "/" + current.ObservedAt.Format(time.RFC3339Nano) + "/" + current.UptimeTicks + "/" + current.EngineEpoch))
	}
	return previous.BootGeneration
}
func (a *App) ingestDevice(snapshot devicesnmp.Snapshot, user Record, session Session, discovery bool, now time.Time) (Record, error) {
	if !session.API || !contains(session.Scopes, "devices:write") || (str(user, "role") != "admin" && str(user, "role") != "operator") || !contains(session.SwitchIDs, snapshot.SwitchID) {
		return nil, fmt.Errorf("switch-bound devices:write collector token required")
	}
	if err := devicesnmp.ValidateSnapshot(snapshot, now); err != nil {
		return nil, err
	}
	if snapshot.ObservedAt.Before(now.Add(-10 * time.Minute)) {
		return nil, fmt.Errorf("device observation must be within10 minutes")
	}
	if discovery && snapshot.ErrorCode != "" {
		return nil, fmt.Errorf("discovery requires successful device data")
	}
	a.deviceMu.Lock()
	defer a.deviceMu.Unlock()
	state := a.store.Snapshot()
	actor, authErr := lookingGlassActor(&state, user, session)
	if authErr != nil || (str(actor, "role") != "admin" && str(actor, "role") != "operator") {
		return nil, fmt.Errorf("collector authority changed")
	}
	if find(&state, "switches", snapshot.SwitchID) == nil {
		return nil, fmt.Errorf("switch not found")
	}
	for _, observed := range snapshot.Interfaces {
		if observed.PortID != "" {
			port := find(&state, "ports", observed.PortID)
			if port == nil || str(port, "switchId") != snapshot.SwitchID {
				return nil, fmt.Errorf("explicit port mapping is outside the token's switch")
			}
			if !discovery && number(port, "snmpIfIndex") != float64(observed.IfIndex) {
				return nil, fmt.Errorf("port/ifIndex mapping must be reviewed before counter ingestion")
			}
			if !discovery && (observed.OwnershipRevision == "" || observed.OwnershipRevision != deviceBindingRevision(&state, port)) {
				return nil, fmt.Errorf("collector ownership revision is stale; fetch bindings before collecting")
			}
			if !discovery && !deviceIdentityMatches(port, observed, snapshot) {
				return nil, fmt.Errorf("observed interface identity changed; collect and review a new discovery before ingestion")
			}

		}
	}
	if discovery {
		id := patchHash(token(24))[:32]
		meta := DeviceDiscovery{ID: id, SwitchID: snapshot.SwitchID, ObservedAt: snapshot.ObservedAt, CreatedAt: now, Status: "pending", SnapshotFile: "discovery-" + id + ".json"}
		checksum, err := a.writeDeviceFile(meta.SnapshotFile, snapshot, true)
		if err != nil {
			return nil, err
		}
		meta.Checksum = checksum
		encoded, _ := json.Marshal(snapshot)
		meta.Size = int64(len(encoded) + 1)
		committed := false
		defer func() {
			if !committed {
				_ = os.Remove(filepath.Join(a.deviceDir(), meta.SnapshotFile))
			}
		}()
		err = a.store.Update(func(s *State) error {
			if find(s, "switches", snapshot.SwitchID) == nil {
				return fmt.Errorf("switch was removed during discovery")
			}
			if len(s.DeviceDiscoveries) >= maxDeviceDiscoveries {
				return fmt.Errorf("discovery proposal limit reached")
			}
			s.DeviceDiscoveries = append(s.DeviceDiscoveries, meta)
			if err := validateDevices(s); err != nil {
				return err
			}
			audit(s, str(user, "id"), "discover", "switches", snapshot.SwitchID)
			return nil
		})
		if err != nil {
			return nil, err
		}
		committed = true
		return discoveryView(meta), nil
	}
	previous, err := a.latestDevice(snapshot.SwitchID)
	if err != nil {
		return nil, err
	}
	if !previous.LastAttemptAt.IsZero() && snapshot.ObservedAt.Before(previous.LastAttemptAt) {
		return nil, fmt.Errorf("out-of-order device observation")
	}
	if snapshot.ObservedAt.Equal(previous.LastAttemptAt) {
		if snapshot.ErrorCode == previous.ErrorCode && (snapshot.ErrorCode != "" || (previous.LastSuccess != nil && patchHash(snapshot) == patchHash(*previous.LastSuccess))) {
			return Record{"ok": true, "duplicate": true}, nil
		}
		return nil, fmt.Errorf("conflicting observation at same timestamp")
	}
	next := previous
	next.Version = 1
	next.SwitchID = snapshot.SwitchID
	next.LastAttemptAt = snapshot.ObservedAt
	next.ErrorCode = snapshot.ErrorCode
	next.LastSecurity = snapshot.Security
	accepted := 0
	skipped := 0
	ignoredSources := 0
	if snapshot.ErrorCode == "" {
		next.BootGeneration = nextBootGeneration(previous, snapshot)
		next.LastSuccess = &snapshot
		next.PortOwners = map[string]devicePortOwner{}
		for _, observed := range snapshot.Interfaces {
			if observed.PortID == "" {
				continue
			}
			port := find(&state, "ports", observed.PortID)
			if port == nil || number(port, "snmpIfIndex") != float64(observed.IfIndex) || observed.OwnershipRevision != deviceBindingRevision(&state, port) {
				continue
			}
			since := deviceOwnershipSince(&state, port)
			if snapshot.ObservedAt.Before(since) {
				continue
			}
			owners := deviceMembers(&state, observed.PortID)
			owner := ""
			if len(owners) == 1 {
				owner = owners[0]
			}
			next.PortOwners[observed.PortID] = devicePortOwner{MemberID: owner, IfIndex: observed.IfIndex, OwnershipSince: since}
		}

		batch := telemetry.Batch{RouterID: deviceCounterSource(snapshot.SwitchID), ObservedAt: snapshot.ObservedAt, Ports: []telemetry.PortSample{}, Sessions: []telemetry.SessionSample{}}
		for _, observed := range snapshot.Interfaces {
			if observed.PortID == "" || observed.CounterBits == 0 {
				continue
			}
			port := find(&state, "ports", observed.PortID)
			if snapshot.ObservedAt.Before(deviceOwnershipSince(&state, port)) {
				skipped++
				continue
			}
			if !trafficSourceAllowed(&state, port, deviceCounterSource(snapshot.SwitchID)) {
				ignoredSources++
				continue
			}
			member := ""
			owners := deviceMembers(&state, observed.PortID)
			if len(owners) == 1 {
				member = owners[0]
			}
			epoch := deviceDigest([]byte(next.BootGeneration + "/" + strconv.FormatUint(uint64(observed.IfIndex), 10) + "/" + observed.DiscontinuityTicks + "/" + deviceOwnershipSince(&state, port).UTC().Format(time.RFC3339Nano)))
			point := telemetry.PortSample{PortID: observed.PortID, OwnershipRevision: observed.OwnershipRevision, MemberID: member, LinkSpeedMbps: observed.SpeedMbps, InOctets: observed.InOctets, OutOctets: observed.OutOctets, CounterBits: observed.CounterBits, CounterEpoch: epoch, OperStatus: observed.OperStatus, Unicast: observed.Unicast, Multicast: observed.Multicast, Broadcast: observed.Broadcast, Errors: observed.Errors, Discards: observed.Discards}
			if point.Errors == nil && (observed.InErrors != "" || observed.OutErrors != "") {
				point.Errors = &traffic.CounterPair{In: observed.InErrors, Out: observed.OutErrors, Bits: 32}
			}
			if point.Discards == nil && (observed.InDiscards != "" || observed.OutDiscards != "") {
				point.Discards = &traffic.CounterPair{In: observed.InDiscards, Out: observed.OutDiscards, Bits: 32}
			}
			if err := a.freezeTrafficSample(&state, port, &point); err != nil {
				return nil, err
			}
			batch.Ports = append(batch.Ports, point)
		}
		if len(batch.Ports) > 0 {
			encoded, encodeErr := json.Marshal(next)
			if encodeErr != nil {
				return nil, fmt.Errorf("device cache could not be encoded")
			}
			if err = a.checkDeviceQuota(observationFile(snapshot.SwitchID), int64(len(encoded)+1)); err != nil {
				return nil, err
			}
			if a.telemetry == nil {
				return nil, fmt.Errorf("telemetry unavailable")
			}
			if err := a.ensureObservationKey(); err != nil {
				return nil, err
			}
			if err = a.telemetry.Ingest(batch, now); err != nil {
				return nil, err
			}
			accepted = len(batch.Ports)
		}
	}
	if _, err = a.writeDeviceFile(observationFile(snapshot.SwitchID), next, false); err != nil {
		return nil, err
	}
	return Record{"ok": true, "acceptedCounters": accepted, "skippedBeforeOwnership": skipped, "ignoredCounterSources": ignoredSources, "acceptedAt": now}, nil
}

func devicePreview(s *State, meta DeviceDiscovery, snapshot devicesnmp.Snapshot) Record {
	rows := []Record{}
	for _, observed := range snapshot.Interfaces {
		row := Record{"ifIndex": observed.IfIndex, "name": observed.Name, "alias": observed.Alias, "description": observed.Description, "speedMbps": observed.SpeedMbps, "mtu": observed.MTU, "operStatus": observed.OperStatus, "adminStatus": observed.AdminStatus, "type": observed.Type, "physicalAddress": observed.PhysicalAddress, "engineIdentity": snapshot.EngineIdentity, "conflicts": []string{}}
		conflicts := []string{}
		var candidate Record
		matched := []Record{}
		for _, port := range s.Resources["ports"] {
			if str(port, "switchId") == meta.SwitchID && str(port, "snmpIfName") != "" && str(port, "snmpIfName") == observed.Name {
				matched = append(matched, port)
			}
		}
		if len(matched) == 1 {
			candidate = matched[0]
		}

		for _, port := range s.Resources["ports"] {
			if str(port, "switchId") != meta.SwitchID {
				continue
			}
			if candidate == nil && number(port, "snmpIfIndex") == float64(observed.IfIndex) {
				candidate = port
				break
			}
		}
		if candidate == nil && observed.PortID != "" {
			candidate = find(s, "ports", observed.PortID)
		}
		if candidate == nil {
			for _, port := range s.Resources["ports"] {
				if str(port, "switchId") == meta.SwitchID && str(port, "name") == observed.Name {
					candidate = port
					break
				}
			}
		}
		if candidate != nil {
			row["existingPortId"] = str(candidate, "id")
			row["existingIfIndex"] = number(candidate, "snmpIfIndex")
			row["existingName"] = str(candidate, "name")
			row["previousIdentity"] = Record{"name": str(candidate, "snmpIfName"), "type": number(candidate, "snmpIfType"), "physicalAddress": str(candidate, "snmpPhysicalAddress"), "engineIdentity": str(candidate, "snmpEngineIdentity")}
			identityChanged := str(candidate, "snmpIfName") != "" && !deviceIdentityMatches(candidate, observed, snapshot)
			row["identityChanged"] = identityChanged
			if identityChanged {
				conflicts = append(conflicts, "approved SNMP interface/device identity differs; explicit replacement required")
			}

			if str(candidate, "name") != observed.Name {
				conflicts = append(conflicts, "observed name differs; existing inventory name will be preserved")
			}
			if number(candidate, "snmpIfIndex") != 0 && number(candidate, "snmpIfIndex") != float64(observed.IfIndex) {
				conflicts = append(conflicts, "existing port is mapped to another ifIndex; explicit replacement required")
			}
			if len(deviceMembers(s, str(candidate, "id"))) > 0 {
				row["assigned"] = true
			}
		}
		row["conflicts"] = conflicts
		rows = append(rows, row)
	}
	return Record{"discovery": discoveryView(meta), "baseChecksum": deviceBaseChecksum(s, meta.SwitchID), "identity": snapshot.Identity, "ports": rows, "entities": snapshot.Entities, "sensors": snapshot.Sensors, "fdb": snapshot.FDB, "warnings": snapshot.Warnings, "inventoryOnly": true}
}
func (a *App) applyDiscovery(meta DeviceDiscovery, snapshot devicesnmp.Snapshot, user Record, base string, selections []devicePortSelection) (Record, error) {
	if snapshot.Security == "authPriv" && snapshot.EngineIdentity == "" {
		return nil, fmt.Errorf("SNMPv3 engine identity missing; update collector and rediscover")
	}
	if snapshot.ObservedAt.Before(time.Now().Add(-24 * time.Hour)) {
		return nil, fmt.Errorf("discovery is older than24 hours; collect a fresh proposal")
	}
	a.deviceMu.Lock()
	defer a.deviceMu.Unlock()
	latest, err := a.latestDevice(meta.SwitchID)
	if err != nil {
		return nil, fmt.Errorf("latest device observation is unavailable for safety checks")
	}
	if latest.LastSuccess != nil && latest.LastSuccess.ObservedAt.After(snapshot.ObservedAt) {
		if snapshot.EngineEpoch != "" && latest.LastSuccess.EngineEpoch != "" && snapshot.EngineEpoch != latest.LastSuccess.EngineEpoch {
			return nil, fmt.Errorf("device engine/boot identity changed after discovery; collect a fresh proposal")
		}
		before, _ := strconv.ParseUint(snapshot.UptimeTicks, 10, 32)
		after, _ := strconv.ParseUint(latest.LastSuccess.UptimeTicks, 10, 32)
		if after < before {
			return nil, fmt.Errorf("device uptime reset after discovery; collect a fresh proposal")
		}
	}

	if len(selections) == 0 || len(selections) > devicesnmp.MaxInterfaces {
		return nil, fmt.Errorf("select1..2048 interfaces to apply")
	}
	var result Record
	err = a.store.Update(func(s *State) error {
		current := discoveryByID(s, meta.ID)
		if current == nil || current.Status != "pending" || current.Checksum != meta.Checksum {
			return fmt.Errorf("discovery is no longer pending")
		}
		if find(s, "switches", meta.SwitchID) == nil || base == "" || base != deviceBaseChecksum(s, meta.SwitchID) {
			return fmt.Errorf("inventory changed; refresh discovery preview")
		}
		rows := map[uint32]devicesnmp.Interface{}
		for _, row := range snapshot.Interfaces {
			rows[row.IfIndex] = row
		}
		seenIndices := map[uint32]bool{}
		seenPorts := map[string]bool{}
		created := []string{}
		updated := []string{}
		changes := []DeviceMappingChange{}
		now := time.Now().UTC()
		for _, selection := range selections {
			observed, exists := rows[selection.IfIndex]
			if !exists || seenIndices[selection.IfIndex] {
				return fmt.Errorf("selection must reference distinct discovered interfaces")
			}
			seenIndices[selection.IfIndex] = true
			if selection.PortID != "" {
				port := find(s, "ports", selection.PortID)
				if port == nil || str(port, "switchId") != meta.SwitchID || seenPorts[selection.PortID] {
					return fmt.Errorf("existing port selection must be distinct and on this switch")
				}
				seenPorts[selection.PortID] = true
				old := number(port, "snmpIfIndex")
				change := DeviceMappingChange{PortID: selection.PortID, BeforeIfIndex: uint32(old), AfterIfIndex: selection.IfIndex, BeforeIdentity: deviceAnchorChecksum(port), IntentChecksum: deviceIntentChecksum(port)}
				identityChanged := str(port, "snmpIfName") != "" && !deviceIdentityMatches(port, observed, snapshot)
				if ((old != 0 && old != float64(selection.IfIndex)) || identityChanged) && !selection.ReplaceMapping {
					return fmt.Errorf("existing mapping replacement requires explicit confirmation")
				}
				if selection.Name != "" && selection.Name != str(port, "name") {
					return fmt.Errorf("discovery cannot rename existing inventory")
				}
				if old != float64(selection.IfIndex) || !deviceIdentityMatches(port, observed, snapshot) {
					port["ownershipSince"] = now.Format(time.RFC3339Nano)
				}
				setDeviceAnchors(port, observed, snapshot)
				port["snmpIfIndex"] = float64(selection.IfIndex)
				port["updatedAt"] = now.Format(time.RFC3339Nano)
				updated = append(updated, selection.PortID)
				change.AfterIdentity = deviceAnchorChecksum(port)
				changes = append(changes, change)
			} else {
				name := selection.Name
				if name == "" {
					name = observed.Name
				}
				if strings.TrimSpace(name) == "" || len(name) > 256 || strings.ContainsAny(name, "\x00\r\n") {
					return fmt.Errorf("invalid new interface name")
				}
				port := Record{"id": token(16), "name": name, "switchId": meta.SwitchID, "snmpIfIndex": float64(selection.IfIndex), "status": "inactive", "createdAt": now.Format(time.RFC3339Nano), "updatedAt": now.Format(time.RFC3339Nano), "ownershipSince": now.Format(time.RFC3339Nano)}
				if observed.SpeedMbps > 0 {
					port["speed"] = float64(observed.SpeedMbps)
				}
				setDeviceAnchors(port, observed, snapshot)
				s.Resources["ports"] = append(s.Resources["ports"], port)
				created = append(created, str(port, "id"))
				changes = append(changes, DeviceMappingChange{PortID: str(port, "id"), Created: true, AfterIfIndex: selection.IfIndex, AfterIdentity: deviceAnchorChecksum(port), IntentChecksum: deviceIntentChecksum(port)})
			}
		}
		if err := validateAll(s); err != nil {
			return err
		}
		if err := validateDevices(s); err != nil {
			return err
		}
		current.Status = "applied"
		current.AppliedAt = &now
		current.AppliedBy = str(user, "id")
		current.ReviewBaseChecksum = base
		current.Changes = changes
		auditDetails(s, str(user, "id"), "apply-discovery", "switches", meta.SwitchID, Record{"discoveryId": meta.ID, "checksum": meta.Checksum, "reviewBaseChecksum": base, "mappingChanges": changes})
		result = Record{"discovery": discoveryView(*current), "created": created, "updated": updated, "inventoryOnly": true}
		return nil
	})
	return result, err
}

type devicePortSelection struct {
	IfIndex        uint32 `json:"ifIndex"`
	PortID         string `json:"portId"`
	Name           string `json:"name"`
	ReplaceMapping bool   `json:"replaceMapping"`
}

func (a *App) devices(w http.ResponseWriter, r *http.Request, user Record, session Session) bool {
	if !strings.HasPrefix(r.URL.Path, "/api/devices") {
		return false
	}
	parts := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/devices"), "/"), "/")
	state := a.store.Snapshot()
	now := time.Now().UTC()
	staff := str(user, "role") != "member"
	if !contains([]string{"admin", "operator", "viewer", "member"}, str(user, "role")) {
		problem(w, 403, "authenticated account required")
		return true
	}
	if len(parts) == 1 && parts[0] == "bindings" && r.Method == "GET" {
		switchID := r.URL.Query().Get("switchId")
		if !session.API || !contains(session.Scopes, "devices:write") || !contains([]string{"admin", "operator"}, str(user, "role")) || !deviceAllowed(session, switchID) {
			problem(w, 403, "switch-bound collector write token required")
			return true
		}
		if find(&state, "switches", switchID) == nil {
			problem(w, 404, "switch not found")
			return true
		}
		mappings := []devicesnmp.PortMapping{}
		for _, port := range state.Resources["ports"] {
			if str(port, "switchId") == switchID && number(port, "snmpIfIndex") > 0 {
				mappings = append(mappings, devicesnmp.PortMapping{PortID: str(port, "id"), IfIndex: uint32(number(port, "snmpIfIndex")), OwnershipRevision: deviceBindingRevision(&state, port)})
			}
		}
		respond(w, 200, Record{"switchId": switchID, "mappings": mappings})
		return true
	}
	if len(parts) == 1 && (parts[0] == "observations" || parts[0] == "discoveries") && r.Method == "POST" {
		if !session.API || !contains(session.Scopes, "devices:write") {
			problem(w, 403, "switch-bound collector token required")
			return true
		}
		var snapshot devicesnmp.Snapshot
		if !decode(w, r, &snapshot) {
			return true
		}
		if !deviceAllowed(session, snapshot.SwitchID) {
			problem(w, 403, "token is not bound to this switch")
			return true
		}
		result, err := a.ingestDevice(snapshot, user, session, parts[0] == "discoveries", now)
		if err != nil {
			problem(w, 422, err.Error())
		} else {
			code := 200
			if parts[0] == "discoveries" {
				code = 201
			}
			respond(w, code, result)
		}
		return true
	}
	if len(parts) == 1 && parts[0] == "status" && r.Method == "GET" {
		respond(w, 200, Record{"items": a.deviceStatus(&state, user, session, now)})
		return true
	}
	if len(parts) == 1 && parts[0] == "ports" && r.Method == "GET" {
		switchID := r.URL.Query().Get("switchId")
		if switchID != "" && !deviceAllowed(session, switchID) {
			problem(w, 403, "token is not bound to this switch")
			return true
		}
		respond(w, 200, Record{"items": a.devicePorts(&state, user, session, switchID, now)})
		return true
	}
	if len(parts) == 1 && parts[0] == "usage" && r.Method == "GET" {
		if !staff || session.API {
			problem(w, 403, "staff browser access required for global storage usage")
			return true
		}
		view, err := a.deviceUsageView(&state, now)
		if err != nil {
			problem(w, 500, "device storage usage unavailable")
		} else {
			respond(w, 200, view)
		}
		return true
	}
	if len(parts) == 2 && parts[0] == "observations" && parts[1] == "clear" && r.Method == "POST" {
		if str(user, "role") != "admin" || session.API {
			problem(w, 403, "administrator browser cleanup required")
			return true
		}
		var input struct {
			SwitchID    string `json:"switchId"`
			Checksum    string `json:"checksum"`
			Reason      string `json:"reason"`
			Acknowledge bool   `json:"acknowledge"`
		}
		if !decode(w, r, &input) {
			return true
		}
		if err := a.clearDeviceObservation(input.SwitchID, input.Checksum, input.Reason, input.Acknowledge, user); err != nil {
			problem(w, 409, err.Error())
		} else {
			respond(w, 200, Record{"ok": true, "telemetryHistoryPreserved": true})
		}
		return true
	}
	if len(parts) == 2 && parts[0] == "discoveries" && r.Method == "DELETE" {
		if str(user, "role") != "admin" || session.API {
			problem(w, 403, "administrator browser cleanup required")
			return true
		}
		var input struct {
			Checksum    string `json:"checksum"`
			Reason      string `json:"reason"`
			Acknowledge bool   `json:"acknowledge"`
		}
		if !decode(w, r, &input) {
			return true
		}
		if err := a.forgetDeviceDiscovery(parts[1], input.Checksum, input.Reason, input.Acknowledge, user); err != nil {
			problem(w, 409, err.Error())
		} else {
			respond(w, 200, Record{"ok": true, "auditSummaryPreserved": true})
		}
		return true
	}
	if !staff {
		problem(w, 403, "device discovery is staff-only")
		return true
	}
	if len(parts) == 1 && parts[0] == "discoveries" && r.Method == "GET" {
		items := []Record{}
		for _, meta := range state.DeviceDiscoveries {
			if deviceAllowed(session, meta.SwitchID) {
				items = append(items, discoveryView(meta))
			}
		}
		respond(w, 200, Record{"items": items})
		return true
	}
	if len(parts) == 3 && parts[0] == "discoveries" {
		meta := discoveryByID(&state, parts[1])
		if meta == nil || !deviceAllowed(session, meta.SwitchID) {
			problem(w, 404, "discovery not found")
			return true
		}
		if (parts[2] == "dismiss" || parts[2] == "prune") && r.Method == "POST" {
			if str(user, "role") != "admin" || session.API {
				problem(w, 403, "administrator browser resolution required")
				return true
			}
			var input struct {
				Reason string `json:"reason"`
			}
			if !decode(w, r, &input) {
				return true
			}
			view, err := a.resolveDeviceDiscovery(meta.ID, parts[2], input.Reason, user)
			if err != nil {
				problem(w, 409, err.Error())
			} else {
				respond(w, 200, view)
			}
			return true
		}
		if meta.PrunedAt != nil {
			problem(w, 410, "raw discovery snapshot was explicitly pruned; review audit remains available")
			return true
		}
		snapshot, err := a.loadDiscovery(*meta)
		if err != nil {
			problem(w, 500, "discovery snapshot failed integrity validation")
			return true
		}
		if parts[2] == "preview" && r.Method == "GET" {
			respond(w, 200, devicePreview(&state, *meta, snapshot))
			return true
		}
		if parts[2] == "apply" && r.Method == "POST" {
			if str(user, "role") != "admin" || session.API {
				problem(w, 403, "administrator browser review is required to apply discovery")
				return true
			}
			var input struct {
				BaseChecksum string                `json:"baseChecksum"`
				Ports        []devicePortSelection `json:"ports"`
			}
			if !decode(w, r, &input) {
				return true
			}
			result, err := a.applyDiscovery(*meta, snapshot, user, input.BaseChecksum, input.Ports)
			if err != nil {
				problem(w, 409, err.Error())
			} else {
				respond(w, 200, result)
			}
			return true
		}
	}
	problem(w, 404, "device endpoint not found")
	return true
}

func (a *App) devicePorts(s *State, user Record, session Session, switchFilter string, now time.Time) []Record {
	out := []Record{}
	member := str(user, "role") == "member"
	cache := map[string]deviceObservation{}
	cacheErrors := map[string]bool{}
	observedTelemetry := telemetry.Snapshot{Ports: map[string]telemetry.PortPoint{}}
	if a.telemetry != nil {
		observedTelemetry = a.telemetry.Snapshot(now)
	}
	for _, port := range s.Resources["ports"] {
		id := str(port, "id")
		switchID := str(port, "switchId")
		if (switchFilter != "" && switchID != switchFilter) || !deviceAllowed(session, switchID) {
			continue
		}
		if member && !contains(deviceMembers(s, id), str(user, "memberId")) {
			continue
		}
		entry := Record{"portId": id, "switchId": switchID, "name": str(port, "name"), "ifIndex": number(port, "snmpIfIndex"), "status": "unknown", "operStatus": "unknown", "adminStatus": "unknown", "inBps": nil, "outBps": nil, "sensors": []Record{}}
		if !member {
			entry["memberIds"] = deviceMembers(s, id)
			entry["configuredSpeedMbps"] = number(port, "speed")
		}
		value, loaded := cache[switchID]
		if !loaded {
			var err error
			value, err = a.latestDevice(switchID)
			cache[switchID] = value
			cacheErrors[switchID] = err != nil
		}
		warnings := []string{}
		if cacheErrors[switchID] {
			entry["status"] = "error"
			entry["errorCode"] = "storage-integrity"
			out = append(out, entry)
			continue
		}
		ownerStamp := value.PortOwners[id]
		if value.LastSuccess != nil && (!member || (ownerStamp.MemberID == str(user, "memberId") && ownerStamp.IfIndex == uint32(number(port, "snmpIfIndex")) && ownerStamp.OwnershipSince.Equal(deviceOwnershipSince(s, port)) && !value.LastSuccess.ObservedAt.Before(deviceOwnershipSince(s, port)))) {
			snapshot := value.LastSuccess
			for _, iface := range snapshot.Interfaces {
				if float64(iface.IfIndex) != number(port, "snmpIfIndex") {
					continue
				}
				lineageValid := iface.PortID == id && iface.OwnershipRevision == deviceBindingRevision(s, port)
				if member && !lineageValid {
					continue
				}
				if !lineageValid {
					warnings = append(warnings, "collector has no current explicit ownership binding; measurements remain staff-only")
				}

				entry["observedAt"] = snapshot.ObservedAt
				entry["status"] = freshness(snapshot.ObservedAt, now)
				entry["operStatus"] = iface.OperStatus
				entry["adminStatus"] = iface.AdminStatus
				entry["speedMbps"] = iface.SpeedMbps
				entry["mtu"] = iface.MTU
				entry["physicalAddress"] = iface.PhysicalAddress
				if iface.AdminStatus == "up" && iface.OperStatus == "down" {
					warnings = append(warnings, "administratively up but link is down")
				}
				if iface.CounterBits == 0 {
					warnings = append(warnings, "octet counters unavailable; no rate fabricated")
				}
				if !member {
					entry["observedName"] = iface.Name
					entry["alias"] = iface.Alias
					entry["description"] = iface.Description
					entry["inOctets"] = iface.InOctets
					entry["outOctets"] = iface.OutOctets
					entry["counterBits"] = iface.CounterBits
					entry["inErrors"] = iface.InErrors
					entry["outErrors"] = iface.OutErrors
					entry["inDiscards"] = iface.InDiscards
					entry["outDiscards"] = iface.OutDiscards
					entry["discontinuityTicks"] = iface.DiscontinuityTicks
					if iface.Name != str(port, "snmpIfName") {
						warnings = append(warnings, "observed interface name differs from reviewed inventory mapping")
					}
					if number(port, "speed") > 0 && float64(iface.SpeedMbps) != number(port, "speed") {
						warnings = append(warnings, "observed speed differs from configured inventory capacity")
					}
					if str(port, "status") == "inactive" && iface.AdminStatus == "up" {
						warnings = append(warnings, "inventory inactive but device administrative state is up")
					}
					warnings = append(warnings, deviceFDBWarnings(s, port, *snapshot)...)
				}
				sensors := []Record{}
				for _, sensor := range snapshot.Sensors {
					if sensor.IfIndex != iface.IfIndex {
						continue
					}
					item := Record{"type": sensor.Type, "value": sensor.Value, "unit": sensor.Unit, "status": sensor.Status}
					if !member {
						item["name"] = sensor.Name
						item["entityIndex"] = sensor.EntityIndex
						item["rawValue"] = sensor.RawValue
						item["scale"] = sensor.Scale
						item["precision"] = sensor.Precision
					}
					sensors = append(sensors, item)
				}
				entry["sensors"] = sensors
				if point, ok := observedTelemetry.Ports[id]; ok && lineageValid && point.OwnershipRevision == deviceBindingRevision(s, port) && !point.ObservedAt.Before(deviceOwnershipSince(s, port)) && point.RouterID == deviceCounterSource(switchID) && (!member || point.MemberID == str(user, "memberId")) && freshness(point.ObservedAt, now) == "fresh" && entry["status"] == "fresh" {
					entry["inBps"] = point.InBps
					entry["outBps"] = point.OutBps
					entry["counterReset"] = point.CounterReset
					entry["counterWrapped"] = point.CounterWrapped
				}
				break
			}
		}
		if value.ErrorCode != "" && (!member || !value.LastAttemptAt.Before(deviceOwnershipSince(s, port))) {
			entry["status"] = "error"
			entry["errorCode"] = value.ErrorCode
			entry["inBps"] = nil
			entry["outBps"] = nil
		}
		entry["warnings"] = warnings
		out = append(out, entry)
	}
	return out
}
func deviceFDBWarnings(s *State, port Record, snapshot devicesnmp.Snapshot) []string {
	vlans := map[uint16]bool{}
	macs := map[string]bool{}
	connections := map[string]bool{}
	for _, connection := range s.Resources["connections"] {
		if contains(monitoringConnectionPorts(connection), str(port, "id")) {
			connections[str(connection, "id")] = true
			vlan := find(s, "vlans", str(connection, "vlanId"))
			vlans[uint16(number(vlan, "tag"))] = true
		}
	}
	for _, record := range s.Resources["mac-addresses"] {
		if connections[str(record, "connectionId")] {
			if parsed, err := net.ParseMAC(str(record, "address")); err == nil {
				macs[parsed.String()] = true
			}
		}
	}
	wrongVLAN, wrongMAC := false, false
	for _, entry := range snapshot.FDB {
		if entry.IfIndex != uint32(number(port, "snmpIfIndex")) || entry.Status != "learned" {
			continue
		}
		if len(vlans) > 0 && len(entry.VLANIDs) > 0 {
			matching := false
			for _, vlan := range entry.VLANIDs {
				if vlans[vlan] {
					matching = true
				}
			}
			if !matching {
				wrongVLAN = true
			}
		}
		parsedMAC, _ := net.ParseMAC(entry.MAC)
		if len(macs) > 0 && !macs[parsedMAC.String()] {
			wrongMAC = true
		}
	}
	warnings := []string{}
	if wrongVLAN {
		warnings = append(warnings, "learned FDB entry is outside this port's registered VLANs")
	}
	if wrongMAC {
		warnings = append(warnings, "learned FDB MAC is absent from this port's registered MAC records")
	}
	return warnings
}
func (a *App) deviceStatus(s *State, user Record, session Session, now time.Time) []Record {
	out := []Record{}
	if str(user, "role") == "member" {
		bySwitch := map[string]Record{}
		for _, port := range a.devicePorts(s, user, session, "", now) {
			id := str(port, "switchId")
			item := bySwitch[id]
			if item == nil {
				item = Record{"switchId": id, "name": str(find(s, "switches", id), "name"), "status": "unknown", "interfaceCount": 0, "warnings": []string{}}
				bySwitch[id] = item
			}
			item["interfaceCount"] = item["interfaceCount"].(int) + 1
			if str(port, "status") == "error" {
				item["status"] = "error"
				item["errorCode"] = port["errorCode"]
			} else if str(item, "status") != "error" && str(port, "status") != "unknown" {
				item["status"] = port["status"]
			}
			if at, ok := port["observedAt"]; ok {
				item["lastObservedAt"] = at
				item["lastSuccessAt"] = at
			}
		}
		keys := []string{}
		for id := range bySwitch {
			keys = append(keys, id)
		}
		sort.Strings(keys)
		for _, id := range keys {
			out = append(out, bySwitch[id])
		}
		return out
	}
	for _, sw := range s.Resources["switches"] {
		id := str(sw, "id")
		if !deviceAllowed(session, id) {
			continue
		}
		item := Record{"switchId": id, "name": str(sw, "name"), "status": "unknown", "interfaceCount": 0, "warnings": []string{}}
		observation, err := a.latestDevice(id)
		if err != nil {
			item["status"] = "error"
			item["errorCode"] = "storage-integrity"
		} else {
			item["security"] = observation.LastSecurity
			if observation.fileChecksum != "" {
				item["observationChecksum"] = observation.fileChecksum
				item["observationSize"] = observation.fileSize
			}
			if !observation.LastAttemptAt.IsZero() {
				item["lastObservedAt"] = observation.LastAttemptAt
			}
			if observation.LastSuccess != nil {
				snapshot := observation.LastSuccess
				item["lastSuccessAt"] = snapshot.ObservedAt
				item["status"] = freshness(snapshot.ObservedAt, now)
				item["interfaceCount"] = len(snapshot.Interfaces)
				item["identity"] = snapshot.Identity
				if observation.LastSecurity == "" {
					item["security"] = snapshot.Security
				}
				item["warnings"] = snapshot.Warnings
				item["uptimeTicks"] = snapshot.UptimeTicks
				item["entityCount"] = len(snapshot.Entities)
				item["sensorCount"] = len(snapshot.Sensors)
				item["fdbCount"] = len(snapshot.FDB)
			}
			if observation.ErrorCode != "" {
				item["status"] = "error"
				item["errorCode"] = observation.ErrorCode
			}
		}
		out = append(out, item)
	}
	return out
}

func deviceBindingRevision(s *State, port Record) string {
	owners := deviceMembers(s, str(port, "id"))
	sort.Strings(owners)
	identity := Record{"portId": str(port, "id"), "switchId": str(port, "switchId"), "ifIndex": number(port, "snmpIfIndex"), "members": owners, "since": deviceOwnershipSince(s, port).UTC().Format(time.RFC3339Nano), "snmpIfName": str(port, "snmpIfName"), "snmpIfType": number(port, "snmpIfType"), "snmpPhysicalAddress": str(port, "snmpPhysicalAddress"), "snmpEngineIdentity": str(port, "snmpEngineIdentity")}
	if binding := corePortBinding(s, str(port, "id")); binding != "" {
		identity["coreBinding"] = binding
	}
	return patchHash(identity)
}

type deviceStorageUsage struct {
	DiscoveryBytes, ObservationBytes int64
	DiscoveryFiles, ObservationFiles int
}

func (a *App) deviceStorageUsage() (deviceStorageUsage, error) {
	var usage deviceStorageUsage
	files, err := os.ReadDir(a.deviceDir())
	if err != nil {
		return usage, err
	}
	for _, file := range files {
		discovery := discoveryFilePattern.MatchString(file.Name())
		observation := observationFilePattern.MatchString(file.Name())
		if !discovery && !observation {
			continue
		}
		info, err := file.Info()
		if err != nil {
			return usage, err
		}
		if !info.Mode().IsRegular() {
			return usage, fmt.Errorf("device artifact is not a regular file")
		}
		if discovery {
			usage.DiscoveryBytes += info.Size()
			usage.DiscoveryFiles++
		} else {
			usage.ObservationBytes += info.Size()
			usage.ObservationFiles++
		}
	}
	return usage, nil
}
func (a *App) checkDeviceQuota(name string, newSize int64) error {
	usage, err := a.deviceStorageUsage()
	if err != nil {
		return err
	}
	oldSize := int64(0)
	if info, e := os.Lstat(filepath.Join(a.deviceDir(), name)); e == nil {
		oldSize = info.Size()
	}
	if discoveryFilePattern.MatchString(name) && usage.DiscoveryBytes-oldSize+newSize > deviceDiscoveryQuota {
		return fmt.Errorf("discovery storage quota exceeded; dismiss/apply and explicitly prune resolved snapshots")
	}
	if observationFilePattern.MatchString(name) {
		if usage.ObservationBytes-oldSize+newSize > deviceObservationQuota || (oldSize == 0 && usage.ObservationFiles >= 4096) {
			return fmt.Errorf("observation storage quota exceeded; explicitly clear stale/deleted device caches")
		}
	}
	return nil
}
func deviceAnchorChecksum(port Record) string {
	return patchHash(Record{"ifName": str(port, "snmpIfName"), "ifType": number(port, "snmpIfType"), "physicalAddress": str(port, "snmpPhysicalAddress"), "engineIdentity": str(port, "snmpEngineIdentity")})
}
func deviceIntentChecksum(port Record) string {
	return patchHash(Record{"name": str(port, "name"), "switchId": str(port, "switchId"), "memberId": str(port, "memberId"), "speed": number(port, "speed"), "status": str(port, "status")})
}
func (a *App) deviceUsageView(s *State, now time.Time) (Record, error) {
	a.deviceMu.Lock()
	defer a.deviceMu.Unlock()
	a.store.mu.RLock()
	if a.store.closed {
		a.store.mu.RUnlock()
		return nil, fmt.Errorf("device store is closed")
	}
	usage, err := a.deviceStorageUsage()
	files, readErr := os.ReadDir(a.deviceDir())
	a.store.mu.RUnlock()
	if err != nil {
		return nil, err
	}
	if readErr != nil {
		return nil, readErr
	}
	items := []Record{}
	for _, file := range files {
		if !observationFilePattern.MatchString(file.Name()) {
			continue
		}
		var observation deviceObservation
		raw, err := a.readDeviceFile(file.Name(), &observation)
		if err != nil {
			return nil, err
		}
		sw := find(s, "switches", observation.SwitchID)
		items = append(items, Record{"switchId": observation.SwitchID, "name": str(sw, "name"), "checksum": deviceDigest(raw), "size": len(raw), "lastObservedAt": observation.LastAttemptAt, "deleted": sw == nil, "stale": now.Sub(observation.LastAttemptAt) > telemetry.StaleAfter})
	}
	return Record{"discovery": Record{"usedBytes": usage.DiscoveryBytes, "quotaBytes": deviceDiscoveryQuota, "fileCount": usage.DiscoveryFiles, "recordCount": len(s.DeviceDiscoveries), "recordLimit": maxDeviceDiscoveries}, "observations": Record{"usedBytes": usage.ObservationBytes, "quotaBytes": deviceObservationQuota, "fileCount": usage.ObservationFiles, "items": items}}, nil
}
func (a *App) resolveDeviceDiscovery(id, action, reason string, user Record) (Record, error) {
	if !patchText(reason, 2000) {
		return nil, fmt.Errorf("a bounded cleanup reason is required")
	}
	a.deviceMu.Lock()
	defer a.deviceMu.Unlock()
	now := time.Now().UTC()
	var result DeviceDiscovery
	err := a.store.Update(func(s *State) error {
		meta := discoveryByID(s, id)
		if meta == nil {
			return fmt.Errorf("discovery not found")
		}
		switch action {
		case "dismiss":
			if meta.Status != "pending" {
				return fmt.Errorf("only pending discovery can be dismissed")
			}
			meta.Status = "dismissed"
			meta.DismissedAt = &now
			meta.DismissedBy = str(user, "id")
			meta.ResolutionReason = reason
		case "prune":
			if meta.Status == "pending" {
				return fmt.Errorf("pending discovery must be explicitly resolved before pruning")
			}
			if meta.PrunedAt == nil {
				meta.PrunedAt = &now
				meta.PrunedBy = str(user, "id")
				meta.PruneReason = reason
			}
		default:
			return fmt.Errorf("unsupported discovery resolution")
		}
		result = *meta
		auditDetails(s, str(user, "id"), action+"-device-discovery", "device-discoveries", id, Record{"switchId": meta.SwitchID, "checksum": meta.Checksum, "reason": reason, "reviewBaseChecksum": meta.ReviewBaseChecksum, "mappingChanges": meta.Changes})
		return validateDevices(s)
	})
	if err != nil {
		return nil, err
	}
	if action == "prune" {
		a.store.mu.RLock()
		defer a.store.mu.RUnlock()
		if a.store.closed {
			return nil, fmt.Errorf("device store closed before raw snapshot cleanup")
		}
		if err = os.Remove(filepath.Join(a.deviceDir(), result.SnapshotFile)); err != nil && !os.IsNotExist(err) {
			return nil, fmt.Errorf("metadata marked pruned; file cleanup failed, retry after storage repair")
		}
		if err = syncDeviceDir(a.deviceDir()); err != nil {
			return nil, err
		}
	}
	return discoveryView(result), nil
}
func syncDeviceDir(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
func (a *App) forgetDeviceDiscovery(id, checksum, reason string, acknowledge bool, user Record) error {
	a.deviceMu.Lock()
	defer a.deviceMu.Unlock()
	if !acknowledge || !patchText(reason, 2000) {
		return fmt.Errorf("explicit acknowledgement and reason required; back up detailed history before cleanup")
	}
	return a.store.Update(func(s *State) error {
		for i, meta := range s.DeviceDiscoveries {
			if meta.ID != id {
				continue
			}
			if meta.PrunedAt == nil || meta.Status == "pending" || checksum == "" || checksum != meta.Checksum {
				return fmt.Errorf("only matching already-pruned terminal metadata can be forgotten")
			}
			if _, err := os.Lstat(filepath.Join(a.deviceDir(), meta.SnapshotFile)); !os.IsNotExist(err) {
				return fmt.Errorf("raw snapshot cleanup is not complete; retry prune before forgetting metadata")
			}
			auditDetails(s, str(user, "id"), "forget-device-discovery", "device-discoveries", id, Record{"switchId": meta.SwitchID, "checksum": meta.Checksum, "reason": reason, "reviewBaseChecksum": meta.ReviewBaseChecksum, "mappingChanges": meta.Changes})
			s.DeviceDiscoveries = append(s.DeviceDiscoveries[:i], s.DeviceDiscoveries[i+1:]...)
			return nil
		}
		return fmt.Errorf("discovery not found")
	})
}
func (a *App) clearDeviceObservation(switchID, checksum, reason string, acknowledge bool, user Record) error {
	if !identifier.MatchString(switchID) || !acknowledge || !patchText(reason, 2000) || !digestPattern.MatchString(checksum) {
		return fmt.Errorf("switchId, current checksum, reason and explicit acknowledgement required")
	}
	a.deviceMu.Lock()
	defer a.deviceMu.Unlock()
	current, err := a.latestDevice(switchID)
	if err != nil {
		return err
	}
	if current.fileChecksum == "" || current.fileChecksum != checksum {
		return fmt.Errorf("device cache changed; refresh before clearing")
	}
	// Persist the explicit cleanup intent before unlinking the reconstructible
	// cache. This event survives a crash even if filesystem cleanup needs retry.
	if err = a.store.Update(func(s *State) error {
		auditDetails(s, str(user, "id"), "clear-device-observation", "switches", switchID, Record{"checksum": checksum, "reason": reason, "telemetryHistoryPreserved": true})
		return nil
	}); err != nil {
		return err
	}
	a.store.mu.RLock()
	defer a.store.mu.RUnlock()
	if a.store.closed {
		return fmt.Errorf("device store closed before cleanup")
	}
	if err = os.Remove(filepath.Join(a.deviceDir(), observationFile(switchID))); err != nil && !os.IsNotExist(err) {
		return err
	}
	return syncDeviceDir(a.deviceDir())
}
