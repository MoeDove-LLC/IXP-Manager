package atlas

import (
	"encoding/json"
	"math"
	"net/netip"
	"strconv"
	"strings"
	"time"
)

func apiTime(raw json.RawMessage) (time.Time, bool) {
	if len(raw) == 0 || string(raw) == "null" {
		return time.Time{}, false
	}
	var n int64
	if json.Unmarshal(raw, &n) == nil && n > 0 {
		return time.Unix(n, 0).UTC(), true
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		if t, e := time.Parse(time.RFC3339Nano, text); e == nil {
			return t, true
		}
		if n, e := strconv.ParseInt(text, 10, 64); e == nil && n > 0 {
			return time.Unix(n, 0).UTC(), true
		}
	}
	return time.Time{}, false
}
func parseProbes(raw []byte, asn uint32, af int, anchor bool) ([]Probe, error) {
	var list struct {
		Results []struct {
			ID          int64  `json:"id"`
			ASNV4       uint32 `json:"asn_v4"`
			ASNV6       uint32 `json:"asn_v6"`
			AddressV4   string `json:"address_v4"`
			AddressV6   string `json:"address_v6"`
			CountryCode string `json:"country_code"`
			IsAnchor    bool   `json:"is_anchor"`
			IsPublic    bool   `json:"is_public"`
			Status      struct {
				ID int `json:"id"`
			} `json:"status"`
			LastConnected json.RawMessage `json:"last_connected"`
		} `json:"results"`
	}
	if json.Unmarshal(raw, &list) != nil || list.Results == nil || len(list.Results) > 200 {
		return nil, &Error{Code: "invalid-probe-response"}
	}
	seen := map[int64]bool{}
	out := []Probe{}
	for _, p := range list.Results {
		if p.ID < 1 || p.ID > 2147483647 || seen[p.ID] {
			return nil, &Error{Code: "invalid-probe-identity"}
		}
		seen[p.ID] = true
		if p.Status.ID != 1 || !p.IsPublic || anchor && !p.IsAnchor {
			continue
		}
		p.AddressV4 = canonicalProbeAddress(p.AddressV4, 4)
		p.AddressV6 = canonicalProbeAddress(p.AddressV6, 6)
		if len(p.CountryCode) != 2 || p.CountryCode[0] < 'A' || p.CountryCode[0] > 'Z' || p.CountryCode[1] < 'A' || p.CountryCode[1] > 'Z' {
			p.CountryCode = ""
		}
		address, probeASN := p.AddressV4, p.ASNV4
		if af == 6 {
			address, probeASN = p.AddressV6, p.ASNV6
		}
		if probeASN != asn || !ValidTarget(address, af) {
			continue
		}
		at, _ := apiTime(p.LastConnected)
		out = append(out, Probe{ID: p.ID, ASNV4: p.ASNV4, ASNV6: p.ASNV6, AddressV4: p.AddressV4, AddressV6: p.AddressV6, CountryCode: p.CountryCode, IsAnchor: p.IsAnchor, IsPublic: p.IsPublic, Status: "connected", LastConnected: at})
	}
	return out, nil
}
func canonicalProbeAddress(raw string, af int) string {
	ip, err := netip.ParseAddr(raw)
	if err != nil {
		return ""
	}
	canonical := ip.String()
	if !ValidTarget(canonical, af) {
		return ""
	}
	return canonical
}
func parseMeasurement(raw []byte) (Measurement, error) {
	var row struct {
		ID          int64           `json:"id"`
		Type        string          `json:"type"`
		AF          int             `json:"af"`
		Target      string          `json:"target"`
		TargetIP    string          `json:"target_ip"`
		Description string          `json:"description"`
		IsPublic    *bool           `json:"is_public"`
		IsOneoff    *bool           `json:"is_oneoff"`
		Interval    int             `json:"interval"`
		Start       json.RawMessage `json:"start_time"`
		Stop        json.RawMessage `json:"stop_time"`
		Status      struct {
			ID   *int   `json:"id"`
			Name string `json:"name"`
		} `json:"status"`
	}
	if json.Unmarshal(raw, &row) != nil || row.ID < 1 || (row.Type != "ping" && row.Type != "traceroute") || row.IsPublic == nil || row.IsOneoff == nil || row.Status.ID == nil {
		return Measurement{}, &Error{Code: "invalid-measurement-response"}
	}
	target := row.TargetIP
	if target == "" {
		target = row.Target
	}
	ip, err := netip.ParseAddr(target)
	if err != nil {
		return Measurement{}, &Error{Code: "invalid-measurement-target"}
	}
	target = ip.String()
	if !ValidTarget(target, row.AF) {
		return Measurement{}, &Error{Code: "invalid-measurement-target"}
	}
	status := ""
	switch *row.Status.ID {
	case 0, 1:
		status = "scheduled"
	case 2:
		status = "running"
	case 3:
		status = "stopping"
	case 4, 5:
		status = "stopped"
	case 6, 7:
		status = "failed"
	case 8:
		status = "archived"
	default:
		return Measurement{}, &Error{Code: "unknown-measurement-status"}
	}
	start, _ := apiTime(row.Start)
	stop, _ := apiTime(row.Stop)
	return Measurement{ID: row.ID, Type: row.Type, AF: row.AF, Target: target, Description: row.Description, IsPublic: *row.IsPublic, IsOneoff: *row.IsOneoff, IntervalSeconds: row.Interval, StartAt: start, StopAt: stop, StatusID: *row.Status.ID, Status: status}, nil
}
func validObservedIP(raw string) string {
	ip, err := netip.ParseAddr(raw)
	if err != nil || ip.Is4In6() || ip.Zone() != "" || ip.IsMulticast() || ip.IsUnspecified() {
		return ""
	}
	return ip.String()
}
func rtt(v *float64) (*float64, error) {
	if v == nil || *v == -1 {
		return nil, nil
	}
	if math.IsInf(*v, 0) || math.IsNaN(*v) || *v < 0 || *v > 3600000 {
		return nil, &Error{Code: "invalid-result-rtt"}
	}
	return v, nil
}
func ParseResults(raw []byte, id int64, start, stop time.Time) ([]Result, error) {
	var rows []struct {
		ID       int64             `json:"msm_id"`
		Probe    int64             `json:"prb_id"`
		Type     string            `json:"type"`
		AF       int               `json:"af"`
		At       int64             `json:"timestamp"`
		Target   string            `json:"dst_addr"`
		Source   string            `json:"src_addr"`
		From     string            `json:"from"`
		Sent     *int              `json:"sent"`
		Received *int              `json:"rcvd"`
		Min      *float64          `json:"min"`
		Avg      *float64          `json:"avg"`
		Max      *float64          `json:"max"`
		Error    json.RawMessage   `json:"error"`
		Result   []json.RawMessage `json:"result"`
	}
	if len(raw) > MaxResponseBytes || json.Unmarshal(raw, &rows) != nil || rows == nil || len(rows) > MaxSamples {
		return nil, &Error{Code: "invalid-or-unbounded-results"}
	}
	out := make([]Result, 0, len(rows))
	for _, row := range rows {
		at := time.Unix(row.At, 0).UTC()
		if row.ID != id || row.Probe < 1 || (row.Type != "ping" && row.Type != "traceroute") || (row.AF != 4 && row.AF != 6) || row.At <= 0 || at.Before(start) || at.After(stop.Add(30*time.Second)) {
			return nil, &Error{Code: "result-identity-or-time-mismatch"}
		}
		r := Result{MeasurementID: id, ProbeID: row.Probe, Type: row.Type, AF: row.AF, Timestamp: at, TargetAddress: validObservedIP(row.Target), SourceAddress: validObservedIP(row.Source), Sent: row.Sent, Received: row.Received}
		if r.SourceAddress == "" {
			r.SourceAddress = validObservedIP(row.From)
		}
		if len(row.Error) > 0 && string(row.Error) != "null" {
			r.Error = "probe-error"
		}
		if row.Type == "ping" {
			if len(row.Result) > 16 {
				return nil, &Error{Code: "too-many-ping-packets"}
			}
			for _, packetRaw := range row.Result {
				var packet struct {
					RTT     *float64        `json:"rtt"`
					Timeout json.RawMessage `json:"x"`
					Error   json.RawMessage `json:"error"`
				}
				if json.Unmarshal(packetRaw, &packet) != nil {
					return nil, &Error{Code: "invalid-ping-packet"}
				}
				hasError := len(packet.Error) > 0 && string(packet.Error) != "null"
				hasTimeout := len(packet.Timeout) > 0 && string(packet.Timeout) != "null"
				if packet.RTT != nil && (hasError || hasTimeout) {
					return nil, &Error{Code: "contradictory-ping-packet"}
				}
				if hasError {
					r.Error = "probe-error"
				}
			}
			if row.Sent != nil && (*row.Sent < 0 || *row.Sent > 16) || row.Received != nil && (*row.Received < 0 || row.Sent != nil && *row.Received > *row.Sent) {
				return nil, &Error{Code: "invalid-ping-counts"}
			}
			var err error
			if r.MinRTT, err = rtt(row.Min); err != nil {
				return nil, err
			}
			if r.AvgRTT, err = rtt(row.Avg); err != nil {
				return nil, err
			}
			if r.MaxRTT, err = rtt(row.Max); err != nil {
				return nil, err
			}
			if row.Received != nil && *row.Received == 0 {
				r.MinRTT, r.AvgRTT, r.MaxRTT = nil, nil, nil
			}
		} else {
			if len(row.Result) > 32 {
				return nil, &Error{Code: "too-many-traceroute-hops"}
			}
			last := 0
			for _, hopRaw := range row.Result {
				var h struct {
					Hop    int             `json:"hop"`
					Error  json.RawMessage `json:"error"`
					Result []struct {
						From       string          `json:"from"`
						RTT        *float64        `json:"rtt"`
						Timeout    json.RawMessage `json:"x"`
						Error      json.RawMessage `json:"err"`
						LocalError json.RawMessage `json:"error"`
					} `json:"result"`
				}
				if json.Unmarshal(hopRaw, &h) != nil || h.Hop > 32 || len(h.Result) > 16 {
					return nil, &Error{Code: "invalid-traceroute-hop"}
				}
				hopError := len(h.Error) > 0 && string(h.Error) != "null"
				if hopError {
					if len(h.Result) > 0 {
						return nil, &Error{Code: "contradictory-traceroute-hop"}
					}
					r.Error = "probe-error"
					if h.Hop == 0 {
						continue
					} // official init errors have no hop number
				}
				if h.Hop <= last {
					return nil, &Error{Code: "invalid-traceroute-hop"}
				}
				last = h.Hop
				hop := Hop{Hop: h.Hop, Replies: []Reply{}}
				if hopError {
					hop.Error = "probe-error"
				}
				for _, v := range h.Result {
					latency, err := rtt(v.RTT)
					if err != nil {
						return nil, err
					}
					reply := Reply{Address: validObservedIP(v.From), RTT: latency, Timeout: len(v.Timeout) > 0 && string(v.Timeout) != "null"}
					if reply.Timeout && (v.RTT != nil || reply.Address != "" || len(v.Error) > 0 && string(v.Error) != "null") {
						return nil, &Error{Code: "contradictory-traceroute-reply"}
					}
					if len(v.Error) > 0 && string(v.Error) != "null" {
						reply.Error = "icmp-error"
						reply.RTT = nil // ICMP error timing is not a successful reply RTT.
					}
					if len(v.LocalError) > 0 && string(v.LocalError) != "null" {
						if v.RTT != nil || reply.Timeout {
							return nil, &Error{Code: "contradictory-traceroute-reply"}
						}
						reply.Error = "probe-error"
						r.Error = "probe-error"
					}
					if reply.Address == "" && reply.RTT != nil {
						return nil, &Error{Code: "rtt-without-hop-address"}
					}
					hop.Replies = append(hop.Replies, reply)
				}
				r.Hops = append(r.Hops, hop)
			}
		}
		if r.TargetAddress == "" {
			if row.Target != "" || r.Error != "probe-error" {
				return nil, &Error{Code: "missing-or-invalid-result-target"}
			}
			r.MinRTT, r.AvgRTT, r.MaxRTT = nil, nil, nil
		} else {
			target, _ := netip.ParseAddr(r.TargetAddress)
			if target.Is4() != (row.AF == 4) {
				return nil, &Error{Code: "result-target-family-mismatch"}
			}
		}
		out = append(out, r)
	}
	return out, nil
}

func PublicError(err error) string {
	if err == nil {
		return ""
	}
	if e, ok := err.(*Error); ok {
		return e.Code
	}
	return "provider-or-storage-failure"
}
func IsUncertain(err error) bool {
	e, ok := err.(*Error)
	return !ok || e.Uncertain || strings.HasPrefix(e.Code, "ambiguous-")
}
