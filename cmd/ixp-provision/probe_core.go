package main

import (
	"fmt"
	"time"
)

type coreProbePort struct {
	PortID           string    `json:"portId"`
	SwitchID         string    `json:"switchId"`
	Status           string    `json:"status"`
	Source           string    `json:"source"`
	ObservedAt       time.Time `json:"observedAt"`
	IdentityVerified bool      `json:"identityVerified"`
	OperStatus       string    `json:"operStatus"`
}

func probeCore(raw []byte, id string, maxAge time.Duration, now time.Time) probeResult {
	var body struct {
		BundleID        string `json:"bundleId"`
		BindingRevision string `json:"bindingRevision"`
		Desired         struct {
			Enabled      bool   `json:"enabled"`
			Type         string `json:"type"`
			EnabledLinks int    `json:"enabledLinks"`
			TotalLinks   int    `json:"totalLinks"`
		} `json:"desired"`
		Physical struct {
			Status           string    `json:"status"`
			Source           string    `json:"source"`
			ObservedAt       time.Time `json:"observedAt"`
			IdentityVerified bool      `json:"identityVerified"`
		} `json:"physical"`
		Links []struct {
			ID      string        `json:"id"`
			Enabled bool          `json:"enabled"`
			SideA   coreProbePort `json:"sideA"`
			SideB   coreProbePort `json:"sideB"`
		} `json:"links"`
	}
	if decodeResponse(raw, &body) != nil || body.BundleID != id || len(body.BindingRevision) != 22 || !body.Desired.Enabled || body.Desired.EnabledLinks < 1 || body.Desired.TotalLinks != len(body.Links) || len(body.Links) > 128 {
		return unknown("core bundle identity, desired state or selected links unavailable")
	}
	if body.Desired.Type != "ecmp" && body.Desired.Type != "l2-lag" && body.Desired.Type != "l3-lag" {
		return unknown("unsupported core bundle type")
	}
	if body.Physical.Source != "snmp-if-mib" || !body.Physical.IdentityVerified || !freshAt(body.Physical.ObservedAt, now, maxAge) {
		return unknown("core physical observations stale or identity not verified")
	}
	seen := map[string]bool{}
	selected, down := 0, 0
	for _, link := range body.Links {
		if !identifier.MatchString(link.ID) || seen["link:"+link.ID] {
			return unknown("ambiguous core links")
		}
		seen["link:"+link.ID] = true
		if !link.Enabled {
			continue
		}
		selected++
		if link.SideA.SwitchID == link.SideB.SwitchID {
			return unknown("core endpoints are not distinct switches")
		}
		for _, p := range []coreProbePort{link.SideA, link.SideB} {
			if !identifier.MatchString(p.PortID) || !identifier.MatchString(p.SwitchID) || seen["port:"+p.PortID] {
				return unknown("ambiguous core physical endpoints")
			}
			seen["port:"+p.PortID] = true
			if p.Status != "fresh" || p.Source != "snmp-if-mib" || !p.IdentityVerified || !freshAt(p.ObservedAt, now, maxAge) {
				return unknown("one or more core endpoint observations stale or unavailable")
			}
			switch p.OperStatus {
			case "up":
			case "down":
				down++
			default:
				return unknown("core interface operational state unknown")
			}
		}
	}
	if selected != body.Desired.EnabledLinks {
		return unknown("core enabled link count changed")
	}
	if down > 0 {
		if body.Physical.Status != "down" && body.Physical.Status != "degraded" {
			return unknown("core physical summary contradicts endpoint observations")
		}
		return probeResult{2, fmt.Sprintf("CRITICAL - %d core physical endpoints report down; protocol health is not assessed", down)}
	}
	if body.Physical.Status != "up" {
		return unknown("core physical summary is incomplete")
	}
	return probeResult{0, fmt.Sprintf("OK - both ends of %d core links report Up; LACP, BFD, BGP and forwarding are not assessed", selected)}
}
