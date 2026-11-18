import { useEffect, useRef, useState, type KeyboardEvent } from "react";
import {
  ArrowUpRight,
  ChevronLeft,
  ChevronRight,
  LoaderCircle,
  Search,
} from "lucide-react";
import { api, APIError } from "@/lib/api";
import { ErrorNotice, message } from "@/components/shared";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogTitle,
} from "@/components/ui/dialog";

type SearchItem = {
  key: string;
  kind: string;
  id: string;
  title: string;
  subtitle: string;
  snippet: string;
  matchedFields: string[];
  href: string;
};
type SearchResult = {
  query: string;
  mode: string;
  groups: { kind: string; label: string; count: number; items: SearchItem[] }[];
  total: number;
  limit: number;
  nextCursor?: string;
  truncated: boolean;
  revision: string;
};
const matchLabels: Record<string, string> = {
  name: "名称",
  title: "标题",
  asn: "ASN",
  address: "地址",
  email: "邮箱",
  username: "用户名",
  body: "备注",
  description: "描述",
  circuitId: "电路编号",
  prefix: "前缀",
  asSet: "AS-SET",
  mac: "MAC",
  tag: "标签",
};
const modes: Record<string, string> = {
  text: "文本",
  asn: "ASN",
  ip: "IP 地址",
  mac: "MAC 地址",
  "as-set": "AS-SET",
  email: "邮箱",
  "patch-port": "配线端口",
  "cross-connect": "交叉连接",
  username: "用户名 / 邮箱",
  "ipv4-suffix": "IPv4 尾部",
  "ipv6-suffix": "IPv6 尾部",
};
const types: [string, string][] = [
  ["members", "成员"],
  ["connections", "成员接入"],
  ["contacts", "联系人"],
  ["users", "用户"],
  ["locations", "机房"],
  ["racks", "机柜"],
  ["switches", "交换机"],
  ["ports", "物理端口"],
  ["vlans", "VLAN"],
  ["routers", "路由器"],
  ["peers", "BGP 邻居"],
  ["vendors", "设备厂商"],
  ["infrastructures", "交换网络"],
  ["mac-addresses", "已登记 MAC"],
  ["learned-macs", "观测 MAC"],
  ["ip-addresses", "IP 地址"],
  ["tags", "标签"],
  ["member-tags", "成员标签关联"],
  ["notes", "备注"],
  ["equipment", "托管设备"],
  ["console-servers", "控制台服务器"],
  ["console-connections", "控制台接入"],
  ["patch-panels", "配线架"],
  ["patch-ports", "配线端口"],
  ["core-bundles", "核心互联"],
  ["core-links", "旧核心链路"],
  ["irr-sources", "IRR 来源"],
  ["contact-groups", "联系人分组"],
  ["contact-group-memberships", "联系人分组关联"],
  ["sflow-exporters", "sFlow 导出器"],
  ["sflow-receivers", "sFlow 接收器"],
  ["provisioning-profiles", "配置工具方案"],
  ["documents", "文档"],
  ["directories", "文档目录"],
];
const localHref = (href: string) => /^#[a-z0-9-]+(?:\?[^#\r\n]*)?$/.test(href);
const memberTypes = new Set([
  "members",
  "connections",
  "contacts",
  "member-tags",
  "tags",
  "notes",
  "equipment",
  "documents",
  "directories",
  "sflow-receivers",
  "patch-ports",
]);

