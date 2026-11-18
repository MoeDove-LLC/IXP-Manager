package app

import (
	"bytes"
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/moedove/ixp-manager/internal/migrate"
)

const legacyPlanSourceUser = "9007199254740993"

func legacyPlanFacts(raw string) migrate.NoteArchiveFacts {
	var facts migrate.NoteArchiveFacts
	if err := json.Unmarshal([]byte(raw), &facts); err != nil {
		panic(err)
	}
	return facts
}
func legacyPlanFixture(t *testing.T) (State, *migrate.AccountArchive, legacyAccountReviewInput, time.Time) {
	t.Helper()
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	s := State{Resources: map[string][]Record{"members": {
		{"id": "member-a", "name": "Current A", "status": "active", "createdAt": "2025-01-01T01:02:03Z"},
		{"id": "member-b", "name": "Current B", "status": "active", "createdAt": "2025-01-02T01:02:03Z"},
	}}, Security: map[string]SecurityUser{}}
	a := &migrate.AccountArchive{Version: 1, Kind: migrate.AccountArchiveKind, Source: "ixp-manager-legacy", Access: "admin", SourceTimezone: "unknown",
		Users: []migrate.AccountArchiveUser{
			{LegacyID: legacyPlanSourceUser, Facts: legacyPlanFacts(`{"id":"9007199254740993","name":" Original name ","email":"ONE@example.test","disabled":0,"created_at":"2001-01-02 03:04:05"}`)},
			{LegacyID: "2", Facts: legacyPlanFacts(`{"id":"2","name":"Other","email":"two@example.test","disabled":false}`)},
		},
		Memberships: []migrate.AccountArchiveMembership{
			{LegacyID: "11", SourceUserID: legacyPlanSourceUser, SourceCustomerID: "101", Facts: legacyPlanFacts(`{"id":"11","user_id":"9007199254740993","customer_id":"101","privs":1}`)},
			{LegacyID: "12", SourceUserID: legacyPlanSourceUser, SourceCustomerID: "102", Facts: legacyPlanFacts(`{"id":"12","user_id":"9007199254740993","customer_id":"102","privs":"2"}`)},
			{LegacyID: "21", SourceUserID: "2", SourceCustomerID: "101", Facts: legacyPlanFacts(`{"id":"21","user_id":"2","customer_id":"101","privs":1}`)},
		},
	}
	input := legacyAccountReviewInput{Organizations: []legacyAccountOrganizationMap{{SourceCustomerID: "101", MemberID: "member-a"}, {SourceCustomerID: "102", MemberID: "member-b"}}, Selected: []legacyAccountSelection{{SourceUserID: legacyPlanSourceUser, SourceMembershipIDs: []string{"11", "12"}}}}
	legacyPlanSeal(t, a, &input)
	return s, a, input, now
}
func legacyPlanSeal(t *testing.T, archive *migrate.AccountArchive, input *legacyAccountReviewInput) {
	t.Helper()
	raw, err := migrate.EncodeAccountArchive(*archive)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := migrate.ReadAccountArchive(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	*archive = *parsed
	input.Archive = append(json.RawMessage{}, raw...)
}
func legacyPlanExisting(s *State, role string) Record {
	u := Record{"id": "current-user", "name": "Current name", "email": "one@example.test", "role": role, "createdAt": "2025-03-01T02:03:04Z", "memberIds": []any{}, "memberAdminIds": []any{}, "passwordHash": "CURRENT-PASSWORD-SECRET"}
	s.Resources["users"] = append(s.Resources["users"], u)
	s.Security["current-user"] = SecurityUser{Generation: 17, MFAEnabled: true, Secret: "CURRENT-MFA-SECRET"}
	return u
}
func legacyPlanAccount(t *testing.T, s *State, archive *migrate.AccountArchive, input legacyAccountReviewInput, now time.Time) legacyAccountReviewAccount {
	t.Helper()
	review, err := buildLegacyAccountReview(s, archive, input, now)
	if err != nil {
		t.Fatal(err)
	}
	if review.ArchiveChecksum != archive.Checksum || len(review.Accounts) != 1 {
		t.Fatal("unexpected plan envelope")
	}
	return review.Accounts[0]
}
func legacyPlanHeld(t *testing.T, account legacyAccountReviewAccount, finding string) {
	t.Helper()
	if account.Status != "held" || !slices.Contains(account.Findings, finding) {
		t.Fatalf("expected held %s, got %s %+v", finding, account.Status, account.Findings)
	}
}

func TestLegacyAccountPlannerReadyIsPureAndDeterministic(t *testing.T) {
	s, a, input, now := legacyPlanFixture(t)
	before, _ := json.Marshal([]any{s, a, input})
	review, err := buildLegacyAccountReview(&s, a, input, now)
	if err != nil {
		t.Fatal(err)
	}
	account := review.Accounts[0]
	if account.Status != "ready" || account.SourceUserID != legacyPlanSourceUser || account.Name != "Original name" || account.Email != "one@example.test" || len(account.Findings) != 0 || len(account.Organizations) != 2 || account.ExistingUserID != "" || account.ExistingCandidate != nil {
		t.Fatal("wrong new-account plan", account)
	}
	if account.Organizations[0].MemberAdmin || !account.Organizations[1].MemberAdmin || account.Organizations[0].MemberCreatedAt != "2025-01-01T01:02:03Z" {
		t.Fatal("source roles collapsed or source creation substituted")
	}
	after, _ := json.Marshal([]any{s, a, input})
	if !bytes.Equal(before, after) {
		t.Fatal("planner changed source, selection or current state")
	}
	slices.Reverse(input.Organizations)
	slices.Reverse(input.Selected[0].SourceMembershipIDs)
	again, err := buildLegacyAccountReview(&s, a, input, now)
	if err != nil || !reflect.DeepEqual(review, again) {
		t.Fatal("equivalent order changed reviewed plan", err)
	}
	// Old numeric IDs do not select a current identity with a different email.
	s.Resources["users"] = []Record{{"id": legacyPlanSourceUser, "email": "different@example.test", "role": "admin", "createdAt": "2025-01-01T00:00:00Z"}}
	account = legacyPlanAccount(t, &s, a, input, now)
	if account.Status != "ready" || account.ExistingUserID != "" {
		t.Fatal("legacy ID became current identity")
	}
}

func TestLegacyAccountPlannerPreservesSelectedSourceDefaultForNewAccount(t *testing.T) {
	s, a, input, now := legacyPlanFixture(t)
	a.Users[0].Facts["custid"] = json.RawMessage(`"102"`)
	legacyPlanSeal(t, a, &input)
	account := legacyPlanAccount(t, &s, a, input, now)
	if account.Status != "ready" || account.Organizations[0].SourceMembershipID != "12" || account.Organizations[1].SourceMembershipID != "11" {
		t.Fatal("selected actual old default was not preserved")
	}
	input.Selected[0].SourceMembershipIDs = []string{"11"}
	account = legacyPlanAccount(t, &s, a, input, now)
	if account.Status != "ready" || len(account.Organizations) != 1 || account.Organizations[0].SourceMembershipID != "11" {
		t.Fatal("unselected old default invented a relationship")
	}
	input.Selected[0].SourceMembershipIDs = []string{"11", "12"}
	u := legacyPlanExisting(&s, "member")
	u["memberIds"], u["memberId"] = []any{"retained"}, "retained"
	s.Resources["members"] = append(s.Resources["members"], Record{"id": "retained", "name": "Retained", "status": "active", "createdAt": "2025-01-01T00:00:00Z"})
	input.Selected[0].ExistingUserID = "current-user"
	account = legacyPlanAccount(t, &s, a, input, now)
	if account.Status != "ready" || account.Organizations[0].SourceMembershipID != "11" || str(u, "memberId") != "retained" {
		t.Fatal("source default changed the existing account default")
	}
}

func TestLegacyAccountPlannerSourceAndOrganizationHolds(t *testing.T) {
	for name, tc := range map[string]struct {
		mutate  func(*State, *migrate.AccountArchive, *legacyAccountReviewInput)
		finding string
	}{
		"disabled": {func(_ *State, a *migrate.AccountArchive, _ *legacyAccountReviewInput) {
			a.Users[0].Facts["disabled"] = json.RawMessage(`1`)
		}, "source-state-disabled"},
		"unknown-state": {func(_ *State, a *migrate.AccountArchive, _ *legacyAccountReviewInput) {
			a.Users[0].Facts["disabled"] = json.RawMessage(`"UNKNOWN-PRIVATE"`)
		}, "source-state-unknown"},
		"invalid-email": {func(_ *State, a *migrate.AccountArchive, _ *legacyAccountReviewInput) {
			a.Users[0].Facts["email"] = json.RawMessage(`"not an email"`)
		}, "source-email-unusable"},
		"unselected-email-collision": {func(_ *State, a *migrate.AccountArchive, _ *legacyAccountReviewInput) {
			a.Users[1].Facts["email"] = json.RawMessage(`"one@EXAMPLE.test"`)
		}, "source-email-collision"},
		"missing-user": {func(_ *State, _ *migrate.AccountArchive, i *legacyAccountReviewInput) {
			i.Selected[0].SourceUserID = "404"
		}, "source-user-unresolved"},
		"missing-relation": {func(_ *State, _ *migrate.AccountArchive, i *legacyAccountReviewInput) {
			i.Selected[0].SourceMembershipIDs = []string{"404"}
		}, "source-membership-unresolved"},
		"other-user-relation": {func(_ *State, _ *migrate.AccountArchive, i *legacyAccountReviewInput) {
			i.Selected[0].SourceMembershipIDs = []string{"21"}
		}, "source-membership-user-mismatch"},
		"source-duplicate-pair": {func(_ *State, a *migrate.AccountArchive, _ *legacyAccountReviewInput) {
			m := a.Memberships[0]
			m.LegacyID = "13"
			m.Facts = legacyPlanFacts(`{"id":"13","user_id":"9007199254740993","customer_id":"101","privs":2}`)
			a.Memberships = append(a.Memberships, m)
		}, "source-membership-duplicate"},
		"public": {func(_ *State, a *migrate.AccountArchive, _ *legacyAccountReviewInput) {
			a.Memberships[0].Facts["privs"] = json.RawMessage(`0`)
		}, "source-privilege-public"},
		"superuser": {func(_ *State, a *migrate.AccountArchive, _ *legacyAccountReviewInput) {
			a.Memberships[0].Facts["privs"] = json.RawMessage(`3`)
		}, "source-privilege-superuser"},
		"unknown-privilege": {func(_ *State, a *migrate.AccountArchive, _ *legacyAccountReviewInput) {
			a.Memberships[0].Facts["privs"] = json.RawMessage(`"UNKNOWN-PRIVATE"`)
		}, "source-privilege-unknown"},
		"missing-map": {func(_ *State, _ *migrate.AccountArchive, i *legacyAccountReviewInput) {
			i.Organizations = i.Organizations[1:]
		}, "organization-mapping-required"},
		"missing-target": {func(_ *State, _ *migrate.AccountArchive, i *legacyAccountReviewInput) {
			i.Organizations[0].MemberID = "missing"
		}, "target-organization-unresolved"},
		"pending-target": {func(s *State, _ *migrate.AccountArchive, _ *legacyAccountReviewInput) {
			s.Resources["members"][0]["status"] = "pending"
		}, "target-organization-inactive"},
		"suspended-target": {func(s *State, _ *migrate.AccountArchive, _ *legacyAccountReviewInput) {
			s.Resources["members"][0]["status"] = "suspended"
		}, "target-organization-inactive"},
		"closed-target": {func(s *State, _ *migrate.AccountArchive, _ *legacyAccountReviewInput) {
			s.Resources["members"][0]["status"] = "closed"
		}, "target-organization-inactive"},
		"unknown-target-entity": {func(s *State, _ *migrate.AccountArchive, _ *legacyAccountReviewInput) {
			delete(s.Resources["members"][0], "createdAt")
		}, "target-organization-entity-unproven"},
		"bad-target-entity": {func(s *State, _ *migrate.AccountArchive, _ *legacyAccountReviewInput) {
			s.Resources["members"][0]["createdAt"] = "not-time"
		}, "target-organization-entity-unproven"},
		"duplicated-current-target": {func(_ *State, _ *migrate.AccountArchive, i *legacyAccountReviewInput) {
			i.Organizations[1].MemberID = i.Organizations[0].MemberID
		}, "target-organization-duplicate"},
	} {
		t.Run(name, func(t *testing.T) {
			s, a, i, now := legacyPlanFixture(t)
			tc.mutate(&s, a, &i)
			legacyPlanSeal(t, a, &i)
			account := legacyPlanAccount(t, &s, a, i, now)
			legacyPlanHeld(t, account, tc.finding)
			raw, _ := json.Marshal(account)
			if bytes.Contains(raw, []byte("UNKNOWN-PRIVATE")) {
				t.Fatal("unknown source scalar leaked")
			}
		})
	}
}

func TestLegacyAccountPlannerSourceLabelsAreInformational(t *testing.T) {
	for _, name := range []string{`null`, `42`, `""`, `"Line\nBreak"`} {
		s, a, input, now := legacyPlanFixture(t)
		a.Users[0].Facts["name"] = json.RawMessage(name)
		delete(a.Users[0].Facts, "created_at")
		legacyPlanSeal(t, a, &input)
		account := legacyPlanAccount(t, &s, a, input, now)
		if account.Status != "ready" || account.Name != account.Email || !slices.Contains(account.Findings, "name-unusable") {
			t.Fatal("missing old label blocked current identity proof")
		}
	}
	// A relation not selected in this review cannot supply privileges or block
	// unrelated eligible relationships just because it contains an old SU role.
	s, a, input, now := legacyPlanFixture(t)
	a.Memberships[1].Facts["privs"] = json.RawMessage(`3`)
	input.Selected[0].SourceMembershipIDs = []string{"11"}
	legacyPlanSeal(t, a, &input)
	account := legacyPlanAccount(t, &s, a, input, now)
	if account.Status != "ready" || len(account.Organizations) != 1 || account.Organizations[0].MemberAdmin {
		t.Fatal("unselected role affected selected grant")
	}
}

func TestLegacyAccountPlannerExistingAttributionAndRoleUpgrade(t *testing.T) {
	for _, role := range []string{"admin", "operator", "viewer"} {
		t.Run(role, func(t *testing.T) {
			s, a, i, now := legacyPlanFixture(t)
			legacyPlanExisting(&s, role)
			i.Selected[0].ExistingUserID = "current-user"
			account := legacyPlanAccount(t, &s, a, i, now)
			legacyPlanHeld(t, account, "existing-account-not-member")
			if account.ExistingCandidate != nil || account.ExistingUserID != "" {
				t.Fatal("staff offered as member candidate")
			}
		})
	}
	t.Run("explicit-selection", func(t *testing.T) {
		s, a, i, now := legacyPlanFixture(t)
		legacyPlanExisting(&s, "member")
		account := legacyPlanAccount(t, &s, a, i, now)
		legacyPlanHeld(t, account, "existing-account-selection-required")
		if account.ExistingCandidate == nil || account.ExistingCandidate.ID != "current-user" || account.ExistingUserID != "" {
			t.Fatal("collision auto-selected or candidate lost")
		}
		i.Selected[0].ExistingUserID = "other-current-user"
		legacyPlanHeld(t, legacyPlanAccount(t, &s, a, i, now), "existing-account-mismatch")
		i.Selected[0].ExistingUserID = "current-user"
		account = legacyPlanAccount(t, &s, a, i, now)
		if account.Status != "ready" || account.ExistingUserID != "current-user" || account.ExistingUserCreatedAt != "2025-03-01T02:03:04Z" || account.ExistingGeneration != 17 {
			t.Fatal("exact current identity binding missing")
		}
		raw, _ := json.Marshal(account)
		if bytes.Contains(raw, []byte("SECRET")) || bytes.Contains(raw, []byte("passwordHash")) || bytes.Contains(raw, []byte("existingUserId")) || bytes.Contains(raw, []byte("existingUserCreatedAt")) || bytes.Contains(raw, []byte("existingGeneration")) {
			t.Fatal("current credentials leaked")
		}
		s.Security["current-user"] = SecurityUser{Generation: 18}
		find(&s, "users", "current-user")["createdAt"] = "2026-01-01T00:00:00Z"
		changed := legacyPlanAccount(t, &s, a, i, now)
		if changed.ExistingGeneration == account.ExistingGeneration || changed.ExistingUserCreatedAt == account.ExistingUserCreatedAt {
			t.Fatal("recreated/security-changed entity failed to change review")
		}
	})
	for _, stamp := range []string{"", "not-time", "0001-01-01T00:00:00Z"} {
		t.Run("unknown-entity-"+stamp, func(t *testing.T) {
			s, a, i, now := legacyPlanFixture(t)
			u := legacyPlanExisting(&s, "member")
			u["createdAt"] = stamp
			i.Selected[0].ExistingUserID = "current-user"
			legacyPlanHeld(t, legacyPlanAccount(t, &s, a, i, now), "existing-account-entity-unproven")
		})
	}
	t.Run("explicit-but-no-collision", func(t *testing.T) {
		s, a, i, now := legacyPlanFixture(t)
		i.Selected[0].ExistingUserID = "missing"
		legacyPlanHeld(t, legacyPlanAccount(t, &s, a, i, now), "existing-account-mismatch")
	})
	t.Run("duplicate-current-email", func(t *testing.T) {
		s, a, i, now := legacyPlanFixture(t)
		legacyPlanExisting(&s, "member")
		s.Resources["users"] = append(s.Resources["users"], Record{"id": "duplicate", "email": "ONE@example.test", "role": "member"})
		i.Selected[0].ExistingUserID = "current-user"
		legacyPlanHeld(t, legacyPlanAccount(t, &s, a, i, now), "current-email-collision")
	})
	for _, tc := range []struct {
		name, relation string
		admin, ready   bool
	}{{"equal-member", "11", false, false}, {"higher-current", "11", true, false}, {"equal-admin", "12", true, false}, {"explicit-upgrade", "12", false, true}} {
		t.Run(tc.name, func(t *testing.T) {
			s, a, i, now := legacyPlanFixture(t)
			u := legacyPlanExisting(&s, "member")
			u["memberIds"] = []any{"member-a", "member-b"}
			if tc.admin {
				u["memberAdminIds"] = []any{"member-a", "member-b"}
			}
			i.Selected[0].ExistingUserID = "current-user"
			i.Selected[0].SourceMembershipIDs = []string{tc.relation}
			account := legacyPlanAccount(t, &s, a, i, now)
			if tc.ready {
				if account.Status != "ready" || account.Organizations[0].CurrentMemberRole != "member" || !account.Organizations[0].MemberAdmin {
					t.Fatal("explicit member-admin upgrade lost")
				}
			} else {
				legacyPlanHeld(t, account, "membership-already-satisfied")
			}
		})
	}
}

func TestLegacyAccountPlannerInvitationOccupancy(t *testing.T) {
	for _, tc := range []struct {
		name, status, finding string
		legacy                bool
	}{
		{"ordinary-pending", "pending", "invitation-already-pending", false},
		{"legacy-pending", "pending", "source-membership-pending", true},
		{"legacy-accepted", "accepted", "source-membership-accepted", true},
		{"revoked-released", "revoked", "", true}, {"expired-released", "expired", "", true},
		{"ordinary-accepted-currently-removed", "accepted", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, a, i, now := legacyPlanFixture(t)
			i.Selected[0].SourceMembershipIDs = []string{"11"}
			invite := Invitation{ID: "previous", Email: "ONE@example.test", MemberID: "member-a", ExpiresAt: now.Add(time.Hour)}
			if tc.legacy {
				invite.LegacyAccount = &legacyAccountInvitation{ArchiveChecksum: a.Checksum, SourceUserID: legacyPlanSourceUser, Organizations: []legacyAccountOrganization{{SourceMembershipID: "11", SourceCustomerID: "101", MemberID: "member-a"}}}
			}
			switch tc.status {
			case "accepted":
				invite.AcceptedAt = now.Add(-24 * time.Hour)
				invite.ExpiresAt = now.Add(-time.Hour)
			case "revoked":
				invite.Revoked = true
			case "expired":
				invite.ExpiresAt = now
			}
			s.Invitations = []Invitation{invite}
			account := legacyPlanAccount(t, &s, a, i, now)
			if tc.finding != "" {
				legacyPlanHeld(t, account, tc.finding)
			} else if account.Status != "ready" {
				t.Fatal("inactive/unbound invitation retained source occupancy", account.Findings)
			}
		})
	}
	t.Run("partial-selection-and-retarget", func(t *testing.T) {
		s, a, i, now := legacyPlanFixture(t)
		s.Invitations = []Invitation{{ID: "previous", Email: "one@example.test", ExpiresAt: now.Add(time.Hour), AcceptedAt: now.Add(-time.Hour), LegacyAccount: &legacyAccountInvitation{ArchiveChecksum: a.Checksum, SourceUserID: legacyPlanSourceUser, Organizations: []legacyAccountOrganization{{SourceMembershipID: "11", SourceCustomerID: "101", MemberID: "old-target"}}}}}
		i.Selected[0].SourceMembershipIDs = []string{"12"}
		if account := legacyPlanAccount(t, &s, a, i, now); account.Status != "ready" {
			t.Fatal("unselected source relation permanently occupied", account.Findings)
		}
		i.Selected[0].SourceMembershipIDs = []string{"11"}
		legacyPlanHeld(t, legacyPlanAccount(t, &s, a, i, now), "source-membership-accepted")
		s.Invitations[0].Revoked = true
		legacyPlanHeld(t, legacyPlanAccount(t, &s, a, i, now), "source-membership-accepted")
	})
	t.Run("source-checksum-is-part-of-binding", func(t *testing.T) {
		s, a, i, now := legacyPlanFixture(t)
		s.Invitations = []Invitation{{ID: "other-archive", Email: "one@example.test", AcceptedAt: now.Add(-time.Hour), LegacyAccount: &legacyAccountInvitation{ArchiveChecksum: strings.Repeat("a", 64), SourceUserID: legacyPlanSourceUser, Organizations: []legacyAccountOrganization{{SourceMembershipID: "11", MemberID: "member-a"}}}}}
		if account := legacyPlanAccount(t, &s, a, i, now); account.Status != "ready" {
			t.Fatal("another archive became the same source identity")
		}
	})
	t.Run("other-legacy-pending-same-email-target", func(t *testing.T) {
		s, a, i, now := legacyPlanFixture(t)
		s.Invitations = []Invitation{{ID: "other-archive", Email: "one@example.test", ExpiresAt: now.Add(time.Hour), LegacyAccount: &legacyAccountInvitation{ArchiveChecksum: strings.Repeat("a", 64), SourceUserID: "404", Organizations: []legacyAccountOrganization{{SourceMembershipID: "88", MemberID: "member-b"}}}}}
		legacyPlanHeld(t, legacyPlanAccount(t, &s, a, i, now), "invitation-already-pending")
	})
	t.Run("other-legacy-pending-different-target", func(t *testing.T) {
		s, a, i, now := legacyPlanFixture(t)
		s.Invitations = []Invitation{{ID: "other-batch", Email: "ONE@example.test", ExpiresAt: now.Add(time.Hour), LegacyAccount: &legacyAccountInvitation{ArchiveChecksum: strings.Repeat("a", 64), SourceUserID: "404", Organizations: []legacyAccountOrganization{{SourceMembershipID: "88", MemberID: "unselected-target"}}}}}
		legacyPlanHeld(t, legacyPlanAccount(t, &s, a, i, now), "legacy-invitation-already-pending")
	})
}

