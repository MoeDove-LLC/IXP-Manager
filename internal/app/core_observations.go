package app

import (
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/moedove/ixp-manager/internal/core"
	"github.com/moedove/ixp-manager/internal/telemetry"
)

type coreLayer struct {
	Status           string     `json:"status"`
	Source           string     `json:"source"`
	ObservedAt       *time.Time `json:"observedAt"`
	IdentityVerified bool       `json:"identityVerified"`
	Message          string     `json:"message"`
}
type corePortObservation struct {
	PortID           string     `json:"portId"`
	SwitchID         string     `json:"switchId"`
	Status           string     `json:"status"`
	Source           string     `json:"source"`
	ObservedAt       *time.Time `json:"observedAt"`
	IdentityVerified bool       `json:"identityVerified"`
	OperStatus       string     `json:"operStatus"`
	AdminStatus      string     `json:"adminStatus"`
	SpeedMbps        *uint64    `json:"speedMbps"`
	InBps            *float64   `json:"inBps"`
	OutBps           *float64   `json:"outBps"`
	CounterReset     bool       `json:"counterReset"`
	CounterWrapped   bool       `json:"counterWrapped"`
	ErrorCode        string     `json:"errorCode,omitempty"`
}
type coreLinkObservation struct {
	ID       string              `json:"id"`
	Enabled  bool                `json:"enabled"`
	Physical coreLayer           `json:"physical"`
	SideA    corePortObservation `json:"sideA"`
	SideB    corePortObservation `json:"sideB"`
}
type coreCapacity struct {
	TotalMbps   *uint64 `json:"totalMbps"`
	EnabledMbps *uint64 `json:"enabledMbps"`
	KnownPorts  int     `json:"knownPorts"`
	TotalPorts  int     `json:"totalPorts"`
	Source      string  `json:"source"`
}
type coreTraffic struct {
	InBps         *float64   `json:"inBps"`
	OutBps        *float64   `json:"outBps"`
	CoveredPorts  int        `json:"coveredPorts"`
	ExpectedPorts int        `json:"expectedPorts"`
	SourceFrom    *time.Time `json:"sourceFrom"`
	SourceTo      *time.Time `json:"sourceTo"`
}

