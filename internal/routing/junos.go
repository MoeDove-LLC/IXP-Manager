package routing

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"sort"
	"strings"
)

const netconfNS = "urn:ietf:params:xml:ns:netconf:base:1.0"

// renderJunos emits a managed configuration group, replaced as a unit so a
// removed peer cannot survive a later merge. No user names become XML names.
func renderJunos(router Router, peers []Peer) (string, error) {
	if _, err := Render(router, peers); err != nil {
		return "", err
	}
	var b bytes.Buffer
	e := xml.NewEncoder(&b)
	e.Indent("", "  ")
	start := func(name string, attrs ...xml.Attr) {
		_ = e.EncodeToken(xml.StartElement{Name: xml.Name{Local: name}, Attr: attrs})
	}
	end := func(name string) { _ = e.EncodeToken(xml.EndElement{Name: xml.Name{Local: name}}) }
	elem := func(name, value string) { _ = e.EncodeElement(value, xml.StartElement{Name: xml.Name{Local: name}}) }
	empty := func(name string) { start(name); end(name) }
	key := router.ID
	if key == "" {
		key = router.RouterID
	}
	group := "moedove_" + Checksum(key)[:16]
	ordered := append([]Peer(nil), peers...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].ID < ordered[j].ID })
	active := []Peer{}
	for _, p := range ordered {
		if p.Enabled {
			active = append(active, p)
		}
	}
	name := func(p Peer) string { return "md_" + Checksum(p.ID)[:16] }
	start("configuration", xml.Attr{Name: xml.Name{Local: "xmlns"}, Value: ""})
	start("groups", xml.Attr{Name: xml.Name{Space: netconfNS, Local: "operation"}, Value: "replace"})
	elem("name", group)
	start("policy-options")
	pathName := func(group originGroup) string {
		if group.ASN == 0 {
			return group.Name + "_first_as"
		}
		return group.Name + "_origin"
	}
	term := func(group originGroup) {
		start("term")
		elem("name", group.Name)
		start("from")
		elem("protocol", "bgp")
		elem("as-path", pathName(group))
		for _, prefix := range group.Prefixes {
			start("route-filter")
			elem("address", prefix)
			empty("exact")
			end("route-filter")
		}
		end("from")
		start("then")
		empty("accept")
		end("then")
		end("term")
	}
	deny := func() {
		start("term")
		elem("name", "default_deny")
		start("then")
		empty("reject")
		end("then")
		end("term")
	}
	// Export matches explicit prefix/path pairs. Junos ignores neighbor matches
	// on BGP export, so it must not be used to establish source authorization.
	start("policy-statement")
	elem("name", group+"_export")
	for _, peer := range active {
		for _, origins := range originGroups(peer) {
			term(origins)
		}
	}
	deny()
	end("policy-statement")
	for _, peer := range active {
		for _, origins := range originGroups(peer) {
			start("as-path")
			elem("name", pathName(origins))
			elem("path", junosOriginPattern(peer.ASN, origins.ASN))
			end("as-path")
		}
		start("policy-statement")
		elem("name", name(peer)+"_import")
		for _, origins := range originGroups(peer) {
			term(origins)
		}
		deny()
		end("policy-statement")
	}
	end("policy-options")
	start("protocols")
	start("bgp")
	for _, p := range active {
		n := name(p)
		ip, _ := netip.ParseAddr(p.Address)
		family := "inet6"
		local := router.LocalIPv6
		if ip.Is4() {
			family = "inet"
			local = router.LocalIPv4
		}
		source, _ := netip.ParseAddr(local)
		start("group")
		if comment := authenticationComment(p, true); comment != "" {
			_ = e.EncodeToken(xml.Comment([]byte(" " + comment + " ")))
		}
		elem("name", n)
		elem("type", "external")
		empty("route-server-client")
		elem("local-address", source.String())
		if p.Password != "" {
			elem("authentication-key", p.Password)
		}
		start("family")
		start(family)
		start("unicast")
		start("accepted-prefix-limit")
		elem("maximum", fmt.Sprint(p.MaxPrefixes))
		start("teardown")
		start("idle-timeout")
		empty("forever")
		end("idle-timeout")
		end("teardown")
		end("accepted-prefix-limit")
		end("unicast")
		end(family)
		end("family")
		elem("import", n+"_import")
		elem("export", group+"_export")
		elem("peer-as", fmt.Sprint(p.ASN))
		start("neighbor")
		elem("name", ip.String())
		end("neighbor")
		end("group")
	}
	end("bgp")
	end("protocols")
	end("groups")
	elem("apply-groups", group)
	start("routing-options")
	rid, _ := netip.ParseAddr(router.RouterID)
	elem("router-id", rid.String())
	start("autonomous-system")
	elem("as-number", fmt.Sprint(router.ASN))
	end("autonomous-system")
	end("routing-options")
	end("configuration")
	if err := e.Flush(); err != nil {
		return "", err
	}
	config := b.String() + "\n"
	if err := ValidateJunosXML(config); err != nil {
		return "", err
	}
	return config, nil
}

// ValidateJunosXML is structural validation, not a native Junos commit check.
// Platform/model support must be checked on the device via NETCONF validate.
func ValidateJunosXML(config string) error {
	if isAS112Artifact("junos", config) {
		if _, err := parseAS112Artifact("junos", config); err != nil {
			return err
		}
	}
	if err := rejectRedactedArtifact(config); err != nil {
		return err
	}
	if len(config) == 0 || len(config) > 16<<20 {
		return errors.New("Junos XML must be 1 byte..16 MiB")
	}
	d := xml.NewDecoder(strings.NewReader(config))
	depth := 0
	roots := 0
	group := false
	apply := false
	options := false
	for {
		tok, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return errors.New("invalid Junos XML structure; parser details withheld")
		}
		switch v := tok.(type) {
		case xml.Directive, xml.ProcInst:
			return errors.New("Junos XML directives and processing instructions are forbidden")
		case xml.StartElement:
			if depth == 0 {
				roots++
				if v.Name.Local != "configuration" || v.Name.Space != "" {
					return errors.New("expected a single unnamespaced configuration root")
				}
			}
			if depth == 1 {
				switch v.Name.Local {
				case "groups":
					group = true
					replace := false
					for _, a := range v.Attr {
						if a.Name.Local == "operation" && a.Name.Space == netconfNS && a.Value == "replace" {
							replace = true
						}
					}
					if !replace {
						return errors.New("managed group requires NETCONF replace operation")
					}
				case "apply-groups":
					apply = true
				case "routing-options":
					options = true
				default:
					return errors.New("unsupported top-level Junos configuration subtree")
				}
			}
			depth++
		case xml.EndElement:
			depth--
		case xml.CharData:
			if depth == 0 && strings.TrimSpace(string(v)) != "" {
				return errors.New("unexpected text outside configuration")
			}
		}
	}
	if roots != 1 || depth != 0 || !group || !apply || !options {
		return errors.New("Junos artifact requires one configuration with managed groups, apply-groups and routing-options")
	}
	return nil
}
