export const accountArchiveLimit = 32 * 1024 * 1024;
export const accountRequestLimit = 33 * 1024 * 1024;
export const accountSelectionLimit = 25;
export const accountMembershipLimit = 32;

type Scalar = string | number | boolean | null;
export type SourceAccount = {
  legacyId: string;
  facts: Record<string, Scalar>;
};
export type SourceAccountMembership = {
  legacyId: string;
  sourceUserId: string;
  sourceCustomerId: string;
  facts: Record<string, Scalar>;
};
export type AccountArchive = {
  version: 1;
  kind: "ixp-manager-account-sources";
  source: "ixp-manager-legacy";
  access: "admin";
  sourceTimezone: string;
  checksum: string;
  users: SourceAccount[];
  memberships: SourceAccountMembership[];
};
export type LoadedAccountArchive = {
  raw: string;
  archive: AccountArchive;
  users: Map<string, SourceAccount>;
  memberships: Map<string, SourceAccountMembership>;
  byUser: Map<string, SourceAccountMembership[]>;
};
export type AccountSelection = {
  sourceUserId: string;
  sourceMembershipIds: string[];
  existingUserId?: string;
};
export type AccountOrganizationMap = { sourceCustomerId: string; memberId: string };
export type AccountReviewRequest = {
  rawArchive: string;
  organizations: AccountOrganizationMap[];
  selected: AccountSelection[];
};
export type ExistingAccountCandidate = { id: string; name: string; email: string };
export type ReviewedAccount = {
  sourceUserId: string;
  name: string;
  email: string;
  status: "ready" | "held";
  findings: string[];
  existingCandidate?: ExistingAccountCandidate;
  organizations: {
    sourceMembershipId: string;
    sourceCustomerId: string;
    memberId: string;
    memberName: string;
    memberAdmin: boolean;
    currentMemberRole?: "member" | "member-admin";
  }[];
};
export type AccountReview = {
  archiveChecksum: string;
  accounts: ReviewedAccount[];
  revision: string;
};

const ownObject = (v: unknown): v is Record<string, unknown> =>
  !!v && typeof v === "object" && !Array.isArray(v);
const sourceID = (v: unknown): v is string =>
  typeof v === "string" && /^[1-9][0-9]{0,18}$/.test(v) &&
  (v.length < 19 || v <= "9223372036854775807");
const currentID = (v: unknown): v is string =>
  typeof v === "string" && /^[A-Za-z0-9_-]{1,128}$/.test(v);
const exactKeys = (v: Record<string, unknown>, keys: string[]) =>
  Object.keys(v).length === keys.length && keys.every(key => Object.hasOwn(v, key));
function checkFacts(value: unknown, allowed: string[]) {
  if (!ownObject(value) || Object.entries(value).some(([key, v]) =>
    !allowed.includes(key) ||
    !(v === null || typeof v === "boolean" ||
      (typeof v === "number" && Number.isFinite(v)) ||
      (typeof v === "string" && new TextEncoder().encode(v).length <= 4096)),
  )) throw new Error("归档包含无法识别的来源字段，请使用离线工具重新导出。");
}

