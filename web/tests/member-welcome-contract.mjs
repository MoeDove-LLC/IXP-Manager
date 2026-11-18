// Local contract assertions only: no application, SMTP or network requests.
import assert from "node:assert/strict";
import {
  checkedWelcomeContext,
  checkedWelcomeReview,
  copyWelcomeDraft,
  validateWelcomeDraft,
  welcomeBytes,
  welcomeQueueBody,
} from "../src/lib/member-welcome-email.ts";

const options = [
  { id: "member", label: "Member NOC", email: "noc@example.test", kind: "member" },
  { id: "contact", label: "Member contact", email: "contact@example.test", kind: "contact" },
  { id: "account", label: "Member account", email: "account@example.test", kind: "account" },
  { id: "operations", label: "IXP support", email: "support@example.test", kind: "operations" },
];
const draft = {
  revision: "a".repeat(64), to: ["noc@example.test"], cc: ["contact@example.test"],
  bcc: ["support@example.test"], subject: "  Welcome  ", text: "Line one\r\nLine two\rLine three",
};
const context = {
  memberId: "member-a", memberName: "Member A", deliveryEnabled: true, from: "ixp@example.test",
  draft, recipientOptions: options, blockingReasons: [],
};
assert.equal(checkedWelcomeContext(context, "member-a"), context);
assert.throws(() => checkedWelcomeContext(context, "other-member"));
assert.throws(() => checkedWelcomeContext({ ...context, recipientOptions: [...options, options[0]] }, "member-a"));
validateWelcomeDraft(draft, options);
validateWelcomeDraft({ ...draft, to: ["account@example.test"] }, options);

const review = {
  reviewToken: "same-proof-token", expiresAt: "2030-01-01T00:00:00Z",
  draft: { ...draft, subject: "Welcome", text: "Line one\nLine two\nLine three" },
  from: context.from, deliveryEnabled: true,
};
assert.equal(checkedWelcomeReview(review, draft, context), review);
for (const patch of [
  { subject: "Changed after submission" },
  { text: "Different body with the same revision and recipients" },
  { revision: "b".repeat(64) },
  { to: ["account@example.test"] },
  { cc: [], bcc: ["support@example.test", "contact@example.test"] },
]) assert.throws(() => checkedWelcomeReview({ ...review, draft: { ...review.draft, ...patch } }, draft, context));

const outside = "outside@different-member.example";
assert.throws(() => validateWelcomeDraft({ ...draft, to: [outside] }, options), error => !error.message.includes(outside));
assert.throws(() => validateWelcomeDraft({ ...draft, to: [] }, options));
assert.throws(() => validateWelcomeDraft({ ...draft, to: ["support@example.test"], bcc: [] }, options));
assert.throws(() => validateWelcomeDraft({ ...draft, cc: ["NOC@EXAMPLE.TEST"] }, options));
assert.throws(() => validateWelcomeDraft({ ...draft, subject: "Injected\r\nBcc: no@example.test" }, options));
assert.equal(welcomeBytes("文".repeat(100)), 300);
validateWelcomeDraft({ ...draft, subject: "文".repeat(100), text: "文".repeat(21845) }, options);
assert.throws(() => validateWelcomeDraft({ ...draft, subject: "文".repeat(101) }, options));
assert.throws(() => validateWelcomeDraft({ ...draft, text: "文".repeat(21846) }, options));
const many = Array.from({ length: 11 }, (_, index) => ({ id: `recipient-${index}`, label: "Scoped", email: `scoped-${index}@example.test`, kind: "account" }));
assert.throws(() => validateWelcomeDraft({ ...draft, to: many.map(option => option.email), cc: [], bcc: [] }, many));

const wire = JSON.stringify(welcomeQueueBody(review));
const editing = copyWelcomeDraft(review.draft);
editing.to.push("account@example.test");
editing.subject = "A new draft must be reviewed separately";
assert.equal(JSON.stringify(welcomeQueueBody(review)), wire, "a separate editor draft cannot change the frozen retry body");
const queued = JSON.parse(wire);
assert.equal(queued.acknowledge, true);
assert.equal(queued.reviewToken, review.reviewToken);
assert.equal(queued.expiresAt, review.expiresAt);
assert.deepEqual(queued.to, review.draft.to);
assert.deepEqual(queued.bcc, review.draft.bcc);
assert.equal(queued.subject, review.draft.subject);
assert.equal(queued.text, review.draft.text);
assert.equal(checkedWelcomeReview({ ...review, deliveryEnabled: false, from: "" }, draft, { ...context, deliveryEnabled: false, from: "" }).deliveryEnabled, false, "disabled delivery still permits reviewing valid content");
console.log("Member welcome recipient scope/content binding/UTF-8 limits/canonical retry contracts passed");
