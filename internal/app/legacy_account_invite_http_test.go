package app

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/moedove/ixp-manager/internal/migrate"
	"github.com/moedove/ixp-manager/internal/notify"
)

func TestLegacyAccountInvitationReviewQueuesAndRedeemsAllReviewedOrganizations(t *testing.T) {
	staff, _ := setupClient(t)
	enableTestMail(staff.a)
	const newEmail, existingEmail = "legacy-new@example.test", "legacy-existing@example.test"
	const password, adminPassword = "legacy-invitation-fixture-password", "very-long-test-password"
	alpha := str(staff.create("members", Record{"name": "Alpha", "asn": 64551, "status": "active"}), "id")
	beta := str(staff.create("members", Record{"name": "Beta", "asn": 64552, "status": "active"}), "id")
	retained := str(staff.create("members", Record{"name": "Retained", "asn": 64553, "status": "active"}), "id")
	existingID := str(staff.create("users", Record{
		"name": "Existing member", "email": existingEmail, "password": password, "role": "member", "memberIds": []string{alpha, retained},
	}), "id")
	existing := docLoginClient(t, staff.a, existingEmail, password)
	enrollment := existing.ok(http.MethodPost, "/api/account/totp/enroll", Record{"currentPassword": password})
	codes := existing.ok(http.MethodPost, "/api/account/totp/verify", Record{"code": codeFor(str(enrollment, "secret"), time.Now())})["recoveryCodes"].([]any)
	staff.create("users", Record{"name": "Operator", "role": "operator", "email": "legacy-operator@example.test", "password": password})
	operator := docLoginClient(t, staff.a, "legacy-operator@example.test", password)
	_, issuerCodes := enableMFA(t, staff)

	const newSource, existingSource = "9007199254740993", "9007199254740995"
	archive := legacyAccountHTTPTestArchive(t, newSource, existingSource, newEmail, existingEmail)
	reviewInput := legacyAccountReviewInput{
		Archive: archive,
		Organizations: []legacyAccountOrganizationMap{
			{SourceCustomerID: "41", MemberID: alpha}, {SourceCustomerID: "42", MemberID: beta},
		},
		Selected: []legacyAccountSelection{
			{SourceUserID: newSource, SourceMembershipIDs: []string{"11", "12"}},
			{SourceUserID: existingSource, SourceMembershipIDs: []string{"21", "22"}},
		},
	}
	if reply := operator.request(http.MethodPost, legacyAccountPreviewPath, reviewInput); reply.Code != http.StatusForbidden {
		t.Fatal("operator gained access to private migration review", reply.Code)
	}
	if reply := staff.request(http.MethodPost, legacyAccountInvitePath, reviewInput); reply.Code != http.StatusPreconditionRequired {
		t.Fatal("invitation without review was not refused", reply.Code)
	}
	held := staff.ok(http.MethodPost, legacyAccountPreviewPath, reviewInput)
	if held["revision"] != "" || !strings.Contains(fmtJSON(held), "existing-account-selection-required") {
		t.Fatal("email match was not held pending explicit account review", held)
	}
	reviewInput.Selected[1].ExistingUserID = existingID
	view := staff.ok(http.MethodPost, legacyAccountPreviewPath, reviewInput)
	revision := str(view, "revision")
	if !validResourceETag(revision) || !strings.Contains(fmtJSON(view), `"currentMemberRole":"member"`) {
		t.Fatal("review did not expose explicit member-admin upgrade", view)
	}
	if err := staff.a.store.Update(func(s *State) error {
		find(s, "members", beta)["name"] = "Beta as currently reviewed"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	request := legacyAccountInvitationRequest{legacyAccountReviewInput: reviewInput, Revision: revision, CurrentPassword: adminPassword, Code: issuerCodes[0]}
	if reply := staff.request(http.MethodPost, legacyAccountInvitePath, request); reply.Code != http.StatusPreconditionFailed {
		t.Fatal("changed organization escaped review binding", reply.Code, reply.Body.String())
	}
	state := staff.a.store.Snapshot()
	if len(state.Invitations) != 0 || len(state.Outbox.Entries) != 0 || emailUser(&state, newEmail) != nil {
		t.Fatal("preview or stale confirmation changed live state")
	}
	request.Revision = str(staff.ok(http.MethodPost, legacyAccountPreviewPath, reviewInput), "revision")
	request.Code = ""
	reply := staff.request(http.MethodPost, legacyAccountInvitePath, request)
	var challenge Record
	if reply.Code != http.StatusForbidden || json.Unmarshal(reply.Body.Bytes(), &challenge) != nil || challenge["mfaRequired"] != true {
		t.Fatal("missing MFA did not return the authentication challenge", reply.Code)
	}
	request.Code = issuerCodes[0]
	queued := staff.ok(http.MethodPost, legacyAccountInvitePath, request)["items"].([]any)
	if len(queued) != 2 {
		t.Fatal("expected one queued invite per account")
	}
	if reply := staff.request(http.MethodPost, legacyAccountInvitePath, request); reply.Code != http.StatusPreconditionFailed {
		t.Fatal("batch replay did not require a fresh review", reply.Code)
	}
	state = staff.a.store.Snapshot()
	if len(state.Invitations) != 2 || len(state.Outbox.Entries) != 2 || emailUser(&state, newEmail) != nil || memberAdministrator(find(&state, "users", existingID), alpha) {
		t.Fatal("queuing invitations prematurely changed account access")
	}

	guest := &client{a: staff.a, t: t}
	newTicket := legacyAccountTicket(t, staff.a, newEmail)
	inspection := guest.ok(http.MethodPost, "/api/invitations/inspect", Record{"token": newTicket})
	newReview := str(inspection, "organizationRevision")
	if inspection["accountExists"] != false || len(inspection["organizations"].([]any)) != 2 || !validResourceETag(newReview) || strings.Contains(fmtJSON(inspection), newSource) {
		t.Fatal("recipient review did not expose exactly the intended organizations", inspection)
	}
	newAcceptance := Record{"token": newTicket, "name": "New active member", "password": password}
	if reply := guest.request(http.MethodPost, "/api/invitations/accept", newAcceptance); reply.Code != http.StatusConflict {
		t.Fatal("legacy invitation accepted before reviewing all organizations", reply.Code)
	}
	newAcceptance["organizationRevision"] = newReview
	guest.ok(http.MethodPost, "/api/invitations/accept", newAcceptance)

	existingTicket := legacyAccountTicket(t, staff.a, existingEmail)
	existingReview := str(guest.ok(http.MethodPost, "/api/invitations/inspect", Record{"token": existingTicket}), "organizationRevision")
	if reply := guest.request(http.MethodPost, "/api/invitations/accept", Record{"token": existingTicket, "organizationRevision": existingReview, "name": "Unrelated claimant", "password": password}); reply.Code != http.StatusUnauthorized {
		t.Fatal("mailbox token alone claimed a current account", reply.Code)
	}
	existing = loginClient(t, staff.a, Record{"email": existingEmail, "password": password, "code": codes[0]})
	existingAcceptance := Record{"token": existingTicket, "organizationRevision": existingReview, "currentPassword": password}
	if reply := existing.request(http.MethodPost, "/api/invitations/accept", existingAcceptance); reply.Code != http.StatusForbidden {
		t.Fatal("existing account acceptance skipped its MFA", reply.Code)
	}
	existingAcceptance["code"] = codes[1]
	existing.ok(http.MethodPost, "/api/invitations/accept", existingAcceptance)
	state = staff.a.store.Snapshot()
	newUser, oldUser := emailUser(&state, newEmail), find(&state, "users", existingID)
	if str(newUser, "role") != "member" || len(membershipIDs(newUser)) != 2 || memberAdministrator(newUser, alpha) || !memberAdministrator(newUser, beta) {
		t.Fatal("new account did not receive only the two reviewed organization roles")
	}
	if str(oldUser, "role") != "member" || len(membershipIDs(oldUser)) != 3 || !contains(membershipIDs(oldUser), retained) || !memberAdministrator(oldUser, alpha) || memberAdministrator(oldUser, beta) || !state.Security[existingID].MFAEnabled {
		t.Fatal("existing membership, role or MFA changed outside the approved grant")
	}
}

func legacyAccountHTTPTestArchive(t *testing.T, newSource, existingSource, newEmail, existingEmail string) json.RawMessage {
	t.Helper()
	text := func(value string) json.RawMessage {
		encoded, _ := json.Marshal(value)
		return encoded
	}
	account := func(id, address string) migrate.AccountArchiveUser {
		return migrate.AccountArchiveUser{LegacyID: id, Facts: migrate.NoteArchiveFacts{
			"id": text(id), "name": text(address), "email": text(address), "disabled": json.RawMessage("0"),
		}}
	}
	relationship := func(id, user, customer, role string) migrate.AccountArchiveMembership {
		return migrate.AccountArchiveMembership{LegacyID: id, SourceUserID: user, SourceCustomerID: customer, Facts: migrate.NoteArchiveFacts{
			"id": text(id), "user_id": text(user), "customer_id": text(customer), "privs": json.RawMessage(role),
		}}
	}
	archive, err := migrate.EncodeAccountArchive(migrate.AccountArchive{
		Version: 1, Kind: migrate.AccountArchiveKind, Source: "ixp-manager-legacy", Access: "admin", SourceTimezone: "unknown",
		Users: []migrate.AccountArchiveUser{account(newSource, newEmail), account(existingSource, existingEmail)},
		Memberships: []migrate.AccountArchiveMembership{
			relationship("11", newSource, "41", "1"), relationship("12", newSource, "42", "2"),
			relationship("21", existingSource, "41", "2"), relationship("22", existingSource, "42", "1"),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return archive
}

func legacyAccountTicket(t *testing.T, app *App, email string) string {
	t.Helper()
	for _, entry := range app.store.Snapshot().Outbox.Entries {
		message, err := notify.OpenMessage(app.store.securityKey, entry)
		if err != nil {
			t.Fatal(err)
		}
		if message.To != email {
			continue
		}
		for _, line := range strings.Split(message.Text, "\n") {
			if _, query, found := strings.Cut(line, "/#accept-invitation?"); found {
				values, err := url.ParseQuery(query)
				if err == nil && values.Get("token") != "" {
					return values.Get("token")
				}
			}
		}
	}
	t.Fatal("test invitation to expected recipient missing")
	return ""
}