// The parsed object is used for bounded local selection only. Keep original JSON
// for transport: RawMessage facts may contain significant number/escape spelling.
export function parseAccountArchive(raw: string): LoadedAccountArchive {
  if (new TextEncoder().encode(raw).length > accountArchiveLimit)
    throw new Error("账号归档不能超过 32 MiB。");
  let parsed: unknown;
  try { parsed = JSON.parse(raw); }
  catch { throw new Error("请选择离线工具生成的有效账号归档 JSON。"); }
  if (!ownObject(parsed) ||
    !exactKeys(parsed, ["version", "kind", "source", "access", "sourceTimezone", "checksum", "users", "memberships"]) ||
    parsed.version !== 1 || parsed.kind !== "ixp-manager-account-sources" ||
    parsed.source !== "ixp-manager-legacy" || parsed.access !== "admin" ||
    typeof parsed.sourceTimezone !== "string" || !parsed.sourceTimezone ||
    typeof parsed.checksum !== "string" || !/^[a-f0-9]{64}$/.test(parsed.checksum) ||
    !Array.isArray(parsed.users) || !Array.isArray(parsed.memberships) ||
    parsed.users.length + parsed.memberships.length === 0 ||
    parsed.users.length + parsed.memberships.length > 100000)
    throw new Error("账号归档格式或记录数不符合要求。");
  const users = new Map<string, SourceAccount>();
  const memberships = new Map<string, SourceAccountMembership>();
  const byUser = new Map<string, SourceAccountMembership[]>();
  for (const user of parsed.users) {
    if (!ownObject(user) || !exactKeys(user, ["legacyId", "facts"]) ||
      !sourceID(user.legacyId) || users.has(user.legacyId))
      throw new Error("旧账号 ID 必须是唯一、精确的十进制字符串。");
    checkFacts(user.facts, ["id", "username", "name", "email", "disabled", "custid", "created", "created_at"]);
    if ((user.facts as Record<string, unknown>).id !== user.legacyId)
      throw new Error("旧账号的来源 ID 不一致。");
    users.set(user.legacyId, user as SourceAccount);
  }
  for (const membership of parsed.memberships) {
    if (!ownObject(membership) || !exactKeys(membership, ["legacyId", "sourceUserId", "sourceCustomerId", "facts"]) ||
      !sourceID(membership.legacyId) || !sourceID(membership.sourceUserId) ||
      !sourceID(membership.sourceCustomerId) || memberships.has(membership.legacyId))
      throw new Error("旧组织关系 ID 必须是唯一、精确的十进制字符串。");
    checkFacts(membership.facts, ["id", "user_id", "customer_id", "privs", "created_at"]);
    const facts = membership.facts as Record<string, unknown>;
    if (facts.id !== membership.legacyId || facts.user_id !== membership.sourceUserId || facts.customer_id !== membership.sourceCustomerId)
      throw new Error("旧组织关系中的来源 ID 不一致。");
    const row = membership as SourceAccountMembership;
    memberships.set(row.legacyId, row);
    const group = byUser.get(row.sourceUserId) ?? [];
    group.push(row);
    byUser.set(row.sourceUserId, group);
  }
  return { raw, archive: parsed as AccountArchive, users, memberships, byUser };
}

export const sourceAccountName = (user: SourceAccount) =>
  [user.facts.name, user.facts.email, user.facts.username].find(
    value => typeof value === "string" && value.trim(),
  ) as string | undefined ?? `旧账号 #${user.legacyId}`;
export const sourceAccountEmail = (user: SourceAccount) =>
  typeof user.facts.email === "string" ? user.facts.email : "未记录邮箱";

export function accountReviewRequest(
  loaded: LoadedAccountArchive,
  selected: AccountSelection[],
  organizationChoices: Record<string, string>,
  candidates: Record<string, ExistingAccountCandidate>,
): AccountReviewRequest {
  if (selected.length < 1 || selected.length > accountSelectionLimit)
    throw new Error("每批请选择 1 至 25 个旧账号。");
  const ids = new Set<string>(), customers = new Set<string>();
  const selection = selected.map(account => {
    if (!loaded.users.has(account.sourceUserId) || ids.has(account.sourceUserId))
      throw new Error("所选旧账号不存在或重复。");
    ids.add(account.sourceUserId);
    if (!account.sourceMembershipIds.length || account.sourceMembershipIds.length > accountMembershipLimit ||
      new Set(account.sourceMembershipIds).size !== account.sourceMembershipIds.length)
      throw new Error("每个账号须明确选择 1 至 32 条组织关系。");
    for (const id of account.sourceMembershipIds) {
      const membership = loaded.memberships.get(id);
      if (!membership || membership.sourceUserId !== account.sourceUserId)
        throw new Error("所选组织关系不属于该旧账号。");
      customers.add(membership.sourceCustomerId);
    }
    if (account.existingUserId && account.existingUserId !== candidates[account.sourceUserId]?.id)
      throw new Error("现有账号关联须来自本次审核返回的候选，请取消旧关联后重新核对。");
    return {
      sourceUserId: account.sourceUserId,
      sourceMembershipIds: [...account.sourceMembershipIds],
      ...(account.existingUserId ? { existingUserId: account.existingUserId } : {}),
    };
  });
  const organizations: AccountOrganizationMap[] = [];
  for (const sourceCustomerId of customers) {
    const memberId = organizationChoices[sourceCustomerId];
    if (memberId) {
      if (!currentID(memberId)) throw new Error("请选择有效的当前组织。");
      organizations.push({ sourceCustomerId, memberId });
    }
  }
  if (organizations.length > 1000) throw new Error("组织关联超过 1000 项上限。");
  return { rawArchive: loaded.raw, selected: selection, organizations };
}

export function accountReviewBody(
  request: AccountReviewRequest,
  activation?: { revision: string; currentPassword: string; code?: string },
) {
  const fields = {
    organizations: request.organizations,
    selected: request.selected,
    ...(activation ?? {}),
  };
  const serialized = JSON.stringify(fields);
  const body = `{"archive":${request.rawArchive},${serialized.slice(1)}`;
  if (new TextEncoder().encode(body).length > accountRequestLimit)
    throw new Error("审核请求超过 33 MiB，请减小归档或本批选择。");
  return body;
}

