import { APIError } from "@/lib/api";

export type MailingBatch = { checksum: string; importedAt: string; users: number; choices: number; bytes: number };
export type SourceMailingUser = { legacyId: string; facts: Record<string, unknown> };
export type MailingArchive = { version: number; kind: string; checksum: string; users: SourceMailingUser[]; choices: { sourceUserId: string; sourceKey: string; format: string; value: unknown }[] };
export type MailingMapping = { id: string; archiveChecksum: string; sourceUserId: string; targetUserId: string; targetUserCreatedAt: string; targetName: string; targetCurrent: boolean; status: "pending" | "accepted" | "cancelled"; revision: number; updatedAt: string; acceptedChoices?: { sourceKey: string; subscribed: boolean }[] | null; lists: { sourceKey: string; listId: string; listEmail: string; listKey: string }[] };
export type MailingMigrationChoice = { sourceKey: string; sourceValue: unknown; desired: "subscribed" | "unsubscribed" | "unknown"; listId: string; listName: string; listEmail: string; listRevision: number; preferenceRevision: number; available: boolean; reason: string };
export type MailingMigrationItem = { mapping: MailingMapping; sourceUser: { legacyId: string }; choices: MailingMigrationChoice[] };
export type MailingMigrationView = { email: string; items: MailingMigrationItem[] };
export type MailingMigrationReview = { mapping: MailingMapping; email: string; choices: MailingMigrationChoice[]; reviewToken: string; expiresAt: string; externalApplied: false };
export type MailingTargetUser = { id: string; createdAt: string; name: string; email: string; role: string };
export type MailingTargetList = { id: string; createdAt: string; key: string; name: string; email: string; enabled: boolean; revision: number };
export const mailingArchiveLimit = 16 * 1024 * 1024;
export const migrationStates = { pending: "待本人确认", accepted: "已接受", cancelled: "已取消" };
export const choiceLabels = { subscribed: "订阅", unsubscribed: "退订", unknown: "来源无法确定" };
export const entityKey = (v: { id: string; createdAt: string }) => JSON.stringify([v.id, v.createdAt]);
export const listKey = (v: MailingTargetList) => JSON.stringify([v.id, v.createdAt, v.revision]);
export const sourceLabel = (v: SourceMailingUser) => [v.facts.name, v.facts.email].filter(x => typeof x === "string" && x.length).join(" · ") || "旧账号";
const revision = (v: unknown) => typeof v === "number" && Number.isSafeInteger(v) && v >= 0;
export function checkedMigrationView(view: MailingMigrationView): MailingMigrationView {
  if (!view || typeof view.email !== "string" || !Array.isArray(view.items) || !view.items.every(item => item && item.mapping && typeof item.mapping.id === "string" && typeof item.mapping.sourceUserId === "string" && revision(item.mapping.revision) && Object.hasOwn(migrationStates, item.mapping.status) && Array.isArray(item.choices) && item.choices.every(c => c && typeof c.sourceKey === "string" && typeof c.listId === "string" && typeof c.listName === "string" && typeof c.listEmail === "string" && typeof c.available === "boolean" && typeof c.reason === "string" && revision(c.listRevision) && revision(c.preferenceRevision) && Object.hasOwn(choiceLabels, c.desired)) && new Set(item.choices.map(c => c.sourceKey)).size === item.choices.length) || new Set(view.items.map(item => item.mapping.id)).size !== view.items.length) throw new Error("邮件订阅迁移响应不完整，请重新读取。");
  return view;
}
export async function downloadMailingSource(checksum: string, stillCurrent: () => boolean) {
  if (!/^[a-f0-9]{64}$/.test(checksum)) throw new Error("来源批次标识无效。");
  const response = await fetch(`/api/mailing-lists/legacy/archives/${checksum}?download=1`, { credentials: "same-origin", headers: { Accept: "application/json" } });
  if (!response.ok) throw new APIError("来源文件当前不可下载，请刷新权限和批次。", response.status);
  if (!response.body || !response.headers.get("Content-Type")?.includes("application/json")) throw new Error("来源响应格式无效。");
  const reader = response.body.getReader(), chunks: Uint8Array<ArrayBuffer>[] = [];
  let total = 0;
  try {
    for (;;) {
      const { done, value } = await reader.read();
      if (done) break;
      total += value.byteLength;
      if (total > mailingArchiveLimit) throw new Error("来源文件超过 16 MiB。");
      chunks.push(new Uint8Array(value));
    }
  } finally { await reader.cancel(); }
  // Keep the original bytes: parsing/stringifying would round legacy int64 IDs.
  if (!stillCurrent()) return;
  const blob = new Blob(chunks, { type: "application/json" }), url = URL.createObjectURL(blob), a = document.createElement("a");
  a.href = url; a.download = `legacy-mailing-${checksum.slice(0, 12)}.json`; a.click(); URL.revokeObjectURL(url);
}
