package provision

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/netip"
	"sort"
	"strings"
)

const MaxArtifactBytes = 4 << 20

func shortID(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:10])
}
func hostID(t Target) string { return "md_" + t.Kind + "_" + shortID(t.ID) }
func containsID(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}
func targetFamily(t Target) int {
	ip, _ := targetAddress(t)
	if ip.Is4() {
		return 4
	}
	return 6
}
func wantedFamily(o Options, family int) bool {
	for _, f := range o.Families {
		if f == family {
			return true
		}
	}
	return false
}
func scopedTargets(in Input) []Target {
	selected := map[string]bool{}
	neededSwitches := map[string]bool{}
	for _, t := range in.Targets {
		if !wantedFamily(in.Options, targetFamily(t)) {
			continue
		}
		if in.Options.InfrastructureID != "" && t.InfrastructureID != in.Options.InfrastructureID {
			continue
		}
		if in.Options.VLANID != "" && t.VLANID != in.Options.VLANID {
			continue
		}
		selected[t.ID] = true
		if t.Kind == "member" {
			for _, id := range t.SwitchIDs {
				neededSwitches[id] = true
			}
		}
	}
	if in.Options.VLANID != "" {
		for _, t := range in.Targets {
			if t.Kind == "switch" && neededSwitches[t.SwitchID] && wantedFamily(in.Options, targetFamily(t)) && (in.Options.InfrastructureID == "" || t.InfrastructureID == in.Options.InfrastructureID) {
				selected[t.ID] = true
			}
		}
	}
	out := []Target{}
	for _, t := range in.Targets {
		if selected[t.ID] {
			out = append(out, t)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
func Render(input Input) (Artifact, error) {
	options, err := NormalizeOptions(input.Options)
	if err != nil {
		return Artifact{}, err
	}
	input.Options = options
	if err = ValidateInput(input); err != nil {
		return Artifact{}, err
	}
	if options.Kind != "dns-arpa" && options.Kind != "switch-core" && options.Kind != "as112-dns" {
		// These generated probes have no VRF/network-namespace selector. The
		// same address on different fabrics cannot be observed independently.
		addresses := map[netip.Addr]Target{}
		for _, t := range scopedTargets(input) {
			if !t.Active || !t.Monitor || !t.CanPing {
				continue
			}
			ip, _ := targetAddress(t)
			if previous, ok := addresses[ip]; ok && (previous.InfrastructureID != t.InfrastructureID || previous.VLANID != t.VLANID || previous.MemberID != t.MemberID) {
				return Artifact{}, fmt.Errorf("monitoring address is reused across different fabrics, VLANs or members; narrow the profile scope because generated probes have no VRF selector")
			}
			addresses[ip] = t
		}
	}
	var artifact Artifact
	switch options.Kind {
	case "as112-dns":
		artifact, err = renderAS112DNS(input)
	case "dns-arpa":
		artifact, err = renderDNS(input)
	case "nagios":
		artifact, err = renderNagios(input)
	case "smokeping":
		artifact, err = renderSmokeping(input)
	case "switch-core":
		artifact, err = renderSwitchCore(input)
	}
	if err != nil {
		return Artifact{}, err
	}
	if len(artifact.Content) > MaxArtifactBytes {
		return Artifact{}, fmt.Errorf("generated artifact exceeds 4 MiB")
	}
	if artifact.TargetCount == 0 {
		artifact.Notices = append(artifact.Notices, Notice{Severity: "warning", Code: "empty-target-set", Message: "没有可用目标；安装此输出可能移除原来的受管记录，请核对差异。"})
	}
	return artifact, nil
}
func reverseOwner(ip netip.Addr) string {
	if ip.Is4() {
		b := ip.As4()
		return fmt.Sprintf("%d.%d.%d.%d.in-addr.arpa.", b[3], b[2], b[1], b[0])
	}
	b := ip.As16()
	hex := fmt.Sprintf("%032x", b)
	parts := make([]string, 0, 32)
	for i := len(hex) - 1; i >= 0; i-- {
		parts = append(parts, string(hex[i]))
	}
	return strings.Join(parts, ".") + ".ip6.arpa."
}
func renderDNS(in Input) (Artifact, error) {
	o := in.Options.DNS
	zone, _ := ReverseZone(o.Prefix)
	prefix, _ := netip.ParsePrefix(o.Prefix)
	artifact := Artifact{Kind: "dns-arpa", Filename: "reverse-" + strings.TrimSuffix(zone, ".") + ".zone", Zone: zone, Complete: true, ValidationLevel: "native", Notices: []Notice{{Severity: "info", Code: "delegation-unverified", Message: "完整区域文件；named-checkzone 只能验证本地语法/记录，不证明上级委派、正向解析或服务已重载。"}}}
	type ptr struct {
		ip   netip.Addr
		name string
	}
	records := map[netip.Addr]string{}
	for _, t := range scopedTargets(in) {
		if o.ActiveOnly && !t.Active {
			continue
		}
		ip, _ := targetAddress(t)
		if !prefix.Contains(ip) {
			continue
		}
		name := t.DNSName
		if name == "" && o.NameSuffix != "" {
			label := t.Kind + "-" + shortID(t.ID)
			if t.Kind == "member" && t.ASN > 0 {
				label = fmt.Sprintf("as%d-%s", t.ASN, shortID(t.ID))
			}
			name = fmt.Sprintf("%s-v%d.%s", label, targetFamily(t), o.NameSuffix)
		}
		if name == "" {
			artifact.Notices = append(artifact.Notices, Notice{Severity: "warning", Code: "missing-dns-name", Message: "地址没有显式DNS主机名且未配置生成后缀，未生成PTR。", TargetID: t.ID})
			continue
		}
		canonical, err := SafeFQDN(name)
		if err != nil {
			return Artifact{}, err
		}
		if old, exists := records[ip]; exists && old != canonical {
			return Artifact{}, fmt.Errorf("conflicting PTR hostnames for an address in the same authoritative zone")
		}
		records[ip] = canonical
	}
	ordered := []ptr{}
	for ip, name := range records {
		ordered = append(ordered, ptr{ip, name})
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].ip.Compare(ordered[j].ip) < 0 })
	var b strings.Builder
	fmt.Fprintf(&b, "; Generated by MoeDove IXP Manager. Complete reverse zone; not activated.\n$ORIGIN %s\n$TTL %d\n@ IN SOA %s %s (\n  %d ; serial\n  %d ; refresh\n  %d ; retry\n  %d ; expire\n  %d ; minimum\n)\n", zone, o.TTL, o.PrimaryNS, o.ResponsibleMailbox, o.Serial, o.Refresh, o.Retry, o.Expire, o.Minimum)
	for _, ns := range o.NameServers {
		fmt.Fprintf(&b, "@ IN NS %s\n", ns)
	}
	b.WriteString("\n")
	for _, record := range ordered {
		fmt.Fprintf(&b, "%s IN PTR %s\n", reverseOwner(record.ip), record.name)
	}
	artifact.Content = b.String()
	artifact.TargetCount = len(ordered)
	return artifact, nil
}
func renderNagios(in Input) (Artifact, error) {
	o := in.Options.Nagios
	artifact := Artifact{Kind: "nagios", Filename: "moedove-nagios-" + shortID(in.Options.VLANID+":"+in.Options.InfrastructureID) + ".cfg", ValidationLevel: "structural", Complete: false, Notices: []Notice{{Severity: "warning", Code: "target-config-required", Message: "这是受限对象include片段；需在目标Nagios完整配置中定义模板和固定命令，再用nagios -v验证。未运行任何检查或重载服务。"}}}
	var b strings.Builder
	b.WriteString("# Generated by MoeDove IXP Manager. Include objects only; not a full Nagios configuration.\n# Required fixed commands: check_moedove_ping4, check_moedove_ping6, check_moedove_ping_busy4, check_moedove_ping_busy6, check_moedove_bgp, check_moedove_router, check_moedove_switch, check_moedove_core\n\n")
	targets := map[string]Target{}
	groups := map[string]map[string]bool{}
	groupAliases := map[string]string{}
	addGroup := func(key, label, host string) {
		if groups[key] == nil {
			groups[key] = map[string]bool{}
			groupAliases[key] = label
		}
		groups[key][host] = true
	}
	service := func(host, description, template, command string) {
		if template != o.ServiceTemplate {
			template += "," + o.ServiceTemplate
		}
		fmt.Fprintf(&b, "define service {\n    use %s\n    host_name %s\n    service_description %s\n    check_command %s\n}\n\n", template, host, description, command)
	}
	for _, t := range scopedTargets(in) {
		if !t.Active || !t.Monitor {
			continue
		}
		ip, _ := targetAddress(t)
		name, err := safeDisplay(t.Name)
		if err != nil {
			return Artifact{}, err
		}
		if name == "" {
			name = t.ID
		}
		host := hostID(t)
		template := o.MemberHostTemplate
		if t.Kind == "switch" {
			template = o.SwitchHostTemplate
		}
		if t.Kind == "router" {
			template = o.RouterHostTemplate
		}
		family := targetFamily(t)
		fmt.Fprintf(&b, "define host {\n    use %s\n    host_name %s\n    alias %s\n    address %s\n", template, host, name, ip)
		if t.CanPing {
			fmt.Fprintf(&b, "    check_command check_moedove_ping%d\n", family)
		} else {
			b.WriteString("    active_checks_enabled 0\n    check_command null\n")
		}
		b.WriteString("}\n\n")
		targets[t.ID] = t
		artifact.TargetCount++
		if t.CanPing {
			template := o.PingServiceTemplate
			cmd := fmt.Sprintf("check_moedove_ping%d", family)
			if t.Busy {
				template = o.BusyPingServiceTemplate
				cmd = fmt.Sprintf("check_moedove_ping_busy%d", family)
			}
			service(host, fmt.Sprintf("PING IPv%d", family), template, cmd)
		}
		addGroup("all/"+t.Kind+fmt.Sprint(family), fmt.Sprintf("All %s IPv%d targets", t.Kind, family), host)
		if t.VLANID != "" {
			addGroup("vlan/"+t.VLANID+fmt.Sprint(family), "VLAN "+t.VLANID+fmt.Sprintf(" IPv%d", family), host)
		}
		if t.InfrastructureID != "" {
			addGroup("fabric/"+t.InfrastructureID, "Infrastructure "+t.InfrastructureID, host)
		}
		for _, id := range t.SwitchIDs {
			addGroup("switch/"+id+fmt.Sprint(family), "Members on switch "+id+fmt.Sprintf(" IPv%d", family), host)
		}
		for _, id := range t.RackIDs {
			addGroup("rack/"+id+fmt.Sprint(family), "Rack "+id+fmt.Sprintf(" IPv%d", family), host)
		}
		for _, id := range t.LocationIDs {
			addGroup("location/"+id+fmt.Sprint(family), "Facility "+id+fmt.Sprintf(" IPv%d", family), host)
		}
		if t.Kind == "switch" {
			if t.SwitchID == "" {
				return Artifact{}, fmt.Errorf("switch target %s requires its actual switch ID", t.ID)
			}
			service(host, "Observed switch health", o.SwitchServiceTemplate, "check_moedove_switch!"+t.SwitchID)
			if t.VendorID != "" {
				addGroup("vendor/"+t.VendorID, "Vendor "+t.VendorID, host)
			}
			if t.Model != "" {
				model, _ := safeDisplay(t.Model)
				addGroup("model/"+t.VendorID+"/"+t.Model, "Model "+model, host)
			}
		}
		if t.Kind == "router" {
			if t.RouterID == "" {
				return Artifact{}, fmt.Errorf("router target %s requires its actual router ID", t.ID)
			}
			service(host, "Router telemetry freshness", o.RouterServiceTemplate, "check_moedove_router!"+t.RouterID)
		}
	}
	sessions := append([]Session(nil), in.Sessions...)
	sort.Slice(sessions, func(i, j int) bool { return sessions[i].ID < sessions[j].ID })
	for _, s := range sessions {
		if !s.Enabled || !s.Monitor {
			continue
		}
		target, ok := targets[s.TargetID]
		if !ok {
			artifact.Notices = append(artifact.Notices, Notice{Severity: "warning", Code: "bgp-target-unavailable", Message: "BGP检查没有已选中、活动且允许监测的成员地址目标，已跳过。", TargetID: s.ID})
			continue
		}
		switch s.RouterRole {
		case "", "route-server", "route-collector", "as112":
		default:
			artifact.Notices = append(artifact.Notices, Notice{Severity: "warning", Code: "router-role-not-monitored", Message: "不支持的路由器监测角色，已跳过。", TargetID: s.ID})
			continue
		}
		service(hostID(target), "BGP "+shortID(s.ID), o.BGPServiceTemplate, "check_moedove_bgp!"+s.ID)
		addGroup("router/"+s.RouterID, "BGP sessions on router "+s.RouterID, hostID(target))
	}
	bundles := append([]CoreBundle(nil), in.CoreBundles...)
	sort.Slice(bundles, func(i, j int) bool { return bundles[i].ID < bundles[j].ID })
	for _, bundle := range bundles {
		if !bundle.Enabled {
			continue
		}
		target, ok := targets[bundle.TargetID]
		if !ok {
			continue
		}
		service(hostID(target), "Core bundle "+shortID(bundle.ID), o.CoreServiceTemplate, "check_moedove_core!"+bundle.ID)
	}
	if len(bundles) == 0 {
		artifact.Notices = append(artifact.Notices, Notice{Severity: "info", Code: "core-bundle-observation-unavailable", Message: "未提供真实core-bundle对象；不会把desired core-link当成健康bundle。"})
	} else {
		artifact.Notices = append(artifact.Notices, Notice{Severity: "info", Code: "core-check-physical-only", Message: "核心检查核对两端新鲜且身份匹配的物理端口状态；不代表 LACP、BFD、BGP 或转发正常。令牌须绑定 bundle 的两侧交换机。"})
	}
	keys := []string{}
	for key := range groups {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		hosts := []string{}
		for host := range groups[key] {
			hosts = append(hosts, host)
		}
		sort.Strings(hosts)
		alias, _ := safeDisplay(groupAliases[key])
		fmt.Fprintf(&b, "define hostgroup {\n    hostgroup_name md_group_%s\n    alias %s\n    members %s\n}\n\n", shortID(key), alias, strings.Join(hosts, ","))
	}
	artifact.Content = b.String()
	return artifact, nil
}
func renderSmokeping(in Input) (Artifact, error) {
	o := in.Options.Smokeping
	artifact := Artifact{Kind: "smokeping", Filename: "moedove-smokeping-" + shortID(in.Options.VLANID+":"+in.Options.InfrastructureID) + ".conf", ValidationLevel: "structural", Complete: false, Notices: []Notice{{Severity: "warning", Code: "target-config-required", Message: "这是Targets子树include片段，必须放入目标Smokeping完整配置并定义probe；需smokeping --check --config=完整配置。未启动探测或重载。"}}}
	var b strings.Builder
	b.WriteString("# Generated by MoeDove IXP Manager. Targets subtree only; no executable paths or probe definitions.\n")
	group := strings.Repeat("+", o.Level)
	section := "md_" + shortID(in.Options.VLANID+":"+in.Options.InfrastructureID)
	fmt.Fprintf(&b, "%s %s\nmenu = MoeDove IXP\ntitle = MoeDove IXP observed latency targets\n\n", group, section)
	for _, t := range scopedTargets(in) {
		if !t.Active || !t.Monitor || !t.CanPing {
			continue
		}
		name, err := safeDisplay(t.Name)
		if err != nil {
			return Artifact{}, err
		}
		if name == "" {
			name = t.ID
		}
		ip, _ := targetAddress(t)
		family := targetFamily(t)
		probe := o.Probe4
		if family == 6 {
			probe = o.Probe6
		}
		fmt.Fprintf(&b, "%s %s\nmenu = %s (IPv%d)\ntitle = %s via %s\nprobe = %s\nhost = %s\n\n", group+"+", hostID(t), name, family, name, ip, probe, ip)
		artifact.TargetCount++
	}
	artifact.Content = b.String()
	return artifact, nil
}
