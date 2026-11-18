package app

import (
	"encoding/json"
	"slices"
	"strings"
	"time"
)

const monitoringPullStaleAfter = 24 * time.Hour

type monitoringDeliveryHealthItem struct {
	ProfileID        string     `json:"profileId"`
	ProfileName      string     `json:"profileName"`
	Kind             string     `json:"kind"`
	ApprovalStatus   string     `json:"approvalStatus"`
	RevisionID       string     `json:"revisionId,omitempty"`
	GeneratedAt      *string    `json:"generatedAt"`
	LastPulledAt     *time.Time `json:"lastPulledAt"`
	LastDownloadedAt *time.Time `json:"lastDownloadedAt"`
	PullStatus       string     `json:"pullStatus"`
	TargetCount      *int       `json:"targetCount,omitempty"`
}
type monitoringDeliveryHealthReport struct {
	Items                 []monitoringDeliveryHealthItem `json:"items"`
	NextAfter             string                         `json:"nextAfter"`
	CheckedAt             time.Time                      `json:"checkedAt"`
	PullStaleAfterSeconds int64                          `json:"pullStaleAfterSeconds"`
}

// s is an immutable snapshot. The caller must not hold the Store mutex:
// readProvisionRevision takes that lock to safely read each encrypted artifact.
// Observed transfer proves only that this version was served, never that a
// monitoring target loaded it or is healthy. Browser downloads are independent.
func (a *App) monitoringDeliveryReport(s *State, after string, limit int, now time.Time) monitoringDeliveryHealthReport {
	result := monitoringDeliveryHealthReport{Items: []monitoringDeliveryHealthItem{}, CheckedAt: now.UTC(), PullStaleAfterSeconds: int64(monitoringPullStaleAfter / time.Second)}
	if s == nil {
		return result
	}
	if limit < 1 {
		limit = 20
	}
	if limit > 25 {
		limit = 25
	}
	profiles := []Record{}
	for _, profile := range s.Resources[provisionProfileKind] {
		if str(profile, "id") > after && monitoringDeliveryKind(profile) != "" {
			profiles = append(profiles, profile)
		}
	}
	slices.SortFunc(profiles, func(left, right Record) int { return strings.Compare(str(left, "id"), str(right, "id")) })
	if len(profiles) > limit {
		profiles = profiles[:limit]
		result.NextAfter = str(profiles[len(profiles)-1], "id")
	}
	heads := map[string]ProvisionRevision{}
	selected := map[string]string{}
	for _, profile := range profiles {
		selected[str(profile, "id")] = monitoringDeliveryKind(profile)
	}
	for _, revision := range s.ProvisionRevisions {
		if selected[revision.ProfileID] == "" || selected[revision.ProfileID] != revision.Kind || revision.Status != "approved" {
			continue
		}
		previous, found := heads[revision.ProfileID]
		if !found || monitoringDeliveryNewer(revision, previous) {
			heads[revision.ProfileID] = revision
		}
	}
	for _, profile := range profiles {
		item := monitoringDeliveryHealthItem{ProfileID: str(profile, "id"), ProfileName: str(profile, "name"), Kind: monitoringDeliveryKind(profile), ApprovalStatus: "missing", PullStatus: "not-observed"}
		if revision, found := heads[item.ProfileID]; found {
			item.RevisionID = revision.ID
			if _, err := time.Parse(time.RFC3339Nano, revision.CreatedAt); err == nil {
				value := revision.CreatedAt
				item.GeneratedAt = &value
			}
			item.LastPulledAt = monitoringDeliveryTime(revision.LastPulledAt)
			item.LastDownloadedAt = monitoringDeliveryTime(revision.LastDownloadedAt)
			if item.LastPulledAt != nil {
				item.PullStatus = "recent"
				if item.LastPulledAt.Before(now.Add(-monitoringPullStaleAfter)) {
					item.PullStatus = "stale"
				}
			}
			if revision.TargetCount >= 0 && revision.TargetCount <= 100000 {
				value := revision.TargetCount
				item.TargetCount = &value
			}
			item.ApprovalStatus = "unavailable"
			if !revision.Pruned {
				if _, err := a.readProvisionRevision(revision); err == nil {
					item.ApprovalStatus = "stale"
					if a.provisionCurrent(s, revision) == nil {
						item.ApprovalStatus = "current"
					}
				}
			}
		}
		result.Items = append(result.Items, item)
	}
	return result
}

func monitoringDeliveryKind(profile Record) string {
	// Read only the configured kind here: a malformed monitoring option must
	// remain visible for the real renderer/currentness check, not vanish from
	// this report just because normalization fails.
	raw, err := json.Marshal(profile["options"])
	if err != nil || len(raw) > 65536 {
		return ""
	}
	var selected struct {
		Kind string `json:"kind"`
	}
	if json.Unmarshal(raw, &selected) != nil {
		return ""
	}
	if selected.Kind == "nagios" || selected.Kind == "smokeping" {
		return selected.Kind
	}
	return ""
}

func monitoringDeliveryNewer(left, right ProvisionRevision) bool {
	l, le := time.Parse(time.RFC3339Nano, left.CreatedAt)
	r, re := time.Parse(time.RFC3339Nano, right.CreatedAt)
	// Store validation normally excludes invalid times. If a caller supplies
	// damaged metadata, report it unavailable rather than fall back to an
	// older healthy revision and conceal the damage.
	if le != nil || re != nil {
		if (le == nil) != (re == nil) {
			return le != nil
		}
		return left.ID > right.ID
	}
	if !l.Equal(r) {
		return l.After(r)
	}
	return left.ID > right.ID
}

func monitoringDeliveryTime(value *time.Time) *time.Time {
	if value == nil || value.IsZero() {
		return nil
	}
	copy := value.UTC()
	return &copy
}