export function checkedAccountReview(
  response: AccountReview,
  request: AccountReviewRequest,
  checksum: string,
): AccountReview {
  const selected = new Map(request.selected.map(item => [item.sourceUserId, item]));
  const mappings = new Map(request.organizations.map(item => [item.sourceCustomerId, item.memberId]));
  const seen = new Set<string>();
  if (!response || response.archiveChecksum !== checksum || typeof response.revision !== "string" || (response.revision !== "" && !/^[a-f0-9]{64}$/.test(response.revision)) ||
    !Array.isArray(response.accounts) || response.accounts.length !== selected.size)
    throw new Error("审核响应与当前归档或账号选择不一致。");
  for (const account of response.accounts) {
    const choice = selected.get(account.sourceUserId);
    if (!choice || seen.has(account.sourceUserId) || typeof account.name !== "string" ||
      typeof account.email !== "string" || !["ready", "held"].includes(account.status) ||
      !Array.isArray(account.findings) || !account.findings.every(v => typeof v === "string") ||
      !Array.isArray(account.organizations))
      throw new Error("审核账号信息不完整，请重新读取。");
    seen.add(account.sourceUserId);
    const candidate = account.existingCandidate;
    if (candidate && (!currentID(candidate.id) || typeof candidate.name !== "string" || typeof candidate.email !== "string"))
      throw new Error("现有账号候选信息不完整。");
    const relationships = new Set<string>();
    for (const org of account.organizations) {
      if (!choice.sourceMembershipIds.includes(org.sourceMembershipId) || relationships.has(org.sourceMembershipId) ||
        mappings.get(org.sourceCustomerId) !== org.memberId || typeof org.memberName !== "string" ||
        typeof org.memberAdmin !== "boolean" || (org.currentMemberRole !== undefined && !["member", "member-admin"].includes(org.currentMemberRole)))
        throw new Error("审核组织关系与已选择的关联不一致。");
      relationships.add(org.sourceMembershipId);
    }
    if (account.status === "ready" && relationships.size !== choice.sourceMembershipIds.length)
      throw new Error("可激活账号缺少已选择的组织关系。");
  }
  if (response.revision && response.accounts.some(account => account.status !== "ready"))
    throw new Error("审核状态与激活凭据不一致。");
  return response;
}

const findings: Record<string, string> = {
  "name-unusable": "旧姓名不完整，新账号需由本人填写姓名。",
  "source-user-unresolved": "找不到对应的旧账号记录。",
  "source-state-disabled": "旧账号已停用，不能直接发送激活邀请。",
  "source-state-unknown": "无法确认旧账号是否启用。",
  "source-email-unusable": "旧邮箱缺失或格式无效。",
  "source-email-collision": "归档中多个旧账号使用同一邮箱。",
  "existing-account-selection-required": "请显式核对并关联现有账号，然后重新审核。",
  "existing-account-mismatch": "所选现有账号与当前候选不一致。",
  "existing-account-not-member": "该邮箱属于工作人员账号，不能通过此流程关联。",
  "existing-account-entity-unproven": "现有账号身份无法核实。",
  "current-email-collision": "当前系统中存在邮箱冲突。",
  "legacy-invitation-already-pending": "该旧账号已有待接受的激活邀请。",
  "source-membership-unresolved": "找不到所选旧组织关系。",
  "source-membership-user-mismatch": "所选关系不属于这个旧账号。",
  "source-membership-duplicate": "旧账号存在重复组织关系。",
  "source-privilege-public": "旧公共访问权限不能转换为成员邀请。",
  "source-privilege-superuser": "旧超级用户权限须单独人工处理。",
  "source-privilege-unknown": "无法确认旧组织权限。",
  "organization-mapping-required": "请关联旧组织与当前组织。",
  "target-organization-duplicate": "同一账号的多个关系指向同一当前组织。",
  "target-organization-unresolved": "当前组织不存在。",
  "target-organization-inactive": "当前组织尚未激活。",
  "target-organization-entity-unproven": "当前组织身份无法核实。",
  "membership-already-satisfied": "现有账号在该组织已有同级或更高权限。",
  "source-membership-accepted": "此来源关系已接受过邀请。",
  "source-membership-pending": "此来源关系已有待接受邀请。",
  "invitation-already-pending": "该账号与组织已有待接受邀请。",
};
export const accountFinding = (code: string) => findings[code] ?? `需要核对：${code}`;
