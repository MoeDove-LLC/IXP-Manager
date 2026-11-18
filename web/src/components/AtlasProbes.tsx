import { useCallback, useEffect, useRef, useState } from "react";
import { RefreshCw } from "lucide-react";
import { api, save } from "@/lib/api";
import { type AtlasProbeCache, probeAddress } from "@/lib/atlas";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import {
  Empty,
  ErrorNotice,
  Loading,
  message,
  time,
} from "@/components/shared";

export const probeKey = (memberId: string, af: number) => `${memberId}:${af}`;
export function useAtlasProbes() {
  const [entries, setEntries] = useState<Record<string, AtlasProbeCache>>({}),
    [loading, setLoading] = useState<Record<string, boolean>>({}),
    [errors, setErrors] = useState<Record<string, string>>({});
  const alive = useRef(true),
    sequence = useRef<Record<string, number>>({});
  useEffect(() => {
    alive.current = true;
    return () => {
      alive.current = false;
    };
  }, []);
  const load = useCallback(
    async (
      memberId: string,
      af: number,
      discover = false,
      anchorOnly = false,
    ) => {
      if (!memberId) return;
      const key = probeKey(memberId, af),
        version = (sequence.current[key] ?? 0) + 1;
      sequence.current[key] = version;
      setLoading((values) => ({ ...values, [key]: true }));
      setErrors((values) => ({ ...values, [key]: "" }));
      try {
        if (discover)
          await save("/atlas/probes/discover", { memberId, af, anchorOnly });
        const data = await api<AtlasProbeCache>(
          `/atlas/probes?${new URLSearchParams({ memberId, af: String(af) })}`,
        );
        if (alive.current && sequence.current[key] === version)
          setEntries((values) => ({ ...values, [key]: data }));
        return data;
      } catch (error) {
        if (alive.current && sequence.current[key] === version)
          setErrors((values) => ({ ...values, [key]: message(error) }));
        return undefined;
      } finally {
        if (alive.current && sequence.current[key] === version)
          setLoading((values) => ({ ...values, [key]: false }));
      }
    },
    [],
  );
  return { entries, loading, errors, load };
}

export default function AtlasProbes({
  cache,
  loading,
  error,
  af,
  canDiscover,
  enabled,
  onDiscover,
  selected,
  onSelection,
  maxSelected = 20,
  label = "来源探针",
}: {
  cache?: AtlasProbeCache;
  loading: boolean;
  error?: string;
  af: number;
  canDiscover: boolean;
  enabled: boolean;
  onDiscover: (anchorOnly: boolean) => void;
  selected?: number[];
  onSelection?: (ids: number[]) => void;
  maxSelected?: number;
  label?: string;
}) {
  const [anchorOnly, setAnchorOnly] = useState(false);
  const selectable = !!onSelection,
    stale = cache?.stale ?? cache?.snapshot?.stale ?? true;
  return (
    <div className="space-y-3">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div className="flex flex-wrap items-center gap-2">
          <span className="text-sm font-medium">{label}</span>
          {cache?.snapshot && (
            <Badge variant={stale ? "outline" : "default"}>
              {stale ? "快照过期" : "已缓存快照"}
            </Badge>
          )}
        </div>
        {canDiscover && (
          <div className="flex flex-wrap items-center gap-3">
            <label className="flex items-center gap-2 text-xs">
              <input
                type="checkbox"
                checked={anchorOnly}
                onChange={(event) => setAnchorOnly(event.target.checked)}
                disabled={loading}
              />
              仅 Atlas Anchor
            </label>
            <Button
              type="button"
              size="sm"
              variant="outline"
              disabled={!enabled || loading}
              onClick={() => onDiscover(anchorOnly)}
            >
              <RefreshCw className={loading ? "animate-spin" : ""} />
              {loading ? "正在获取…" : "发现 / 更新探针"}
            </Button>
          </div>
        )}
      </div>
      <ErrorNotice message={error ?? ""} />
      {error && cache?.snapshot && (
        <p className="notice">获取失败，以下仍为上次保存的探针快照。</p>
      )}
      {cache?.snapshot && (
        <p className="text-xs leading-6 text-muted-foreground">
          AS{cache.snapshot.asn} · IPv{af} · 获取于{" "}
          {time(cache.snapshot.fetchedAt)} · 缓存 {cache.items.length}{" "}
          个探针。探针公开性与测量结果公开性是分别设置的。
        </p>
      )}
      {stale && cache?.snapshot && (
        <p className="notice">
          请先更新过期的探针快照，再用于新提案。快照中的连接状态不代表持续在线。
        </p>
      )}
      {cache?.snapshot?.truncated && (
        <p className="notice">
          这里只显示 provider 返回的有界部分探针，不能视为该成员的完整探针清单。
        </p>
      )}
      {loading && !cache ? (
        <Loading />
      ) : !cache?.items.length ? (
        <Empty
          title="没有缓存的可用探针"
          description={
            canDiscover
              ? "选择成员与地址族后发现探针，或检查 Atlas 集成是否已配置。"
              : "当前成员与地址族尚无可查看的探针快照。"
          }
        />
      ) : (
        <div className="max-h-72 overflow-auto rounded-lg border">
          <table>
            <thead>
              <tr>
                {selectable && <th>选择</th>}
                <th>探针</th>
                <th>地址</th>
                <th>地区 / 属性</th>
                <th>快照状态</th>
              </tr>
            </thead>
            <tbody>
              {cache.items.map((probe) => (
                <tr key={probe.id}>
                  {selectable && (
                    <td>
                      <input
                        type="checkbox"
                        aria-label={`选择 Atlas 探针 ${probe.id}`}
                        checked={selected?.includes(probe.id) ?? false}
                        disabled={
                          stale ||
                          loading ||
                          !probeAddress(probe, af) ||
                          (!selected?.includes(probe.id) &&
                            (selected?.length ?? 0) >= maxSelected)
                        }
                        onChange={(event) =>
                          onSelection?.(
                            event.target.checked
                              ? [...(selected ?? []), probe.id]
                              : (selected ?? []).filter(
                                  (id) => id !== probe.id,
                                ),
                          )
                        }
                      />
                    </td>
                  )}
                  <td className="mono text-xs">#{probe.id}</td>
                  <td className="mono break-all text-xs">
                    {probeAddress(probe, af) || "此地址族未提供地址"}
                  </td>
                  <td className="text-xs">
                    {probe.countryCode || "地区未知"}
                    {probe.isAnchor ? " · Anchor" : ""}
                    {probe.isPublic ? " · 公开探针" : ""}
                  </td>
                  <td className="text-xs">
                    {String(probe.status)}
                    <p className="mt-1 text-[10px] text-muted-foreground">
                      最近连接 {time(probe.lastConnected)}
                    </p>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      {selectable && (
        <p className="text-xs text-muted-foreground">
          已选择 {selected?.length ?? 0} 个来源探针；当前上限 {maxSelected}。
        </p>
      )}
    </div>
  );
}
