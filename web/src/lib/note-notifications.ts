export type NoteWatch = {
  id: string;
  createdAt: string;
  name?: string;
  stale?: boolean;
};
export type NoteMode = "none" | "watched" | "all";
export type NotePreferences = {
  revision: number;
  mode: NoteMode;
  memberWatches: NoteWatch[];
  noteWatches: NoteWatch[];
  deliveryEnabled: boolean;
  legacyPreferencesImported: false;
};
export type NoteSummary = {
  revision: string;
  unreadCount: number;
  items: {
    id: string;
    name: string;
    memberId: string;
    memberName: string;
    createdAt: string;
    updatedAt: string;
    visibility: string;
    revision: number;
    unread: boolean;
    watched: boolean;
  }[];
  members: {
    id: string;
    createdAt: string;
    name: string;
    unreadCount: number;
  }[];
  legacyPreferencesImported: false;
};
export type NoteDraft = Pick<
  NotePreferences,
  "mode" | "memberWatches" | "noteWatches"
>;
export const noteModeLabels: Record<NoteMode, string> = {
  none: "不发送备注邮件",
  watched: "仅通知订阅的成员或备注",
  all: "通知所有可见备注",
};
export const watchKey = (watch: Pick<NoteWatch, "id" | "createdAt">) =>
  `${watch.id}\u0000${watch.createdAt}`;
export const noteDraft = (value: NoteDraft): NoteDraft => ({
  mode: value.mode,
  memberWatches: value.memberWatches.map((w) => ({ ...w })),
  noteWatches: value.noteWatches.map((w) => ({ ...w })),
});
export const noteDraftKey = (value: NoteDraft) =>
  JSON.stringify({
    mode: value.mode,
    members: value.memberWatches.map(watchKey).sort(),
    notes: value.noteWatches.map(watchKey).sort(),
  });
export const notePreferenceBody = (draft: NoteDraft, revision: number) => ({
  revision,
  mode: draft.mode,
  memberWatches: draft.memberWatches.map(({ id, createdAt }) => ({
    id,
    createdAt,
  })),
  noteWatches: draft.noteWatches.map(({ id, createdAt }) => ({
    id,
    createdAt,
  })),
});
