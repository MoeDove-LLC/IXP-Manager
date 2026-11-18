// Pure local contract tests. No application server, SMTP, browser or real archive.
import assert from "node:assert/strict";
import {
  accountReviewBody,
  accountReviewRequest,
  checkedAccountReview,
  parseAccountArchive,
} from "../src/lib/account-archive.ts";
import { checkedInviteInfo, invitationConsent } from "../src/lib/invitation-review.ts";

const sourceId = "9007199254740993", customerId = "9007199254740995";
const source = {
  version: 1, kind: "ixp-manager-account-sources", source: "ixp-manager-legacy",
  access: "admin", sourceTimezone: "unknown", checksum: "a".repeat(64),
  users: [
    { legacyId: sourceId, facts: { id: sourceId, email: "first@example.test", disabled: false, custid: customerId } },
    { legacyId: "2", facts: { id: "2", name: "Unselected evidence", email: "other@example.test", disabled: false } },
  ],
  memberships: [
    { legacyId: "12", sourceUserId: sourceId, sourceCustomerId: customerId, facts: { id: "12", user_id: sourceId, customer_id: customerId, privs: 1 } },
    { legacyId: "13", sourceUserId: sourceId, sourceCustomerId: "8", facts: { id: "13", user_id: sourceId, customer_id: "8", privs: 2 } },
    { legacyId: "14", sourceUserId: "2", sourceCustomerId: "8", facts: { id: "14", user_id: "2", customer_id: "8", privs: 1 } },
  ],
};
const raw = JSON.stringify(source).replace('"privs":1', '"privs":1e0');
const loaded = parseAccountArchive(raw);
assert.equal(loaded.archive.users[0].legacyId, sourceId);
assert.equal(loaded.archive.memberships[0].sourceCustomerId, customerId);
assert.equal(loaded.users.get(sourceId).facts.custid, customerId);
assert.deepEqual(loaded.byUser.get(sourceId).map(row => row.legacyId), ["12", "13"]);

const selections = [{ sourceUserId: sourceId, sourceMembershipIds: ["12", "13"] }];
const request = accountReviewRequest(loaded, selections, { [customerId]: "member-first", "8": "member-second" }, {});
selections[0].sourceMembershipIds.pop();
assert.deepEqual(request.selected[0].sourceMembershipIds, ["12", "13"], "review request must be an immutable selection snapshot");
const body = accountReviewBody(request, { revision: "b".repeat(64), currentPassword: 'synthetic-"password', code: "123456" });
assert.ok(body.includes('"privs":1e0'), "original RawMessage number spelling must survive transport");
assert.ok(body.includes('"Unselected evidence"'), "send the complete source, not a reduced archive that hides collisions");
const parsedBody = JSON.parse(body);
assert.equal(parsedBody.archive.users[0].legacyId, sourceId);
assert.equal(parsedBody.organizations[0].sourceCustomerId, customerId);
assert.equal(parsedBody.currentPassword, 'synthetic-"password');
assert.deepEqual(parsedBody.selected[0].sourceMembershipIds, ["12", "13"]);

for (const mutate of [
  data => { data.users[0].legacyId = Number(sourceId); },
  data => { data.users[0].facts.id = Number(sourceId); },
  data => { data.memberships[0].sourceCustomerId = Number(customerId); },
  data => { data.users[0].facts.passwordHash = "never-transfer"; },
  data => { data.users.push(data.users[0]); },
  data => { data.memberships[0].facts.user_id = "2"; },
]) {
  const modified = structuredClone(source);
  mutate(modified);
  assert.throws(() => parseAccountArchive(JSON.stringify(modified)));
}
assert.throws(() => accountReviewRequest(loaded, Array.from({ length: 26 }, () => ({ sourceUserId: sourceId, sourceMembershipIds: ["12"] })), {}, {}));
assert.throws(() => accountReviewRequest(loaded, [{ sourceUserId: sourceId, sourceMembershipIds: ["14"] }], {}, {}));
assert.throws(() => accountReviewRequest(loaded, [{ sourceUserId: sourceId, sourceMembershipIds: Array.from({ length: 33 }, (_, i) => String(i + 1)) }], {}, {}));
assert.throws(() => accountReviewRequest(loaded, [{ sourceUserId: sourceId, sourceMembershipIds: ["12"], existingUserId: "unreviewed-account" }], {}, {}));
assert.equal(accountReviewRequest(loaded, [{ sourceUserId: sourceId, sourceMembershipIds: ["12"], existingUserId: "reviewed-account" }], {}, { [sourceId]: { id: "reviewed-account", name: "Reviewed", email: "first@example.test" } }).selected[0].existingUserId, "reviewed-account");

const review = {
  archiveChecksum: source.checksum, revision: "b".repeat(64),
  accounts: [{ sourceUserId: sourceId, name: "First", email: "first@example.test", status: "ready", findings: [], organizations: [
    { sourceMembershipId: "12", sourceCustomerId: customerId, memberId: "member-first", memberName: "First organization", memberAdmin: false },
    { sourceMembershipId: "13", sourceCustomerId: "8", memberId: "member-second", memberName: "Second organization", memberAdmin: true, currentMemberRole: "member" },
  ] }],
};
assert.deepEqual(checkedAccountReview(review, request, source.checksum), review);
const foreign = structuredClone(review); foreign.accounts[0].organizations[0].memberId = "other-member";
assert.throws(() => checkedAccountReview(foreign, request, source.checksum));
const incomplete = structuredClone(review); incomplete.accounts[0].organizations.pop();
assert.throws(() => checkedAccountReview(incomplete, request, source.checksum));
const held = structuredClone(review); held.revision = ""; held.accounts[0].status = "held"; held.accounts[0].findings = ["membership-already-satisfied"]; held.accounts[0].organizations[1].currentMemberRole = "member-admin";
assert.equal(checkedAccountReview(held, request, source.checksum).accounts[0].status, "held");

const ordinary = { email: "recipient@example.test", memberId: "first", memberName: "First", expiresAt: "2030-01-01T00:00:00Z", accountExists: false };
assert.deepEqual(invitationConsent(ordinary), {}, "ordinary invitations keep their old acceptance contract");
const info = { ...ordinary, organizations: [{ memberId: "first", memberName: "Primary", memberAdmin: true }, { memberId: "second", memberName: "Another", memberAdmin: false }], organizationRevision: "c".repeat(64) };
assert.deepEqual(checkedInviteInfo(info).organizations.map(org => org.memberId), ["first", "second"], "recipient organization order must not be sorted or shortened");
assert.deepEqual(invitationConsent(info), { organizationRevision: "c".repeat(64) });
assert.deepEqual(invitationConsent({ ...info, accountExists: true }), { organizationRevision: "c".repeat(64) });
assert.throws(() => checkedInviteInfo({ ...info, organizationRevision: undefined }));
assert.throws(() => checkedInviteInfo({ ...info, organizations: [info.organizations[1], info.organizations[0]] }));
assert.throws(() => checkedInviteInfo({ ...info, organizations: [info.organizations[0], info.organizations[0]] }));
console.log("Account archive exact IDs/raw JSON/selection and candidate bounds/response binding/recipient consent contracts passed");
