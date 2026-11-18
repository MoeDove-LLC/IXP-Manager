package snmp

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gosnmp/gosnmp"
)

const systemBase = ".1.3.6.1.2.1.1."
const ifTable = ".1.3.6.1.2.1.2.2.1"
const ifXTable = ".1.3.6.1.2.1.31.1.1.1"
const entityTable = ".1.3.6.1.2.1.47.1.1.1.1"
const entityAliases = ".1.3.6.1.2.1.47.1.3.2.1.2"
const sensorTable = ".1.3.6.1.2.1.99.1.1.1"
const bridgePortMap = ".1.3.6.1.2.1.17.1.4.1.2"
const qFDBPort = ".1.3.6.1.2.1.17.7.1.2.2.1.2"
const qFDBStatus = ".1.3.6.1.2.1.17.7.1.2.2.1.3"
const qVLANFDB = ".1.3.6.1.2.1.17.7.1.4.2.1.3"

var systemOIDs = map[string]bool{systemBase + "1.0": true, systemBase + "2.0": true, systemBase + "3.0": true, systemBase + "4.0": true, systemBase + "5.0": true, systemBase + "6.0": true}
var walkRoots = map[string]bool{ifTable: true, ifXTable: true, entityTable: true, entityAliases: true, sensorTable: true, bridgePortMap: true, qFDBPort: true, qFDBStatus: true, qVLANFDB: true}

type readBudget struct {
	values, bytes int
	ctx           context.Context
}

func (b *readBudget) walk(client Transport, root string) (map[string]gosnmp.SnmpPDU, error) {
	out := map[string]gosnmp.SnmpPDU{}
	err := client.Walk(root, func(p gosnmp.SnmpPDU) error {
		if err := b.ctx.Err(); err != nil {
			return errors.New("collection timed out")
		}
		oid := "." + strings.TrimPrefix(p.Name, ".")
		if !strings.HasPrefix(oid, root+".") {
			return errors.New("SNMP walk escaped its fixed subtree")
		}
		if p.Type == gosnmp.EndOfMibView || p.Type == gosnmp.NoSuchObject || p.Type == gosnmp.NoSuchInstance {
			return nil
		}
		suffix := strings.TrimPrefix(oid, root+".")
		if _, exists := out[suffix]; exists {
			return errors.New("duplicate SNMP table instance")
		}
		b.values++
		b.bytes += len(oid) + len(fmt.Sprint(p.Value))
		if b.values > 250000 || b.bytes > 32<<20 {
			return errors.New("SNMP walk exceeded bounded response budget")
		}
		out[suffix] = p
		return nil
	})
	if err != nil {
		return nil, safeTransportError(err)
	}
	return out, nil
}
func unsigned(value any) (uint64, bool) {
	switch v := value.(type) {
	case uint64:
		return v, true
	case uint32:
		return uint64(v), true
	case uint:
		return uint64(v), true
	case int:
		if v >= 0 {
			return uint64(v), true
		}
	case int64:
		if v >= 0 {
			return uint64(v), true
		}
	case *big.Int:
		if v != nil && v.Sign() >= 0 && v.BitLen() <= 64 {
			return v.Uint64(), true
		}
	}
	return 0, false
}
func signed(value any) (int64, bool) {
	switch v := value.(type) {
	case int:
		return int64(v), true
	case int32:
		return int64(v), true
	case int64:
		return v, true
	default:
		if n, ok := unsigned(value); ok && n <= 1<<63-1 {
			return int64(n), true
		}
	}
	return 0, false
}
func text(value any) (string, error) {
	var s string
	switch v := value.(type) {
	case string:
		s = v
	case []byte:
		s = string(v)
	case nil:
		return "", nil
	default:
		return "", errors.New("SNMP value is not text")
	}
	s = strings.TrimRight(s, "\x00")
	if !utf8.ValidString(s) || len(s) > 2048 || strings.ContainsAny(s, "\x00\r") {
		return "", errors.New("invalid bounded SNMP text")
	}
	return s, nil
}
func u32(p gosnmp.SnmpPDU) uint32 {
	n, ok := unsigned(p.Value)
	if !ok || n > 1<<32-1 {
		return 0
	}
	return uint32(n)
}
func uintText(p gosnmp.SnmpPDU, bits int) string {
	n, ok := unsigned(p.Value)
	if !ok || (bits == 32 && n > 1<<32-1) {
		return ""
	}
	return strconv.FormatUint(n, 10)
}
func enumStatus(n uint32, admin bool) string {
	if n == 1 {
		return "up"
	}
	if n == 2 {
		return "down"
	}
	if admin && n == 3 {
		return "testing"
	}
	return "unknown"
}

