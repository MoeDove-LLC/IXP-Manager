import { useCallback, useEffect, useId, useRef, useState } from "react";
import { MailPlus, RefreshCw, UserMinus, Users } from "lucide-react";
import { api, APIError, captureSessionContext } from "@/lib/api";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Empty, ErrorNotice, Loading, message, time } from "@/components/shared";
import MemberUserRemoval from "@/components/MemberUserRemoval";

type MemberUser = {
  id: string;
  name: string;
  email: string;
  memberRole: "member" | "member-admin";
  mfaEnabled: boolean;
  createdAt: string;
};
type RosterResponse = { memberId: string; items: MemberUser[] };
type RosterState = {
  memberId: string;
  request: number;
  contextCurrent: () => boolean;
  status: "loading" | "ready" | "error";
  items: MemberUser[];
  error?: string;
  forbidden?: boolean;
};

export default function MemberUsers({
  memberId,
  memberName,
}: {
  memberId: string;
  memberName: string;
}) {
  const titleId = useId();
  const [state, setState] = useState<RosterState | null>(null),
    [notice, setNotice] = useState(""),
    [removing, setRemoving] = useState<{
      memberId: string;
      target: MemberUser;
      contextCurrent: () => boolean;
    } | null>(null);
  const sequence = useRef(0),
    pending = useRef<AbortController | null>(null);

  const load = useCallback(async (nextNotice = "") => {
    setRemoving(null);
    setNotice(nextNotice);
    pending.current?.abort();
    const controller = new AbortController();
    pending.current = controller;
    const request = ++sequence.current,
      contextCurrent = captureSessionContext();
    const current = () =>
      !controller.signal.aborted &&
      request === sequence.current &&
      contextCurrent();
    // Never retain rows while an account/organization or a manual reload is pending.
    setState({ memberId, request, contextCurrent, status: "loading", items: [] });
    try {
      const data = await api<RosterResponse>("/member/users", {
        signal: controller.signal,
        cache: "no-store",
      });
      if (!current()) return;
      if (data.memberId !== memberId || !Array.isArray(data.items))
        throw new Error("名册与当前组织不一致，已停止展示，请重新加载。");
      if (
        data.items.some(
          (item) =>
            !item ||
            typeof item.id !== "string" ||
            typeof item.name !== "string" ||
            typeof item.email !== "string" ||
            typeof item.createdAt !== "string" ||
            typeof item.mfaEnabled !== "boolean" ||
            !["member", "member-admin"].includes(item.memberRole),
        )
      )
        throw new Error("账号名册格式无法识别，请重新加载。");
      setState({
        memberId,
        request,
        contextCurrent,
        status: "ready",
        items: data.items,
      });
    } catch (error) {
      if (!current()) return;
      const forbidden = error instanceof APIError && error.status === 403;
      setState({
        memberId,
        request,
        contextCurrent,
        status: "error",
        items: [],
        forbidden,
        error: forbidden
          ? "当前账号没有查看组织账号的权限，请重新确认成员管理员权限。"
          : message(error),
      });
    }
  }, [memberId]);

  useEffect(() => {
    void load();
    return () => {
      sequence.current++;
      pending.current?.abort();
    };
  }, [load]);

  const contextCurrent = state?.contextCurrent() ?? true;
  // A same-account session refresh may preserve the component's React key.
  // Hide old rows during render, then obtain a fresh roster for the new context.
  useEffect(() => {
    if (
      state &&
      state.memberId === memberId &&
      state.request === sequence.current &&
      !contextCurrent
    )
      void load();
  }, [state, memberId, contextCurrent, load]);

  const visible = state?.memberId === memberId && contextCurrent ? state : null;
  const loading = !visible || visible.status === "loading";
  return (
    <section aria-labelledby={titleId} className="mt-6" data-member-id={memberId}>
      <Card className="overflow-hidden">
        <CardHeader>
          <div className="flex flex-wrap items-start justify-between gap-3">
            <div className="min-w-0 space-y-2">
              <CardTitle id={titleId} className="flex items-center gap-2">
                <Users className="size-4 text-primary" />
                组织账号
              </CardTitle>
              <CardDescription className="break-words">
                {memberName}
              </CardDescription>
            </div>
            <div className="flex flex-wrap gap-2">
              <Button
                size="sm"
                variant="outline"
                disabled={loading}
                onClick={() => void load()}
              >
                <RefreshCw className={loading ? "animate-spin" : ""} />
                刷新名册
              </Button>
              {!visible?.forbidden && (
                <Button size="sm" variant="outline" asChild>
                  <a href="#invitations">
                    <MailPlus />
                    邀请成员
                  </a>
                </Button>
              )}
            </div>
          </div>
        </CardHeader>
        {notice && (
          <CardContent className="pb-4">
            <p className="notice text-sm" role="status">
              {notice}
            </p>
          </CardContent>
        )}
        {loading ? (
          <Loading />
        ) : visible.status === "error" ? (
          <CardContent>
            <ErrorNotice message={visible.error ?? "名册加载失败，请重新加载。"} />
          </CardContent>
        ) : !visible.items.length ? (
          <Empty
            title="暂无组织账号"
            description="当前组织暂未返回成员账号，可刷新名册或通过成员邀请添加账号。"
          />
        ) : (
          <>
            <p className="border-y bg-muted/20 px-6 py-3 text-xs text-muted-foreground">
              共 {visible.items.length} 个账号
            </p>
            <ul className="divide-y">
              {visible.items.map((user) => (
                <li
                  key={user.id}
                  className="grid min-w-0 gap-4 px-6 py-4 sm:grid-cols-2 xl:grid-cols-[minmax(0,2fr)_minmax(0,1fr)_minmax(0,1fr)_minmax(0,1.3fr)]"
                >
                  <div className="min-w-0">
                    <p className="break-words text-sm font-medium">{user.name}</p>
                    <p className="mt-1 break-all text-sm text-muted-foreground">
                      {user.email}
                    </p>
                  </div>
                  <div className="space-y-1.5">
                    <p className="text-xs text-muted-foreground">组织权限</p>
                    <Badge variant="outline">
                      {user.memberRole === "member-admin" ? "成员管理员" : "普通成员"}
                    </Badge>
                  </div>
                  <div className="space-y-1.5">
                    <p className="text-xs text-muted-foreground">双重验证</p>
                    <Badge variant={user.mfaEnabled ? "default" : "outline"}>
                      {user.mfaEnabled ? "已启用" : "未启用"}
                    </Badge>
                  </div>
                  <div className="min-w-0 space-y-1.5">
                    <p className="text-xs text-muted-foreground">账号创建时间</p>
                    <p className="break-words text-sm">{time(user.createdAt)}</p>
                  </div>
                  <div className="flex justify-end sm:col-span-2 xl:col-span-4">
                    <Button
                      size="sm"
                      variant="outline"
                      aria-label={`移出组织 ${user.name}`}
                      onClick={() =>
                        setRemoving({
                          memberId,
                          target: user,
                          contextCurrent: captureSessionContext(),
                        })
                      }
                    >
                      <UserMinus />
                      移出组织
                    </Button>
                  </div>
                </li>
              ))}
            </ul>
          </>
        )}
      </Card>
      {removing && removing.memberId === memberId && removing.contextCurrent() && (
        <MemberUserRemoval
          key={`${memberId}:${removing.target.id}`}
          memberId={memberId}
          target={removing.target}
          onClose={() => setRemoving(null)}
          onRefresh={() => void load()}
          onRemoved={(result, preview) => {
            setRemoving(null);
            if (result.signedOut) {
              window.dispatchEvent(new Event("session-expired"));
              return;
            }
            void load(
              `${preview.target.name}已移出${preview.memberName}。` +
                (preview.detachesAccount
                  ? "账号已保留，需重新受邀加入组织。"
                  : "账号与其他组织关系已保留。"),
            );
          }}
        />
      )}
    </section>
  );
}
