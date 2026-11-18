import { Badge } from "@/components/ui/badge";
import AS112Guidance from "@/components/AS112Guidance";
import { type AS112Bundle } from "@/lib/as112";

export default function AS112BundlePreview({ bundle }: { bundle: AS112Bundle }) {
  return <section aria-label="AS112 DNS 服务包摘要" className="mb-4 min-w-0 space-y-4">
    <AS112Guidance service={bundle.service} ipv4={bundle.families.includes(4)} ipv6={bundle.families.includes(6)} link={false} />
    <div className="space-y-3 rounded-lg border p-4 text-xs leading-6">
      <div className="flex flex-wrap gap-2"><Badge variant="outline">PowerDNS Authoritative · BIND 后端</Badge><Badge variant="outline">目标健康检查尚未执行</Badge></div>
      <dl className="grid gap-3 sm:grid-cols-2">
        <div><dt className="font-medium">节点身份</dt><dd className="break-all">{bundle.identity.serverName} · {bundle.identity.uniqueIP}</dd><dd>{bundle.identity.facility} · {bundle.identity.location}</dd></div>
        <div><dt className="font-medium">须在本机绑定的服务地址</dt><dd className="break-all">{bundle.listenAddresses.join(" · ")}</dd></div>
        <div><dt className="font-medium">绑定的 DNS 节点地址</dt><dd className="break-all">{bundle.nodeAddresses.join(" · ")}</dd></div>
        <div><dt className="font-medium">健康检查前必须全部存在于本机的地址</dt><dd className="break-all" aria-label="AS112 必需本机地址">{bundle.requiredLocalAddresses.join(" · ")}</dd></div>
      </dl>
      <p className="text-muted-foreground">所有服务地址与所选地址族的节点地址必须属于同一台被检查主机；Junos 双栈包含两个明确的 DNS 下一跳。这里只展示绑定，尚未证明这些地址已经配置到本机。</p>
      <details><summary className="cursor-pointer font-medium">权威区（{bundle.zones.length}）与文件（{bundle.files.length}）</summary><p className="mt-2 break-all text-muted-foreground">{bundle.zones.join(" · ")}</p><ul className="mt-2 list-inside list-disc break-all">{bundle.files.map(file => <li key={file.path}>{file.path}</li>)}</ul></details>
      <details><summary className="cursor-pointer font-medium">查看 DNS 健康检查计划</summary>
        <p className="mt-2 text-muted-foreground">对上述各服务地址分别检查 UDP 和 TCP；要求权威回答且不提供递归。查询计划不会在浏览器中执行。</p>
        <ul className="mt-2 space-y-1">{bundle.healthPlan.checks.map(check => <li key={`${check.name}:${check.type}`} className="break-all">{check.name} · {check.type} · 预期 {check.rcode} · AA=true / RA=false</li>)}</ul>
        <ul className="mt-2 list-inside list-disc">{bundle.healthPlan.requirements.map(requirement => <li key={requirement}>{requirement}</li>)}</ul>
      </details>
      <p className="text-muted-foreground">该 JSON 包须用配套 CLI 严格解包并检查。文件原生解析不等于服务地址已绑定、DNS 已启动或 BGP 已宣告；持续健康失败后的撤回仍需目标运维联动。</p>
    </div>
  </section>;
}