func earlierCoreTime(a, b *time.Time) *time.Time {
	if b == nil {
		return a
	}
	if a == nil || b.Before(*a) {
		t := *b
		return &t
	}
	return a
}
func coreCombinedPhysical(layers []coreLayer) coreLayer {
	out := coreLayer{Status: "unknown", Source: "snmp-if-mib", Message: "端口观测不证明 LACP、BFD、BGP 或端到端转发。"}
	if len(layers) == 0 {
		out.Message = "没有启用链路，不能判定互联正常。"
		return out
	}
	counts := map[string]int{}
	out.IdentityVerified = true
	for _, layer := range layers {
		counts[layer.Status]++
		out.ObservedAt = earlierCoreTime(out.ObservedAt, layer.ObservedAt)
		out.IdentityVerified = out.IdentityVerified && layer.IdentityVerified
	}
	switch {
	case counts["up"] == len(layers):
		out.Status = "up"
	case counts["down"] == len(layers):
		out.Status = "down"
	case counts["down"] > 0 || counts["degraded"] > 0:
		out.Status = "degraded"
	case counts["error"] > 0:
		out.Status = "error"
	case counts["stale"] > 0:
		out.Status = "stale"
	}
	return out
}
func corePortLayer(p corePortObservation) coreLayer {
	out := coreLayer{Status: p.Status, Source: p.Source, ObservedAt: p.ObservedAt, IdentityVerified: p.IdentityVerified}
	if p.Status == "fresh" {
		out.Status = "unknown"
		if p.OperStatus == "up" || p.OperStatus == "down" {
			out.Status = p.OperStatus
		}
	}
	return out
}
func (a *App) coreObservedPort(s *State, endpoint core.Endpoint, device deviceObservation, readErr error, points map[string]telemetry.PortPoint, now time.Time) corePortObservation {
	p := find(s, "ports", endpoint.PortID)
	out := corePortObservation{PortID: endpoint.PortID, SwitchID: str(p, "switchId"), Status: "unknown", Source: "snmp-if-mib", OperStatus: "unknown", AdminStatus: "unknown"}
	if readErr != nil {
		out.Status = "error"
		out.ErrorCode = "storage-integrity"
		return out
	}
	if device.ErrorCode != "" {
		out.Status = "error"
		out.ErrorCode = "collection-failed"
		return out
	}
	if p == nil || device.LastSuccess == nil || number(p, "snmpIfIndex") == 0 {
		return out
	}
	snapshot := device.LastSuccess
	owner, hasOwner := device.PortOwners[endpoint.PortID]
	if !hasOwner || owner.IfIndex != uint32(number(p, "snmpIfIndex")) || !owner.OwnershipSince.Equal(deviceOwnershipSince(s, p)) || snapshot.ObservedAt.Before(deviceOwnershipSince(s, p)) {
		out.ErrorCode = "binding-changed"
		return out
	}
	for _, iface := range snapshot.Interfaces {
		if iface.PortID != endpoint.PortID || float64(iface.IfIndex) != number(p, "snmpIfIndex") {
			continue
		}
		if iface.OwnershipRevision != deviceBindingRevision(s, p) || !deviceIdentityMatches(p, iface, *snapshot) {
			out.ErrorCode = "identity-changed"
			return out
		}
		out.IdentityVerified = true
		at := snapshot.ObservedAt
		out.ObservedAt = &at
		out.Status = freshness(at, now)
		if at.After(now.Add(30 * time.Second)) {
			out.Status = "unknown"
			out.ErrorCode = "invalid-clock"
			return out
		}
		out.OperStatus, out.AdminStatus = iface.OperStatus, iface.AdminStatus
		if iface.SpeedMbps > 0 {
			speed := iface.SpeedMbps
			out.SpeedMbps = &speed
		}
		if point, ok := points[endpoint.PortID]; ok && point.RouterID == deviceCounterSource(out.SwitchID) && currentPortObservation(s, p, point) && freshness(point.ObservedAt, now) == "fresh" && point.ObservedAt.Equal(at) && out.Status == "fresh" {
			out.InBps, out.OutBps = point.InBps, point.OutBps
			out.CounterReset, out.CounterWrapped = point.CounterReset, point.CounterWrapped
		}
		return out
	}
	return out
}
func coreSideCapacity(s *State, b core.Bundle, side string) coreCapacity {
	out := coreCapacity{TotalPorts: len(b.Links), Source: "physical-port-inventory"}
	var total, enabled uint64
	knownEnabled := true
	for _, l := range b.Links {
		e := l.SideA
		if side == "b" {
			e = l.SideB
		}
		var speed uint64
		if value := number(find(s, "ports", e.PortID), "speed"); value > 0 {
			speed = uint64(value)
		}
		if speed > 0 {
			out.KnownPorts++
			total += speed
			if b.Enabled && l.Enabled {
				enabled += speed
			}
		} else if b.Enabled && l.Enabled {
			knownEnabled = false
		}
	}
	if out.KnownPorts == out.TotalPorts {
		out.TotalMbps = &total
	}
	if knownEnabled {
		out.EnabledMbps = &enabled
	}
	return out
}
func coreTrafficFromPorts(ports []corePortObservation) coreTraffic {
	out := coreTraffic{ExpectedPorts: len(ports)}
	var in, outRate float64
	inCount, outCount := 0, 0
	for _, p := range ports {
		if p.Status != "fresh" || !p.IdentityVerified {
			continue
		}
		out.SourceFrom = earlierCoreTime(out.SourceFrom, p.ObservedAt)
		if p.ObservedAt != nil && (out.SourceTo == nil || p.ObservedAt.After(*out.SourceTo)) {
			at := *p.ObservedAt
			out.SourceTo = &at
		}
		if p.InBps != nil {
			in += *p.InBps
			inCount++
		}
		if p.OutBps != nil {
			outRate += *p.OutBps
			outCount++
		}
		if p.InBps != nil && p.OutBps != nil {
			out.CoveredPorts++
		}
	}
	if len(ports) > 0 && inCount == len(ports) {
		out.InBps = &in
	}
	if len(ports) > 0 && outCount == len(ports) {
		out.OutBps = &outRate
	}
	return out
}
func coreProtocolLayer(b core.Bundle, kind string) coreLayer {
	requested := kind == "lacp" && b.Type != core.ECMP || kind == "bgp" && b.Type != core.L2LAG
	if kind == "bfd" {
		requested = b.Type == core.L3LAG && b.BFD
		if b.Type == core.ECMP {
			for _, l := range b.Links {
				requested = requested || l.Enabled && l.BFD
			}
		}
	}
	status, message := "not-applicable", "此类型或计划没有请求该协议。"
	if requested {
		status, message = "unsupported", "尚无独立协议观测；端口 Up 不能证明协议正常。"
	}
	return coreLayer{Status: status, Source: "none", Message: message}
}