func Collect(ctx context.Context, c Config) (Snapshot, error) {
	if err := ValidateConfig(c); err != nil {
		return Snapshot{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	client, err := Open(ctx, c)
	if err != nil {
		return Snapshot{}, err
	}
	defer client.Close()
	return CollectWithTransport(ctx, c, client)
}
func CollectWithTransport(ctx context.Context, c Config, client Transport) (Snapshot, error) {
	if err := ValidateConfig(c); err != nil {
		return Snapshot{}, err
	}
	result := Snapshot{Version: 1, SwitchID: c.SwitchID, Security: "authPriv", Interfaces: []Interface{}, Entities: []Entity{}, Sensors: []Sensor{}, FDB: []FDBEntry{}, Warnings: []string{}}
	if c.Version == "2c" {
		result.Security = "community"
		result.Warnings = append(result.Warnings, "SNMPv2c transport is explicitly enabled; management traffic is not encrypted")
	}
	budget := &readBudget{ctx: ctx}
	oids := []string{}
	for oid := range systemOIDs {
		oids = append(oids, oid)
	}
	sort.Strings(oids)
	values, err := client.Get(oids)
	if err != nil {
		return Snapshot{}, safeTransportError(err)
	}
	for _, p := range values {
		oid := "." + strings.TrimPrefix(p.Name, ".")
		if !systemOIDs[oid] {
			return Snapshot{}, errors.New("unexpected SNMP system OID")
		}
		if p.Type == gosnmp.NoSuchObject || p.Type == gosnmp.NoSuchInstance {
			continue
		}
		if oid == systemBase+"3.0" {
			result.UptimeTicks = uintText(p, 32)
			continue
		}
		value, e := text(p.Value)
		if e != nil {
			return Snapshot{}, e
		}
		switch oid {
		case systemBase + "1.0":
			result.Identity.Description = value
		case systemBase + "2.0":
			result.Identity.ObjectID = value
		case systemBase + "4.0":
			result.Identity.Contact = value
		case systemBase + "5.0":
			result.Identity.Name = value
		case systemBase + "6.0":
			result.Identity.Location = value
		}
	}
	if result.UptimeTicks == "" {
		return Snapshot{}, errors.New("SNMP sysUpTime is required for counter discontinuity detection")
	}
	basic, err := budget.walk(client, ifTable)
	if err != nil {
		return Snapshot{}, err
	}
	extended, err := budget.walk(client, ifXTable)
	if err != nil {
		result.Warnings = append(result.Warnings, "IF-X table unavailable; only explicit32-bit counters can be reported")
		extended = map[string]gosnmp.SnmpPDU{}
	}
	rows := map[uint32]bool{}
	for key := range basic {
		parts := strings.Split(key, ".")
		if len(parts) != 2 {
			return Snapshot{}, errors.New("invalid IF-MIB table index")
		}
		index, e := strconv.ParseUint(parts[1], 10, 31)
		if e != nil || index == 0 {
			return Snapshot{}, errors.New("invalid ifIndex")
		}
		rows[uint32(index)] = true
	}
	if len(rows) == 0 || len(rows) > MaxInterfaces {
		return Snapshot{}, errors.New("IF-MIB must contain1..2048 interfaces")
	}
	indices := []uint32{}
	for i := range rows {
		indices = append(indices, i)
	}
	sort.Slice(indices, func(i, j int) bool { return indices[i] < indices[j] })
	mappings := map[uint32]PortMapping{}
	for _, m := range c.Mappings {
		mappings[m.IfIndex] = m
	}
	for _, index := range indices {
		suffix := "." + strconv.FormatUint(uint64(index), 10)
		b := func(column string) gosnmp.SnmpPDU { return basic[column+suffix] }
		x := func(column string) gosnmp.SnmpPDU { return extended[column+suffix] }
		row := Interface{IfIndex: index, PortID: mappings[index].PortID, OwnershipRevision: mappings[index].OwnershipRevision, Type: u32(b("3")), MTU: u32(b("4")), AdminStatus: enumStatus(u32(b("7")), true), OperStatus: enumStatus(u32(b("8")), false), LastChangeTicks: uintText(b("9"), 32), DiscontinuityTicks: uintText(x("19"), 32)}
		// RFC2863 IF-X high-capacity packets have independent per-direction
		// fallbacks. Deprecated NUcast counters cannot split multicast/broadcast.
		row.Unicast = metricPair(x("7"), x("11"), b("11"), b("17"))
		row.Multicast = metricPair(x("8"), x("12"), x("2"), x("4"))
		row.Broadcast = metricPair(x("9"), x("13"), x("3"), x("5"))
		row.Errors = metricPair(gosnmp.SnmpPDU{}, gosnmp.SnmpPDU{}, b("14"), b("20"))
		row.Discards = metricPair(gosnmp.SnmpPDU{}, gosnmp.SnmpPDU{}, b("13"), b("19"))
		if row.Errors != nil {
			row.InErrors, row.OutErrors = row.Errors.In, row.Errors.Out
		}
		if row.Discards != nil {
			row.InDiscards, row.OutDiscards = row.Discards.In, row.Discards.Out
		}
		row.Name, err = text(x("1").Value)
		if err != nil {
			return Snapshot{}, err
		}
		row.Description, err = text(b("2").Value)
		if err != nil {
			return Snapshot{}, err
		}
		if row.Name == "" {
			row.Name = row.Description
		}
		if row.Name == "" || len(row.Name) > 256 || strings.Contains(row.Name, "\n") {
			return Snapshot{}, errors.New("interface requires a valid name or description")
		}
		row.Alias, err = text(x("18").Value)
		if err != nil {
			return Snapshot{}, err
		}
		if declared := u32(b("1")); declared != 0 && declared != index {
			return Snapshot{}, errors.New("ifIndex value disagrees with table instance")
		}
		row.SpeedMbps = uint64(u32(x("15")))
		if row.SpeedMbps == 0 {
			if speed := u32(b("5")); speed != 1<<32-1 {
				row.SpeedMbps = uint64(speed) / 1000000
			}
		}
		if physical, ok := b("6").Value.([]byte); ok && len(physical) == 6 {
			row.PhysicalAddress = net.HardwareAddr(physical).String()
		}
		if x("6").Type == gosnmp.Counter64 && x("10").Type == gosnmp.Counter64 {
			row.InOctets = uintText(x("6"), 64)
			row.OutOctets = uintText(x("10"), 64)
			row.CounterBits = 64
		} else if b("10").Type == gosnmp.Counter32 && b("16").Type == gosnmp.Counter32 {
			row.InOctets = uintText(b("10"), 32)
			row.OutOctets = uintText(b("16"), 32)
			row.CounterBits = 32
		}
		if row.InOctets == "" || row.OutOctets == "" {
			row.InOctets = ""
			row.OutOctets = ""
			row.CounterBits = 0
			result.Warnings = append(result.Warnings, fmt.Sprintf("ifIndex%d has no complete octet counters", index))
		}
		result.Interfaces = append(result.Interfaces, row)
	}
	for _, mapping := range c.Mappings {
		if !rows[mapping.IfIndex] {
			return Snapshot{}, errors.New("an explicitly mapped interface disappeared; discovery/review is required")
		}
	}
	if c.CollectEntities || c.CollectSensors {
		entities, aliases, e := readEntities(budget, client)
		if e != nil {
			result.Warnings = append(result.Warnings, "ENTITY identity/mapping data unavailable")
		} else {
			result.Entities = entities
			if c.CollectSensors {
				sensors, e := readSensors(budget, client, entities, aliases)
				if e != nil {
					result.Warnings = append(result.Warnings, "ENTITY-SENSOR data unavailable")
				} else {
					result.Sensors = sensors
				}
			}
		}
	}
	if c.CollectFDB {
		entries, e := readFDB(budget, client)
		if e != nil {
			result.Warnings = append(result.Warnings, "Q-BRIDGE forwarding table unavailable or exceeded bounds")
		} else {
			result.FDB = entries
		}
	}
	if err := ctx.Err(); err != nil {
		return Snapshot{}, errors.New("SNMP collection timed out or was cancelled")
	}
	result.ObservedAt = time.Now().UTC()
	result.EngineEpoch = client.EngineEpoch()
	if c.Version == "3" {
		if identified, ok := client.(interface{ EngineIdentity() string }); ok {
			result.EngineIdentity = identified.EngineIdentity()
		}
		if result.EngineIdentity == "" {
			return Snapshot{}, errors.New("authenticated SNMP engine identity unavailable")
		}
	}
	if err = ValidateSnapshot(result, time.Now()); err != nil {
		return Snapshot{}, err
	}
	return result, nil
}
