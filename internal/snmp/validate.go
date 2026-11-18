package snmp

import (
	"encoding/json"
	"errors"
	"github.com/moedove/ixp-manager/internal/traffic"
	"math"
	"net"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

func ValidateSnapshot(s Snapshot, now time.Time) error {
	if s.Version != 1 || !resourceID.MatchString(s.SwitchID) || s.ObservedAt.IsZero() || s.ObservedAt.After(now.Add(30*time.Second)) {
		return errors.New("invalid device snapshot identity/time")
	}
	if s.Security != "authPriv" && s.Security != "community" {
		return errors.New("device security must be authPriv or explicit community mode")
	}
	if len(s.Interfaces) > MaxInterfaces || len(s.Entities) > MaxEntities || len(s.Sensors) > MaxSensors || len(s.FDB) > MaxFDB || len(s.Warnings) > 4096 {
		return errors.New("device snapshot exceeds bounded collection limits")
	}
	if s.EngineIdentity != "" && !regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(s.EngineIdentity) {
		return errors.New("invalid stable SNMP engine identity")
	}
	if len(s.EngineEpoch) > 128 || strings.ContainsAny(s.EngineEpoch, "\x00\r\n") {
		return errors.New("invalid engine epoch")
	}
	if s.ErrorCode != "" {
		if s.ErrorCode != "timeout" && s.ErrorCode != "authentication" && s.ErrorCode != "transport" && s.ErrorCode != "invalid-response" && s.ErrorCode != "mapping-mismatch" {
			return errors.New("unsupported collection failure code")
		}
		if len(s.Interfaces)+len(s.Entities)+len(s.Sensors)+len(s.FDB) > 0 {
			return errors.New("failed observation must not contain fabricated measurements")
		}
	} else {
		if len(s.Interfaces) == 0 {
			return errors.New("successful device snapshot requires interfaces")
		}
		if _, err := parseCounter(s.UptimeTicks, 32); err != nil {
			return errors.New("valid sysUpTime ticks required")
		}
	}
	for _, value := range []string{s.Identity.Name, s.Identity.Description, s.Identity.Location, s.Identity.Contact, s.Identity.ObjectID} {
		if !safeText(value, 2048) {
			return errors.New("invalid identity text")
		}
	}
	seen := map[uint32]bool{}
	mapped := map[string]bool{}
	for _, port := range s.Interfaces {
		if port.IfIndex < 1 || port.IfIndex > 2147483647 || seen[port.IfIndex] || !safeText(port.Name, 256) || strings.TrimSpace(port.Name) == "" || strings.Contains(port.Name, "\n") || !safeText(port.Alias, 2048) || !safeText(port.Description, 2048) {
			return errors.New("invalid/duplicate discovered interface")
		}
		seen[port.IfIndex] = true
		if port.OwnershipRevision != "" && !regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(port.OwnershipRevision) {
			return errors.New("invalid ownership revision")
		}
		if port.PortID != "" {
			if !resourceID.MatchString(port.PortID) || mapped[port.PortID] {
				return errors.New("invalid/duplicate explicit port mapping")
			}
			mapped[port.PortID] = true
		}
		if port.SpeedMbps > 10000000 || port.MTU > 1<<24 {
			return errors.New("unreasonable interface speed/MTU")
		}
		if port.AdminStatus != "up" && port.AdminStatus != "down" && port.AdminStatus != "testing" && port.AdminStatus != "unknown" {
			return errors.New("invalid interface administrative status")
		}
		if port.OperStatus != "up" && port.OperStatus != "down" && port.OperStatus != "unknown" {
			return errors.New("invalid interface operational status")
		}
		if port.CounterBits == 0 {
			if port.InOctets != "" || port.OutOctets != "" {
				return errors.New("unknown counter type must have no octet sample")
			}
		} else {
			if port.CounterBits != 32 && port.CounterBits != 64 {
				return errors.New("counterBits must be32/64")
			}
			if _, err := parseCounter(port.InOctets, port.CounterBits); err != nil {
				return err
			}
			if _, err := parseCounter(port.OutOctets, port.CounterBits); err != nil {
				return err
			}
		}
		for _, counter := range []string{port.DiscontinuityTicks, port.LastChangeTicks, port.InErrors, port.OutErrors, port.InDiscards, port.OutDiscards} {
			if counter != "" {
				if _, err := parseCounter(counter, 32); err != nil {
					return err
				}
			}
		}
		for _, metric := range []*traffic.CounterPair{port.Unicast, port.Multicast, port.Broadcast} {
			if err := validateMetricPair(metric, false); err != nil {
				return err
			}
		}
		for _, metric := range []*traffic.CounterPair{port.Errors, port.Discards} {
			if err := validateMetricPair(metric, true); err != nil {
				return err
			}
		}
		if err := validateMetricAliases(port.Errors, port.InErrors, port.OutErrors); err != nil {
			return err
		}
		if err := validateMetricAliases(port.Discards, port.InDiscards, port.OutDiscards); err != nil {
			return err
		}
		if port.PhysicalAddress != "" {
			mac, err := net.ParseMAC(port.PhysicalAddress)
			if err != nil || len(mac) != 6 {
				return errors.New("invalid physical MAC")
			}
		}
	}
	entities := map[uint32]bool{}
	for _, entity := range s.Entities {
		if entity.Index == 0 || entities[entity.Index] || (entity.IfIndex != 0 && !seen[entity.IfIndex]) {
			return errors.New("invalid entity index/mapping")
		}
		entities[entity.Index] = true
		for _, value := range []string{entity.Name, entity.Description, entity.Manufacturer, entity.Model, entity.Serial, entity.HardwareRevision, entity.FirmwareRevision, entity.SoftwareRevision} {
			if !safeText(value, 2048) {
				return errors.New("invalid entity label")
			}
		}
	}
	sensors := map[uint32]bool{}
	for _, sensor := range s.Sensors {
		if sensor.EntityIndex == 0 || sensors[sensor.EntityIndex] || (sensor.IfIndex != 0 && !seen[sensor.IfIndex]) || !safeText(sensor.Name, 2048) {
			return errors.New("invalid sensor mapping")
		}
		sensors[sensor.EntityIndex] = true
		raw, err := strconv.ParseInt(sensor.RawValue, 10, 32)
		if err != nil {
			return errors.New("invalid raw sensor value")
		}
		value, unit := sensorValue(sensor.Type, sensor.Scale, sensor.Precision, raw)
		if sensor.Status != "ok" {
			value = nil
		}
		if sensor.Status != "ok" && sensor.Status != "unknown" && sensor.Status != "unavailable" && sensor.Status != "nonoperational" {
			return errors.New("invalid sensor state")
		}
		if unit != sensor.Unit || (value == nil) != (sensor.Value == nil) || (value != nil && (math.IsInf(*sensor.Value, 0) || math.IsNaN(*sensor.Value) || *value != *sensor.Value)) {
			return errors.New("sensor value/unit must agree with standard raw scaling")
		}
	}
	fdb := map[string]bool{}
	for _, entry := range s.FDB {
		mac, err := net.ParseMAC(entry.MAC)
		key := strconv.FormatUint(uint64(entry.FDBID), 10) + "/" + entry.MAC
		if err != nil || len(mac) != 6 || fdb[key] || (entry.IfIndex != 0 && !seen[entry.IfIndex]) || len(entry.VLANIDs) > 4094 {
			return errors.New("invalid FDB entry")
		}
		fdb[key] = true
		for _, vlan := range entry.VLANIDs {
			if vlan < 1 || vlan > 4094 {
				return errors.New("invalid observed VLAN")
			}
		}
		if !slicesContains([]string{"other", "invalid", "learned", "self", "management", "unknown"}, entry.Status) {
			return errors.New("invalid FDB status")
		}
	}
	for _, warning := range s.Warnings {
		if !safeText(warning, 2048) {
			return errors.New("invalid device warning")
		}
	}
	raw, err := json.Marshal(s)
	if err != nil || len(raw) > MaxSnapshotBytes {
		return errors.New("device snapshot exceeds8MiB")
	}
	return nil
}
func safeText(value string, max int) bool {
	return utf8.ValidString(value) && len(value) <= max && !strings.ContainsAny(value, "\x00\r")
}
func parseCounter(raw string, bits int) (uint64, error) {
	if raw == "" || len(raw) > 20 || strings.TrimLeft(raw, "0123456789") != "" {
		return 0, errors.New("counter must be an unsigned decimal string")
	}
	value, err := strconv.ParseUint(raw, 10, bits)
	if err != nil {
		return 0, errors.New("counter exceeds declared width")
	}
	return value, nil
}
func slicesContains(values []string, value string) bool {
	for _, v := range values {
		if v == value {
			return true
		}
	}
	return false
}