func (a *App) coreObservations(s *State, b core.Bundle, now time.Time) Record {
	type cachedDevice struct {
		value deviceObservation
		err   error
	}
	cache := map[string]cachedDevice{}
	for _, id := range []string{b.SideA.SwitchID, b.SideB.SwitchID} {
		v, e := a.latestDevice(id)
		cache[id] = cachedDevice{v, e}
	}
	points := map[string]telemetry.PortPoint{}
	if a.telemetry != nil {
		points = a.telemetry.Snapshot(now).Ports
	}
	links := []coreLinkObservation{}
	layers := []coreLayer{}
	sideA, sideB := []corePortObservation{}, []corePortObservation{}
	for _, l := range b.Links {
		aDevice, bDevice := cache[b.SideA.SwitchID], cache[b.SideB.SwitchID]
		left := a.coreObservedPort(s, l.SideA, aDevice.value, aDevice.err, points, now)
		right := a.coreObservedPort(s, l.SideB, bDevice.value, bDevice.err, points, now)
		physical := coreCombinedPhysical([]coreLayer{corePortLayer(left), corePortLayer(right)})
		links = append(links, coreLinkObservation{ID: l.ID, Enabled: l.Enabled, Physical: physical, SideA: left, SideB: right})
		if l.Enabled {
			layers = append(layers, physical)
			sideA = append(sideA, left)
			sideB = append(sideB, right)
		}
	}
	return Record{"bundleId": b.ID, "bindingRevision": b.BindingRevision, "desired": Record{"enabled": b.Enabled, "type": b.Type, "enabledLinks": len(layers), "totalLinks": len(b.Links)}, "physical": coreCombinedPhysical(layers), "lacp": coreProtocolLayer(b, "lacp"), "bgp": coreProtocolLayer(b, "bgp"), "bfd": coreProtocolLayer(b, "bfd"), "links": links, "capacity": Record{"sideA": coreSideCapacity(s, b, "a"), "sideB": coreSideCapacity(s, b, "b")}, "traffic": Record{"sideA": coreTrafficFromPorts(sideA), "sideB": coreTrafficFromPorts(sideB)}, "staleAfterSeconds": int(telemetry.StaleAfter.Seconds())}
}

func (a *App) coreObservationRoutes(w http.ResponseWriter, r *http.Request, user Record, session Session) bool {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/core/bundles/"), "/")
	if !strings.HasPrefix(r.URL.Path, "/api/core/bundles/") || len(parts) != 2 || (parts[1] != "observations" && parts[1] != "series") {
		return false
	}
	if r.Method != "GET" {
		problem(w, 405, "method not allowed")
		return true
	}
	s := a.store.Snapshot()
	_, scope, err := a.coreActor(&s, user, session, false)
	if err != nil {
		problem(w, 403, "core observation access denied")
		return true
	}
	b := coreBundleByID(&s, parts[0])
	if b == nil || !coreVisible(*b, scope) {
		problem(w, 404, "core bundle not found")
		return true
	}
	revision := a.coreRevision(&s, *b)
	var result Record
	if parts[1] == "observations" {
		result = a.coreObservations(&s, *b, time.Now().UTC())
	} else {
		var ok bool
		result, ok = a.coreSeries(w, r, &s, *b)
		if !ok {
			return true
		}
	}
	// Reading device files and time series can overlap an allocation change.
	// Never return those observations under a newly assigned topology.
	a.store.mu.RLock()
	_, scope, err = a.coreActor(&a.store.state, user, session, false)
	current := coreBundleByID(&a.store.state, b.ID)
	valid := !a.store.closed && err == nil && current != nil && coreVisible(*current, scope) && a.coreRevision(&a.store.state, *current) == revision
	a.store.mu.RUnlock()
	if !valid {
		problem(w, 409, "core topology or access changed; reload observations")
		return true
	}
	respond(w, 200, result)
	return true
}

