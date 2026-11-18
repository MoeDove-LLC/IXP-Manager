package routeros

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"strconv"

	"github.com/moedove/ixp-manager/internal/routing"
)

func (a Attribute) displayValue() string {
	if a.Sensitive || a.Name == "tcp-md5-key" {
		return routing.RedactedBGPPassword
	}
	return a.Value
}
func (a Attribute) String() string   { return a.Name + "=" + a.displayValue() }
func (a Attribute) GoString() string { return a.String() }
func (a Attribute) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Name      string `json:"name"`
		Value     string `json:"value"`
		Sensitive bool   `json:"sensitive"`
	}{a.Name, a.displayValue(), a.Sensitive || a.Name == "tcp-md5-key"})
}
func (c Credential) String() string {
	return "Credential{revision=" + c.Revision + ",password=" + routing.RedactedBGPPassword + "}"
}
func (c Credential) GoString() string { return c.String() }

func attr(name, value string) Attribute {
	return Attribute{Name: name, Value: value, Sensitive: name == "tcp-md5-key"}
}
func number(n uint32) string { return strconv.FormatUint(uint64(n), 10) }

// Compile translates a canonical plan to fixed API resources/actions/attributes.
// It performs NO network I/O, owns NO Safe Mode session, and grants NO permission
// to activate. An executor must independently verify the exact-version semantics,
// both channels' device identity, the fresh base, ownership and every returned ID.
// Never serialize Commands and then treat that redacted serialization as input.
func Compile(p Plan, credentials Credentials) (CompiledPlan, error) {
	if err := ValidatePlan(p); err != nil {
		return CompiledPlan{}, err
	}
	out := CompiledPlan{PlanChecksum: p.Checksum, Commands: []CompiledCommand{}}
	peers := map[string]Peer{}
	for _, v := range p.Desired.Peers {
		peers[v.ID] = v
	}
	for _, op := range p.Operations {
		cmd := CompiledCommand{OperationID: op.ID, Phase: op.Phase, Object: op.Object, Attributes: []Attribute{}}
		switch op.Kind {
		case AddTable:
			cmd.Resource, cmd.Action = RoutingTable, Add
			// On the tested RouterOS API, fib is a presence switch: fib=no
			// enables it too. Omit it and require explicit non-FIB read-back.
			cmd.Attributes = []Attribute{attr("name", p.Owner)}
		case AddInstance:
			cmd.Resource, cmd.Action = BGPInstance, Add
			cmd.Attributes = []Attribute{attr("name", p.Owner), attr("as", number(p.Desired.ASN)), attr("router-id", p.Desired.RouterID), attr("routing-table", p.Owner)}
		case SetInstance:
			cmd.Resource, cmd.Action = BGPInstance, Set
			cmd.Attributes = []Attribute{attr("as", number(p.Desired.ASN)), attr("router-id", p.Desired.RouterID)}
		case AddRule, SetEntry:
			cmd.Resource = FilterRule
			cmd.Action = Set
			if op.Kind == AddRule {
				cmd.Action = Add
				cmd.Attributes = append(cmd.Attributes, attr("chain", op.Rule.Chain), attr("comment", op.Rule.Comment))
			}
			value, err := compileRule(*op.Rule, peers)
			if err != nil {
				return CompiledPlan{}, err
			}
			cmd.Attributes = append(cmd.Attributes, attr("rule", value))
		case AddDisabledConnection, SetConnection:
			cmd.Resource = BGPConnection
			cmd.Action = Set
			if op.Kind == AddDisabledConnection {
				cmd.Action = Add
			}
			cmd.Attributes = connectionAttributes(*op.Connection)
		case SetAuthentication:
			cmd.Resource, cmd.Action = BGPConnection, Set
			auth := op.Connection.Authentication
			password := ""
			if auth.Enabled {
				c, ok := credentials[op.PeerID]
				if !ok || c.Revision != auth.Revision {
					return CompiledPlan{}, errors.New("required credential is missing or its revision changed")
				}
				if err := routing.ValidateBGPPassword(c.Password); err != nil || c.Password == "" {
					return CompiledPlan{}, errors.New("required credential is invalid")
				}
				password = c.Password
			}
			cmd.Attributes = []Attribute{attr("tcp-md5-key", password)}
		case UnsetUnusedFamilyLimit:
			cmd.Resource, cmd.Action = BGPConnection, Unset
			cmd.Property = "input.limit-process-routes-ipv6"
			if op.Connection.Family == "ipv6" {
				cmd.Property = "input.limit-process-routes-ipv4"
			}
		case EnableConnection:
			cmd.Resource, cmd.Action = BGPConnection, Enable
		case DisableConnection:
			cmd.Resource, cmd.Action = BGPConnection, Disable
		case RemoveConnection:
			cmd.Resource, cmd.Action = BGPConnection, Remove
		case RemoveRule:
			cmd.Resource, cmd.Action = FilterRule, Remove
		default:
			return CompiledPlan{}, errors.New("unsupported typed operation")
		}
		out.Commands = append(out.Commands, cmd)
	}
	return out, nil
}

