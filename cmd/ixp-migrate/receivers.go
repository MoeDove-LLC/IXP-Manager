package main

import (
	"fmt"
	"sort"
	"strconv"

	"github.com/moedove/ixp-manager/internal/migrate"
)

func (c *converter) receiverError(table string, r row, message string) error {
	c.warn(table, r, message)
	return fmt.Errorf("unsafe receiver/MAC migration: %s id %d: %s; no safe import file can be produced", table, idOf(r), message)
}
func (c *converter) receiverUsed(table string, r row, field string) {
	c.used[table][strconv.FormatInt(idOf(r), 10)+":"+field] = true
}
func (c *converter) receiverInteger(table string, r row, field string) (int64, error) {
	c.receiverUsed(table, r, field)
	_, boolean := r[field].(bool)
	n, err := integer(r[field])
	if err != nil || boolean {
		return 0, c.receiverError(table, r, field+" must be an explicit exact integer")
	}
	return n, nil
}
func (c *converter) receiverReference(table string, r row, field, target string) (row, error) {
	id, err := c.receiverInteger(table, r, field)
	if err != nil {
		return nil, err
	}
	linked := c.index[target][strconv.FormatInt(id, 10)]
	if id < 1 || linked == nil {
		return nil, c.receiverError(table, r, "missing or invalid "+field+" reference")
	}
	return linked, nil
}
func (c *converter) receiverText(table string, r row, field string) (string, error) {
	c.receiverUsed(table, r, field)
	value, ok := r[field].(string)
	if !ok {
		return "", c.receiverError(table, r, field+" must be explicit text")
	}
	return value, nil
}
func (c *converter) receiverEnabled(r row) (bool, error) {
	value, exists := r["active"]
	if !exists {
		return true, nil
	}
	// Optional, explicitly documented extension. Stock v5 has no active column
	// and its API exports every receiver record, independent of port status.
	c.receiverUsed("sflow_receiver", r, "active")
	if boolean, ok := value.(bool); ok {
		return boolean, nil
	}
	n, err := integer(value)
	if err != nil || n != 0 && n != 1 {
		return false, c.receiverError("sflow_receiver", r, "active extension must be explicit boolean or0/1")
	}
	return n == 1, nil
}
func (c *converter) convertedRecord(kind, id string) row {
	for _, r := range c.result.Resources[kind] {
		if r["id"] == id {
			return r
		}
	}
	return nil
}
func (c *converter) receiverConnection(table string, r, vli row) (migrate.ReceiverConnection, error) {
	vi, err := c.receiverReference("vlaninterface", vli, "virtualinterfaceid", "virtualinterface")
	if err != nil {
		return migrate.ReceiverConnection{}, err
	}
	member, err := c.receiverReference("virtualinterface", vi, "custid", "cust")
	if err != nil {
		return migrate.ReceiverConnection{}, err
	}
	vlan, err := c.receiverReference("vlaninterface", vli, "vlanid", "vlan")
	if err != nil {
		return migrate.ReceiverConnection{}, err
	}
	connection := c.convertedRecord("connections", legacyID("connections", idOf(vli)))
	memberID, vlanID := legacyID("members", idOf(member)), legacyID("vlans", idOf(vlan))
	if connection == nil || c.convertedRecord("members", memberID) == nil || c.convertedRecord("vlans", vlanID) == nil || connection["memberId"] != memberID || connection["vlanId"] != vlanID {
		return migrate.ReceiverConnection{}, c.receiverError(table, r, "VLAN attachment lacks a successfully converted member-owned connection")
	}
	ports := []string{}
	if id, ok := connection["portId"].(string); ok {
		ports = append(ports, id)
	}
	if ids, ok := connection["portIds"].([]string); ok {
		ports = append(ports, ids...)
	}
	if len(ports) == 0 || len(ports) > 64 {
		return migrate.ReceiverConnection{}, c.receiverError(table, r, "connection has no complete physical port set")
	}
	convertedVLAN := c.convertedRecord("vlans", vlanID)
	for _, id := range ports {
		port := c.convertedRecord("ports", id)
		switchID, _ := port["switchId"].(string)
		sw := c.convertedRecord("switches", switchID)
		if port == nil || sw == nil || port["memberId"] != memberID || sw["infrastructureId"] != convertedVLAN["infrastructureId"] {
			return migrate.ReceiverConnection{}, c.receiverError(table, r, "connection physical ports do not close to its member and VLAN infrastructure")
		}
	}
	return migrate.ReceiverConnection{ID: connection["id"].(string), MemberID: memberID, VLANID: vlanID, VirtualInterfaceID: idOf(vi)}, nil
}
func (c *converter) receiverTimes(table string, r row) {
	for _, field := range []string{"created", "created_at", "updated_at", "firstseen", "lastseen"} {
		if _, exists := r[field]; exists {
			c.receiverUsed(table, r, field)
		}
	}
}

