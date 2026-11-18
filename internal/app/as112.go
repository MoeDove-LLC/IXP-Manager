package app

import (
	"errors"
	"net/http"
	"net/netip"

	"github.com/moedove/ixp-manager/internal/provision"
	"github.com/moedove/ixp-manager/internal/routing"
)

func as112ProvisionInput(s *State, options provision.Options) (provision.Input, error) {
	input := provision.Input{}
	normalized, err := provision.NormalizeOptions(options)
	if err != nil {
		return input, err
	}
	if s == nil {
		return input, errors.New("AS112 inventory is unavailable")
	}
	router := find(s, "routers", normalized.RouterID)
	if router == nil || routerRole(router) != routing.RoleAS112 || !validNoteEntity(noteEntity(router)) {
		return input, errors.New("AS112 DNS profile requires its current AS112 router entity")
	}
	if err = validateRouterRole(router); err != nil {
		return input, err
	}
	ip, _ := netip.ParseAddr(normalized.AS112.UniqueIP)
	sameLocal := false
	addresses := []string{str(router, "localIpv4"), str(router, "localIpv6")}
	if routerPlatform(router) == "junos" {
		addresses = []string{str(router, "as112NextHop4"), str(router, "as112NextHop6")}
	}
	v4, _ := router["as112IPv4"].(bool)
	v6, _ := router["as112IPv6"].(bool)
	nodes := []string{}
	for i, value := range addresses {
		if i == 0 && v4 || i == 1 && v6 {
			nodes = append(nodes, value)
		}
	}
	for _, value := range nodes {
		local, e := netip.ParseAddr(value)
		sameLocal = sameLocal || e == nil && local == ip
	}
	if !sameLocal {
		return input, errors.New("AS112 uniqueIP must match the configured local node address or Junos DNS backend next hop")
	}
	service := routing.AS112Service(routing.Router{AS112Service: str(router, "as112Service"), AS112IPv4: v4, AS112IPv6: v6})
	input.Options = normalized
	input.AS112Service = &service
	input.AS112RouterCreatedAt = str(router, "createdAt")
	input.AS112NodeAddresses = nodes
	return input, provision.ValidateInput(input)
}

func (a *App) currentProvisionDownload(r *http.Request, user Record, session Session, opened ProvisionRevision) (ProvisionRevision, string, int, error) {
	a.store.mu.RLock()
	defer a.store.mu.RUnlock()
	s := &a.store.state
	if _, err := a.atlasActor(s, user, session, "admin", "operator"); err != nil || a.store.closed || r.Context().Err() != nil {
		return ProvisionRevision{}, "", 401, errors.New("provisioning authority changed; sign in again")
	}
	var current *ProvisionRevision
	for i := range s.ProvisionRevisions {
		if s.ProvisionRevisions[i].ID == opened.ID {
			current = &s.ProvisionRevisions[i]
			break
		}
	}
	if current == nil || current.Status != "approved" || current.Pruned || current.Checksum != opened.Checksum || current.RenderChecksum != opened.RenderChecksum {
		return ProvisionRevision{}, "", 409, errors.New("provisioning revision changed; refresh before downloading")
	}
	if !provisionTokenAllows(session, "provisioning:pull", current.ProfileID) || (current.Kind == provision.RFC2317MergeKind && session.API) {
		return ProvisionRevision{}, "", 403, errors.New("profile-bound provisioning pull access required")
	}
	if session.API {
		valid := false
		for _, token := range s.Security[session.UserID].Tokens {
			valid = valid || token.ID == session.TokenID && contains(token.Scopes, "provisioning:pull") && contains(token.ProfileIDs, current.ProfileID)
		}
		if !valid {
			return ProvisionRevision{}, "", 403, errors.New("current token profile binding changed")
		}
	}
	if !provisionSerialApproved(s, *current) {
		return ProvisionRevision{}, "", 409, errors.New("DNS serial was superseded")
	}
	if err := a.provisionCurrent(s, *current); err != nil {
		return ProvisionRevision{}, "", 409, errors.New("provisioning profile or source changed; generate and review again")
	}
	content, err := a.readProvisionRevisionLocked(*current)
	if err != nil {
		return ProvisionRevision{}, "", 500, errors.New("configuration artifact failed its integrity check")
	}
	if _, err = a.atlasActor(s, user, session, "admin", "operator"); err != nil || r.Context().Err() != nil {
		return ProvisionRevision{}, "", 401, errors.New("provisioning authority changed; sign in again")
	}
	return *current, content, 200, nil
}
