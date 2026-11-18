package app

import (
	"github.com/moedove/ixp-manager/internal/routing"
	"strings"
	"time"
)

type DiffLine struct {
	Type string `json:"type"`
	Text string `json:"text"`
}
type CredentialChange struct {
	PeerID           string `json:"peerId"`
	Change           string `json:"change"`
	BeforeRevision   string `json:"beforeRevision,omitempty"`
	AfterRevision    string `json:"afterRevision,omitempty"`
	BeforeConfigured bool   `json:"beforeConfigured"`
	AfterConfigured  bool   `json:"afterConfigured"`
}

type ConfigDiff struct {
	Redacted          bool               `json:"redacted"`
	BaseUnavailable   bool               `json:"baseUnavailable,omitempty"`
	CredentialChanges []CredentialChange `json:"credentialChanges"`
	BaseID            string             `json:"baseId"`
	Changes           []DiffLine         `json:"changes"`
	Added             int                `json:"added"`
	Removed           int                `json:"removed"`
	Truncated         bool               `json:"truncated"`
}

// configDiff uses a bounded resynchronization window. It is a review aid, not a
// patch to execute: the immutable full artifact remains the deployment input.
func configDiff(baseID, before, after string) ConfigDiff {
	result := ConfigDiff{BaseID: baseID, Changes: []DiffLine{}}
	lines := func(s string) []string {
		if s == "" {
			return nil
		}
		return strings.Split(strings.TrimSuffix(s, "\n"), "\n")
	}
	a, b := lines(before), lines(after)
	i, j := 0, 0
	emit := func(kind, text string) {
		if kind == "add" {
			result.Added++
		}
		if kind == "remove" {
			result.Removed++
		}
		if len(result.Changes) < 20000 {
			result.Changes = append(result.Changes, DiffLine{kind, text})
		} else {
			result.Truncated = true
		}
	}
	for i < len(a) || j < len(b) {
		if i < len(a) && j < len(b) && a[i] == b[j] {
			emit("context", a[i])
			i++
			j++
			continue
		}
		if i == len(a) {
			emit("add", b[j])
			j++
			continue
		}
		if j == len(b) {
			emit("remove", a[i])
			i++
			continue
		}
		ai, bj, best := -1, -1, 161
		index := map[string]int{}
		for n := j; n < len(b) && n < j+80; n++ {
			if _, ok := index[b[n]]; !ok {
				index[b[n]] = n
			}
		}
		for n := i; n < len(a) && n < i+80; n++ {
			if m, ok := index[a[n]]; ok && n-i+m-j < best {
				ai, bj, best = n, m, n-i+m-j
			}
		}
		if ai < 0 {
			emit("remove", a[i])
			emit("add", b[j])
			i++
			j++
			continue
		}
		for i < ai {
			emit("remove", a[i])
			i++
		}
		for j < bj {
			emit("add", b[j])
			j++
		}
	}
	return result
}
func (a *App) deploymentDiff(s *State, d Deployment) (ConfigDiff, error) {
	var base Deployment
	target, _ := time.Parse(time.RFC3339Nano, d.CreatedAt)
	var latest time.Time
	for _, candidate := range s.Deployments {
		created, err := time.Parse(time.RFC3339Nano, candidate.CreatedAt)
		if err == nil && candidate.RouterID == d.RouterID && candidate.Platform == d.Platform && candidate.Status == "approved" && created.Before(target) && created.After(latest) {
			base = candidate
			latest = created
		}
	}
	after, err := a.readDeploymentArtifact(d)
	if err != nil {
		return ConfigDiff{}, err
	}
	after, err = routing.RedactConfig(d.Platform, after)
	if err != nil {
		return ConfigDiff{}, err
	}
	before := ""
	if base.ID != "" && !base.Pruned {
		before, err = a.readDeploymentArtifact(base)
		if err != nil {
			return ConfigDiff{}, err
		}
		before, err = routing.RedactConfig(base.Platform, before)
		if err != nil {
			return ConfigDiff{}, err
		}
	}
	result := configDiff(base.ID, before, after)
	result.Redacted = true
	result.BaseUnavailable = base.Pruned
	result.CredentialChanges = publicCredentialChanges(base, d)
	return result, nil
}