func TestLegacyAccountPlannerRejectsMalformedInput(t *testing.T) {
	for name, mutate := range map[string]func(*migrate.AccountArchive, *legacyAccountReviewInput){
		"empty-selection": func(_ *migrate.AccountArchive, i *legacyAccountReviewInput) { i.Selected = nil },
		"too-many-users": func(_ *migrate.AccountArchive, i *legacyAccountReviewInput) {
			i.Selected = make([]legacyAccountSelection, 26)
		},
		"too-many-maps": func(_ *migrate.AccountArchive, i *legacyAccountReviewInput) {
			i.Organizations = make([]legacyAccountOrganizationMap, 1001)
		},
		"duplicate-map": func(_ *migrate.AccountArchive, i *legacyAccountReviewInput) {
			i.Organizations = append(i.Organizations, i.Organizations[0])
		},
		"unknown-source-map": func(_ *migrate.AccountArchive, i *legacyAccountReviewInput) {
			i.Organizations[0].SourceCustomerID = "404"
		},
		"invalid-current-map": func(_ *migrate.AccountArchive, i *legacyAccountReviewInput) { i.Organizations[0].MemberID = "bad/id" },
		"duplicate-selection": func(_ *migrate.AccountArchive, i *legacyAccountReviewInput) {
			i.Selected = append(i.Selected, i.Selected[0])
		},
		"invalid-source-selection": func(_ *migrate.AccountArchive, i *legacyAccountReviewInput) { i.Selected[0].SourceUserID = "01" },
		"empty-relations":          func(_ *migrate.AccountArchive, i *legacyAccountReviewInput) { i.Selected[0].SourceMembershipIDs = nil },
		"too-many-relations": func(_ *migrate.AccountArchive, i *legacyAccountReviewInput) {
			i.Selected[0].SourceMembershipIDs = make([]string, 33)
		},
		"duplicate-relation": func(_ *migrate.AccountArchive, i *legacyAccountReviewInput) {
			i.Selected[0].SourceMembershipIDs = []string{"11", "11"}
		},
		"invalid-relation": func(_ *migrate.AccountArchive, i *legacyAccountReviewInput) {
			i.Selected[0].SourceMembershipIDs = []string{"+11"}
		},
		"invalid-existing-id": func(_ *migrate.AccountArchive, i *legacyAccountReviewInput) { i.Selected[0].ExistingUserID = " x " },
		"tampered-archive": func(a *migrate.AccountArchive, _ *legacyAccountReviewInput) {
			a.Users[0].Facts["disabled"] = json.RawMessage(`1`)
		},
	} {
		t.Run(name, func(t *testing.T) {
			s, a, i, now := legacyPlanFixture(t)
			mutate(a, &i)
			if _, err := buildLegacyAccountReview(&s, a, i, now); err == nil {
				t.Fatal("malformed plan input accepted")
			}
		})
	}
}
