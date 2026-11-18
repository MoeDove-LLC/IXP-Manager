package sflow

import (
	"errors"
	"sort"
)

// Members is an authorization directory, not a paginated statistics query. The
// caller still checks current identities before disclosing inventory labels.
// Legacy IDs without entity revision cannot establish a current member identity.
func (s *Store) Members(switchIDs []string) ([]MemberIdentity, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return nil, errors.New("sFlow store closed")
	}
	if len(switchIDs) > maxExporters {
		return nil, errors.New("authorized switch set exceeds bound")
	}
	scope := map[string]bool{}
	for _, id := range switchIDs {
		if !identifier.MatchString(id) || scope[id] {
			return nil, errors.New("invalid authorized switch set")
		}
		scope[id] = true
	}
	seen := map[MemberIdentity]bool{}
	for _, row := range s.state.Rows {
		if row.UnattributedReason != "" || len(scope) > 0 && !scope[row.SwitchID] {
			continue
		}
		for _, identity := range []MemberIdentity{{row.SourceMemberID, row.SourceMemberRevision}, {row.DestinationMemberID, row.DestinationMemberRevision}} {
			if identifier.MatchString(identity.ID) && validDigest(identity.Revision) {
				seen[identity] = true
			}
		}
	}
	result := make([]MemberIdentity, 0, len(seen))
	for identity := range seen {
		result = append(result, identity)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].ID == result[j].ID {
			return result[i].Revision < result[j].Revision
		}
		return result[i].ID < result[j].ID
	})
	return result, nil
}
