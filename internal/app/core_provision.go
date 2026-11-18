package app

import (
	"github.com/moedove/ixp-manager/internal/core"
	"github.com/moedove/ixp-manager/internal/provision"
)

func coreProvisionInput(s *State, options provision.Options) (provision.Input, error) {
	out := provision.Input{Options: options, Targets: []provision.Target{}}
	if err := validateCore(s); err != nil {
		return out, err
	}
	var err error
	out.Options, err = provision.NormalizeOptions(options)
	if err != nil {
		return out, err
	}
	inventory := core.PlanInventory{Switches: []core.SwitchInventory{}, Ports: []core.PortInventory{}}
	for _, sw := range s.Resources["switches"] {
		inventory.Switches = append(inventory.Switches, core.SwitchInventory{ID: str(sw, "id"), Name: str(sw, "name"), InfrastructureID: fabricID(sw), Active: sw["active"] != false, ASN: uint32(number(sw, "asn")), LoopbackIPv4: str(sw, "loopbackIpv4"), LoopbackIPv6: str(sw, "loopbackIpv6"), LoopbackName: str(sw, "loopbackName")})
	}
	for _, p := range s.Resources["ports"] {
		row := core.PortInventory{ID: str(p, "id"), SwitchID: str(p, "switchId"), Name: str(p, "name")}
		if number(p, "speed") > 0 {
			speed := uint64(number(p, "speed"))
			row.SpeedMbps = &speed
		}
		inventory.Ports = append(inventory.Ports, row)
	}
	plan, err := core.BuildSwitchPlan(options.SwitchID, s.CoreBundles, inventory)
	if err != nil {
		return out, err
	}
	out.CoreConfig = &plan
	return out, provision.ValidateInput(out)
}

func coreMonitoringInput(s *State, in *provision.Input) {
	// Core links do not belong to any one peering VLAN. A scoped profile must
	// include both sides of a bundle, rather than silently crossing fabrics.
	if in.Options.VLANID != "" {
		return
	}
	for _, b := range s.CoreBundles {
		if in.Options.InfrastructureID != "" && (fabricID(find(s, "switches", b.SideA.SwitchID)) != in.Options.InfrastructureID || fabricID(find(s, "switches", b.SideB.SwitchID)) != in.Options.InfrastructureID) {
			continue
		}
		anchor := ""
		for _, id := range []string{b.SideA.SwitchID, b.SideB.SwitchID} {
			for _, target := range in.Targets {
				if target.Kind == "switch" && target.SwitchID == id && target.Active && target.Monitor {
					anchor = target.ID
					break
				}
			}
			if anchor != "" {
				break
			}
		}
		if anchor != "" {
			in.CoreBundles = append(in.CoreBundles, provision.CoreBundle{ID: b.ID, TargetID: anchor, Enabled: b.Enabled})
		}
	}
}
