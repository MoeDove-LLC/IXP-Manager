package app

import (
	"context"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/moedove/ixp-manager/internal/notify"
)

func queueReviewedLegacyInvitation(t *testing.T, a *App, email string, organizations []legacyAccountOrganization) (Invitation, string) {
	t.Helper()
	issuerID := accountID(a)
	raw := token(32)
	now := time.Now().UTC()
	var invitation Invitation
	err := a.store.Update(func(s *State) error {
		invitation = Invitation{
			ID: token(16), Email: email, MemberID: organizations[0].MemberID, MemberAdmin: organizations[0].MemberAdmin,
			InvitedBy: issuerID, TokenHash: digest(raw), CreatedAt: now, ExpiresAt: now.Add(time.Hour),
			LegacyAccount: &legacyAccountInvitation{
				ArchiveChecksum: digest("fixture archive"), SourceUserID: "100", IssuerCreatedAt: str(find(s, "users", issuerID), "createdAt"),
				Organizations: organizations,
			},
		}
		if user := emailUser(s, email); user != nil {
			invitation.ExistingUserID = str(user, "id")
			invitation.Generation = s.Security[invitation.ExistingUserID].Generation
			invitation.LegacyAccount.ExistingUserCreatedAt = str(user, "createdAt")
		}
		message := notify.Message{From: a.mailConfig.From, To: email, Subject: "Reviewed fixture invitation", Text: mailLink(a.mailConfig.BaseURL, "accept-invitation", raw)}
		if _, err := s.Outbox.Enqueue("invite:"+invitation.ID, message, a.store.securityKey, now, invitation.ExpiresAt); err != nil {
			return err
		}
		s.Invitations = append(s.Invitations, invitation)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return invitation, raw
}

func legacyInvitationTestOrganization(member Record, sourceMembershipID, sourceCustomerID string, admin bool) legacyAccountOrganization {
	return legacyAccountOrganization{
		SourceMembershipID: sourceMembershipID, SourceCustomerID: sourceCustomerID, MemberID: str(member, "id"),
		MemberName: str(member, "name"), MemberCreatedAt: str(member, "createdAt"), MemberAdmin: admin,
	}
}

func TestLegacyAccountInvitationNewRecipientConsentsToEveryOrganization(t *testing.T) {
	staff, _ := setupClient(t)
	sender := enableTestMail(staff.a)
	first := staff.create("members", Record{"name": "First organization", "asn": 64530, "status": "active"})
	second := staff.create("members", Record{"name": "Second organization", "asn": 64531, "status": "active"})
	organizations := []legacyAccountOrganization{
		legacyInvitationTestOrganization(first, "10", "1", true), legacyInvitationTestOrganization(second, "11", "2", false),
	}
	firstID, secondID := organizations[0].MemberID, organizations[1].MemberID
	const email = "new-legacy@example.test"
	invitation, raw := queueReviewedLegacyInvitation(t, staff.a, email, organizations)
	delegated := staff.create("users", Record{
		"name": "Member administrator", "email": "legacy-tenant-admin@example.test", "role": "member",
		"password": "legacy-tenant-password", "memberIds": []string{firstID, secondID}, "memberAdminIds": []string{firstID, secondID},
	})
	tenant := docLoginClient(t, staff.a, str(delegated, "email"), "legacy-tenant-password")
	if items := tenant.ok(http.MethodGet, "/api/invitations", nil)["items"].([]any); len(items) != 0 {
		t.Fatal("organization administrator saw a reviewed legacy invitation")
	}
	if w := tenant.request(http.MethodDelete, "/api/invitations/"+invitation.ID, nil); w.Code != http.StatusConflict {
		t.Fatalf("organization administrator could revoke a legacy invitation: HTTP %d", w.Code)
	}
	if w := staff.request(http.MethodPost, "/api/invitations", Record{
		"email": email, "memberId": secondID, "currentPassword": "very-long-test-password",
	}); w.Code != http.StatusForbidden {
		t.Fatalf("secondary organization was omitted from pending overlap: HTTP %d", w.Code)
	}
	publicOrganizations := []any{
		map[string]any{"memberId": firstID, "memberName": first["name"], "memberAdmin": true},
		map[string]any{"memberId": secondID, "memberName": second["name"], "memberAdmin": false},
	}
	listed := staff.ok(http.MethodGet, "/api/invitations", nil)["items"].([]any)
	if len(listed) != 1 || !reflect.DeepEqual(listed[0].(map[string]any)["organizations"], publicOrganizations) {
		t.Fatalf("administrator did not see the exact reviewed organization list: %#v", listed)
	}
	anonymous := &client{a: staff.a, t: t}
	inspection := anonymous.ok(http.MethodPost, "/api/invitations/inspect", Record{"token": raw})
	if len(inspection) != 8 || inspection["accountExists"] != false || inspection["memberId"] != firstID || inspection["memberName"] != first["name"] ||
		inspection["memberAdmin"] != true || !reflect.DeepEqual(inspection["organizations"], publicOrganizations) || !validResourceETag(str(inspection, "organizationRevision")) {
		t.Fatalf("recipient consent view included wrong or excess fields: %#v", inspection)
	}
	s := staff.a.store.Snapshot()
	if emailUser(&s, email) != nil {
		t.Fatal("review or inspect created account before consent")
	}
	if err := staff.a.deliverOne(context.Background(), time.Now()); err != nil || len(sender.messages) != 1 || sender.messages[0].To != email {
		t.Fatalf("applicable invitation was not mailed to the reviewed recipient: %v", err)
	}
	accept := Record{"token": raw, "name": "New legacy recipient", "password": "new-legacy-account-password"}
	if w := anonymous.request(http.MethodPost, "/api/invitations/accept", accept); w.Code != http.StatusConflict {
		t.Fatalf("legacy invitation accepted without consent revision: HTTP %d", w.Code)
	}
	if err := staff.a.store.Update(func(s *State) error {
		find(s, "members", secondID)["name"] = "Renamed second organization"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	accept["organizationRevision"] = inspection["organizationRevision"]
	if w := anonymous.request(http.MethodPost, "/api/invitations/accept", accept); w.Code != http.StatusConflict {
		t.Fatalf("legacy invitation accepted an out-of-date secondary organization name: HTTP %d", w.Code)
	}
	updated := anonymous.ok(http.MethodPost, "/api/invitations/inspect", Record{"token": raw})
	if updated["organizationRevision"] == inspection["organizationRevision"] || updated["organizations"].([]any)[1].(map[string]any)["memberName"] != "Renamed second organization" {
		t.Fatal("reinspection did not reflect the renamed organization")
	}
	accept["organizationRevision"] = updated["organizationRevision"]
	anonymous.ok(http.MethodPost, "/api/invitations/accept", accept)
	s = staff.a.store.Snapshot()
	user := emailUser(&s, email)
	if str(user, "role") != "member" || str(user, "memberId") != firstID || !slices.Equal(membershipIDs(user), []string{firstID, secondID}) ||
		!memberAdministrator(user, firstID) || memberAdministrator(user, secondID) {
		t.Fatal("accepted invitation did not grant every reviewed role")
	}
	var audited []string
	for _, entry := range s.Audit {
		if entry.Action == "accept-invitation" && entry.Actor == str(user, "id") {
			audited = append(audited, entry.ResourceID)
		}
	}
	if !slices.Equal(audited, []string{firstID, secondID}) {
		t.Fatalf("not every accepted organization was audited: %v", audited)
	}
	if w := anonymous.request(http.MethodPost, "/api/invitations/inspect", Record{"token": raw}); w.Code != http.StatusBadRequest {
		t.Fatalf("accepted legacy token was reusable: HTTP %d", w.Code)
	}
}

func TestLegacyAccountInvitationExistingRecipientPreservesAccount(t *testing.T) {
	staff, _ := setupClient(t)
	enableTestMail(staff.a)
	first := staff.create("members", Record{"name": "First", "asn": 64530, "status": "active"})
	second := staff.create("members", Record{"name": "Second", "asn": 64531, "status": "active"})
	extra := staff.create("members", Record{"name": "Existing default", "asn": 64532, "status": "active"})
	const email, password = "existing-legacy@example.test", "very-long-test-password"
	firstID, secondID, extraID := str(first, "id"), str(second, "id"), str(extra, "id")
	recipient := staff.create("users", Record{"name": "Existing account", "email": email, "password": password, "role": "member",
		"memberId": extraID, "memberIds": []string{extraID, firstID}, "memberAdminIds": []string{extraID}})
	c := docLoginClient(t, staff.a, email, password)
	_, codes := enableMFA(t, c)
	organizations := []legacyAccountOrganization{
		legacyInvitationTestOrganization(first, "10", "1", true), legacyInvitationTestOrganization(second, "11", "2", false),
	}
	organizations[0].CurrentMemberRole = "member"
	_, raw := queueReviewedLegacyInvitation(t, staff.a, email, organizations)
	before := staff.a.store.Snapshot()
	id := str(recipient, "id")
	for label, mutate := range map[string]func(*State){
		"generation changed": func(s *State) { invalidateSecurity(s, id) },
		"account replaced": func(s *State) {
			find(s, "users", id)["createdAt"] = "2020-01-01T00:00:00Z"
		},
		"mailbox changed": func(s *State) { find(s, "users", id)["email"] = "changed@example.test" },
		"global role changed": func(s *State) {
			find(s, "users", id)["role"] = "viewer"
		},
		"current reviewed membership changed": func(s *State) {
			find(s, "users", id)["memberAdminIds"] = []any{extraID, firstID}
		},
	} {
		t.Run(label, func(t *testing.T) {
			s := cloneState(before)
			mutate(&s)
			if _, err := findInvitation(&s, raw, time.Now()); err == nil || mailStillApplicable(&s, s.Outbox.Entries[0], time.Now()) {
				t.Fatal("stale existing-account invitation remained inspectable or deliverable")
			}
		})
	}
	anonymous := &client{a: staff.a, t: t}
	inspection := anonymous.ok(http.MethodPost, "/api/invitations/inspect", Record{"token": raw})
	request := Record{"token": raw, "currentPassword": password, "code": codes[0], "organizationRevision": inspection["organizationRevision"]}
	if w := anonymous.request(http.MethodPost, "/api/invitations/accept", request); w.Code != http.StatusUnauthorized {
		t.Fatalf("existing recipient could accept without its session: HTTP %d", w.Code)
	}
	if w := staff.request(http.MethodPost, "/api/invitations/accept", request); w.Code != http.StatusForbidden {
		t.Fatalf("a different signed-in account accepted: HTTP %d", w.Code)
	}
	if w := c.request(http.MethodPost, "/api/invitations/accept", Record{"token": raw, "currentPassword": password, "organizationRevision": inspection["organizationRevision"]}); w.Code != http.StatusForbidden {
		t.Fatalf("MFA was not required for acceptance: HTTP %d", w.Code)
	}
	c.ok(http.MethodPost, "/api/invitations/accept", request)
	after := staff.a.store.Snapshot()
	user := find(&after, "users", id)
	if str(user, "role") != "member" || str(user, "memberId") != extraID || !slices.Equal(membershipIDs(user), []string{extraID, firstID, secondID}) ||
		!memberAdministrator(user, extraID) || !memberAdministrator(user, firstID) || memberAdministrator(user, secondID) ||
		str(user, "createdAt") != str(recipient, "createdAt") || str(user, "passwordHash") != str(find(&before, "users", id), "passwordHash") {
		t.Fatal("existing identity, role, default, credentials or memberships changed incorrectly")
	}
	security := after.Security[id]
	if !security.MFAEnabled || security.Secret != before.Security[id].Secret || security.Generation != before.Security[id].Generation+1 {
		t.Fatal("acceptance failed to preserve MFA and rotate old authorities")
	}
	if w := c.request(http.MethodGet, "/api/session", nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("old session remained valid after grants: HTTP %d", w.Code)
	}
	loginClient(t, staff.a, Record{"email": email, "password": password, "code": codes[1]})
}

func TestLegacyAccountInvitationStaleSecondaryOrganizationBlocksDeliveryAndAcceptance(t *testing.T) {
	staff, _ := setupClient(t)
	sender := enableTestMail(staff.a)
	first := staff.create("members", Record{"name": "First", "asn": 64530, "status": "active"})
	second := staff.create("members", Record{"name": "Second", "asn": 64531, "status": "active"})
	organizations := []legacyAccountOrganization{
		legacyInvitationTestOrganization(first, "10", "1", true), legacyInvitationTestOrganization(second, "11", "2", false),
	}
	const email = "stale-legacy@example.test"
	invitation, raw := queueReviewedLegacyInvitation(t, staff.a, email, organizations)
	original := staff.a.store.Snapshot()
	for label, mutate := range map[string]func(*State){
		"secondary suspended": func(s *State) { find(s, "members", str(second, "id"))["status"] = "suspended" },
		"secondary replaced":  func(s *State) { find(s, "members", str(second, "id"))["createdAt"] = "2020-01-01T00:00:00Z" },
		"issuer replaced":     func(s *State) { find(s, "users", invitation.InvitedBy)["createdAt"] = "2020-01-01T00:00:00Z" },
		"issuer role removed": func(s *State) { find(s, "users", invitation.InvitedBy)["role"] = "viewer" },
		"recipient now exists": func(s *State) {
			s.Resources["users"] = append(s.Resources["users"], Record{"id": "after-issuance", "email": email, "role": "member"})
		},
	} {
		t.Run(label, func(t *testing.T) {
			s := cloneState(original)
			mutate(&s)
			if _, err := findInvitation(&s, raw, time.Now()); err == nil || mailStillApplicable(&s, s.Outbox.Entries[0], time.Now()) {
				t.Fatal("stale invitation remained inspectable or deliverable")
			}
		})
	}
	if err := staff.a.store.Update(func(s *State) error {
		find(s, "members", str(second, "id"))["status"] = "suspended"
		s.Outbox.Entries[0].Status = "failed"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if w := staff.request(http.MethodPost, "/api/notifications/"+original.Outbox.Entries[0].ID+"/retry", nil); w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "no longer applicable") {
		t.Fatalf("stale legacy message was retried: HTTP %d %s", w.Code, w.Body.String())
	}
	if err := staff.a.store.Update(func(s *State) error {
		s.Outbox.Entries[0].Status = "pending"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	anonymous := &client{a: staff.a, t: t}
	if w := anonymous.request(http.MethodPost, "/api/invitations/accept", Record{"token": raw, "name": "Blocked", "password": "not-granted-password"}); w.Code != http.StatusBadRequest {
		t.Fatalf("partial acceptance after secondary organization went stale: HTTP %d", w.Code)
	}
	if err := staff.a.deliverOne(context.Background(), time.Now()); err != nil {
		t.Fatal(err)
	}
	state := staff.a.store.Snapshot()
	if emailUser(&state, email) != nil || len(sender.messages) != 0 || state.Outbox.Entries[0].Status != "expired" {
		t.Fatal("stale secondary organization allowed account creation or mail delivery")
	}
	staff.ok(http.MethodDelete, "/api/invitations/"+invitation.ID, nil)
	state = staff.a.store.Snapshot()
	if !state.Invitations[0].Revoked {
		t.Fatal("global administrator could not close stale invitation")
	}
}
