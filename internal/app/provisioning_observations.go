package app

import (
	"errors"
	"time"
)

const provisionResponseObservationInterval = 5 * time.Minute

var errProvisionObservationSkipped = errors.New("provisioning response observation skipped")

func observesProvisionResponses(kind string) bool {
	return kind == "nagios" || kind == "smokeping"
}

func validProvisionResponseTimes(d ProvisionRevision, createdAt time.Time) bool {
	for _, observedAt := range []*time.Time{d.LastPulledAt, d.LastDownloadedAt} {
		if observedAt != nil && (!observesProvisionResponses(d.Kind) || d.Status != "approved" || observedAt.IsZero() || observedAt.Before(createdAt)) {
			return false
		}
	}
	return true
}

func provisionResponseObservationDue(d ProvisionRevision, pulled bool, at time.Time) bool {
	last := d.LastDownloadedAt
	if pulled {
		last = d.LastPulledAt
	}
	return last == nil || !at.Before(last.Add(provisionResponseObservationInterval))
}

func (a *App) recordProvisionResponse(served ProvisionRevision, pulled bool, at time.Time) {
	if !observesProvisionResponses(served.Kind) || !provisionResponseObservationDue(served, pulled, at) {
		return
	}
	at = at.UTC()
	// The response has already been written. A failed disk commit leaves no
	// observation and cannot change the response.
	_ = a.store.Update(func(s *State) error {
		for i := range s.ProvisionRevisions {
			current := &s.ProvisionRevisions[i]
			if current.ID != served.ID {
				continue
			}
			if current.Status != "approved" || current.Pruned || current.Kind != served.Kind ||
				current.ProfileID != served.ProfileID || current.ProfileRevision != served.ProfileRevision || current.CreatedAt != served.CreatedAt ||
				current.Checksum != served.Checksum || current.RenderChecksum != served.RenderChecksum || current.fileMetadata() != served.fileMetadata() ||
				!provisionResponseObservationDue(*current, pulled, at) {
				return errProvisionObservationSkipped
			}
			if pulled {
				current.LastPulledAt = &at
			} else {
				current.LastDownloadedAt = &at
			}
			return validateProvisionRevision(*current)
		}
		return errProvisionObservationSkipped
	})
}