func (a *App) coreSeries(w http.ResponseWriter, r *http.Request, s *State, b core.Bundle) (Record, bool) {
	side := r.URL.Query().Get("side")
	if side != "a" && side != "b" {
		problem(w, 400, "series side must be a or b")
		return nil, false
	}
	if a.telemetry == nil {
		problem(w, 503, "telemetry unavailable")
		return nil, false
	}
	now := time.Now().UTC()
	from, to := now.Add(-time.Hour), now
	for _, entry := range []struct {
		name string
		dest *time.Time
	}{{"from", &from}, {"to", &to}} {
		if raw := r.URL.Query().Get(entry.name); raw != "" {
			at, err := time.Parse(time.RFC3339, raw)
			if err != nil {
				problem(w, 400, "series times must be RFC3339")
				return nil, false
			}
			*entry.dest = at
		}
	}
	if from.After(to) || from.Before(now.Add(-telemetry.Retention-time.Minute)) || to.After(now.Add(30*time.Second)) {
		problem(w, 400, "series range must be ordered and within retention")
		return nil, false
	}
	if from.Before(now.Add(-telemetry.Retention)) {
		from = now.Add(-telemetry.Retention)
	}
	ids := []string{}
	buckets := map[int64]map[string]telemetry.PortPoint{}
	for _, link := range b.Links {
		if !link.Enabled {
			continue
		}
		e := link.SideA
		if side == "b" {
			e = link.SideB
		}
		ids = append(ids, e.PortID)
		p := find(s, "ports", e.PortID)
		for _, point := range a.telemetry.Series(e.PortID, from, to) {
			if point.RouterID != deviceCounterSource(str(p, "switchId")) || !currentPortObservation(s, p, point) {
				continue
			}
			bucket := point.ObservedAt.Unix() / 60 * 60
			if buckets[bucket] == nil {
				buckets[bucket] = map[string]telemetry.PortPoint{}
			}
			old, exists := buckets[bucket][e.PortID]
			if !exists || point.ObservedAt.After(old.ObservedAt) {
				buckets[bucket][e.PortID] = point
			}
		}
	}
	keys := []int64{}
	for key := range buckets {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	points := []Record{}
	for _, key := range keys {
		ports := []corePortObservation{}
		reset, wrapped := false, false
		for _, id := range ids {
			port := corePortObservation{PortID: id, Status: "unknown"}
			if p, ok := buckets[key][id]; ok {
				at := p.ObservedAt
				port.Status = "fresh"
				port.IdentityVerified = true
				port.ObservedAt = &at
				port.InBps = p.InBps
				port.OutBps = p.OutBps
				reset = reset || p.CounterReset
				wrapped = wrapped || p.CounterWrapped
			}
			ports = append(ports, port)
		}
		t := coreTrafficFromPorts(ports)
		points = append(points, Record{"observedAt": time.Unix(key, 0).UTC(), "inBps": t.InBps, "outBps": t.OutBps, "coveredPorts": t.CoveredPorts, "expectedPorts": t.ExpectedPorts, "sourceFrom": t.SourceFrom, "sourceTo": t.SourceTo, "counterReset": reset, "counterWrapped": wrapped})
	}
	return Record{"bundleId": b.ID, "bindingRevision": b.BindingRevision, "side": side, "portIds": ids, "from": from, "to": to, "bucketSeconds": 60, "alignment": "latest-per-port-in-bucket", "points": points, "notice": "每分钟桶内采用每个端口最后一次实际速率；缺失端口不补零。A/B 两侧独立，不能相加为总流量。"}, true
}
