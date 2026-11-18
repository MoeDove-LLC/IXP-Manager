import { Badge } from "@/components/ui/badge";

export default function AS112Guidance({ service, ipv4, ipv6, platform, nextHop4, nextHop6, link = true }: { service?: string; ipv4?: boolean; ipv6?: boolean; platform?: string; nextHop4?: string; nextHop6?: string; link?: boolean }) {
  const selected = service === "direct-delegation" || service === "dname" || service === "both";
  const rows = [
    { service: "direct-delegation", label: "直接委派 DD", v4: "192.175.48.0/24", v6: "2620:4f:8000::/48" },
    { service: "dname", label: "DNAME 接收服务", v4: "192.31.196.0/24", v6: "2001:4:112::/48" },
  ].filter(row => service === "both" || service === row.service);
  return <section aria-label="AS112 服务边界" className="min-w-0 space-y-3 rounded-lg border p-4 text-xs leading-6">
    <div className="flex flex-wrap items-center gap-2"><Badge variant="outline">AS 112</Badge><span className="font-medium">权威 DNS 与本源服务路由</span></div>
    {selected && <div className="space-y-2">{rows.map(row => <div key={row.service}><p className="font-medium">{row.label}</p><p className="break-all text-muted-foreground">{[ipv4 && row.v4, ipv6 && row.v6].filter(Boolean).join(" · ") || "尚未选择地址族"}</p></div>)}</div>}
    <p>仅向手工配置并启用的邻居宣告本源服务前缀，固定拒收所有 BGP 路由，不提供成员间转发。DNS 主机的返回路径须在操作系统单独配置。</p>
    {platform === "junos" && <p className="break-all">Junos 只向明确的 DNS 后端转发固定服务地址（/32、/128），不在路由器上运行 PowerDNS。{nextHop4 !== undefined || nextHop6 !== undefined ? <>DNS 下一跳：{[nextHop4, nextHop6].filter(Boolean).join(" · ") || "尚未填写"}。</> : "具体下一跳见本版本配置。"}仍需目标 NETCONF validate 与 confirmed-commit。</p>}
    <p>DNS 节点须先配置服务地址并启动权威 DNS，通过 IPv4／IPv6、UDP／TCP 检查后再宣告路由。DNS 或服务地址失效时须撤回宣告；配置解析与健康检查计划均不代表服务已经上线。</p>
    <p className="text-muted-foreground">DNAME 由来源区管理员配置，AS112 节点提供 empty.as112.arpa 权威空区。{link && <a href="#provisioning" className="text-primary underline">前往服务配置生成 AS112 DNS 包</a>}</p>
  </section>;
}
