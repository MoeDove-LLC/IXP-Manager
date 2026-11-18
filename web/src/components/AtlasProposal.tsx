import { useEffect, useState, type FormEvent } from "react";
import { save } from "@/lib/api";
import type { Row } from "@/lib/resources";
import { type AtlasLimits, type AtlasRun, probeAddress } from "@/lib/atlas";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogTitle,
} from "@/components/ui/dialog";
import { ErrorNotice, message } from "@/components/shared";
import AtlasProbes, {
  probeKey,
  useAtlasProbes,
} from "@/components/AtlasProbes";

type Participant = {
  memberId: string;
  probeIds: number[];
  targetProbeId: string;
};
export default function AtlasProposal({
  members,
  connections,
  vlans,
  limits,
  enabled,
  onClose,
  onCreated,
}: {
  members: Row[];
  connections: Row[];
  vlans: Row[];
  limits: AtlasLimits;
  enabled: boolean;
  onClose: () => void;
  onCreated: (run: AtlasRun) => void;
}) {
  const [name, setName] = useState(""),
    [mode, setMode] = useState<"single" | "matrix">("single"),
    [af, setAF] = useState(4),
    [type, setType] = useState("ping"),
    [oneoff, setOneoff] = useState(true),
    [interval, setInterval] = useState(String(limits.minIntervalSeconds)),
    [duration, setDuration] = useState("3600"),
    [startAt, setStartAt] = useState(""),
    [isPublic, setPublic] = useState(false),
    [sourceMemberId, setSource] = useState(""),
    [targetMemberId, setTarget] = useState(""),
    [targetType, setTargetType] = useState("probe"),
    [targetId, setTargetId] = useState(""),
    [sourceProbes, setSourceProbes] = useState<number[]>([]),
    [vlanId, setVlan] = useState(""),
    [participants, setParticipants] = useState<Participant[]>([]),
    [busy, setBusy] = useState(false),
    [error, setError] = useState("");
  const probes = useAtlasProbes();
  const networkMembers = members.filter(
    (member) => Number(member.asn) > 0 && member.status === "active",
  );
  const eligible = networkMembers.filter((member) =>
    connections.some(
      (connection) =>
        connection.memberId === member.id &&
        connection.vlanId === vlanId &&
        connection.status === "active" &&
        connection[af === 6 ? "ipv6" : "ipv4"],
    ),
  );
  const targetConnections = connections.filter(
    (connection) =>
      connection.memberId === targetMemberId &&
      connection.status === "active" &&
      connection[af === 6 ? "ipv6" : "ipv4"],
  );
  const participantIDs = participants
    .map((participant) => participant.memberId)
    .join(",");
  useEffect(() => {
    const ids =
      mode === "matrix"
        ? participantIDs.split(",").filter(Boolean)
        : [
            sourceMemberId,
            ...(targetType === "probe" ? [targetMemberId] : []),
          ].filter(Boolean);
    for (const id of new Set(ids)) void probes.load(id, af);
  }, [
    mode,
    participantIDs,
    sourceMemberId,
    targetMemberId,
    targetType,
    af,
    probes.load,
  ]);
  const chooseAF = (value: number) => {
    setAF(value);
    setSourceProbes([]);
    setTargetId("");
    setParticipants([]);
  };
  const fresh = (memberId: string) => {
    const value = probes.entries[probeKey(memberId, af)];
    return !!value?.snapshot && !value.stale && !value.snapshot.stale;
  };
  const submit = async (event: FormEvent) => {
    event.preventDefault();
    setBusy(true);
    setError("");
    try {
      const common = {
        name: name.trim(),
        mode,
        type,
        af,
        isOneoff: oneoff,
        isPublic,
        ...(!oneoff
          ? {
              intervalSeconds: Number(interval),
              durationSeconds: Number(duration),
            }
          : {}),
        ...(startAt ? { startAt: new Date(startAt).toISOString() } : {}),
      };
      let scope: Record<string, unknown>;
      if (mode === "single") {
        if (
          !sourceMemberId ||
          !targetMemberId ||
          !targetId ||
          !sourceProbes.length
        )
          throw new Error("请选择来源成员、来源探针及明确的目标。");
        if (
          !fresh(sourceMemberId) ||
          (targetType === "probe" && !fresh(targetMemberId))
        )
          throw new Error("探针快照缺失或已过期，请先更新并重新选择探针。");
        scope = {
          sourceMemberId,
          targetMemberId,
          targetType,
          targetId,
          probeIds: sourceProbes,
        };
      } else {
        if (!vlanId || participants.length < 2 || participants.length > 10)
          throw new Error("矩阵需要同一 VLAN 下的 2–10 个成员。");
        for (const participant of participants) {
          if (
            !participant.probeIds.length ||
            !participant.targetProbeId ||
            !fresh(participant.memberId)
          )
            throw new Error(
              `请完成 ${members.find((member) => member.id === participant.memberId)?.name ?? participant.memberId} 的新鲜来源探针与固定目标探针选择。`,
            );
        }
        scope = {
          vlanId,
          participants: participants.map((participant) => ({
            ...participant,
            targetProbeId: Number(participant.targetProbeId),
          })),
        };
      }
      const run = await save<AtlasRun>("/atlas/runs", { ...common, ...scope });
      onCreated(run);
    } catch (error) {
      setError(message(error));
    } finally {
      setBusy(false);
    }
  };
  const probePicker = (
    memberId: string,
    selected: number[],
    change: (ids: number[]) => void,
    label: string,
  ) => {
    const key = probeKey(memberId, af);
    return (
      <AtlasProbes
        cache={probes.entries[key]}
        loading={!!probes.loading[key]}
        error={probes.errors[key]}
        af={af}
        canDiscover
        enabled={enabled}
        onDiscover={(anchor) => {
          change([]);
          if (mode === "single" && memberId === targetMemberId) setTargetId("");
          if (mode === "matrix")
            setParticipants((values) =>
              values.map((value) =>
                value.memberId === memberId
                  ? { ...value, probeIds: [], targetProbeId: "" }
                  : value,
              ),
            );
          void probes.load(memberId, af, true, anchor);
        }}
        selected={selected}
        onSelection={change}
        maxSelected={limits.maxProbes}
        label={label}
      />
    );
  };
  const targetProbeSelect = (
    memberId: string,
    value: string,
    change: (id: string) => void,
    id: string,
  ) => {
    const cache = probes.entries[probeKey(memberId, af)];
    return (
      <div className="field">
        <label htmlFor={id}>固定目标探针 *</label>
        <select
          id={id}
          required
          value={value}
          disabled={!fresh(memberId)}
          onChange={(event) => change(event.target.value)}
        >
          <option value="">请选择一个目标探针</option>
          {cache?.items
            .filter((probe) => probeAddress(probe, af))
            .map((probe) => (
              <option key={probe.id} value={probe.id}>
                #{probe.id} · {probeAddress(probe, af)}
              </option>
            ))}
        </select>
        <p className="text-xs leading-6 text-muted-foreground">
          目标地址由服务器按所选探针快照冻结，不随机选择。
        </p>
      </div>
    );
  };
  return (
    <Dialog open onOpenChange={(value) => !value && !busy && onClose()}>
      <DialogContent className="max-w-4xl">
        <DialogTitle className="text-lg font-semibold">
          创建 Atlas 测量提案
        </DialogTitle>
        <DialogDescription className="text-sm leading-6 text-muted-foreground">
          先计算固定探针与目标的样本、额度上界，之后由管理员独立审批和提交。默认创建私有测量提案。
        </DialogDescription>
        <form onSubmit={submit} className="space-y-5">
          <ErrorNotice message={error} />
          <fieldset disabled={busy} className="space-y-5">
            <div className="form-grid">
              <div className="field">
                <label htmlFor="atlas-name">提案名称 *</label>
                <Input
                  id="atlas-name"
                  required
                  maxLength={200}
                  value={name}
                  onChange={(event) => setName(event.target.value)}
                />
              </div>
              <div className="field">
                <label htmlFor="atlas-mode">测量范围</label>
                <select
                  id="atlas-mode"
                  value={mode}
                  onChange={(event) =>
                    setMode(event.target.value as "single" | "matrix")
                  }
                >
                  <option value="single">单个方向</option>
                  <option value="matrix">成员有向互测矩阵</option>
                </select>
              </div>
              <div className="field">
                <label htmlFor="atlas-af">地址族</label>
                <select
                  id="atlas-af"
                  value={af}
                  onChange={(event) => chooseAF(Number(event.target.value))}
                >
                  <option value={4}>IPv4</option>
                  <option value={6}>IPv6</option>
                </select>
              </div>
              <div className="field">
                <label htmlFor="atlas-type">测量类型</label>
                <select
                  id="atlas-type"
                  value={type}
                  onChange={(event) => setType(event.target.value)}
                >
                  <option value="ping">Ping</option>
                  <option value="traceroute">Traceroute</option>
                </select>
              </div>
            </div>
            {mode === "single" ? (
              <>
                <div className="form-grid">
                  <div className="field">
                    <label htmlFor="atlas-source">来源成员 *</label>
                    <select
                      id="atlas-source"
                      required
                      value={sourceMemberId}
                      onChange={(event) => {
                        setSource(event.target.value);
                        setSourceProbes([]);
                      }}
                    >
                      <option value="">请选择来源成员</option>
                      {networkMembers.map((member) => (
                        <option key={member.id} value={member.id}>
                          {member.name} · AS{String(member.asn)}
                        </option>
                      ))}
                    </select>
                  </div>
                  <div className="field">
                    <label htmlFor="atlas-target-member">目标成员 *</label>
                    <select
                      id="atlas-target-member"
                      required
                      value={targetMemberId}
                      onChange={(event) => {
                        setTarget(event.target.value);
                        setTargetId("");
                      }}
                    >
                      <option value="">请选择目标成员</option>
                      {networkMembers.map((member) => (
                        <option key={member.id} value={member.id}>
                          {member.name} · AS{String(member.asn)}
                        </option>
                      ))}
                    </select>
                  </div>
                </div>
                {sourceMemberId &&
                  probePicker(
                    sourceMemberId,
                    sourceProbes,
                    setSourceProbes,
                    "来源探针（显式选择）",
                  )}
                <div className="field">
                  <label htmlFor="atlas-target-type">目标类型</label>
                  <select
                    id="atlas-target-type"
                    value={targetType}
                    onChange={(event) => {
                      setTargetType(event.target.value);
                      setTargetId("");
                    }}
                  >
                    <option value="probe">目标成员的 Atlas 探针地址</option>
                    <option value="connection">目标成员的已登记接入地址</option>
                  </select>
                </div>
                {targetMemberId && targetType === "probe" && (
                  <>
                    <AtlasProbes
                      cache={probes.entries[probeKey(targetMemberId, af)]}
                      loading={!!probes.loading[probeKey(targetMemberId, af)]}
                      error={probes.errors[probeKey(targetMemberId, af)]}
                      af={af}
                      canDiscover
                      enabled={enabled}
                      onDiscover={(anchor) => {
                        setTargetId("");
                        if (targetMemberId === sourceMemberId)
                          setSourceProbes([]);
                        void probes.load(targetMemberId, af, true, anchor);
                      }}
                      label="目标成员探针快照"
                    />
                    {targetProbeSelect(
                      targetMemberId,
                      targetId,
                      setTargetId,
                      "atlas-target-probe",
                    )}
                  </>
                )}
                {targetType === "connection" && (
                  <div className="field">
                    <label htmlFor="atlas-target-connection">
                      目标接入地址 *
                    </label>
                    <select
                      id="atlas-target-connection"
                      required
                      value={targetId}
                      onChange={(event) => setTargetId(event.target.value)}
                    >
                      <option value="">请选择已登记接入</option>
                      {targetConnections.map((connection) => (
                        <option key={connection.id} value={connection.id}>
                          {connection.name} ·{" "}
                          {String(connection[af === 6 ? "ipv6" : "ipv4"])}
                        </option>
                      ))}
                    </select>
                  </div>
                )}
              </>
            ) : (
              <>
                <div className="field">
                  <label htmlFor="atlas-vlan">共同接入的 VLAN *</label>
                  <select
                    id="atlas-vlan"
                    required
                    value={vlanId}
                    onChange={(event) => {
                      setVlan(event.target.value);
                      setParticipants([]);
                    }}
                  >
                    <option value="">请选择 VLAN</option>
                    {vlans.map((vlan) => (
                      <option key={vlan.id} value={vlan.id}>
                        {vlan.name} · VLAN {String(vlan.tag)}
                      </option>
                    ))}
                  </select>
                </div>
                <fieldset className="rounded-lg border p-4">
                  <legend className="px-1 text-xs text-muted-foreground">
                    参与成员（2–10 个）
                  </legend>
                  <div className="grid gap-3 sm:grid-cols-2">
                    {eligible.map((member) => (
                      <label
                        key={member.id}
                        className="flex items-center gap-2 text-sm"
                      >
                        <input
                          type="checkbox"
                          checked={participants.some(
                            (participant) => participant.memberId === member.id,
                          )}
                          disabled={
                            !participants.some(
                              (participant) =>
                                participant.memberId === member.id,
                            ) && participants.length >= 10
                          }
                          onChange={(event) =>
                            setParticipants(
                              event.target.checked
                                ? [
                                    ...participants,
                                    {
                                      memberId: member.id,
                                      probeIds: [],
                                      targetProbeId: "",
                                    },
                                  ]
                                : participants.filter(
                                    (participant) =>
                                      participant.memberId !== member.id,
                                  ),
                            )
                          }
                        />
                        {member.name} · AS{String(member.asn)}
                      </label>
                    ))}
                  </div>
                  {!eligible.length && (
                    <p className="text-xs text-muted-foreground">
                      请先选择具有当前地址族活动接入的 VLAN。
                    </p>
                  )}
                </fieldset>
                <p className="notice">
                  已选 {participants.length} 个成员，将提议{" "}
                  {participants.length * Math.max(0, participants.length - 1)}{" "}
                  个有向测量。每个方向的来源探针和固定目标都需明确选择，总额度上界一次审核。
                </p>
                {participants.map((participant) => (
                  <div
                    key={participant.memberId}
                    className="space-y-4 rounded-lg border p-4"
                    data-atlas-participant={participant.memberId}
                  >
                    <h3 className="font-semibold">
                      {members.find(
                        (member) => member.id === participant.memberId,
                      )?.name ?? participant.memberId}
                    </h3>
                    {probePicker(
                      participant.memberId,
                      participant.probeIds,
                      (ids) =>
                        setParticipants((values) =>
                          values.map((value) =>
                            value.memberId === participant.memberId
                              ? { ...value, probeIds: ids }
                              : value,
                          ),
                        ),
                      "此成员作为来源时使用的探针",
                    )}
                    {targetProbeSelect(
                      participant.memberId,
                      participant.targetProbeId,
                      (id) =>
                        setParticipants((values) =>
                          values.map((value) =>
                            value.memberId === participant.memberId
                              ? { ...value, targetProbeId: id }
                              : value,
                          ),
                        ),
                      `atlas-participant-target-${participant.memberId}`,
                    )}
                  </div>
                ))}
              </>
            )}
            <div className="rounded-lg border p-4">
              <label className="flex items-center gap-2 text-sm">
                <input
                  type="checkbox"
                  checked={oneoff}
                  onChange={(event) => setOneoff(event.target.checked)}
                />
                一次性测量
              </label>
              {!oneoff && (
                <div className="form-grid mt-4">
                  <div className="field">
                    <label htmlFor="atlas-interval">间隔（秒）*</label>
                    <Input
                      id="atlas-interval"
                      required
                      type="number"
                      min={limits.minIntervalSeconds}
                      max={
                        limits.maxIntervalSeconds ?? limits.maxDurationSeconds
                      }
                      value={interval}
                      onChange={(event) => setInterval(event.target.value)}
                    />
                  </div>
                  <div className="field">
                    <label htmlFor="atlas-duration">持续时间（秒）*</label>
                    <Input
                      id="atlas-duration"
                      required
                      type="number"
                      min={1}
                      max={limits.maxDurationSeconds}
                      value={duration}
                      onChange={(event) => setDuration(event.target.value)}
                    />
                  </div>
                </div>
              )}
              <div className="field mt-4">
                <label htmlFor="atlas-start">开始时间（可选，本地时区）</label>
                <Input
                  id="atlas-start"
                  type="datetime-local"
                  step={1}
                  value={startAt}
                  onChange={(event) => setStartAt(event.target.value)}
                />
                <p className="text-xs text-muted-foreground">
                  未指定时，由 provider 按提交时间安排。
                </p>
              </div>
            </div>
            <div className="space-y-3 rounded-lg border p-4">
              <div className="flex flex-wrap items-center gap-2">
                <Badge variant={isPublic ? "outline" : "default"}>
                  {isPublic ? "提议公开结果" : "默认私有结果"}
                </Badge>
                <span className="text-xs text-muted-foreground">
                  结果公开范围与探针是否公开无关。
                </span>
              </div>
              <label className="flex items-start gap-2 text-sm leading-6">
                <input
                  type="checkbox"
                  className="mt-1"
                  checked={isPublic}
                  onChange={(event) => setPublic(event.target.checked)}
                />
                提议公开此测量的结果（管理员审批时还需单独确认）。
              </label>
            </div>
            <p className="text-xs leading-6 text-muted-foreground">
              固定 ICMP、3 个探测包、64 字节；Traceroute 最多 32
              跳。服务端限制：{limits.maxSamples} 个样本上界、
              {limits.maxEstimatedCredits} credits
              估算上界。最终数字以创建后的提案为准，实际扣费未由当前接口报告。
            </p>
          </fieldset>
          <div className="flex justify-end gap-2 border-t pt-4">
            <Button
              type="button"
              variant="outline"
              disabled={busy}
              onClick={onClose}
            >
              取消
            </Button>
            <Button disabled={busy}>
              {busy ? "正在计算提案…" : "创建待审核提案"}
            </Button>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}
