import { useEffect, useState, type FormEvent } from "react";
import { Pencil, Plus, Trash2 } from "lucide-react";
import { api, save, APIError } from "@/lib/api";
import type { Row } from "@/lib/resources";
import {
  provisioningLabels,
  type ProvisioningOptions,
  type ProvisioningProfile,
} from "@/lib/provisioning";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { Card } from "@/components/ui/card";
import {
  Dialog,
  DialogContent,
  DialogTitle,
  DialogDescription,
} from "@/components/ui/dialog";
import { Empty, ErrorNotice, message, time } from "@/components/shared";
import AS112Guidance from "@/components/AS112Guidance";

const nagiosFields = [
  ["memberHostTemplate", "成员主机模板"],
  ["switchHostTemplate", "交换机主机模板"],
  ["routerHostTemplate", "路由器主机模板"],
  ["serviceTemplate", "通用服务模板"],
  ["pingServiceTemplate", "Ping 服务模板"],
  ["busyPingServiceTemplate", "繁忙主机 Ping 模板"],
  ["bgpServiceTemplate", "BGP 会话模板"],
  ["routerServiceTemplate", "路由器服务模板"],
  ["switchServiceTemplate", "交换机服务模板"],
  ["coreServiceTemplate", "核心链路服务模板"],
] as const;
const timers = [
  ["ttl", "TTL", 86400],
  ["refresh", "刷新间隔", 43200],
  ["retry", "重试间隔", 7200],
  ["expire", "过期间隔", 1209600],
  ["minimum", "否定缓存 TTL", 7200],
] as const;
type Profile = ProvisioningProfile;
type Form = {
  name: string;
  dnsMode: "" | "new" | "existing";
  dnsSerialFloor: string;
} & ProvisioningOptions;
const defaults: Form = {
  name: "",
  dnsMode: "",
  dnsSerialFloor: "",
  kind: "dns-arpa",
  families: [4, 6],
  vlanId: "",
  infrastructureId: "",
  switchId: "",
  routerId: "",
  as112: { serverName: "", responsibleMailbox: "", facility: "", location: "", uniqueIP: "", loc: "" },
  dns: {
    prefix: "",
    primaryNS: "",
    responsibleMailbox: "",
    nameServers: [],
    ttl: 86400,
    refresh: 43200,
    retry: 7200,
    expire: 1209600,
    minimum: 7200,
    nameSuffix: "",
    activeOnly: false,
  },
  nagios: {},
  smokeping: { level: 1, probe4: "FPing", probe6: "FPing6" },
};

