import { useEffect, useState, type FormEvent } from "react";
import { Download, Globe2, Plus, RefreshCw, Search } from "lucide-react";
import { api, save } from "@/lib/api";
import type { Row } from "@/lib/resources";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import {
  Dialog,
  DialogContent,
  DialogTitle,
  DialogDescription,
} from "@/components/ui/dialog";
import {
  Empty,
  ErrorNotice,
  Heading,
  Loading,
  message,
} from "@/components/shared";
type Address = {
  id?: string;
  address: string;
  allocatable?: boolean;
  memberId?: string;
  state: "free" | "reserved" | "assigned" | "router";
  connectionIds: string[];
  routerIds: string[];
};
type Pool = {
  vlanId: string;
  prefixes: {
    family: number;
    prefix: string;
    usable: string;
    occupied: number;
    available: string;
  }[];
  items: Address[];
};
const states = {
  free: "可分配",
  reserved: "已预留",
  assigned: "连接正在使用",
  router: "路由器地址",
};
const exact = (value: string) => {
  try {
    return BigInt(value).toLocaleString("zh-CN");
  } catch {
    return value;
  }
};
export default function AddressPools({ role }: { role: string }) {
  const write = role === "admin" || role === "operator";
  const [vlans, setVlans] = useState<Row[]>([]),
    [members, setMembers] = useState<Row[]>([]),
    [vlan, setVlan] = useState(""),
    [pool, setPool] = useState<Pool | null>(null),
    [loading, setLoading] = useState(true),
    [busy, setBusy] = useState(false),
    [error, setError] = useState(""),
    [search, setSearch] = useState(""),
    [open, setOpen] = useState(false),
    [formError, setFormError] = useState(""),
    [memberId, setMemberId] = useState(""),
    [family, setFamily] = useState("4"),
    [prefix, setPrefix] = useState(""),
    [count, setCount] = useState("1"),
    [address, setAddress] = useState(""),
    [allocated, setAllocated] = useState<Row[]>([]),
    [releasing, setReleasing] = useState<Address | null>(null),
    [refresh, setRefresh] = useState(0);
  useEffect(() => {
    let active = true;
    Promise.all([
      api<{ items: Row[] }>("/resources/vlans"),
      api<{ items: Row[] }>("/resources/members"),
    ])
      .then(([v, m]) => {
        if (active) {
          setVlans(v.items);
          setMembers(m.items);
          setVlan(v.items[0]?.id ?? "");
          if (!v.items.length) setLoading(false);
        }
      })
      .catch((e) => {
        if (active) {
          setError(message(e));
          setLoading(false);
        }
      });
    return () => {
      active = false;
    };
  }, []);
  useEffect(() => {
    setPool(null);
    setAllocated([]);
    setSearch("");
    if (!vlan) return;
    const controller = new AbortController();
    setLoading(true);
    setError("");
    api<Pool>(`/ipam?vlanId=${encodeURIComponent(vlan)}`, {
      signal: controller.signal,
    })
      .then((data) => setPool(data))
      .catch((e) => {
        if (!controller.signal.aborted) setError(message(e));
      })
      .finally(() => {
        if (!controller.signal.aborted) setLoading(false);
      });
    return () => controller.abort();
  }, [vlan, refresh]);
  const allocate = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setFormError("");
    try {
      const result = await save<{ items: Row[] }>("/ipam/allocate", {
        vlanId: vlan,
        memberId,
        family: Number(family),
        ...(prefix ? { prefix } : {}),
        count: address.trim() ? 1 : Number(count),
        ...(address.trim() ? { address: address.trim() } : {}),
      });
      setOpen(false);
      const next = await api<Pool>(`/ipam?vlanId=${encodeURIComponent(vlan)}`);
      setPool(next);
      setAllocated(result.items);
    } catch (e) {
      setFormError(message(e));
    } finally {
      setBusy(false);
    }
  };
  const release = async () => {
    if (!releasing?.id) return;
    setBusy(true);
    setFormError("");
    try {
      await save("/ipam/release", {
        id: releasing.id,
        expectedMemberId: releasing.memberId,
      });
      setReleasing(null);
      setPool(await api<Pool>(`/ipam?vlanId=${encodeURIComponent(vlan)}`));
      setAllocated([]);
    } catch (e) {
      setFormError(message(e));
    } finally {
      setBusy(false);
    }
  };
  const filtered =
    pool?.items.filter((i) =>
      `${i.address} ${members.find((m) => m.id === i.memberId)?.name ?? ""}`
        .toLowerCase()
        .includes(search.toLowerCase()),
    ) ?? [];
  return (
    <>
      <Heading
        title="地址分配"
        description="按交换 VLAN 规划成员地址，自动避开已分配、连接使用与路由器占用的地址。"
      >
        {write && (
          <Button
            disabled={!pool?.prefixes.some((p) => p.available !== "0")}
            onClick={() => {
              setOpen(true);
              setFormError("");
              setMemberId("");
              setFamily(
                String(
                  pool?.prefixes.find((p) => p.available !== "0")?.family ??
                    pool?.prefixes[0]?.family ??
                    4,
                ),
              );
              setPrefix(
                pool?.prefixes.find((p) => p.available !== "0")?.prefix ??
                  pool?.prefixes[0]?.prefix ??
                  "",
              );
              setCount("1");
              setAddress("");
            }}
          >
            <Plus />
            分配地址
          </Button>
        )}
      </Heading>
      <ErrorNotice message={error} />
      <div className="mb-5 flex flex-wrap items-center justify-between gap-3">
        <div className="field min-w-60">
          <label htmlFor="ipam-vlan">交换 VLAN</label>
          <select
            id="ipam-vlan"
            value={vlan}
            onChange={(e) => setVlan(e.target.value)}
          >
            {!vlans.length && <option value="">尚无 VLAN</option>}
            {vlans.map((v) => (
              <option key={v.id} value={v.id}>
                {v.name}
              </option>
            ))}
          </select>
        </div>
        <Button
          variant="outline"
          disabled={loading || !vlan}
          onClick={() => setRefresh((v) => v + 1)}
        >
          <RefreshCw />
          刷新分配状态
        </Button>
      </div>
      {loading ? (
        <Loading />
      ) : !vlan ? (
        <Card>
          <Empty
            title="先建立交换 VLAN"
            description="在 VLAN 与地址页面登记交换网段后，即可自动分配地址。"
            action="管理 VLAN"
            onAction={() => {
              location.hash = "vlans";
            }}
          />
        </Card>
      ) : (
        pool && (
          <>
            <div className="grid gap-5 md:grid-cols-2">
              {pool.prefixes.map((p) => (
                <Card key={p.prefix}>
                  <CardHeader>
                    <CardTitle className="flex items-center gap-2">
                      <Globe2 className="size-4 text-primary" />
                      IPv{p.family} · {p.prefix}
                    </CardTitle>
                  </CardHeader>
                  <CardContent>
                    <dl className="space-y-3 text-xs">
                      <div className="flex flex-wrap justify-between gap-2">
                        <dt className="text-muted-foreground">可使用总数</dt>
                        <dd className="mono break-all">{exact(p.usable)}</dd>
                      </div>
                      <div className="flex justify-between gap-2">
                        <dt className="text-muted-foreground">已占用</dt>
                        <dd className="mono">{p.occupied}</dd>
                      </div>
                      <div className="flex flex-wrap justify-between gap-2">
                        <dt className="text-muted-foreground">可继续分配</dt>
                        <dd className="mono break-all">{exact(p.available)}</dd>
                      </div>
                    </dl>
                  </CardContent>
                </Card>
              ))}
            </div>
            {allocated.length > 0 && (
              <div className="notice my-5" role="status">
                <p className="font-medium">
                  本次已预留 {allocated.length} 个地址
                </p>
                <div className="mt-2 flex flex-wrap gap-2">
                  {allocated.map((a) => (
                    <code
                      key={a.id}
                      className="rounded bg-background px-2 py-1"
                    >
                      {String(a.address)}
                    </code>
                  ))}
                </div>
                <p className="mt-2">
                  地址预留后仍需关联成员接入连接，才会作为实际互联地址使用。
                </p>
              </div>
            )}
            <Card className="mt-6 overflow-hidden">
              <div className="flex flex-wrap items-center justify-between gap-3 border-b p-4">
                <div className="relative w-full max-w-sm">
                  <Search className="absolute left-3 top-3 size-4 text-muted-foreground" />
                  <Input
                    aria-label="搜索地址"
                    className="pl-9"
                    value={search}
                    onChange={(e) => setSearch(e.target.value)}
                    placeholder="搜索地址或成员…"
                  />
                </div>
                <p className="text-xs text-muted-foreground">
                  只列出现有记录，不展开整个 IPv6 网段
                </p>
              </div>
              {!filtered.length ? (
                <Empty
                  title="还没有地址分配记录"
                  description="自动分配会从可用网段中选择空闲地址，也可在向导中指定一个地址。"
                />
              ) : (
                <div className="overflow-x-auto">
                  <table>
                    <thead>
                      <tr>
                        <th>地址</th>
                        <th>成员</th>
                        <th>状态</th>
                        <th>关联</th>
                        {write && <th className="text-right">操作</th>}
                      </tr>
                    </thead>
                    <tbody>
                      {filtered.map((i) => (
                        <tr key={i.address}>
                          <td className="mono text-xs">{i.address}</td>
                          <td>
                            {members.find((m) => m.id === i.memberId)?.name ??
                              (i.memberId || "—")}
                          </td>
                          <td>
                            <Badge
                              variant={
                                i.state === "assigned" || i.state === "router"
                                  ? "default"
                                  : "outline"
                              }
                            >
                              {i.allocatable === false
                                ? "不可作为主机地址"
                                : states[i.state]}
                            </Badge>
                          </td>
                          <td className="text-xs text-muted-foreground">
                            {i.connectionIds.length
                              ? `${i.connectionIds.length} 条连接`
                              : i.routerIds.length
                                ? `${i.routerIds.length} 台路由器`
                                : "未关联使用"}
                          </td>
                          {write && (
                            <td className="text-right">
                              <Button
                                variant="ghost"
                                size="sm"
                                disabled={!i.id || i.state !== "reserved"}
                                title={
                                  i.state === "assigned" || i.state === "router"
                                    ? "使用中的地址不能释放"
                                    : undefined
                                }
                                onClick={() => {
                                  setReleasing(i);
                                  setFormError("");
                                }}
                              >
                                释放预留
                              </Button>
                            </td>
                          )}
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </div>
              )}
            </Card>
          </>
        )
      )}
      <Dialog open={open} onOpenChange={(v) => !busy && setOpen(v)}>
        <DialogContent>
          <DialogTitle className="text-lg font-semibold">
            为成员分配地址
          </DialogTitle>
          <DialogDescription className="text-sm leading-6 text-muted-foreground">
            当前 VLAN：{vlans.find((v) => v.id === vlan)?.name}
            。分配时会重新核对地址占用，整个批次一起提交。
          </DialogDescription>
          <form onSubmit={allocate} className="space-y-4">
            <ErrorNotice message={formError} />
            <div className="field">
              <label htmlFor="allocate-member">成员网络</label>
              <select
                id="allocate-member"
                required
                value={memberId}
                onChange={(e) => setMemberId(e.target.value)}
              >
                <option value="">选择成员</option>
                {members.map((m) => (
                  <option
                    key={m.id}
                    value={m.id}
                    disabled={["suspended", "closed"].includes(
                      String(m.status),
                    )}
                  >
                    {m.name}
                  </option>
                ))}
              </select>
            </div>
            <div className="form-grid">
              <div className="field">
                <label htmlFor="allocate-family">地址族</label>
                <select
                  id="allocate-family"
                  value={family}
                  onChange={(e) => {
                    setFamily(e.target.value);
                    setPrefix(
                      pool?.prefixes.find(
                        (p) =>
                          p.family === Number(e.target.value) &&
                          p.available !== "0",
                      )?.prefix ??
                        pool?.prefixes.find(
                          (p) => p.family === Number(e.target.value),
                        )?.prefix ??
                        "",
                    );
                  }}
                >
                  {[...new Set(pool?.prefixes.map((p) => p.family) ?? [])].map(
                    (value) => (
                      <option key={value} value={value}>
                        IPv{value}
                      </option>
                    ),
                  )}
                </select>
              </div>
              <div className="field">
                <label htmlFor="allocate-count">数量（最多 64）</label>
                <Input
                  id="allocate-count"
                  type="number"
                  min={1}
                  max={64}
                  required
                  disabled={!!address.trim()}
                  value={address.trim() ? "1" : count}
                  onChange={(e) => setCount(e.target.value)}
                />
              </div>
            </div>
            <div className="field">
              <label htmlFor="allocate-prefix">目标子网</label>
              <select
                id="allocate-prefix"
                value={prefix}
                required
                onChange={(e) => setPrefix(e.target.value)}
              >
                {pool?.prefixes
                  .filter((p) => p.family === Number(family))
                  .map((p) => (
                    <option
                      key={p.prefix}
                      value={p.prefix}
                      disabled={p.available === "0"}
                    >
                      {p.prefix} · 剩余 {exact(p.available)}
                    </option>
                  ))}
              </select>
              <p className="text-xs text-muted-foreground">
                每次只从明确选择的子网分配。指定地址也必须属于此网段。
              </p>
            </div>
            <div className="field">
              <label htmlFor="allocate-address">指定地址（可选）</label>
              <Input
                id="allocate-address"
                value={address}
                onChange={(e) => setAddress(e.target.value)}
                placeholder="留空自动选择；指定时只分配一个地址"
              />
            </div>
            <div className="flex justify-end gap-2">
              <Button
                type="button"
                variant="outline"
                disabled={busy}
                onClick={() => setOpen(false)}
              >
                取消
              </Button>
              <Button disabled={busy}>
                {busy ? "正在分配…" : "确认预留地址"}
              </Button>
            </div>
          </form>
        </DialogContent>
      </Dialog>
      <Dialog
        open={!!releasing}
        onOpenChange={(v) => !busy && !v && setReleasing(null)}
      >
        <DialogContent>
          <DialogTitle className="text-lg font-semibold">
            释放预留地址
          </DialogTitle>
          <DialogDescription className="text-sm leading-6 text-muted-foreground">
            将释放 {releasing?.address} 对{" "}
            {members.find((m) => m.id === releasing?.memberId)?.name ??
              "当前成员"}{" "}
            的预留。如果地址已关联连接、路由器，或归属已变化，服务器会拒绝操作。
          </DialogDescription>
          <ErrorNotice message={formError} />
          <div className="flex justify-end gap-2">
            <Button
              variant="outline"
              disabled={busy}
              onClick={() => setReleasing(null)}
            >
              取消
            </Button>
            <Button
              variant="destructive"
              disabled={busy}
              onClick={() => void release()}
            >
              确认释放地址
            </Button>
          </div>
        </DialogContent>
      </Dialog>
    </>
  );
}
