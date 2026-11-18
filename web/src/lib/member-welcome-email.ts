export const welcomeMaxRecipients = 10;
export const welcomeMaxSubjectBytes = 300;
export const welcomeMaxTextBytes = 64 * 1024;
export const welcomeHeaders = ["to", "cc", "bcc"] as const;
export type WelcomeHeader = typeof welcomeHeaders[number];
export type WelcomeRecipient = {
  id: string;
  label: string;
  email: string;
  kind: "member" | "contact" | "account" | "operations";
};
export type WelcomeDraft = {
  revision: string;
  to: string[];
  cc: string[];
  bcc: string[];
  subject: string;
  text: string;
};
export type WelcomeContext = {
  memberId: string;
  memberName: string;
  deliveryEnabled: boolean;
  from: string;
  draft: WelcomeDraft;
  recipientOptions: WelcomeRecipient[];
  blockingReasons: string[];
};
export type WelcomeReview = {
  reviewToken: string;
  expiresAt: string;
  draft: WelcomeDraft;
  from: string;
  deliveryEnabled: boolean;
};
export type WelcomeReceipt = {
  id: string;
  outboxId: string;
  status: string;
  replayed: boolean;
};

export const welcomeHeaderLabel: Record<WelcomeHeader, string> = {
  to: "收件人 To",
  cc: "抄送 CC",
  bcc: "密送 BCC",
};
export const welcomeKindLabel: Record<WelcomeRecipient["kind"], string> = {
  member: "成员邮箱",
  contact: "成员联系人",
  account: "成员账号",
  operations: "交换中心支持",
};
export const welcomeBytes = (value: string) => new TextEncoder().encode(value).length;
export const normalizedWelcomeEmail = (value: string) => value.trim().toLowerCase();
const revision = (value: unknown): value is string =>
  typeof value === "string" && /^[a-f0-9]{64}$/.test(value);
const email = (value: unknown): value is string =>
  typeof value === "string" && value.length > 0 && value.length <= 254 &&
  !/[\s\r\n\x00,<>]/u.test(value) && value.includes("@");

export function checkedWelcomeDraft(draft: WelcomeDraft): WelcomeDraft {
  if (!draft || !revision(draft.revision) || typeof draft.subject !== "string" ||
    typeof draft.text !== "string" ||
    !welcomeHeaders.every(header => Array.isArray(draft[header]) && draft[header].every(email)))
    throw new Error("欢迎邮件草稿格式无法识别，请重新读取。");
  return draft;
}

export function checkedWelcomeContext(value: WelcomeContext, memberId: string): WelcomeContext {
  if (!value || value.memberId !== memberId || typeof value.memberName !== "string" ||
    typeof value.deliveryEnabled !== "boolean" || typeof value.from !== "string" ||
    !Array.isArray(value.recipientOptions) || !Array.isArray(value.blockingReasons) ||
    !value.blockingReasons.every(code => typeof code === "string"))
    throw new Error("欢迎邮件资料与当前成员不一致，请重新读取。");
  checkedWelcomeDraft(value.draft);
  const ids = new Set<string>(), emails = new Set<string>();
  for (const option of value.recipientOptions) {
    if (!option || typeof option.id !== "string" || !option.id || ids.has(option.id) ||
      typeof option.label !== "string" || !email(option.email) ||
      !Object.hasOwn(welcomeKindLabel, option.kind) || emails.has(normalizedWelcomeEmail(option.email)))
      throw new Error("当前成员的收件人资料无法核对，请重新读取。");
    ids.add(option.id);
    emails.add(normalizedWelcomeEmail(option.email));
  }
  return value;
}

export function validateWelcomeDraft(draft: WelcomeDraft, options: WelcomeRecipient[]) {
  checkedWelcomeDraft(draft);
  const selected = welcomeHeaders.flatMap(header => draft[header]);
  const canonical = selected.map(normalizedWelcomeEmail);
  if (!draft.to.length) throw new Error("请至少选择一个 To 收件人。");
  if (selected.length > welcomeMaxRecipients) throw new Error("To、CC、BCC 合计最多 10 个地址。");
  if (new Set(canonical).size !== canonical.length) throw new Error("同一邮箱只能放在一个收件栏目中。");
  const allowed = new Map(options.map(option => [normalizedWelcomeEmail(option.email), option]));
  if (canonical.some(value => !allowed.has(value)))
    throw new Error("草稿含已不在当前成员范围内的地址，请移除后重新选择。");
  if (!draft.to.some(value => {
    const option = allowed.get(normalizedWelcomeEmail(value));
    return option && ["member", "contact", "account"].includes(option.kind);
  })) throw new Error("To 至少需要一个本成员、联系人或成员账号的地址。");
  if (!draft.subject.trim() || /[\r\n\x00]/u.test(draft.subject) || welcomeBytes(draft.subject) > welcomeMaxSubjectBytes)
    throw new Error("邮件主题须为单行文字，最多 300 字节。");
  if (!draft.text.trim() || draft.text.includes("\0") || welcomeBytes(draft.text) > welcomeMaxTextBytes)
    throw new Error("纯文本正文不能为空，且不能超过 64 KiB。");
}

export function copyWelcomeDraft(draft: WelcomeDraft): WelcomeDraft {
  return { ...draft, to: [...draft.to], cc: [...draft.cc], bcc: [...draft.bcc] };
}

export function checkedWelcomeReview(
  value: WelcomeReview,
  submitted: WelcomeDraft,
  context: WelcomeContext,
): WelcomeReview {
  if (!value || typeof value.reviewToken !== "string" || !value.reviewToken ||
    typeof value.expiresAt !== "string" || !Number.isFinite(Date.parse(value.expiresAt)) ||
    typeof value.from !== "string" || typeof value.deliveryEnabled !== "boolean")
    throw new Error("欢迎邮件预览资料不完整，请重新读取。");
  validateWelcomeDraft(value.draft, context.recipientOptions);
  const signature = (addresses: string[]) => addresses.map(normalizedWelcomeEmail).sort().join("\n");
  if (value.draft.revision !== submitted.revision ||
    welcomeHeaders.some(header => signature(value.draft[header]) !== signature(submitted[header])) ||
    value.draft.subject !== submitted.subject.trim() ||
    value.draft.text !== submitted.text.replace(/\r\n?/g, "\n"))
    throw new Error("预览未绑定当前草稿与收件栏目，请重新读取。");
  return value;
}

export function welcomeQueueBody(review: WelcomeReview) {
  return { ...review.draft, reviewToken: review.reviewToken, expiresAt: review.expiresAt, acknowledge: true };
}

export function welcomeBlockLabel(code: string) {
  return ({
    "member-entity-unproven": "成员身份暂无法确认，请核对成员资料后重新读取。",
    "member-state-ineligible": "当前成员状态不适合排队欢迎邮件，请核对成员状态。",
    "member-recipient-unavailable": "当前成员没有可用的成员相关收件地址，请先维护联系资料。",
    "delivery-disabled": "邮件投递尚未启用，可以预览，启用投递后才能确认排队。",
  } as Record<string, string>)[code] ?? "当前发送条件尚未满足，请核对成员资料后重新读取。";
}

export const welcomeReceiptStatus = (status: string) => ({
  queued: "已排队", pending: "等待投递", sending: "传输处理中",
  sent: "邮件传输已接受", failed: "投递未完成", expired: "队列已过期",
} as Record<string, string>)[status] ?? "状态待核对";