func (c *converter) registeredMACs() error {
	seen := map[string]bool{}
	for _, r := range c.source.Tables["l2address"] {
		vli, err := c.receiverReference("l2address", r, "vlan_interface_id", "vlaninterface")
		if err != nil {
			return err
		}
		connection, err := c.receiverConnection("l2address", r, vli)
		if err != nil {
			return err
		}
		raw, err := c.receiverText("l2address", r, "mac")
		if err != nil {
			return err
		}
		mac, err := migrate.RegisteredMAC(raw)
		if err != nil {
			return c.receiverError("l2address", r, err.Error())
		}
		key := connection.VLANID + "/" + mac
		if seen[key] {
			return c.receiverError("l2address", r, "registered MAC is duplicated or claimed by multiple connections on the same VLAN")
		}
		seen[key] = true
		c.add("mac-addresses", r, row{"id": legacyID("mac-addresses", idOf(r)), "name": fmt.Sprintf("Legacy registered MAC %d", idOf(r)), "connectionId": connection.ID, "address": mac})
		c.receiverTimes("l2address", r)
		c.resolve("l2address", r)
	}
	return nil
}

func (c *converter) sflowReceivers() error {
	if len(c.source.Tables["sflow_receiver"]) > 512 {
		return fmt.Errorf("receiver source exceeds512-row import limit")
	}
	rows := append([]row(nil), c.source.Tables["sflow_receiver"]...)
	sort.Slice(rows, func(i, j int) bool { return idOf(rows[i]) < idOf(rows[j]) })
	for _, r := range rows {
		// Unknown lifecycle flags cannot be silently interpreted as enabled.
		for _, field := range []string{"enabled", "status", "approved", "approved_at", "approved_by"} {
			if value, present := r[field]; present && value != nil && value != "" {
				return c.receiverError("sflow_receiver", r, "unsupported lifecycle field "+field+"; review the source explicitly")
			}
		}
		vi, err := c.receiverReference("sflow_receiver", r, "virtual_interface_id", "virtualinterface")
		if err != nil {
			return err
		}
		member, err := c.receiverReference("virtualinterface", vi, "custid", "cust")
		if err != nil {
			return err
		}
		memberID := legacyID("members", idOf(member))
		if c.convertedRecord("members", memberID) == nil {
			return c.receiverError("sflow_receiver", r, "owner member was not converted")
		}
		if c.coreGraph.vis[idOf(vi)] {
			return c.receiverError("sflow_receiver", r, "core virtual interface is not a member receiver scope")
		}
		attachments := []migrate.ReceiverConnection{}
		for _, vli := range c.source.Tables["vlaninterface"] {
			ref, err := integer(vli["virtualinterfaceid"])
			_, boolean := vli["virtualinterfaceid"].(bool)
			if err != nil || boolean || ref < 1 {
				return c.receiverError("vlaninterface", vli, "cannot establish virtual interface ownership while converting receiver scope")
			}
			if ref != idOf(vi) {
				continue
			}
			connection, err := c.receiverConnection("sflow_receiver", r, vli)
			if err != nil {
				return err
			}
			attachments = append(attachments, connection)
		}
		address, err := c.receiverText("sflow_receiver", r, "dst_ip")
		if err != nil {
			return err
		}
		port, err := c.receiverInteger("sflow_receiver", r, "dst_port")
		if err != nil {
			return err
		}
		enabled, err := c.receiverEnabled(r)
		if err != nil {
			return err
		}
		converted, err := migrate.ConvertReceiver(migrate.ReceiverSource{ID: idOf(r), VirtualInterfaceID: idOf(vi), MemberID: memberID, Address: address, Port: port, Enabled: enabled}, attachments)
		if err != nil {
			return c.receiverError("sflow_receiver", r, err.Error())
		}
		c.result.SFlowReceivers = append(c.result.SFlowReceivers, converted)
		c.receiverTimes("sflow_receiver", r)
		c.resolve("sflow_receiver", r)
	}
	return nil
}