func connectionAttributes(c Connection) []Attribute {
	limit := "input.limit-process-routes-ipv4"
	if c.Family == "ipv6" {
		limit = "input.limit-process-routes-ipv6"
	}
	// The unused AF has its own typed unset operation. Empty Set values are
	// rejected by the tested target and must not be used as a reset surrogate.
	return []Attribute{
		attr("name", c.Name), attr("instance", c.Instance), attr("as", number(c.LocalASN)), attr("routing-table", c.Instance), attr("templates", ""),
		attr("disabled", "yes"), attr("afi", c.Family), attr("local.address", c.LocalAddress),
		attr("connect", "yes"), attr("listen", "yes"),
		attr("local.role", "ebgp-rs"), attr("remote.address", c.RemoteAddress), attr("remote.as", number(c.RemoteASN)),
		attr("nexthop-choice", "propagate"), attr("input.filter", c.InputChain),
		attr(limit, strconv.Itoa(c.MaxPrefixes)),
		attr("output.filter-chain", c.OutputChain), attr("output.default-prepend", "0"), attr("output.default-originate", "never"),
	}
}

func compileRule(r Rule, peers map[string]Peer) (string, error) {
	switch r.Kind {
	case RejectRule:
		return "reject;", nil
	case JumpRule:
		return "jump " + r.JumpTarget + ";", nil
	case RecipientRule:
		p, ok := peers[r.PeerID]
		if !ok {
			return "", errors.New("rule has no typed peer")
		}
		return fmt.Sprintf("if (bgp-output-remote-as == %d && bgp-output-remote-addr == %s) { jump %s; }", p.ASN, p.Address, r.JumpTarget), nil
	case ImportRule, ExportRule:
		p, ok := peers[r.PeerID]
		if !ok {
			return "", errors.New("rule has no typed peer")
		}
		prefix, err := netip.ParsePrefix(r.Prefix)
		if err != nil || prefix.String() != r.Prefix {
			return "", errors.New("rule prefix is not canonical")
		}
		origin := ""
		if r.OriginASN != 0 {
			origin = fmt.Sprintf(" && bgp-as-path %d$", r.OriginASN)
		}
		recipient := ""
		if r.Kind == ExportRule {
			recipient = " && bgp-input-remote-as != bgp-output-remote-as"
		}
		// Both input and output re-check the source tuple and exact prefix/origin.
		// An old accepted route therefore cannot escape a newly narrowed policy
		// merely because it remains in the table awaiting input re-evaluation.
		return fmt.Sprintf("if (protocol bgp && bgp-input-remote-as == %d && bgp-input-remote-addr == %s && dst == %s && bgp-path-len > 0 && bgp-path-len <= 64 && bgp-as-path ^%d%s%s) { accept; }", p.ASN, p.Address, r.Prefix, p.ASN, origin, recipient), nil
	default:
		return "", errors.New("unsupported typed filter rule")
	}
}
