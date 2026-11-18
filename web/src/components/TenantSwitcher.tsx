import { useEffect, useState } from "react";
import { api, save } from "@/lib/api";
import { message } from "@/components/shared";
import type { Session } from "@/App";
type Membership = { id: string; name: string; role: "member" | "member-admin" };
export default function TenantSwitcher({
  session,
  onSwitch,
  version,
  onLabel,
}: {
  session: Session;
  version: number;
  onLabel: (label: string) => void;
  onSwitch: (session: Session) => void;
}) {
  const [items, setItems] = useState<Membership[]>([]),
    [busy, setBusy] = useState(false),
    [error, setError] = useState("");
  useEffect(() => {
    let active = true;
    api<{ items: Membership[]; activeMemberId: string }>("/memberships")
      .then((data) => {
        if (active) {
          setItems(data.items);
          onLabel(
            data.items.find((m) => m.id === data.activeMemberId)?.name ?? "",
          );
        }
      })
      .catch((e) => active && setError(message(e)));
    return () => {
      active = false;
    };
  }, [session.user.id, session.user.memberId, version]);
  const select = async (memberId: string) => {
    setBusy(true);
    setError("");
    try {
      const next = await save<Session>("/memberships/select", { memberId });
      onLabel(items.find((m) => m.id === next.user.memberId)?.name ?? "");
      onSwitch(next);
    } catch (e) {
      setError(message(e));
    } finally {
      setBusy(false);
    }
  };
  return (
    <div className="mx-2 mb-2 rounded-lg border bg-[#fafafd] p-3">
      <label
        className="mb-2 block text-[10px] text-muted-foreground"
        htmlFor="active-membership"
      >
        当前成员网络
      </label>
      <select
        id="active-membership"
        className="!p-2 !text-xs"
        value={session.user.memberId ?? ""}
        disabled={busy || !items.length}
        onChange={(e) => void select(e.target.value)}
      >
        {!items.length && (
          <option value={session.user.memberId ?? ""}>正在加载成员…</option>
        )}
        {items.map((m) => (
          <option key={m.id} value={m.id}>
            {m.name}
          </option>
        ))}
      </select>
      <p className="mt-2 text-[10px] text-muted-foreground">
        {busy
          ? "正在切换工作空间…"
          : session.user.memberRole === "member-admin"
            ? "成员管理员 · 可维护联络资料"
            : "成员访问 · 资料只读"}
      </p>
      {items.length > 1 && (
        <p className="mt-2 text-[10px] leading-5 text-muted-foreground">
          切换成员会同步其他同源标签页。
        </p>
      )}
      {error && (
        <p role="alert" className="mt-2 text-xs text-destructive">
          {error}
        </p>
      )}
    </div>
  );
}
