package snmp

import (
	"errors"
	"fmt"
	"math"
	"net"
	"sort"
	"strconv"
	"strings"

	"github.com/gosnmp/gosnmp"
)

func tableRows(table map[string]gosnmp.SnmpPDU, limit int) ([]uint32, error) {
	rows := map[uint32]bool{}
	for key := range table {
		parts := strings.Split(key, ".")
		if len(parts) != 2 {
			return nil, errors.New("invalid standard MIB row index")
		}
		n, e := strconv.ParseUint(parts[1], 10, 31)
		if e != nil || n == 0 {
			return nil, errors.New("invalid standard MIB index")
		}
		rows[uint32(n)] = true
	}
	if len(rows) > limit {
		return nil, errors.New("standard MIB table exceeded row limit")
	}
	indices := []uint32{}
	for id := range rows {
		indices = append(indices, id)
	}
	sort.Slice(indices, func(i, j int) bool { return indices[i] < indices[j] })
	return indices, nil
}
func readEntities(budget *readBudget, client Transport) ([]Entity, map[uint32]uint32, error) {
	table, err := budget.walk(client, entityTable)
	if err != nil {
		return nil, nil, err
	}
	indices, err := tableRows(table, MaxEntities)
	if err != nil {
		return nil, nil, err
	}
	aliases := map[uint32]uint32{}
	aliasTable, err := budget.walk(client, entityAliases)
	if err == nil {
		for key, p := range aliasTable {
			parts := strings.Split(key, ".")
			if len(parts) != 2 {
				continue
			}
			entity, e := strconv.ParseUint(parts[0], 10, 31)
			value, e2 := text(p.Value)
			if e != nil || e2 != nil {
				continue
			}
			prefix := ifTable + ".1."
			oid := "." + strings.TrimPrefix(value, ".")
			if !strings.HasPrefix(oid, prefix) {
				continue
			}
			index, e := strconv.ParseUint(strings.TrimPrefix(oid, prefix), 10, 31)
			if e == nil && index > 0 {
				if previous := aliases[uint32(entity)]; previous != 0 && previous != uint32(index) {
					return nil, nil, errors.New("entity maps to conflicting interfaces")
				}
				aliases[uint32(entity)] = uint32(index)
			}
		}
	}
	entities := []Entity{}
	for _, index := range indices {
		suffix := "." + strconv.FormatUint(uint64(index), 10)
		get := func(column string) gosnmp.SnmpPDU { return table[column+suffix] }
		entity := Entity{Index: index, ParentIndex: u32(get("4")), Class: u32(get("5")), IfIndex: aliases[index]}
		for column, target := range map[string]*string{"2": &entity.Description, "7": &entity.Name, "8": &entity.HardwareRevision, "9": &entity.FirmwareRevision, "10": &entity.SoftwareRevision, "11": &entity.Serial, "12": &entity.Manufacturer, "13": &entity.Model} {
			*target, err = text(get(column).Value)
			if err != nil {
				return nil, nil, err
			}
		}
		entities = append(entities, entity)
	}
	return entities, aliases, nil
}
func sensorValue(kind, scale, precision int, raw int64) (*float64, string) {
	units := map[int]string{3: "V AC", 4: "V DC", 5: "A", 6: "W", 7: "Hz", 8: "°C", 9: "%RH", 10: "rpm", 11: "m³/min", 12: "boolean"}
	unit, known := units[kind]
	if !known || scale < 1 || scale > 17 || precision < -8 || precision > 9 || raw <= -1000000000 || raw >= 1000000000 {
		return nil, "unknown"
	}
	if kind == 12 {
		if raw != 1 && raw != 2 {
			return nil, unit
		}
		v := float64(0)
		if raw == 1 {
			v = 1
		}
		return &v, unit
	}
	if kind == 9 && (raw < 0 || raw > 100) {
		return nil, unit
	}
	// Negative precision reports significant/accurate digits, not a multiplier.
	fraction := precision
	if fraction < 0 {
		fraction = 0
	}
	if kind == 9 || kind == 10 {
		fraction = 0
	}
	value := float64(raw) * math.Pow10((scale-9)*3-fraction)
	if math.IsInf(value, 0) || math.IsNaN(value) {
		return nil, unit
	}
	return &value, unit
}
func readSensors(budget *readBudget, client Transport, entities []Entity, aliases map[uint32]uint32) ([]Sensor, error) {
	table, err := budget.walk(client, sensorTable)
	if err != nil {
		return nil, err
	}
	indices, err := tableRows(table, MaxSensors)
	if err != nil {
		return nil, err
	}
	byID := map[uint32]Entity{}
	for _, entity := range entities {
		byID[entity.Index] = entity
	}
	sensors := []Sensor{}
	for _, index := range indices {
		suffix := "." + strconv.FormatUint(uint64(index), 10)
		get := func(column string) gosnmp.SnmpPDU { return table[column+suffix] }
		kind, kok := signed(get("1").Value)
		scale, sok := signed(get("2").Value)
		precision, pok := signed(get("3").Value)
		raw, vok := signed(get("4").Value)
		if !kok || !sok || !pok || !vok {
			return nil, errors.New("incomplete ENTITY-SENSOR reading")
		}
		status := map[uint32]string{1: "ok", 2: "unavailable", 3: "nonoperational"}[u32(get("5"))]
		if status == "" {
			status = "unknown"
		}
		value, unit := sensorValue(int(kind), int(scale), int(precision), raw)
		if status != "ok" {
			value = nil
		}
		sensor := Sensor{EntityIndex: index, Name: byID[index].Name, Type: int(kind), Scale: int(scale), Precision: int(precision), RawValue: strconv.FormatInt(raw, 10), Value: value, Unit: unit, Status: status}
		visited := map[uint32]bool{}
		parent := index
		for depth := 0; parent != 0 && depth < 32; depth++ {
			if visited[parent] {
				return nil, errors.New("entity containment cycle")
			}
			visited[parent] = true
			if ifIndex := aliases[parent]; ifIndex != 0 {
				sensor.IfIndex = ifIndex
				break
			}
			parent = byID[parent].ParentIndex
		}
		sensors = append(sensors, sensor)
	}
	return sensors, nil
}
func readFDB(budget *readBudget, client Transport) ([]FDBEntry, error) {
	ports, err := budget.walk(client, qFDBPort)
	if err != nil {
		return nil, err
	}
	if len(ports) > MaxFDB {
		return nil, errors.New("FDB row limit exceeded")
	}
	statuses, err := budget.walk(client, qFDBStatus)
	if err != nil {
		return nil, err
	}
	bridge, err := budget.walk(client, bridgePortMap)
	if err != nil {
		return nil, err
	}
	vlans, err := budget.walk(client, qVLANFDB)
	if err != nil {
		return nil, err
	}
	fdbVLANs := map[uint32][]uint16{}
	for key, p := range vlans {
		parts := strings.Split(key, ".")
		if len(parts) != 2 {
			continue
		}
		v, e := strconv.ParseUint(parts[1], 10, 16)
		id := u32(p)
		if e == nil && v > 0 && v < 4095 && id > 0 {
			already := false
			for _, existing := range fdbVLANs[id] {
				if existing == uint16(v) {
					already = true
				}
			}
			if !already {
				fdbVLANs[id] = append(fdbVLANs[id], uint16(v))
			}
		}
	}
	out := []FDBEntry{}
	for key, p := range ports {
		parts := strings.Split(key, ".")
		if len(parts) != 7 {
			return nil, errors.New("invalid Q-BRIDGE FDB index")
		}
		fdb, e := strconv.ParseUint(parts[0], 10, 32)
		if e != nil {
			return nil, e
		}
		mac := make(net.HardwareAddr, 6)
		for i := 0; i < 6; i++ {
			value, e := strconv.ParseUint(parts[i+1], 10, 8)
			if e != nil {
				return nil, errors.New("invalid FDB MAC index")
			}
			mac[i] = byte(value)
		}
		port := u32(p)
		status := map[uint32]string{1: "other", 2: "invalid", 3: "learned", 4: "self", 5: "management"}[u32(statuses[key])]
		if status == "" {
			status = "unknown"
		}
		list := fdbVLANs[uint32(fdb)]
		if list == nil {
			list = []uint16{}
		}
		sort.Slice(list, func(i, j int) bool { return list[i] < list[j] })
		out = append(out, FDBEntry{FDBID: uint32(fdb), VLANIDs: list, MAC: mac.String(), BridgePort: port, IfIndex: u32(bridge[strconv.FormatUint(uint64(port), 10)]), Status: status})
	}
	sort.Slice(out, func(i, j int) bool {
		return fmt.Sprintf("%010d/%s", out[i].FDBID, out[i].MAC) < fmt.Sprintf("%010d/%s", out[j].FDBID, out[j].MAC)
	})
	return out, nil
}
