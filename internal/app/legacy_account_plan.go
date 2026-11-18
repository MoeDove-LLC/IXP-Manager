package app

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/moedove/ixp-manager/internal/migrate"
)

type legacyAccountOrganizationMap struct {
	SourceCustomerID string `json:"sourceCustomerId"`
	MemberID         string `json:"memberId"`
}
type legacyAccountSelection struct {
	SourceUserID        string   `json:"sourceUserId"`
	SourceMembershipIDs []string `json:"sourceMembershipIds"`
	ExistingUserID      string   `json:"existingUserId,omitempty"`
}
type legacyAccountReviewInput struct {
	Archive       json.RawMessage                `json:"archive"`
	Organizations []legacyAccountOrganizationMap `json:"organizations"`
	Selected      []legacyAccountSelection       `json:"selected"`
}
type legacyAccountExisting struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email"`
}
type legacyAccountOrganization struct {
	SourceMembershipID string `json:"sourceMembershipId"`
	SourceCustomerID   string `json:"sourceCustomerId"`
	MemberID           string `json:"memberId"`
	MemberName         string `json:"memberName"`
	MemberCreatedAt    string `json:"memberCreatedAt"`
	MemberAdmin        bool   `json:"memberAdmin"`
	CurrentMemberRole  string `json:"currentMemberRole,omitempty"`
}
type legacyAccountReviewAccount struct {
	SourceUserID          string                      `json:"sourceUserId"`
	Name                  string                      `json:"name"`
	Email                 string                      `json:"email"`
	Status                string                      `json:"status"`
	Findings              []string                    `json:"findings"`
	ExistingCandidate     *legacyAccountExisting      `json:"existingCandidate,omitempty"`
	ExistingUserID        string                      `json:"-"`
	ExistingUserCreatedAt string                      `json:"-"`
	ExistingGeneration    uint64                      `json:"-"`
	Organizations         []legacyAccountOrganization `json:"organizations"`
}
type legacyAccountReview struct {
	ArchiveChecksum string                       `json:"archiveChecksum"`
	Accounts        []legacyAccountReviewAccount `json:"accounts"`
}

