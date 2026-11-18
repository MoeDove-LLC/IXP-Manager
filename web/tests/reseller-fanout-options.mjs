// Pure inventory-selection contracts, with no API, device or role mutations.
import assert from "node:assert/strict";
import {
  fanoutAccessOwners,
  fanoutDirectReseller,
  fanoutDraftProblem,
  fanoutPortLabel,
  fanoutPortOwner,
  fanoutPortProblem,
  fanoutReferencesReady,
} from "../src/lib/reseller-fanouts.ts";

const refs = {
  members: [
    { id: "member", name: "Downstream", resellerId: "reseller" },
    { id: "reseller", name: "Direct reseller", isReseller: true, resellerId: "grandparent" },
    { id: "grandparent", name: "Upstream reseller", isReseller: true },
    { id: "other", name: "Other member" },
  ],
  switches: [{ id: "sw-a", name: "Left switch" }, { id: "sw-b", name: "Right switch" }],
  ports: [
    { id: "left", name: "xe-1", switchId: "sw-a" },
    { id: "lag", name: "xe-2", switchId: "sw-a", memberId: "member" },
    { id: "owner-only", name: "xe-3", switchId: "sw-a", memberId: "member" },
    { id: "mixed", name: "xe-4", switchId: "sw-a" },
    { id: "right", name: "xe-5", switchId: "sw-b", memberId: "reseller" },
    { id: "unowned", name: "xe-6", switchId: "sw-b" },
    { id: "ancestor-owned", name: "xe-7", switchId: "sw-b", memberId: "grandparent" },
    { id: "connected", name: "xe-8", switchId: "sw-b", memberId: "reseller" },
    { id: "core", name: "xe-9", switchId: "sw-b", coreReserved: true },
    { id: "flat-core", name: "xe-10", switchId: "sw-b" },
    { id: "claimed", name: "xe-11", switchId: "sw-b" },
    { id: "patch-other", name: "xe-12", switchId: "sw-b" },
    { id: "patch-matching", name: "xe-13", switchId: "sw-b" },
  ],
  connections: [
    { id: "c1", memberId: "member", portId: "left", vlanId: "v1" },
    { id: "c2", memberId: "member", portIds: ["left", "lag"], vlanId: "v2" },
    { id: "c3", memberId: "member", portId: "mixed" },
    { id: "c4", memberId: "other", portId: "mixed", status: "disabled" },
    { id: "c5", memberId: "reseller", portId: "connected", status: "disabled" },
  ],
  "core-links": [{ id: "old-core", portAId: "flat-core", portBId: "elsewhere" }],
  "patch-ports": [
    { id: "patch-a", switchPortId: "patch-other", memberId: "other", status: "reserved" },
    { id: "patch-b", switchPortId: "patch-matching", memberId: "reseller", status: "connected" },
  ],
  "reseller-fanouts": [{ id: "taken", memberId: "other", resellerId: "reseller", peeringPortId: "unused-left", fanoutPortId: "claimed" }],
};
const before = structuredClone(refs);
assert.equal(fanoutReferencesReady(refs), true);
assert.equal(fanoutReferencesReady({ ...refs, "core-links": undefined }), false);
assert.equal(fanoutDirectReseller("member", refs).id, "reseller", "do not traverse to an ancestor reseller");
assert.equal(fanoutDirectReseller("other", refs), undefined);
assert.deepEqual(fanoutAccessOwners("left", refs), ["member"], "same-member reuse across VLAN records remains one owner");
assert.equal(fanoutPortProblem("peering", "left", "member", "reseller", refs), "");
assert.equal(fanoutPortProblem("peering", "lag", "member", "reseller", refs), "");
assert.ok(fanoutPortProblem("peering", "owner-only", "member", "reseller", refs), "ports.memberId is not a real access connection");
assert.ok(fanoutPortProblem("peering", "mixed", "member", "reseller", refs), "disabled foreign connection still owns its inventory assignment");
for (const id of ["right", "unowned", "patch-matching"])
  assert.equal(fanoutPortProblem("fanout", id, "member", "reseller", refs), "", id);
for (const id of ["ancestor-owned", "connected", "core", "flat-core", "claimed", "patch-other", "left"])
  assert.ok(fanoutPortProblem("fanout", id, "member", "reseller", refs), id);
assert.equal(fanoutPortLabel("left", refs), "Left switch / xe-1");
assert.equal(fanoutPortOwner("right", refs), "Direct reseller");
assert.equal(fanoutPortOwner("unowned", refs), "未指定", "do not infer owner for an unassigned reseller-side port");

const draft = { memberId: "member", resellerId: "reseller", peeringPortId: "left", fanoutPortId: "right" };
assert.equal(fanoutDraftProblem(draft, refs), "");
assert.ok(fanoutDraftProblem({ ...draft, resellerId: "grandparent" }, refs));
assert.ok(fanoutDraftProblem({ ...draft, fanoutPortId: "left" }, refs));
assert.ok(fanoutDraftProblem({ ...draft, peeringPortId: "" }, refs));
const editing = structuredClone(refs);
editing["reseller-fanouts"].push({ id: "self", ...draft });
assert.ok(fanoutDraftProblem(draft, editing), "a new relation must not take an occupied pair");
assert.equal(fanoutDraftProblem(draft, editing, "self"), "", "editing may retain its own pair");
assert.equal(fanoutDraftProblem({ ...draft, fanoutPortId: "unowned" }, editing, "self"), "", "replacement retains left ownership and selects a free right side");
assert.deepEqual(refs, before, "option filtering must not change port roles, owners, VLANs or connections");
console.log("Reseller fanout direct-owner/access/LAG/occupancy/patch/core and nonmutating selection contracts passed");