export default function GlobalSearch({
  open,
  role,
  onOpenChange,
  onNavigate,
}: {
  open: boolean;
  role: string;
  onOpenChange: (value: boolean) => void;
  onNavigate: (href: string) => void;
}) {
  const [query, setQuery] = useState(""),
    [kind, setKind] = useState(""),
    [switchId, setSwitchId] = useState(""),
    [result, setResult] = useState<SearchResult | null>(null),
    [loading, setLoading] = useState(false),
    [error, setError] = useState(""),
    [retry, setRetry] = useState(0),
    [page, setPage] = useState(0),
    [cursors, setCursors] = useState<string[]>([""]),
    [selected, setSelected] = useState(0),
    [needsRestart, setNeedsRestart] = useState(false);
  const request = useRef<AbortController | null>(null),
    timer = useRef<ReturnType<typeof setTimeout> | null>(null),
    input = useRef<HTMLInputElement>(null);
  const items = result?.groups.flatMap((group) => group.items) ?? [];
  const availableTypes = types.filter(([value]) =>
    value === "users"
      ? role === "admin"
      : role !== "member" || memberTypes.has(value),
  );
  const resetResults = () => {
    setResult(null);
    setPage(0);
    setCursors([""]);
    setSelected(0);
  };
  const run = async (
    term: string,
    cursor = "",
    nextPage = 0,
    expectedRevision = "",
  ) => {
    if (!open || !term.trim()) return;
    if ([...term.trim()].length < 2 || [...term.trim()].length > 128) {
      resetResults();
      setError("请输入 2–128 个字符。");
      return;
    }
    if (timer.current) clearTimeout(timer.current);
    request.current?.abort();
    const controller = new AbortController();
    request.current = controller;
    setLoading(true);
    setError("");
    setNeedsRestart(false);
    setResult(null);
    setSelected(0);
    try {
      const params = new URLSearchParams({
        q: term.trim(),
        limit: "20",
        ...(kind && availableTypes.some(([value]) => value === kind)
          ? { types: kind }
          : {}),
        ...(role !== "member" && switchId.trim()
          ? { switchId: switchId.trim() }
          : {}),
        ...(cursor ? { cursor } : {}),
      });
      const data = await api<SearchResult>(`/search?${params}`, {
        signal: controller.signal,
      });
      if (controller.signal.aborted) return;
      if (
        !data.revision ||
        !Array.isArray(data.groups) ||
        data.groups.some(
          (group) =>
            !Array.isArray(group.items) ||
            group.items.some((item) => !localHref(item.href)),
        )
      )
        throw new Error("搜索结果格式或本地导航地址无效，未显示结果。");
      if (expectedRevision && data.revision !== expectedRevision)
        throw new APIError("搜索范围已变化，请从第一页重新搜索。", 409);
      if (data.nextCursor && data.nextCursor === cursor)
        throw new Error("搜索分页没有前进，请重新搜索。");
      setResult(data);
      setPage(nextPage);
      setCursors((previous) => [...previous.slice(0, nextPage), cursor]);
    } catch (e) {
      if (controller.signal.aborted) return;
      resetResults();
      if (e instanceof APIError && e.status === 409) {
        setError("数据或授权范围已变化，旧结果已清除。请从第一页重新搜索。");
        setNeedsRestart(true);
      } else if (e instanceof APIError && e.status === 403) {
        setError(
          "当前权限无法读取搜索结果，旧结果已清除。请核对登录状态后重试。",
        );
        setNeedsRestart(true);
      } else setError(message(e));
    } finally {
      if (!controller.signal.aborted) setLoading(false);
    }
  };
  useEffect(() => {
    request.current?.abort();
    if (timer.current) clearTimeout(timer.current);
    resetResults();
    setLoading(false);
    setError("");
    setNeedsRestart(false);
    if (
      open &&
      [...query.trim()].length >= 2 &&
      [...query.trim()].length <= 128
    )
      timer.current = setTimeout(() => void run(query), 250);
    return () => {
      if (timer.current) clearTimeout(timer.current);
      request.current?.abort();
    };
  }, [open, query, kind, switchId, retry, role]);
  useEffect(() => {
    if (role === "member") setSwitchId("");
    if (kind && !availableTypes.some(([value]) => value === kind)) setKind("");
  }, [role]);
  useEffect(() => {
    if (result)
      document
        .getElementById(`global-search-result-${selected}`)
        ?.scrollIntoView({ block: "nearest" });
  }, [selected, result]);
  const close = (value: boolean) => {
    if (!value) {
      request.current?.abort();
      if (timer.current) clearTimeout(timer.current);
      setQuery("");
      setKind("");
      setSwitchId("");
      resetResults();
      setError("");
      setLoading(false);
    }
    onOpenChange(value);
  };
  const navigate = (item: SearchItem) => {
    if (!localHref(item.href)) {
      resetResults();
      setError("该结果没有可用的本地导航地址。");
      return;
    }
    close(false);
    onNavigate(item.href);
  };
  const keyDown = (event: KeyboardEvent<HTMLInputElement>) => {
    if (event.nativeEvent.isComposing) return;
    if (
      (event.key === "ArrowDown" || event.key === "ArrowUp") &&
      items.length
    ) {
      event.preventDefault();
      setSelected((index) =>
        event.key === "ArrowDown"
          ? (index + 1) % items.length
          : (index - 1 + items.length) % items.length,
      );
    } else if (event.key === "Enter") {
      event.preventDefault();
      if (!loading && items[selected]) navigate(items[selected]);
      else if (!loading) void run(query);
    }
  };
  let itemIndex = 0;
  return (
    <Dialog open={open} onOpenChange={close}>
      <DialogContent
        className="max-w-3xl"
        onOpenAutoFocus={(event) => {
          event.preventDefault();
          input.current?.focus();
        }}
      >
        <DialogTitle className="flex items-center gap-2 text-lg font-semibold">
          <Search className="size-5 text-primary" />
          全局搜索
        </DialogTitle>
        <DialogDescription className="text-sm text-muted-foreground">
          查找你可访问的成员、网络资产、备注与文档目录。
        </DialogDescription>
        <div className="relative">
          <Search className="pointer-events-none absolute left-3 top-3 size-4 text-muted-foreground" />
          <Input
            ref={input}
            id="global-search-query"
            role="combobox"
            aria-label="全局搜索内容"
            aria-autocomplete="list"
            aria-controls="global-search-results"
            aria-expanded={!!result && items.length > 0}
            aria-activedescendant={
              items[selected] ? `global-search-result-${selected}` : undefined
            }
            autoComplete="off"
            maxLength={256}
            className="pr-10 pl-9"
            placeholder="名称、ASN、IP、MAC、AS-SET、邮箱…"
            value={query}
            onChange={(e) => {
              request.current?.abort();
              resetResults();
              setQuery(e.target.value);
            }}
            onKeyDown={keyDown}
          />
          {loading && (
            <LoaderCircle
              className="absolute right-3 top-3 size-4 animate-spin text-primary"
              aria-label="正在搜索"
            />
          )}
        </div>
        <div className="grid gap-3 sm:grid-cols-2">
          <div className="field">
            <label htmlFor="global-search-type">搜索类型</label>
            <select
              id="global-search-type"
              value={kind}
              onChange={(e) => {
                request.current?.abort();
                resetResults();
                setKind(e.target.value);
              }}
            >
              <option value="">全部可访问类型</option>
              {availableTypes.map(([value, label]) => (
                <option key={value} value={value}>
                  {label}
                </option>
              ))}
            </select>
          </div>
          {role !== "member" && (
            <details className="rounded-lg border p-3 text-xs">
              <summary className="cursor-pointer text-muted-foreground">
                限定观测 MAC 的交换机
              </summary>
              <div className="field mt-3">
                <label htmlFor="global-search-switch">交换机 ID（可选）</label>
                <Input
                  id="global-search-switch"
                  value={switchId}
                  onChange={(e) => {
                    request.current?.abort();
                    resetResults();
                    setSwitchId(e.target.value);
                  }}
                  placeholder="仅限定观测 MAC 范围"
                />
              </div>
            </details>
          )}
        </div>
        <ErrorNotice
          message={
            error ||
            ([...query.trim()].length > 128
              ? "搜索内容最多支持 128 个字符。"
              : "")
          }
        />
        {needsRestart && (
          <Button
            variant="outline"
            onClick={() => setRetry((value) => value + 1)}
          >
            从第一页重新搜索
          </Button>
        )}
        {[...query.trim()].length < 2 ? (
          <div className="py-8 text-center text-sm leading-7 text-muted-foreground">
            <p>输入至少 2 个字符开始搜索</p>
            <p className="mt-2 text-xs">
              例如 AS64500、192.0.2.1、PPP-123 或 xc:电路编号
            </p>
          </div>
        ) : loading ? (
          <p
            role="status"
            className="py-8 text-center text-sm text-muted-foreground"
          >
            正在搜索当前可访问范围…
          </p>
        ) : (
          result && (
            <>
              <div className="flex flex-wrap items-center justify-between gap-2 text-xs text-muted-foreground">
                <span>
                  共 {result.total} 条可访问匹配 · 本页 {items.length} 条
                </span>
                {modes[result.mode] && (
                  <Badge variant="outline">{modes[result.mode]}</Badge>
                )}
              </div>
              {!items.length ? (
                <p
                  role="status"
                  className="py-8 text-center text-sm text-muted-foreground"
                >
                  没有可访问的匹配结果，请尝试其他关键词。
                </p>
              ) : (
                <div
                  id="global-search-results"
                  role="listbox"
                  aria-label="全局搜索结果"
                  className="max-h-[52dvh] space-y-4 overflow-y-auto overscroll-contain pr-1"
                >
                  {result.groups.map((group) => (
                    <div key={group.kind} role="group" aria-label={group.label}>
                      <h3 className="mb-2 px-2 text-xs font-semibold text-muted-foreground">
                        {group.label} · {group.count}
                      </h3>
                      <div className="space-y-1">
                        {group.items.map((item) => {
                          const index = itemIndex++;
                          const matches = [
                            ...new Set(
                              (item.matchedFields ?? [])
                                .map((field) => matchLabels[field])
                                .filter(Boolean),
                            ),
                          ];
                          return (
                            <button
                              key={
                                item.key ||
                                `${item.kind}/${item.id}/${item.href}`
                              }
                              type="button"
                              id={`global-search-result-${index}`}
                              role="option"
                              aria-selected={selected === index}
                              tabIndex={-1}
                              data-search-kind={item.kind}
                              data-search-id={item.id}
                              className={`flex w-full items-start gap-3 rounded-lg border px-3 py-3 text-left ${selected === index ? "border-primary/30 bg-primary/5" : "border-transparent hover:bg-muted/50"}`}
                              onMouseMove={() => setSelected(index)}
                              onClick={() => navigate(item)}
                            >
                              <div className="min-w-0 flex-1">
                                <p className="break-words text-sm font-semibold">
                                  {item.title}
                                </p>
                                {item.subtitle && (
                                  <p className="mt-1 break-all text-xs text-muted-foreground">
                                    {item.subtitle}
                                  </p>
                                )}
                                {item.snippet && (
                                  <p className="mt-1 line-clamp-2 break-words text-xs leading-5 text-muted-foreground">
                                    {item.snippet}
                                  </p>
                                )}
                                {matches.length > 0 && (
                                  <p className="mt-1 text-[11px] text-muted-foreground">
                                    匹配：{matches.join("、")}
                                  </p>
                                )}
                              </div>
                              <ArrowUpRight className="mt-1 size-4 shrink-0 text-muted-foreground" />
                            </button>
                          );
                        })}
                      </div>
                    </div>
                  ))}
                </div>
              )}
              <div className="flex flex-wrap items-center justify-between gap-2 border-t pt-3 text-xs text-muted-foreground">
                <span>↑ ↓ 选择 · Enter 打开 · Esc 关闭</span>
                <div className="flex items-center gap-2">
                  <Button
                    variant="ghost"
                    size="sm"
                    disabled={loading || page === 0}
                    onClick={() =>
                      void run(
                        query,
                        cursors[page - 1],
                        page - 1,
                        result.revision,
                      )
                    }
                  >
                    <ChevronLeft />
                    上一页
                  </Button>
                  <span>第 {page + 1} 页</span>
                  <Button
                    variant="ghost"
                    size="sm"
                    disabled={loading || !result.nextCursor}
                    onClick={() =>
                      void run(
                        query,
                        result.nextCursor,
                        page + 1,
                        result.revision,
                      )
                    }
                  >
                    下一页
                    <ChevronRight />
                  </Button>
                </div>
              </div>
              {result.truncated && !result.nextCursor && (
                <p className="text-xs text-muted-foreground">
                  结果已达到本次返回上限，请用更具体的关键词缩小范围。
                </p>
              )}
            </>
          )
        )}
      </DialogContent>
    </Dialog>
  );
}
