import type { NoteDraft, NotePreferences } from "@/lib/note-notifications";

export type LegacyNoteBatch = { checksum: string; importedAt: string; importedBy: string; sourceTimezone: string; notes: number; users: number; preferences: number; bytes: number };
export type LegacyNoteSource = { id: string; facts: Record<string, unknown>; visibility?: string; privacyKnown?: boolean; format?: string; prefsState?: string; notesState?: string; notes?: Record<string, unknown> };
export type LegacyNoteArchive = { version: number; sourceTimezone: string; checksum: string; notes: LegacyNoteSource[]; users: LegacyNoteSource[]; preferences: LegacyNoteSource[]; warnings: string[] };
export type LegacyNoteMapping = { id: string; archiveChecksum: string; sourceUserId: string; targetUserId: string; targetUserCreatedAt: string; targetName: string; targetCurrent: boolean; status: "pending" | "cancelled" | "accepted"; revision: number; createdAt: string; createdBy: string; updatedAt: string; acceptedAt?: string; acceptedPreferenceRevision?: number };
export type LegacyNoteIndex = { batches: LegacyNoteBatch[]; mappings: LegacyNoteMapping[]; canManage: boolean; readOnlySources: true };
export type LegacyNotePending = { mapping: LegacyNoteMapping; sourceUser: LegacyNoteSource; sourcePreferences: LegacyNoteSource[]; sourceTimezone: string; sourceJSON: string; sourceJSONCanonical: true; available?: boolean };
export type LegacyNotePendingList = { items: LegacyNotePending[]; preferences: NotePreferences };
export type LegacyNoteReview = LegacyNotePending & { reviewToken: string; expiresAt: string; before: NotePreferences; after: NotePreferences; deliveryEnabled: boolean; email: string };
export type LegacyNoteAcceptance = { mapping: LegacyNoteMapping; preferences: NotePreferences; accepted: true; mailQueued: false; legacyReadCursorsApplied: false; legacyBodiesApplied: false };
export type CurrentStaff = { id: string; name: string; email: string; role: string; createdAt: string };

export const noteStaffRoles = ["admin", "operator", "viewer"];
export const sourceUserID = (source: LegacyNoteSource) => /^legacy-note-user-([1-9][0-9]*)$/.exec(source.id)?.[1] ?? "";
export const sourceName = (source: LegacyNoteSource) => [source.facts.name, source.facts.title, source.facts.username, source.facts.email].find(value => typeof value === "string" && value.length > 0) as string | undefined;
export const selectionBody = (draft: NoteDraft) => ({ mode: draft.mode, memberWatches: draft.memberWatches.map(({ id, createdAt }) => ({ id, createdAt })), noteWatches: draft.noteWatches.map(({ id, createdAt }) => ({ id, createdAt })) });
export const blankLegacySelection = (): NoteDraft => ({ mode: "none", memberWatches: [], noteWatches: [] });
export const staffEntityKey = (staff: Pick<CurrentStaff, "id" | "createdAt">) => `${encodeURIComponent(staff.id)}|${encodeURIComponent(staff.createdAt)}`;
export function sourceFactText(value: unknown): string {
  if (value === null) return "null（原空值）";
  if (typeof value === "string") return value || "（原空字符串）";
  if (typeof value === "number" && !Number.isSafeInteger(value)) return "原数值超出浏览器安全整数范围，请下载规范化归档核对。";
  return JSON.stringify(value, (_key, item) => typeof item === "number" && !Number.isSafeInteger(item) ? "（大整数：请下载规范化归档核对）" : item, 2) ?? "（未记录）";
}
export function downloadCanonicalSource(text: string, name: string) {
  const url = URL.createObjectURL(new Blob([text], { type: "application/json" }));
  const link = document.createElement("a"); link.href = url; link.download = name; link.click(); URL.revokeObjectURL(url);
}