// The caller decodes input.Archive with ReadAccountArchive before calling this
// planner. No state, source facts, selections or invitations are changed here.
// "ready" means a reviewable invitation candidate, not an account grant.
func buildLegacyAccountReview(s *State, archive *migrate.AccountArchive, input legacyAccountReviewInput, now time.Time) (legacyAccountReview, error) {
	result := legacyAccountReview{Accounts: []legacyAccountReviewAccount{}}
	if s == nil || now.IsZero() || migrate.ValidateAccountArchive(archive) != nil {
		return result, errors.New("valid current state, time and account source archive required")
	}
	if len(input.Selected) == 0 || len(input.Selected) > 25 || len(input.Organizations) > 1000 {
		return result, errors.New("select 1 to 25 source accounts and at most 1000 organization mappings")
	}
	result.ArchiveChecksum = archive.Checksum
	users := make(map[string]migrate.AccountArchiveUser, len(archive.Users))
	emails := map[string]int{}
	for _, user := range archive.Users {
		users[user.LegacyID] = user
		if email := migrate.AccountArchiveEmail(user); email != "" {
			emails[email]++
		}
	}
	memberships := make(map[string]migrate.AccountArchiveMembership, len(archive.Memberships))
	pairs, sourceOrganizations := map[string]int{}, map[string]bool{}
	for _, membership := range archive.Memberships {
		memberships[membership.LegacyID] = membership
		pairs[membership.SourceUserID+":"+membership.SourceCustomerID]++
		sourceOrganizations[membership.SourceCustomerID] = true
	}
	mapped := map[string]string{}
	for _, mapping := range input.Organizations {
		if !migrate.AccountArchiveID(mapping.SourceCustomerID) || !sourceOrganizations[mapping.SourceCustomerID] || !identifier.MatchString(mapping.MemberID) || mapped[mapping.SourceCustomerID] != "" {
			return result, errors.New("organization mappings require unique known source IDs and valid current member IDs")
		}
		mapped[mapping.SourceCustomerID] = mapping.MemberID
	}
	selectedUsers := map[string]bool{}
	for _, selection := range input.Selected {
		if !migrate.AccountArchiveID(selection.SourceUserID) || selectedUsers[selection.SourceUserID] || len(selection.SourceMembershipIDs) == 0 || len(selection.SourceMembershipIDs) > 32 || (selection.ExistingUserID != "" && !identifier.MatchString(selection.ExistingUserID)) {
			return result, errors.New("source selections require unique account IDs, 1 to 32 relationships and an optional exact current account ID")
		}
		selectedUsers[selection.SourceUserID] = true
		seen := map[string]bool{}
		for _, id := range selection.SourceMembershipIDs {
			if !migrate.AccountArchiveID(id) || seen[id] {
				return result, errors.New("selected source relationship IDs must be exact and unique")
			}
			seen[id] = true
		}
	}
	for _, selection := range input.Selected {
		account := legacyAccountReviewAccount{SourceUserID: selection.SourceUserID, Status: "ready", Findings: []string{}, Organizations: []legacyAccountOrganization{}}
		hold := func(code string) { account.Status = "held"; account.Findings = append(account.Findings, code) }
		user, exists := users[selection.SourceUserID]
		if !exists {
			account.Name = "Legacy account " + selection.SourceUserID
			hold("source-user-unresolved")
			result.Accounts = append(result.Accounts, account)
			continue
		}
		account.Email = migrate.AccountArchiveEmail(user)
		account.Name = legacyAccountSourceName(user)
		if account.Name == "" {
			account.Name = account.Email
			if account.Name == "" {
				account.Name = "Legacy account " + selection.SourceUserID
			}
			account.Findings = append(account.Findings, "name-unusable")
		}
		if state := migrate.AccountArchiveUserState(user); state != "active" {
			hold("source-state-" + state)
		}
		if account.Email == "" {
			hold("source-email-unusable")
		} else if emails[account.Email] != 1 {
			hold("source-email-collision")
		}
		var existing Record
		if account.Email != "" {
			candidates := []Record{}
			for _, current := range s.Resources["users"] {
				if strings.EqualFold(strings.TrimSpace(str(current, "email")), account.Email) {
					candidates = append(candidates, current)
				}
			}
			switch len(candidates) {
			case 0:
				if selection.ExistingUserID != "" {
					hold("existing-account-mismatch")
				}
			case 1:
				candidate := candidates[0]
				if str(candidate, "role") != "member" {
					hold("existing-account-not-member")
				} else {
					account.ExistingCandidate = &legacyAccountExisting{ID: str(candidate, "id"), Name: str(candidate, "name"), Email: str(candidate, "email")}
					if selection.ExistingUserID == "" {
						hold("existing-account-selection-required")
					} else if selection.ExistingUserID != str(candidate, "id") {
						hold("existing-account-mismatch")
					} else if !legacyAccountCurrentEntity(candidate) {
						hold("existing-account-entity-unproven")
					} else {
						existing = candidate
						account.ExistingUserID = str(candidate, "id")
						account.ExistingUserCreatedAt = str(candidate, "createdAt")
						account.ExistingGeneration = s.Security[account.ExistingUserID].Generation
					}
				}
			default:
				hold("current-email-collision")
			}
		}
		// A legacy invitation accepts one complete reviewed organization set.
		// Another outstanding set for the same mailbox could become obsolete
		// as soon as the first set creates or changes the current account.
		for _, invitation := range s.Invitations {
			if invitation.LegacyAccount != nil && invitationStatus(invitation, now) == "pending" && strings.EqualFold(strings.TrimSpace(invitation.Email), account.Email) {
				hold("legacy-invitation-already-pending")
			}
		}
		seenTargets := map[string]bool{}
		for _, id := range selection.SourceMembershipIDs {
			membership, exists := memberships[id]
			if !exists {
				hold("source-membership-unresolved")
				continue
			}
			if membership.SourceUserID != user.LegacyID {
				hold("source-membership-user-mismatch")
				continue
			}
			if pairs[membership.SourceUserID+":"+membership.SourceCustomerID] != 1 {
				hold("source-membership-duplicate")
			}
			role := migrate.AccountArchivePrivilege(membership)
			if role != "member" && role != "member-admin" {
				hold("source-privilege-" + role)
				continue
			}
			memberID := mapped[membership.SourceCustomerID]
			if memberID == "" {
				hold("organization-mapping-required")
				continue
			}
			if seenTargets[memberID] {
				hold("target-organization-duplicate")
			}
			seenTargets[memberID] = true
			member := find(s, "members", memberID)
			if member == nil {
				hold("target-organization-unresolved")
				continue
			}
			if str(member, "status") != "active" {
				hold("target-organization-inactive")
			}
			if !legacyAccountCurrentEntity(member) {
				hold("target-organization-entity-unproven")
			}
			organization := legacyAccountOrganization{SourceMembershipID: membership.LegacyID, SourceCustomerID: membership.SourceCustomerID, MemberID: memberID, MemberName: str(member, "name"), MemberCreatedAt: str(member, "createdAt"), MemberAdmin: role == "member-admin"}
			if existing != nil && contains(membershipIDs(existing), memberID) {
				organization.CurrentMemberRole = "member"
				if memberAdministrator(existing, memberID) {
					organization.CurrentMemberRole = "member-admin"
				}
				// An explicitly attributed member -> member-admin invitation is
				// an actual reviewed upgrade. Equal or higher existing roles hold.
				if !organization.MemberAdmin || organization.CurrentMemberRole == "member-admin" {
					hold("membership-already-satisfied")
				}
			}
			for _, invitation := range s.Invitations {
				status := invitationStatus(invitation, now)
				if status != "pending" && status != "accepted" {
					continue
				}
				binding := invitation.LegacyAccount
				if binding != nil && binding.ArchiveChecksum == archive.Checksum && binding.SourceUserID == user.LegacyID {
					for _, previous := range binding.Organizations {
						if previous.SourceMembershipID == id {
							hold("source-membership-" + status)
						}
					}
				}
				if status == "pending" && strings.EqualFold(strings.TrimSpace(invitation.Email), account.Email) {
					if invitation.MemberID == memberID {
						hold("invitation-already-pending")
					}
					if binding != nil {
						for _, previous := range binding.Organizations {
							if previous.MemberID == memberID {
								hold("invitation-already-pending")
							}
						}
					}
				}
			}
			account.Organizations = append(account.Organizations, organization)
		}
		slices.Sort(account.Findings)
		account.Findings = slices.Compact(account.Findings)
		slices.SortFunc(account.Organizations, func(left, right legacyAccountOrganization) int {
			return strings.Compare(left.SourceMembershipID, right.SourceMembershipID)
		})
		// The old default is useful only when its actual relationship was
		// selected and mapped. It cannot invent a grant or replace the current
		// default on an explicitly attributed existing account.
		if account.ExistingUserID == "" {
			var sourceDefault string
			if json.Unmarshal(user.Facts["custid"], &sourceDefault) == nil && migrate.AccountArchiveID(sourceDefault) {
				for index, organization := range account.Organizations {
					if organization.SourceCustomerID == sourceDefault {
						copy(account.Organizations[1:index+1], account.Organizations[:index])
						account.Organizations[0] = organization
						break
					}
				}
			}
		}
		result.Accounts = append(result.Accounts, account)
	}
	slices.SortFunc(result.Accounts, func(left, right legacyAccountReviewAccount) int {
		return strings.Compare(left.SourceUserID, right.SourceUserID)
	})
	return result, nil
}

func legacyAccountCurrentEntity(record Record) bool {
	created, err := time.Parse(time.RFC3339Nano, str(record, "createdAt"))
	return identifier.MatchString(str(record, "id")) && err == nil && !created.IsZero()
}
func legacyAccountSourceName(user migrate.AccountArchiveUser) string {
	var value string
	if json.Unmarshal(user.Facts["name"], &value) != nil {
		return ""
	}
	value = strings.TrimSpace(value)
	if len(value) == 0 || len(value) > 200 || strings.ContainsFunc(value, unicode.IsControl) {
		return ""
	}
	return value
}
