import {
  coreCapacity,
  coreEndpointStates,
  corePortLabel,
  coreTypes,
  coreWarningLabels,
  coreDesired,
  type CoreBundle,
  type CoreDerived,
  type CoreInventory,
  type CoreWarning,
} from "@/lib/core";
import { Badge } from "@/components/ui/badge";

export default function CorePlan({
  bundle,
  inventory,
  derived,
  warnings = [],
  compact = false,
}: {
  bundle: CoreBundle;
  inventory: CoreInventory;
  derived?: CoreDerived;
  warnings?: CoreWarning[];
  compact?: boolean;
}) {
  const switchName = (id: string) =>
      String(inventory.switches.find((sw) => sw.id === id)?.name ?? id),
    memberName = (id?: string) =>
      id
        ? String(
            inventory.members.find((member) => member.id === id)?.name ?? id,
          )
        : "未指定";
  return (
    <div className="space-y-4">
      <div>
        <p className="text-sm font-semibold">{bundle.name || "尚未命名"}</p>
        <p className="text-xs text-muted-foreground">
          图表标题：{bundle.graphTitle || "未设置"}
        </p>
      </div>
      <div className="flex flex-wrap gap-2">
        <Badge variant="outline">
          {(coreTypes[bundle.type] ?? bundle.type) || "未选择类型"}
        </Badge>
        <Badge variant="outline">
          {bundle.enabled ? "期望启用" : "期望停用"}
        </Badge>
        <Badge variant="outline">{bundle.links.length} 条成对链路</Badge>
      </div>
      <div className="grid gap-3 md:grid-cols-2">
        {(["sideA", "sideB"] as const).map((side, index) => (
          <div key={side} className="rounded-lg border bg-muted/20 p-3">
            <p className="text-xs text-muted-foreground">
              端点 {index ? "B" : "A"}
            </p>
            <p className="mt-1 break-all text-sm font-semibold">
              {switchName(bundle[side].switchId) || "未选择交换机"}
            </p>
            <p className="mt-2 break-all text-xs leading-6">
              {bundle[side].interfaceName || "未指定逻辑接口名"}
              {bundle[side].channelGroup !== undefined
                ? ` · 组号 ${bundle[side].channelGroup}`
                : ""}
              <br />
              MTU {bundle[side].mtu || "未设置"} ·{" "}
              {bundle[side].trunk ? "802.1q trunk 意图" : "未设 trunk"}
              <br />
              归属：{memberName(bundle[side].ownerMemberId)}
              <br />
              快速 LACP：{bundle[side].fastLacp ? "期望启用" : "未请求"} · LAG
              framing：{bundle[side].lagFraming ? "已设" : "未设"}
            </p>
            {bundle[side].description && (
              <p className="mt-1 whitespace-pre-wrap break-words text-xs text-muted-foreground">
                {bundle[side].description}
              </p>
            )}
          </div>
        ))}
      </div>
      <div className="overflow-x-auto rounded-lg border">
        <table>
          <thead>
            <tr>
              <th>链路</th>
              <th>A 端口</th>
              <th>B 端口</th>
              <th>期望状态</th>
              {!compact && <th>声明参数</th>}
            </tr>
          </thead>
          <tbody>
            {bundle.links.map((link, index) => (
              <tr key={link.id || index}>
                <td className="text-xs">#{index + 1}</td>
                <td className="text-xs">
                  {corePortLabel(link.sideA.portId, inventory)}
                  {bundle.type === "ecmp" && (
                    <p className="mono mt-1 text-[11px] text-muted-foreground">
                      {[link.ipv4Subnet, link.ipv6Subnet]
                        .filter(Boolean)
                        .join(" / ") || "未设子网"}
                    </p>
                  )}
                </td>
                <td className="text-xs">
                  {corePortLabel(link.sideB.portId, inventory)}
                </td>
                <td className="text-xs">
                  {link.enabled ? "启用" : "停用"}
                  {bundle.type === "ecmp" && link.bfd ? " · BFD 意图" : ""}
                </td>
                {!compact && (
                  <td className="text-xs leading-6">
                    A {coreCapacity(link.sideA.speedMbps)} · {link.sideA.duplex}{" "}
                    · {link.sideA.autoneg ? "自协商" : "固定"}
                    <br />B {coreCapacity(link.sideB.speedMbps)} ·{" "}
                    {link.sideB.duplex} ·{" "}
                    {link.sideB.autoneg ? "自协商" : "固定"}
                    <p className="text-muted-foreground">
                      A{" "}
                      {coreEndpointStates[link.sideA.status ?? ""] ??
                        link.sideA.status}{" "}
                      / B{" "}
                      {coreEndpointStates[link.sideB.status ?? ""] ??
                        link.sideB.status}
                    </p>
                  </td>
                )}
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      {bundle.type === "l3-lag" && (
        <p className="mono break-all text-xs">
          Bundle 子网：
          {[bundle.ipv4Subnet, bundle.ipv6Subnet].filter(Boolean).join(" / ") ||
            "未设置"}{" "}
          · BFD：{bundle.bfd ? "期望启用" : "未请求"}
        </p>
      )}
      {bundle.type === "l2-lag" && (
        <p className="text-xs">STP：{bundle.stp ? "期望启用" : "未请求"}</p>
      )}
      {(bundle.cost !== undefined || bundle.preference !== undefined) && (
        <p className="text-xs text-muted-foreground">
          协议 cost：{bundle.cost ?? "未设置"} · preference：
          {bundle.preference ?? "未设置"}。这些元数据不代表备用或保护链路。
        </p>
      )}
      {derived && (
        <>
          <div className="grid gap-3 sm:grid-cols-2">
            <div className="rounded-lg border p-3">
              <p className="text-xs text-muted-foreground">
                匹配双端库存的名义容量
              </p>
              <p className="mt-2 text-lg font-semibold">
                {derived.capacityKnown
                  ? coreCapacity(derived.capacityMbps)
                  : "端口速率不完整或两端不一致"}
              </p>
            </div>
            <div className="rounded-lg border p-3">
              <p className="text-xs text-muted-foreground">BGP 草案条件</p>
              <p className="mt-2 text-sm font-semibold">
                {bundle.type === "l2-lag"
                  ? "二层类型不派生核心 BGP 邻接"
                  : derived.bgpReady
                    ? "草案字段齐全，尚未应用"
                    : "草案尚待补齐或核对"}
              </p>
            </div>
          </div>
          {bundle.type !== "l2-lag" && (
            <div className="rounded-lg border p-3 text-xs leading-6">
              <p>
                A：AS{derived.localAsnA || "未设"} · Router ID{" "}
                {derived.routerIdA || "未设"}
              </p>
              <p>
                B：AS{derived.localAsnB || "未设"} · Router ID{" "}
                {derived.routerIdB || "未设"}
              </p>
              <p className="mt-1 text-muted-foreground">
                ASN 与 Router ID
                来自交换机库存；缺失时可保存草案，不表示可部署。
              </p>
            </div>
          )}
          {derived.addresses.length > 0 && (
            <div className="overflow-x-auto rounded-lg border">
              <table>
                <thead>
                  <tr>
                    <th>范围</th>
                    <th>子网</th>
                    <th>A 地址 / B 的对端</th>
                    <th>B 地址 / A 的对端</th>
                    <th>计划协议</th>
                  </tr>
                </thead>
                <tbody>
                  {derived.addresses.flatMap((entry, index) =>
                    (["ipv4", "ipv6"] as const).flatMap((family) =>
                      entry[family]
                        ? [
                            <tr key={`${entry.linkId || index}-${family}`}>
                              <td className="text-xs">
                                {entry.linkId
                                  ? `链路 #${bundle.links.findIndex((link) => link.id === entry.linkId) + 1}`
                                  : "Bundle"}
                              </td>
                              <td className="mono text-xs">
                                {entry[family]!.subnet}
                              </td>
                              <td className="mono text-xs">
                                {entry[family]!.sideA}
                              </td>
                              <td className="mono text-xs">
                                {entry[family]!.sideB}
                              </td>
                              <td className="text-xs">
                                {entry.enabled ? "计划启用" : "计划停用"}
                                {entry.bfd ? " · BFD" : ""}
                              </td>
                            </tr>,
                          ]
                        : [],
                    ),
                  )}
                </tbody>
              </table>
            </div>
          )}
        </>
      )}
      {warnings.length > 0 && (
        <div className="space-y-2">
          {warnings.map((warning, index) => (
            <p key={`${warning.code}-${index}`} className="notice">
              <span className="mono mr-2">{warning.code}</span>
              {coreWarningLabels[warning.code] ?? warning.message}
            </p>
          ))}
        </div>
      )}
      {compact && (
        <details className="rounded-lg border p-3">
          <summary className="cursor-pointer text-xs font-medium">
            完整库存字段（只读核对）
          </summary>
          <pre className="mono mt-3 max-h-72 overflow-auto whitespace-pre-wrap break-all text-xs">
            {JSON.stringify(coreDesired(bundle), null, 2)}
          </pre>
        </details>
      )}
    </div>
  );
}
