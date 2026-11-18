package provision

import (
	"errors"
	"fmt"
	"net/netip"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/moedove/ixp-manager/internal/as112"
	"github.com/moedove/ixp-manager/internal/core"
)

var safeIdentifier = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_-]{0,127}$`)
var safeInventoryID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
var safeDNSLabel = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)

func SafeFQDN(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.TrimSuffix(value, ".")
	if value == "" || len(value) > 253 {
		return "", errors.New("DNS name must contain 1..253 ASCII characters")
	}
	if _, err := netip.ParseAddr(value); err == nil {
		return "", errors.New("DNS name must be a hostname, not an IP literal")
	}
	for _, label := range strings.Split(value, ".") {
		if !safeDNSLabel.MatchString(label) {
			return "", errors.New("DNS labels must be ASCII letters/digits/hyphens; use punycode for international names")
		}
	}
	return value + ".", nil
}
func ReverseZone(raw string) (string, error) {
	prefix, err := netip.ParsePrefix(raw)
	if err != nil || prefix != prefix.Masked() || prefix.Addr().Is4In6() {
		return "", errors.New("reverse DNS prefix must be canonical CIDR")
	}
	if prefix.Addr().Is4() {
		if prefix.Bits() < 8 || prefix.Bits()%8 != 0 {
			return "", errors.New("IPv4 authoritative zone requires an octet boundary (/8,/16,/24,/32); classless networks require explicit RFC 2317 parent delegation or separately delegated /24 zones")
		}
		bytes := prefix.Addr().As4()
		parts := []string{}
		for i := prefix.Bits()/8 - 1; i >= 0; i-- {
			parts = append(parts, fmt.Sprint(bytes[i]))
		}
		return strings.Join(parts, ".") + ".in-addr.arpa.", nil
	}
	if prefix.Bits() < 4 || prefix.Bits()%4 != 0 {
		return "", errors.New("IPv6 authoritative zone requires a nibble-aligned prefix; request correct delegation rather than inventing a partial ip6.arpa zone")
	}
	bytes := prefix.Addr().As16()
	digits := fmt.Sprintf("%032x", bytes)
	parts := []string{}
	for i := prefix.Bits()/4 - 1; i >= 0; i-- {
		parts = append(parts, string(digits[i]))
	}
	return strings.Join(parts, ".") + ".ip6.arpa.", nil
}
func NormalizeOptions(o Options) (Options, error) {
	o.Families = append([]int(nil), o.Families...)
	switch o.Kind {
	case "dns-arpa", "nagios", "smokeping", "switch-core", "as112-dns":
	default:
		return o, errors.New("kind must be dns-arpa, nagios, smokeping or switch-core")
	}
	if o.Kind == "as112-dns" {
		if !safeInventoryID.MatchString(o.RouterID) || o.AS112 == nil || o.SwitchID != "" || o.VLANID != "" || o.InfrastructureID != "" || !reflect.DeepEqual(o.DNS, DNSOptions{}) || o.Nagios != (NagiosOptions{}) || o.Smokeping != (SmokepingOptions{}) {
			return o, errors.New("as112-dns requires one routerId and its DNS identity without unrelated scope/template options")
		}
		identity, err := as112.NormalizeIdentity(*o.AS112)
		if err != nil {
			return o, err
		}
		o.AS112 = &identity
	} else if o.RouterID != "" || o.AS112 != nil {
		return o, errors.New("AS112 options require kind as112-dns")
	}
	for _, id := range []string{o.VLANID, o.InfrastructureID} {
		if id != "" && !safeInventoryID.MatchString(id) {
			return o, errors.New("invalid scope identifier")
		}
	}
	if len(o.Families) == 0 {
		o.Families = []int{4, 6}
	}
	seen := map[int]bool{}
	for _, family := range o.Families {
		if (family != 4 && family != 6) || seen[family] {
			return o, errors.New("families must contain distinct 4 and/or 6")
		}
		seen[family] = true
	}
	sort.Ints(o.Families)
	if o.Kind == "as112-dns" && len(o.Families) != 2 {
		return o, errors.New("AS112 address families are selected on its bound router, not independently on the DNS profile")
	}
	if o.Kind == "switch-core" {
		if !safeInventoryID.MatchString(o.SwitchID) || o.VLANID != "" || o.InfrastructureID != "" || len(o.Families) != 2 {
			return o, errors.New("switch-core requires one switchId and its complete dual-stack plan, without VLAN or fabric filters")
		}
		if !reflect.DeepEqual(o.DNS, DNSOptions{}) || o.Nagios != (NagiosOptions{}) || o.Smokeping != (SmokepingOptions{}) {
			return o, errors.New("switch-core cannot contain DNS or monitoring template options")
		}
	} else if o.SwitchID != "" {
		return o, errors.New("switchId is only used by switch-core")
	}
	if o.Kind == "dns-arpa" {
		if _, err := ReverseZone(o.DNS.Prefix); err != nil {
			return o, err
		}
		if o.DNS.Serial == 0 {
			return o, errors.New("a frozen nonzero SOA serial is required")
		}
		var err error
		o.DNS.PrimaryNS, err = SafeFQDN(o.DNS.PrimaryNS)
		if err != nil {
			return o, fmt.Errorf("primaryNS: %w", err)
		}
		o.DNS.ResponsibleMailbox, err = SafeFQDN(o.DNS.ResponsibleMailbox)
		if err != nil {
			return o, fmt.Errorf("responsibleMailbox must be DNS RNAME (hostmaster.example.net.): %w", err)
		}
		if len(o.DNS.NameServers) == 0 || len(o.DNS.NameServers) > 8 {
			return o, errors.New("one to eight authoritative nameservers are required")
		}
		names := []string{}
		for _, raw := range o.DNS.NameServers {
			canonical, err := SafeFQDN(raw)
			if err != nil {
				return o, fmt.Errorf("nameServers: %w", err)
			}
			duplicate := false
			for _, v := range names {
				if v == canonical {
					duplicate = true
				}
			}
			if !duplicate {
				names = append(names, canonical)
			}
		}
		sort.Strings(names)
		o.DNS.NameServers = names
		zone, _ := ReverseZone(o.DNS.Prefix)
		for _, ns := range append([]string{o.DNS.PrimaryNS}, names...) {
			if ns == zone || strings.HasSuffix(ns, "."+zone) {
				return o, errors.New("in-zone nameservers require glue address records; use out-of-zone nameservers for this complete reverse-zone renderer")
			}
		}
		prefix, _ := netip.ParsePrefix(o.DNS.Prefix)
		family := 6
		if prefix.Addr().Is4() {
			family = 4
		}
		if !seen[family] {
			return o, errors.New("DNS prefix family is excluded by families selection")
		}
		if o.DNS.NameSuffix != "" {
			o.DNS.NameSuffix, err = SafeFQDN(o.DNS.NameSuffix)
			if err != nil {
				return o, fmt.Errorf("nameSuffix: %w", err)
			}
		}
		values := []struct {
			target   *uint32
			fallback uint32
			maximum  uint32
		}{{&o.DNS.TTL, 86400, 604800}, {&o.DNS.Refresh, 43200, 604800}, {&o.DNS.Retry, 7200, 604800}, {&o.DNS.Expire, 1209600, 31536000}, {&o.DNS.Minimum, 7200, 604800}}
		for _, v := range values {
			if *v.target == 0 {
				*v.target = v.fallback
			}
			if *v.target > v.maximum {
				return o, errors.New("DNS TTL or timer exceeds supported bound")
			}
		}
		if o.DNS.Expire <= o.DNS.Refresh || o.DNS.Expire <= o.DNS.Retry {
			return o, errors.New("SOA expire must exceed refresh and retry")
		}
	}
	if o.Kind == "nagios" {
		values := []struct {
			target   *string
			fallback string
		}{{&o.Nagios.MemberHostTemplate, "ixp-manager-member-host"}, {&o.Nagios.SwitchHostTemplate, "ixp-manager-production-switch"}, {&o.Nagios.RouterHostTemplate, "ixp-manager-host-birdseye-daemon"}, {&o.Nagios.ServiceTemplate, "ixp-manager-member-service"}, {&o.Nagios.PingServiceTemplate, "ixp-manager-member-ping-service"}, {&o.Nagios.BusyPingServiceTemplate, "ixp-manager-member-ping-busy-service"}, {&o.Nagios.BGPServiceTemplate, "ixp-manager-member-bgp-session-service"}, {&o.Nagios.RouterServiceTemplate, "ixp-manager-service-birdseye-daemon"}, {&o.Nagios.SwitchServiceTemplate, "ixp-manager-switch-service"}, {&o.Nagios.CoreServiceTemplate, "ixp-manager-core-bundle-service"}}
		for _, v := range values {
			if *v.target == "" {
				*v.target = v.fallback
			}
			if !safeIdentifier.MatchString(*v.target) {
				return o, errors.New("Nagios template must be a safe identifier, not a command, argument or path")
			}
		}
	}
	if o.Kind == "smokeping" {
		if o.Smokeping.Level == 0 {
			o.Smokeping.Level = 1
		}
		if o.Smokeping.Level < 1 || o.Smokeping.Level > 4 {
			return o, errors.New("Smokeping level must be 1..4")
		}
		if o.Smokeping.Probe4 == "" {
			o.Smokeping.Probe4 = "FPing"
		}
		if o.Smokeping.Probe6 == "" {
			o.Smokeping.Probe6 = "FPing6"
		}
		if !safeIdentifier.MatchString(o.Smokeping.Probe4) || !safeIdentifier.MatchString(o.Smokeping.Probe6) {
			return o, errors.New("Smokeping probe must be a safe identifier")
		}
	}
	return o, nil
}
func safeDisplay(raw string) (string, error) {
	if len(raw) > 2000 {
		return "", errors.New("display label exceeds 2000 bytes")
	}
	var b strings.Builder
	replacements := map[rune]rune{'#': '＃', ';': '；', '\\': '＼', '$': '＄', '`': '｀', '{': '｛', '}': '｝', '|': '｜', '&': '＆', '<': '＜', '>': '＞', '"': '＂', '\'': '＇', '=': '＝', '@': '＠'}
	for _, r := range raw {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == '\u2028' || r == '\u2029' {
			return "", errors.New("display label contains control characters or newlines")
		}
		if replacement, ok := replacements[r]; ok {
			r = replacement
		}
		b.WriteRune(r)
	}
	return b.String(), nil
}
func targetAddress(t Target) (netip.Addr, error) {
	ip, err := netip.ParseAddr(t.Address)
	if err != nil || ip.Zone() != "" || ip.Is4In6() || ip.IsUnspecified() || ip.IsMulticast() {
		return netip.Addr{}, errors.New("target address must be a unicast IP literal without a zone")
	}
	return ip, nil
}
func ValidateInput(in Input) error {
	if _, err := NormalizeOptions(in.Options); err != nil {
		return err
	}
	if in.Options.Kind == "as112-dns" {
		if in.AS112Service == nil || in.CoreConfig != nil || len(in.Targets)+len(in.Sessions)+len(in.CoreBundles) != 0 {
			return errors.New("AS112 DNS input requires only its bound service")
		}
		if in.AS112RouterCreatedAt != "" {
			if _, err := time.Parse(time.RFC3339Nano, in.AS112RouterCreatedAt); err != nil {
				return errors.New("invalid AS112 router entity binding")
			}
		}
		_, err := as112.RenderNode(in.Options.RouterID, in.AS112RouterCreatedAt, *in.AS112Service, *in.Options.AS112, in.AS112NodeAddresses)
		return err
	}
	if in.AS112Service != nil || in.AS112RouterCreatedAt != "" || len(in.AS112NodeAddresses) > 0 {
		return errors.New("AS112 service input requires as112-dns")
	}
	if in.Options.Kind == "switch-core" {
		if in.CoreConfig == nil || in.CoreConfig.SwitchID != in.Options.SwitchID || len(in.Targets)+len(in.Sessions)+len(in.CoreBundles) != 0 {
			return errors.New("switch-core requires exactly its selected typed plan")
		}
		return core.ValidateSwitchPlan(*in.CoreConfig)
	}
	if in.CoreConfig != nil {
		return errors.New("coreConfig is only used by switch-core")
	}
	if len(in.Targets) > 10000 || len(in.Sessions) > 10000 || len(in.CoreBundles) > 10000 {
		return errors.New("provisioning input exceeds 10000 objects per type")
	}
	ids := map[string]bool{}
	for _, t := range in.Targets {
		if !safeInventoryID.MatchString(t.ID) || ids[t.ID] {
			return errors.New("target IDs must be safe and unique")
		}
		ids[t.ID] = true
		switch t.Kind {
		case "member", "switch", "router":
		default:
			return errors.New("target kind must be member, switch or router")
		}
		if _, err := targetAddress(t); err != nil {
			return fmt.Errorf("target %s: %w", t.ID, err)
		}
		for _, name := range []string{t.Name, t.VLANName, t.Model} {
			if _, err := safeDisplay(name); err != nil {
				return fmt.Errorf("target %s: %w", t.ID, err)
			}
		}
		if t.DNSName != "" {
			if _, err := SafeFQDN(t.DNSName); err != nil {
				return fmt.Errorf("target %s DNS name: %w", t.ID, err)
			}
		}
		groups := append([]string{t.MemberID, t.VLANID, t.InfrastructureID, t.SwitchID, t.RouterID, t.VendorID}, t.SwitchIDs...)
		groups = append(groups, t.RackIDs...)
		groups = append(groups, t.LocationIDs...)
		for _, id := range groups {
			if id != "" && !safeInventoryID.MatchString(id) {
				return errors.New("invalid target association identifier")
			}
		}
	}
	sessionIDs := map[string]bool{}
	for _, s := range in.Sessions {
		if !safeInventoryID.MatchString(s.ID) || sessionIDs[s.ID] || !safeInventoryID.MatchString(s.RouterID) {
			return errors.New("invalid or duplicate session identifier")
		}
		sessionIDs[s.ID] = true
		if s.TargetID != "" && !ids[s.TargetID] {
			return errors.New("session target reference does not exist")
		}
	}
	bundleIDs := map[string]bool{}
	for _, b := range in.CoreBundles {
		if !safeInventoryID.MatchString(b.ID) || bundleIDs[b.ID] || !ids[b.TargetID] {
			return errors.New("invalid core bundle reference")
		}
		bundleIDs[b.ID] = true
	}
	return nil
}