export default function ProvisioningProfiles({
  role,
  profiles,
  onChange,
}: {
  role: string;
  profiles: Profile[];
  onChange: () => void | Promise<void>;
}) {
  const canWrite = role === "admin" || role === "operator";
  const [vlans, setVlans] = useState<Row[]>([]),
    [fabrics, setFabrics] = useState<Row[]>([]),
    [switches, setSwitches] = useState<Row[]>([]),
    [routers, setRouters] = useState<Row[]>([]),
    [error, setError] = useState(""),
    [formError, setFormError] = useState(""),
    [open, setOpen] = useState(false),
    [editing, setEditing] = useState<Profile | null>(null),
    [form, setForm] = useState<Form>(defaults),
    [busy, setBusy] = useState(false),
    [deleting, setDeleting] = useState<Profile | null>(null),
    [stale, setStale] = useState(false),
    [serversText, setServersText] = useState("");
  useEffect(() => {
    const controller = new AbortController();
    Promise.all([
      api<{ items: Row[] }>("/resources/vlans", { signal: controller.signal }),
      api<{ items: Row[] }>("/resources/infrastructures", {
        signal: controller.signal,
      }),
      api<{ items: Row[] }>("/resources/switches", { signal: controller.signal }),
      api<{ items: Row[] }>("/resources/routers", { signal: controller.signal }),
    ])
      .then(([v, i, s, r]) => {
        if (controller.signal.aborted) return;
        setVlans(v.items);
        setFabrics(i.items);
        setSwitches(s.items);
        setRouters(r.items);
      })
      .catch((e) => {
        if (!controller.signal.aborted) setError(message(e));
      });
    return () => controller.abort();
  }, []);
  const edit = (row?: Profile) => {
    setEditing(row ?? null);
    setForm({
      ...defaults,
      ...row?.options,
      name: row?.name ?? "",
      dnsMode:
        row?.options.kind !== "dns-arpa"
          ? ""
          : row?.dnsNewZone === true
            ? "new"
            : typeof row?.dnsSerialFloor === "number"
              ? "existing"
              : "",
      dnsSerialFloor:
        row?.options.kind === "dns-arpa" &&
        typeof row?.dnsSerialFloor === "number"
          ? String(row.dnsSerialFloor)
          : "",
      kind: row?.options.kind ?? "dns-arpa",
      families: row?.options.families ?? [4, 6],
      dns: { ...defaults.dns!, ...row?.options.dns },
      nagios: { ...row?.options.nagios },
      smokeping: { ...defaults.smokeping!, ...row?.options.smokeping },
      as112: { ...defaults.as112!, ...row?.options.as112 },
    });
    setServersText(row?.options.dns?.nameServers?.join("\n") ?? "");
    setFormError("");
    setStale(false);
    setOpen(true);
  };
  const changeDNS = (key: string, value: unknown) =>
    setForm({ ...form, dns: { ...form.dns!, [key]: value } });
  const as112Router = routers.find(router => router.id === form.routerId && router.role === "as112");
  const selectedVLAN = vlans.find((vlan) => vlan.id === form.vlanId),
    prefixes = selectedVLAN
      ? [
          selectedVLAN.ipv4Prefix,
          selectedVLAN.ipv6Prefix,
          ...(Array.isArray(selectedVLAN.prefixes)
            ? selectedVLAN.prefixes
            : []),
        ]
          .filter(Boolean)
          .map(String)
      : [];
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setFormError("");
    try {
      if (form.kind === "switch-core" && !switches.some(sw => sw.id === form.switchId))
        throw new Error("请选择已登记的目标交换机。");
      if (form.kind === "as112-dns" && !as112Router)
        throw new Error("请选择已登记的 AS112 路由节点。");
      if (form.kind !== "dns-arpa" && form.kind !== "as112-dns" && !form.families.length)
        throw new Error("至少选择一个地址族。");
      if (
        form.kind === "dns-arpa" &&
        (!form.dnsMode ||
          (form.dnsMode === "existing" && form.dnsSerialFloor === ""))
      )
        throw new Error(
          "请明确选择全新区，或填写现有权威区的当前 SOA 序列号。",
        );
      const data: Record<string, unknown> = form.kind === "as112-dns" ? {
        kind: "as112-dns", routerId: form.routerId, as112: Object.fromEntries(Object.entries(form.as112 ?? {}).map(([key, value]) => [key, value.trim()])),
      } : form.kind === "switch-core" ? {
        kind: "switch-core", switchId: form.switchId, families: [4, 6],
      } : {
        kind: form.kind,
        families:
          form.kind === "dns-arpa"
            ? [form.dns?.prefix.includes(":") ? 6 : 4]
            : form.families,
        ...(form.vlanId ? { vlanId: form.vlanId } : {}),
        ...(form.infrastructureId
          ? { infrastructureId: form.infrastructureId }
          : {}),
      };
      if (form.kind === "dns-arpa") {
        data.dns = {
          ...Object.fromEntries(
            Object.keys(defaults.dns!).map((key) => [
              key,
              form.dns?.[key as keyof NonNullable<Form["dns"]>],
            ]),
          ),
          nameServers: serversText
            .split(/[\n,]/)
            .map((value) => value.trim())
            .filter(Boolean),
        };
      }
      if (form.kind === "nagios")
        data.nagios = Object.fromEntries(
          Object.entries(form.nagios ?? {})
            .filter(([, value]) => value.trim())
            .map(([key, value]) => [key, value.trim()]),
        );
      if (form.kind === "smokeping") data.smokeping = form.smokeping;
      await save(
        `/resources/provisioning-profiles${editing ? `/${editing.id}` : ""}`,
        {
          name: form.name.trim(),
          options: data,
          ...(form.kind === "dns-arpa"
            ? form.dnsMode === "new"
              ? { dnsNewZone: true }
              : {
                  dnsNewZone: false,
                  dnsSerialFloor: Number(form.dnsSerialFloor),
                }
            : {}),
          ...(editing ? { expectedRevision: editing.revision } : {}),
        },
        editing ? "PUT" : "POST",
      );
      setOpen(false);
      await onChange();
    } catch (e) {
      if (e instanceof APIError && e.status === 409) {
        setStale(true);
        setFormError(
          "生成规则已被其他操作修改，请关闭此窗口并重新打开最新规则。未覆盖其他人的修改。",
        );
        await onChange();
      } else setFormError(message(e));
    } finally {
      setBusy(false);
    }
  };
  const remove = async () => {
    if (!deleting) return;
    setBusy(true);
    setFormError("");
    try {
      await save(
        `/resources/provisioning-profiles/${deleting.id}`,
        { expectedRevision: deleting.revision },
        "DELETE",
      );
      setDeleting(null);
      await onChange();
    } catch (e) {
      if (e instanceof APIError && e.status === 409) {
        setStale(true);
        setFormError(
          "生成规则已被其他操作修改，请关闭此窗口并重新打开最新规则。未覆盖其他人的修改。",
        );
        await onChange();
      } else setFormError(message(e));
    } finally {
      setBusy(false);
    }
  };
  return (
    <>
      <div className="mb-4 flex flex-wrap items-center justify-between gap-3">
        <div>
          <h2 className="text-base font-semibold">生成规则</h2>
          <p className="mt-1 text-xs leading-6 text-muted-foreground">
            固定选项控制生成范围；规则修改不覆盖已有版本。
          </p>
        </div>
        {canWrite && (
          <Button onClick={() => edit()}>
            <Plus />
            新增生成规则
          </Button>
        )}
      </div>
      <ErrorNotice message={error} />
      <Card className="overflow-hidden">
        {!profiles.length ? (
          <Empty
            title="先建立一条生成规则"
            description="选择目标交换机或工具范围与选项，再生成可审查版本。"
            action={canWrite ? "新增生成规则" : undefined}
            onAction={() => edit()}
          />
        ) : (
          <div className="overflow-x-auto">
            <table>
              <thead>
                <tr>
                  <th>规则</th>
                  <th>工具</th>
                  <th>网络范围</th>
                  <th>更新时间</th>
                  {canWrite && <th className="text-right">操作</th>}
                </tr>
              </thead>
              <tbody>
                {profiles.map((row) => (
                  <tr key={row.id}>
                    <td className="font-medium">{row.name}</td>
                    <td>
                      <Badge variant="outline">
                        {provisioningLabels[String(row.options.kind)] ??
                          String(row.options.kind)}
                      </Badge>
                    </td>
                    <td className="text-xs">
                      {row.options.kind === "as112-dns" ? (routers.find(router => router.id === row.options.routerId)?.name ?? "目标 AS112 节点缺失") : row.options.kind === "switch-core" ? (switches.find(sw => sw.id === row.options.switchId)?.name ?? row.options.switchId ?? "目标交换机缺失") : vlans.find((v) => v.id === row.options.vlanId)?.name ??
                        (row.options.vlanId
                          ? String(row.options.vlanId)
                          : (fabrics.find(
                              (f) => f.id === row.options.infrastructureId,
                            )?.name ?? "全部库存"))}
                    </td>
                    <td className="text-xs text-muted-foreground">
                      {time(row.updatedAt)}
                    </td>
                    {canWrite && (
                      <td>
                        <div className="flex justify-end gap-1">
                          <Button
                            variant="ghost"
                            size="icon"
                            aria-label={`编辑生成规则 ${row.name}`}
                            onClick={() => edit(row as Profile)}
                          >
                            <Pencil />
                          </Button>
                          <Button
                            variant="ghost"
                            size="icon"
                            aria-label={`删除生成规则 ${row.name}`}
                            onClick={() => {
                              setDeleting(row);
                              setFormError("");
                              setStale(false);
                            }}
                          >
                            <Trash2 />
                          </Button>
                        </div>
                      </td>
                    )}
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </Card>
      <Dialog open={open} onOpenChange={(value) => !busy && setOpen(value)}>
        <DialogContent className="max-w-2xl">
          <DialogTitle className="text-lg font-semibold">
            {editing ? "编辑生成规则" : "新增生成规则"}
          </DialogTitle>
          <DialogDescription className="text-sm leading-6 text-muted-foreground">
            {form.kind === "as112-dns" ? "绑定 AS112 路由节点，生成 PowerDNS Authoritative、BIND 后端区文件与尚未执行的健康检查计划。服务模式和地址族取自路由节点。" : form.kind === "switch-core" ? "选择一台已登记交换机，生成完整双栈核心网络 JSON 计划。计划须由目标适配器转换，并独立验证设备原生配置。" : "使用固定的工具选项生成配置。模板与探测器名称必须在目标服务中已有定义，不接受任意命令或文件路径。"}
          </DialogDescription>
          <form onSubmit={submit} className="space-y-5">
            <ErrorNotice message={formError} />
            <fieldset disabled={busy} className="space-y-5">
              <div className="form-grid">
                <div className="field">
                  <label htmlFor="profile-name">规则名称 *</label>
                  <Input
                    id="profile-name"
                    required
                    value={form.name}
                    maxLength={200}
                    onChange={(e) => setForm({ ...form, name: e.target.value })}
                  />
                </div>
                <div className="field">
                  <label htmlFor="profile-kind">工具 *</label>
                  <select
                    id="profile-kind"
                    value={form.kind}
                    onChange={(e) =>
                      setForm({ ...form, kind: e.target.value as Form["kind"], ...(e.target.value === "switch-core" ? { families: [4, 6] } : {}) })
                    }
                  >
                    {Object.entries(provisioningLabels).map(
                      ([value, label]) => (
                        <option key={value} value={value}>
                          {label}
                        </option>
                      ),
                    )}
                  </select>
                </div>
                {form.kind === "as112-dns" ? <div className="field md:col-span-2">
                  <label htmlFor="profile-as112-router">AS112 路由节点 *</label>
                  <select id="profile-as112-router" required value={form.routerId ?? ""} onChange={event => setForm({ ...form, routerId: event.target.value })}>
                    <option value="">请选择 AS112 节点</option>
                    {routers.filter(router => router.role === "as112").map(router => <option key={router.id} value={router.id}>{router.name} · {String(router.platform)} · AS112</option>)}
                    {form.routerId && !as112Router && <option value={form.routerId}>原目标已不可用，请重新选择</option>}
                  </select>
                  <p className="text-xs text-muted-foreground">请先在路由库存创建 AS112 角色；不会自动建立或启用 BGP 邻居。</p>
                </div> : form.kind === "switch-core" ? (
                  <div className="field md:col-span-2">
                    <label htmlFor="profile-switch">目标交换机 *</label>
                    <select id="profile-switch" required value={form.switchId ?? ""} onChange={e => setForm({ ...form, switchId: e.target.value })}>
                      <option value="">请选择目标交换机</option>
                      {switches.map(sw => <option key={sw.id} value={sw.id}>{sw.name} · {sw.id}{sw.active === false ? "（库存已停用）" : ""}</option>)}
                    </select>
                    {!switches.length && <p className="text-xs text-muted-foreground">请先在交换机库存中登记目标，再建立生成规则。</p>}
                  </div>
                ) : <>
                <div className="field">
                  <label htmlFor="profile-fabric">交换网络</label>
                  <select
                    id="profile-fabric"
                    value={form.infrastructureId ?? ""}
                    onChange={(e) =>
                      setForm({
                        ...form,
                        infrastructureId: e.target.value,
                        vlanId: "",
                      })
                    }
                  >
                    <option value="">不限定交换网络</option>
                    {fabrics.map((fabric) => (
                      <option key={fabric.id} value={fabric.id}>
                        {fabric.name}
                      </option>
                    ))}
                  </select>
                </div>
                <div className="field">
                  <label htmlFor="profile-vlan">
                    VLAN{form.kind === "dns-arpa" ? " *" : ""}
                  </label>
                  <select
                    id="profile-vlan"
                    required={form.kind === "dns-arpa"}
                    value={form.vlanId ?? ""}
                    onChange={(e) =>
                      setForm({
                        ...form,
                        vlanId: e.target.value,
                        dns: { ...form.dns!, prefix: "" },
                      })
                    }
                  >
                    <option value="">
                      {form.kind === "dns-arpa" ? "请选择 VLAN" : "不限定 VLAN"}
                    </option>
                    {vlans
                      .filter(
                        (vlan) =>
                          !form.infrastructureId ||
                          vlan.infrastructureId === form.infrastructureId,
                      )
                      .map((vlan) => (
                        <option key={vlan.id} value={vlan.id}>
                          {vlan.name} · VLAN {String(vlan.tag)}
                        </option>
                      ))}
                  </select>
                </div>
                </>}
              </div>
              {form.kind === "as112-dns" && <>
                <AS112Guidance service={String(as112Router?.as112Service ?? "")} ipv4={as112Router?.as112IPv4 === true} ipv6={as112Router?.as112IPv6 === true} platform={String(as112Router?.platform ?? "")} nextHop4={String(as112Router?.as112NextHop4 ?? "")} nextHop6={String(as112Router?.as112NextHop6 ?? "")} link={false} />
                <div className="form-grid">{([
                  ["serverName", "DNS 主机名称", "as112.example.net", true],
                  ["responsibleMailbox", "SOA 责任邮箱（RNAME）", "hostmaster.example.net", true],
                  ["facility", "设施或机构", "交换中心机房", true],
                  ["location", "城市和国家", "Shanghai, CN", true],
                  ["uniqueIP", "节点唯一 IP", "203.0.113.1", true],
                  ["loc", "LOC 位置（可选）", "31 13 0 N 121 28 0 E 10m", false],
                ] as const).map(([key, label, placeholder, required]) => <div className="field" key={key}>
                  <label htmlFor={`profile-as112-${key}`}>{label}{required ? " *" : ""}</label>
                  <Input id={`profile-as112-${key}`} value={form.as112?.[key] ?? ""} required={required} maxLength={255} placeholder={placeholder} onChange={event => setForm({ ...form, as112: { ...form.as112!, [key]: event.target.value } })} />
                </div>)}</div>
                <p className="text-xs leading-6 text-muted-foreground">主机名称和 RNAME 可以不写末尾圆点。RNAME 使用 DNS 名称形式，例如 hostmaster.example.net；唯一 IP 必须是目标节点实际配置的本机单播地址，不是共享的 anycast 地址，也不是任意远端探测目标。可选 LOC 使用 RFC 1876 格式。</p>
                {as112Router && <p className="text-xs leading-6 text-muted-foreground">此规则的唯一 IP 须匹配{as112Router.platform === "junos" ? "已填写的 DNS 下一跳" : "路由节点的本地传输地址"}之一：<span className="break-all">{(as112Router.platform === "junos" ? [as112Router.as112NextHop4, as112Router.as112NextHop6] : [as112Router.localIpv4, as112Router.localIpv6]).filter(Boolean).join(" · ")}</span></p>}
                <p className="text-xs leading-6 text-muted-foreground">变更节点地址时，可先保存新的唯一 IP 意图，再更新路由节点地址或 Junos 下一跳。两者未一致期间，生成、校验、批准和下载均会拒绝；修复绑定后再生成新版本。</p>
                <p className="notice">生成包包含真实 PowerDNS 配置与权威区文件；原生检查不运行 DNS 服务。下载后须严格解包到新私密目录，并以该目录为 WorkingDirectory。实际绑定地址、UDP/TCP 响应与持续健康撤回需在目标主机验证。</p>
              </>}
              {form.kind === "switch-core" && (
                <p className="notice">固定包含 IPv4 与 IPv6，按所选交换机的核心互联库存生成接口、邻居和 floodlist。缺少 ASN、IPv4 router ID、接口名或 IPv6 所需 MTU 会阻止生成；结构检查不代表目标设备已验证或在线。</p>
              )}
              {form.kind !== "dns-arpa" && form.kind !== "switch-core" && form.kind !== "as112-dns" && (
                <fieldset className="rounded-lg border p-3">
                  <legend className="px-1 text-xs text-muted-foreground">
                    地址族
                  </legend>
                  <div className="flex gap-5">
                    {[4, 6].map((family) => (
                      <label
                        key={family}
                        className="flex items-center gap-2 text-sm"
                      >
                        <input
                          type="checkbox"
                          checked={form.families.includes(family)}
                          onChange={(e) =>
                            setForm({
                              ...form,
                              families: e.target.checked
                                ? [...form.families, family]
                                : form.families.filter(
                                    (value) => value !== family,
                                  ),
                            })
                          }
                        />
                        IPv{family}
                      </label>
                    ))}
                  </div>
                </fieldset>
              )}
              {form.kind === "dns-arpa" && (
                <>
                  <p className="notice">
                    生成一个完整反向解析区。IPv4 前缀需位于字节边界、IPv6
                    需位于半字节边界；其他前缀请先规划 RFC 2317 委派或拆分。SOA
                    序列号由服务端生成并固定。
                  </p>
                  <div className="form-grid">
                    <div className="field">
                      <label htmlFor="profile-dns-mode">DNS 区发布状态 *</label>
                      <select
                        id="profile-dns-mode"
                        required
                        value={form.dnsMode}
                        onChange={(e) =>
                          setForm({
                            ...form,
                            dnsMode: e.target.value as Form["dnsMode"],
                            dnsSerialFloor:
                              e.target.value === "new"
                                ? ""
                                : form.dnsSerialFloor,
                          })
                        }
                      >
                        <option value="">请明确选择初始序列号依据</option>
                        <option value="new">全新、尚未发布的区</option>
                        <option value="existing">现有权威区</option>
                      </select>
                    </div>
                    {form.dnsMode === "existing" && (
                      <div className="field">
                        <label htmlFor="profile-dns-floor">
                          现有权威区当前 SOA serial *
                        </label>
                        <Input
                          id="profile-dns-floor"
                          type="number"
                          min={0}
                          max={4294967295}
                          step={1}
                          required
                          value={form.dnsSerialFloor}
                          onChange={(e) =>
                            setForm({ ...form, dnsSerialFloor: e.target.value })
                          }
                        />
                      </div>
                    )}
                  </div>
                  <p className="text-xs leading-6 text-muted-foreground">
                    请从现有权威 DNS
                    核对实际序列号，不能仅按当前时间推测。此值提供初始基线；最近已分配的序列由服务端按 RFC 1982 顺序保留，回绕或删除重建不会重置账本。
                  </p>
                  <div className="form-grid">
                    <div className="field">
                      <label htmlFor="profile-dns-prefix">区前缀 *</label>
                      <select
                        id="profile-dns-prefix"
                        required
                        value={form.dns?.prefix ?? ""}
                        onChange={(e) => changeDNS("prefix", e.target.value)}
                      >
                        <option value="">请选择已登记的 VLAN 前缀</option>
                        {prefixes.map((prefix) => (
                          <option key={prefix} value={prefix}>
                            {prefix}
                          </option>
                        ))}
                      </select>
                      {!prefixes.length && (
                        <p className="text-xs text-muted-foreground">
                          请先选择包含前缀的 VLAN；只生成完整匹配的登记地址区。
                        </p>
                      )}
                    </div>
                    <div className="field">
                      <label htmlFor="profile-dns-primary">
                        主名称服务器 *
                      </label>
                      <Input
                        id="profile-dns-primary"
                        placeholder="ns1.example.net."
                        required
                        value={form.dns?.primaryNS ?? ""}
                        onChange={(e) => changeDNS("primaryNS", e.target.value)}
                      />
                    </div>
                    <div className="field">
                      <label htmlFor="profile-dns-mailbox">
                        SOA 负责人域名格式 *
                      </label>
                      <Input
                        id="profile-dns-mailbox"
                        placeholder="hostmaster.example.net."
                        required
                        value={form.dns?.responsibleMailbox ?? ""}
                        onChange={(e) =>
                          changeDNS("responsibleMailbox", e.target.value)
                        }
                      />
                      <p className="text-xs leading-6 text-muted-foreground">
                        使用 DNS RNAME 格式，不填写带 @ 的电子邮箱。
                      </p>
                    </div>
                    <div className="field">
                      <label htmlFor="profile-dns-suffix">
                        备用主机名后缀（可选）
                      </label>
                      <Input
                        id="profile-dns-suffix"
                        placeholder="members.example.net."
                        value={form.dns?.nameSuffix ?? ""}
                        onChange={(e) =>
                          changeDNS("nameSuffix", e.target.value)
                        }
                      />
                      <p className="text-xs leading-6 text-muted-foreground">
                        缺少登记主机名时用于生成确定的名称；留空不会假造主机名。
                      </p>
                    </div>
                    <div className="field md:col-span-2">
                      <label htmlFor="profile-dns-servers">
                        NS 列表（1–8 个，每行一个）*
                      </label>
                      <textarea
                        id="profile-dns-servers"
                        required
                        rows={3}
                        placeholder={"ns1.example.net.\nns2.example.net."}
                        value={serversText}
                        onChange={(e) => setServersText(e.target.value)}
                      />
                    </div>
                    {timers.map(([key, label, value]) => (
                      <div className="field" key={key}>
                        <label htmlFor={`profile-dns-${key}`}>
                          {label}（秒）
                        </label>
                        <Input
                          id={`profile-dns-${key}`}
                          type="number"
                          required
                          min={1}
                          max={key === "expire" ? 31536000 : 604800}
                          value={form.dns?.[key] ?? value}
                          onChange={(e) =>
                            changeDNS(key, Number(e.target.value))
                          }
                        />
                      </div>
                    ))}
                  </div>
                  <label className="flex items-start gap-2 text-sm leading-6">
                    <input
                      type="checkbox"
                      className="mt-1"
                      checked={form.dns?.activeOnly ?? false}
                      onChange={(e) =>
                        changeDNS("activeOnly", e.target.checked)
                      }
                    />
                    仅生成启用目标的 PTR；默认保留已分配地址的反向解析覆盖。
                  </label>
                </>
              )}
              {form.kind === "nagios" && (
                <>
                  <p className="notice">
                    生成 host/service/hostgroup 片段，依赖目标 Nagios
                    中已有的模板和检查命令。留空使用服务端固定默认名称；下载后仍须把片段并入完整配置再原生校验。
                  </p>
                  <div className="form-grid">
                    {nagiosFields.map(([key, label]) => (
                      <div className="field" key={key}>
                        <label htmlFor={`profile-nagios-${key}`}>{label}</label>
                        <Input
                          id={`profile-nagios-${key}`}
                          pattern={"[A-Za-z0-9_][A-Za-z0-9_\\-]{0,127}"}
                          maxLength={128}
                          placeholder="使用服务端默认名称"
                          value={form.nagios?.[key] ?? ""}
                          onChange={(e) =>
                            setForm({
                              ...form,
                              nagios: { ...form.nagios, [key]: e.target.value },
                            })
                          }
                        />
                      </div>
                    ))}
                  </div>
                </>
              )}
              {form.kind === "smokeping" && (
                <>
                  <p className="notice">
                    生成 Targets 片段。FPing / FPing6
                    或自定义安全探测器标识须已在目标定义；本地结构检查不代表目标完整配置已通过。
                  </p>
                  <div className="form-grid">
                    <div className="field">
                      <label htmlFor="profile-smoke-level">起始标题层级</label>
                      <select
                        id="profile-smoke-level"
                        value={form.smokeping?.level ?? 1}
                        onChange={(e) =>
                          setForm({
                            ...form,
                            smokeping: {
                              ...form.smokeping!,
                              level: Number(e.target.value),
                            },
                          })
                        }
                      >
                        {[1, 2, 3, 4].map((level) => (
                          <option key={level} value={level}>
                            {level} 级
                          </option>
                        ))}
                      </select>
                    </div>
                    {([4, 6] as const).map((family) => (
                      <div className="field" key={family}>
                        <label htmlFor={`profile-smoke-${family}`}>
                          IPv{family} 探测器
                        </label>
                        <Input
                          id={`profile-smoke-${family}`}
                          required
                          pattern={"[A-Za-z0-9_][A-Za-z0-9_\\-]{0,127}"}
                          maxLength={128}
                          value={form.smokeping?.[`probe${family}`] ?? ""}
                          onChange={(e) =>
                            setForm({
                              ...form,
                              smokeping: {
                                ...form.smokeping!,
                                [`probe${family}`]: e.target.value,
                              },
                            })
                          }
                        />
                      </div>
                    ))}
                  </div>
                </>
              )}
            </fieldset>
            <div className="flex justify-end gap-2 border-t pt-4">
              <Button
                type="button"
                variant="outline"
                disabled={busy}
                onClick={() => setOpen(false)}
              >
                取消
              </Button>
              <Button disabled={busy || stale}>
                {busy ? "正在保存…" : "保存生成规则"}
              </Button>
            </div>
          </form>
        </DialogContent>
      </Dialog>
      <Dialog
        open={!!deleting}
        onOpenChange={(value) => !value && !busy && setDeleting(null)}
      >
        <DialogContent>
          <DialogTitle className="text-lg font-semibold">
            删除生成规则？
          </DialogTitle>
          <DialogDescription className="text-sm leading-6 text-muted-foreground">
            删除“{deleting?.name}
            ”后将不能再按此规则创建版本。已有版本是否允许保留由服务端引用规则决定。
          </DialogDescription>
          <ErrorNotice message={formError} />
          <div className="flex justify-end gap-2">
            <Button
              variant="outline"
              disabled={busy}
              onClick={() => setDeleting(null)}
            >
              取消
            </Button>
            <Button
              variant="destructive"
              disabled={busy || stale}
              onClick={() => void remove()}
            >
              确认删除规则
            </Button>
          </div>
        </DialogContent>
      </Dialog>
    </>
  );
}
