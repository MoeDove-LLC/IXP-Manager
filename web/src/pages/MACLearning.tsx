import { useEffect, useRef, useState } from "react";
import { RefreshCw, ShieldCheck, Settings2 } from "lucide-react";
import { api } from "@/lib/api";
import type { Row } from "@/lib/resources";
import {
  Empty,
  ErrorNotice,
  Heading,
  Loading,
  message,
  time,
} from "@/components/shared";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import MACLearningReview from "@/components/MACLearningReview";
import {
  macDraftKey,
  macRegistrationLabels,
  type LearnedMAC,
  type MACLearning as Catalog,
} from "@/lib/mac-learning";

const observationLabels = {
  fresh: "读取时证据有效",
  stale: "证据已过期",
  error: "最近采集失败",
  unknown: "尚无可用观测",
};
export default function MACLearning({
  role,
  requestedSwitchId = "",
  requestedCandidateId = "",
}: {
  role: string;
  requestedSwitchId?: string;
  requestedCandidateId?: string;
}) {
  const [switches, setSwitches] = useState<Row[]>([]),
    [selected, setSelected] = useState(""),
    [catalog, setCatalog] = useState<Catalog | null>(null),
    [loading, setLoading] = useState(true),
    [error, setError] = useState(""),
    [notice, setNotice] = useState(""),
    [review, setReview] = useState<{
      candidate: LearnedMAC;
      checksum: string;
      observedAt?: string;
    } | null>(null),
    [drafts, setDrafts] = useState<Record<string, string>>({}),
    [target, setTarget] = useState(requestedCandidateId);
  const request = useRef<AbortController | null>(null),
    switchRequest = useRef<AbortController | null>(null);
  const load = async (switchId: string, offset = 0, candidateId = "") => {
    request.current?.abort();
    const controller = new AbortController();
    request.current = controller;
    setLoading(true);
    setError("");
    setCatalog(null);
    try {
      const data = await api<Catalog>(
        `/devices/mac-learning?${new URLSearchParams({ switchId, offset: String(offset), limit: "100", ...(candidateId ? { candidateId } : {}) })}`,
        { signal: controller.signal },
      );
      if (controller.signal.aborted) return;
      if (data.switch.id !== switchId)
        throw new Error("观测返回的交换机不匹配，未显示数据。");
      data.registered ??= [];
      data.items = (data.items ?? []).map((item) => ({
        ...item,
        vlanTags: item.vlanTags ?? [],
        reasons: item.reasons ?? [],
        registeredIds: item.registeredIds ?? [],
      }));
      data.notices ??= [];
      setCatalog(data);
    } catch (e) {
      if (!controller.signal.aborted) setError(message(e));
    } finally {
      if (!controller.signal.aborted) setLoading(false);
    }
  };
  useEffect(() => {
    if (role === "member") {
      setLoading(false);
      return;
    }
    const controller = new AbortController();
    switchRequest.current = controller;
    api<{ items: Row[] }>("/resources/switches", { signal: controller.signal })
      .then((data) => {
        if (controller.signal.aborted) return;
        setSwitches(data.items);
        const first = requestedSwitchId || data.items[0]?.id || "";
        if (
          requestedSwitchId &&
          !data.items.some((sw) => sw.id === requestedSwitchId)
        )
          throw new Error(
            "目标交换机不存在或当前无权查看；未自动选择其他交换机。",
          );
        setSelected(first);
        if (first) void load(first, 0, requestedCandidateId);
        else setLoading(false);
      })
      .catch((e) => {
        if (!controller.signal.aborted) {
          setError(message(e));
          setLoading(false);
        }
      });
    return () => {
      controller.abort();
      request.current?.abort();
    };
  }, [role, requestedSwitchId, requestedCandidateId]);
  useEffect(() => {
    if (!target || !catalog) return;
    const frame = requestAnimationFrame(() =>
      document
        .getElementById(`mac-candidate-${target}`)
        ?.scrollIntoView({ block: "center", behavior: "smooth" }),
    );
    return () => cancelAnimationFrame(frame);
  }, [target, catalog]);
  if (role === "member")
    return <ErrorNotice message="学习观测与登记审阅仅对工作人员开放。" />;
  return (
    <>
      <Heading
        title="MAC 登记与学习对照"
        description="按交换机核对转发表观测与已有登记；只有证据明确的候选项可以人工登记。"
        className="flex-col sm:flex-row"
      >
        <Button asChild variant="outline">
          <a href="#mac-addresses">
            <Settings2 />
            管理已登记 MAC
          </a>
        </Button>
      </Heading>
      <div className="mb-5 flex flex-wrap items-end gap-3">
        <div className="field min-w-0 flex-1 sm:max-w-sm">
          <label htmlFor="mac-learning-switch">交换机</label>
          <select
            id="mac-learning-switch"
            value={selected}
            onChange={(e) => {
              setSelected(e.target.value);
              setTarget("");
              setNotice("");
              void load(e.target.value);
            }}
          >
            {!switches.length && <option value="">暂无交换机</option>}
            {switches.map((sw) => (
              <option key={sw.id} value={sw.id}>
                {sw.name}
              </option>
            ))}
          </select>
        </div>
        <Button
          variant="outline"
          disabled={!selected || loading}
          onClick={() => void load(selected)}
        >
          <RefreshCw />
          刷新 MAC 证据
        </Button>
      </div>
      <ErrorNotice message={error} />
      {notice && (
        <p role="status" className="notice mb-5">
          {notice}
        </p>
      )}
      {loading ? (
        <Loading />
      ) : !selected ? (
        <Card>
          <Empty
            title="先登记交换机"
            description="在设备资产中登记交换机并审核 SNMP 接口映射后，再核对学习到的 MAC。"
          />
        </Card>
      ) : (
        catalog && (
          <div className="space-y-5">
            <div className="flex flex-wrap items-center justify-between gap-3 rounded-lg border bg-background p-4">
              <div>
                <Badge
                  variant={
                    catalog.observation.status === "fresh"
                      ? "default"
                      : "outline"
                  }
                >
                  {observationLabels[catalog.observation.status] ??
                    catalog.observation.status}
                </Badge>
                <p className="mt-2 text-xs text-muted-foreground">
                  观测：{time(catalog.observation.observedAt)} · 最近尝试：
                  {time(catalog.observation.lastAttemptAt)}
                </p>
              </div>
              <p className="max-w-md text-xs leading-6 text-muted-foreground">
                未出现在本次观测中不代表离线。登记、冲突和归属由服务端重新核对，观测不会自动改动登记。
              </p>
            </div>
            <div className="grid items-start gap-5 xl:grid-cols-2">
              <Card>
                <CardHeader>
                  <CardTitle>
                    已登记 MAC ·{" "}
                    {catalog.registeredTotal ?? catalog.registered.length}
                  </CardTitle>
                </CardHeader>
                <CardContent className="space-y-3">
                  {catalog.registeredTruncated && (
                    <p className="notice text-xs">
                      此处仅显示前 {catalog.registered.length}{" "}
                      条登记用于对照；请在“管理已登记 MAC”查看完整登记。
                    </p>
                  )}
                  {!catalog.registered.length ? (
                    <Empty
                      title="此交换机暂无关联登记"
                      description="在右侧核对明确的学习证据后，可将候选 MAC 登记到唯一连接。"
                    />
                  ) : (
                    catalog.registered.map((r) => (
                      <article
                        key={r.id}
                        className="rounded-lg border p-3 text-xs leading-6"
                        data-testid="registered-mac"
                      >
                        <div className="flex flex-wrap items-center justify-between gap-2">
                          <strong className="break-all font-mono text-sm">
                            {r.address}
                          </strong>
                          <Badge variant="outline">
                            {r.observed ? "本次观测见到" : "本次未见"}
                          </Badge>
                        </div>
                        <p className="mt-1 break-all">{r.name}</p>
                        <p className="break-all text-muted-foreground">
                          {r.memberLabel} · {r.connectionLabel}
                        </p>
                        <p className="text-muted-foreground">{r.vlanLabel}</p>
                      </article>
                    ))
                  )}
                </CardContent>
              </Card>
              <Card>
                <CardHeader>
                  <CardTitle>转发表学习证据 · {catalog.totalRows}</CardTitle>
                </CardHeader>
                <CardContent className="space-y-3">
                  {!catalog.items.length ? (
                    <Empty
                      title="本次没有可列出的 FDB 记录"
                      description="请先取得包含转发表的有效观测；空结果不证明没有成员流量。"
                    />
                  ) : (
                    catalog.items.map((item) => (
                      <article
                        key={item.id}
                        id={`mac-candidate-${item.id}`}
                        className={`rounded-lg border p-3 text-xs leading-6 ${target === item.id ? "border-primary bg-primary/5 ring-2 ring-primary/20" : ""}`}
                        data-testid="learned-mac"
                        data-located={target === item.id ? "true" : undefined}
                        data-mac={item.mac}
                      >
                        <div className="flex flex-wrap items-center justify-between gap-2">
                          <strong className="break-all font-mono text-sm">
                            {item.mac}
                          </strong>
                          <Badge variant="outline">
                            {macRegistrationLabels[item.registrationStatus] ??
                              item.registrationStatus}
                          </Badge>
                        </div>
                        <p className="mt-2 break-all">
                          {item.portLabel || "物理接口未确定"} · ifIndex{" "}
                          {item.ifIndex || "未提供"} · VLAN tag{" "}
                          {item.vlanTags.join("、") || "未明确"}
                        </p>
                        {item.connectionLabel && (
                          <p className="break-all">
                            {item.memberLabel} · {item.connectionLabel} ·{" "}
                            {item.vlanLabel}
                          </p>
                        )}
                        {!!item.reasons?.length && (
                          <div className="mt-2 space-y-1 text-muted-foreground">
                            {item.reasons.map((reason, i) => (
                              <p key={i}>{reason}</p>
                            ))}
                          </div>
                        )}
                        {item.canReview && catalog.capabilities.canReview && (
                          <Button
                            variant="outline"
                            size="sm"
                            className="mt-3"
                            onClick={() =>
                              setReview({
                                candidate: item,
                                checksum: catalog.observation.checksum,
                                observedAt: catalog.observation.observedAt,
                              })
                            }
                          >
                            <ShieldCheck />
                            审阅并登记
                          </Button>
                        )}
                      </article>
                    ))
                  )}
                  {catalog.totalRows > catalog.limit && (
                    <div className="flex flex-wrap items-center justify-between gap-2 pt-2 text-xs">
                      <span>
                        第 {catalog.offset + 1}–
                        {catalog.offset + catalog.items.length} 条 /{" "}
                        {catalog.totalRows}
                      </span>
                      <div className="flex gap-2">
                        <Button
                          size="sm"
                          variant="ghost"
                          disabled={catalog.offset <= 0}
                          onClick={() =>
                            void load(
                              selected,
                              Math.max(0, catalog.offset - catalog.limit),
                            )
                          }
                        >
                          上一页
                        </Button>
                        <Button
                          size="sm"
                          variant="ghost"
                          disabled={!catalog.truncated}
                          onClick={() =>
                            void load(selected, catalog.offset + catalog.limit)
                          }
                        >
                          下一页
                        </Button>
                      </div>
                    </div>
                  )}
                </CardContent>
              </Card>
            </div>
            {!!catalog.notices?.length && (
              <div className="notice space-y-1">
                {catalog.notices.map((text, i) => (
                  <p key={i}>{text}</p>
                ))}
              </div>
            )}
          </div>
        )
      )}
      {review && catalog && (
        <MACLearningReview
          switchId={selected}
          switchName={catalog.switch.name}
          observationChecksum={review.checksum}
          observedAt={review.observedAt}
          candidate={review.candidate}
          initialName={drafts[macDraftKey(selected, review.candidate)]}
          canApply={catalog.capabilities.canApply}
          onClose={() => setReview(null)}
          onApplied={async (value) => {
            setNotice(
              value.receiverReviewRequired
                ? "MAC 已登记。如有相关 sFlow 接收器，请重新审阅其转发范围。"
                : "MAC 已登记。",
            );
            const key = macDraftKey(selected, review.candidate);
            setDrafts((d) => {
              const next = { ...d };
              delete next[key];
              return next;
            });
            await load(selected);
          }}
          onRefresh={async (name) => {
            setDrafts((d) => ({
              ...d,
              [macDraftKey(selected, review.candidate)]: name,
            }));
            setReview(null);
            setNotice(
              "未提交的登记名称已保留。请根据最新观测重新选择候选项并审阅。",
            );
            await load(selected);
          }}
        />
      )}
    </>
  );
}
