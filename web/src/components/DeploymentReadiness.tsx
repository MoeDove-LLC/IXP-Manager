import { RefreshCw, ShieldCheck } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { ErrorNotice, time } from "@/components/shared";
import type { RouterFamily } from "@/lib/router-platforms";

export type DeploymentReadinessResult = {
  id: string;
  routerId: string;
  platform: string;
  currentPlatform: string;
  status: string;
  reviewIdentifier: string;
  checkedAt: string;
  inputs: "current" | "changed" | "unavailable";
  artifact: "verified" | "pruned" | "unavailable";
  validationLevel: "native" | "structural" | "none";
  nativeCheckerAvailable: boolean;
  addressFamily?: RouterFamily;
  activationMode: string;
  targetChecked: false;
  actions: Record<"validate" | "approve" | "download" | "generate", { allowed: boolean; reasons: string[] }> & {
    routerosHandoff?: { allowed: boolean; reasons: string[] };
  };
};

const reasons: Record<string, string> = {
  "router-missing": "原目标路由器已删除。请先确认库存中的正确目标。",
  "platform-changed": "目标平台已变更。请按当前路由器生成并审阅新版本。",
  "inputs-changed": "路由或认证输入已变化。请生成新版本，再重新校验和审阅。",
  "inputs-invalid": "当前输入无法生成配置。请修正路由器、成员状态或对等连接后重新检查。",
  "artifact-pruned": "该版本的配置文件已清理。请生成新版本。",
  "artifact-unavailable": "文件缺失或完整性检查失败。请先核对配置存储与备份，再生成可审阅的版本。",
  "preview-only": "此平台当前只提供预览，尚不能通过本流程校验和批准为可交付版本。",
  "checker-missing": "未找到对应平台的原生检查器。请配置该平台的检查器路径后重新检查。",
  "staff-write-required": "需要管理员或操作员的配置写入权限。",
  "browser-admin-required": "需要管理员在浏览器中审阅并批准。",
  "validation-required": "请先完成此版本的配置校验。",
  "approval-required": "请先由管理员批准此版本。",
  "download-permission-required": "下载需要管理员或操作员；自动化还须使用绑定该路由器的拉取令牌。",
};
const activation: Record<string, string> = {
  "bird1-confirmed-configure": "目标机的 ixp-agent 使用同地址族的受管配置、私密配置文件和控制 socket。核对基线与候选文件的 SHA-256 后，等待原生确认和配置读回，再更新启动文件。",
  "bird-confirmed-configure": "目标 BIRD 仍须核对守护进程版本、原生解析与限时配置确认；未确认或失败时按本机发布流程回退。",
  "frr-local-watchdog": "目标 FRR 仍须核对实际配置与运行状态，由专用本机看护服务暂存、确认或回退；超时保护依赖看护服务持续运行。",
  "openbgpd-local-watchdog": "目标 OpenBGPD 仍须核对专用实例、控制 socket 与进程身份，再由本机看护服务暂存、确认或回退；这不是设备原生事务。",
  "quagga-local-watchdog": "批准后下载配置文件和实际 SHA-256。在目标机通过同一 UID 和私密 Unix socket 使用 ixp-agent stage、confirm 或 cancel，守护程序固定专用 bgpd 路径与 SHA-256。暂存和回退都会替换进程并中断 BGP 会话，请安排独占维护窗口。",
  "netconf-confirmed-commit": "控制台只检查 XML 结构。目标 Junos 仍必须执行 candidate validate 和 confirmed-commit，再明确确认；未确认时由设备按期限回滚。",
  unavailable: "此平台的生产激活流程尚未开放；不能根据预览或文件状态推断设备已通过验证。",
};

export default function DeploymentReadiness({ value, loading, error, busy, onRefresh, onGenerate }: { value: DeploymentReadinessResult | null; loading: boolean; error: string; busy: boolean; onRefresh: () => void; onGenerate: () => void }) {
  const applicable = value?.status === "draft" ? "validate" : value?.status === "validated" ? "approve" : "download";
  const messages = [...new Set(value?.actions[applicable]?.reasons ?? [])].filter(code => !code.endsWith("already-recorded"));
  const stale = value && (value.inputs !== "current" || value.artifact !== "verified");
  return <section aria-label="版本发布就绪检查" className="mb-4 space-y-3 rounded-lg border p-4">
    <div className="flex flex-wrap items-start justify-between gap-3"><div><h3 className="flex items-center gap-2 text-sm font-semibold"><ShieldCheck className="size-4" />版本发布就绪检查</h3><p className="mt-1 text-xs text-muted-foreground">{loading ? "正在检查当前输入与配置文件…" : value ? `检查时间：${time(value.checkedAt)}` : "尚未取得当前检查结果。"}</p></div><Button variant="outline" size="sm" disabled={loading || busy} onClick={onRefresh}><RefreshCw />重新检查</Button></div>
    <ErrorNotice message={error} />
    {value && <>
      <div className="flex flex-wrap gap-2"><Badge variant={value.inputs === "current" ? "secondary" : "outline"}>{value.inputs === "current" ? "当前输入一致" : value.inputs === "changed" ? "输入已变化" : "输入不可确认"}</Badge><Badge variant={value.artifact === "verified" ? "secondary" : "outline"}>{value.artifact === "verified" ? "文件完整性通过" : value.artifact === "pruned" ? "文件已清理" : "文件不可交付"}</Badge><Badge variant="outline">{value.validationLevel === "native" ? "原生语法校验流程" : value.validationLevel === "structural" ? "仅结构检查" : "仅预览"}</Badge></div>
      {value.currentPlatform && value.currentPlatform !== value.platform && <p className="text-xs text-muted-foreground">当前库存平台已变为 {value.currentPlatform}，此版本仍属于 {value.platform}。</p>}
      {value.platform === "bird1" && value.addressFamily && (
        <p className="text-xs leading-6 text-muted-foreground">此版本使用 IPv{value.addressFamily}。原生检查器为 <code>{value.addressFamily === 4 ? "bird" : "bird6"}</code>，配置参数 <code>{value.addressFamily === 4 ? "--bird1" : "--bird1-ipv6"}</code>。</p>
      )}
      {value.platform === "quagga" && value.status === "draft" && !value.nativeCheckerAvailable && (
        <p className="text-xs leading-6 text-muted-foreground">请在管理服务用 <code>--quagga-bgpd</code> 指定 Quagga 1.2.4 的 <code>bgpd</code>。运行校验的环境需要支持独立用户和网络命名空间。</p>
      )}
      {value.validationLevel === "native" && value.status === "draft" && <p className="text-xs leading-6 text-muted-foreground">{value.nativeCheckerAvailable ? "已找到检查器，仍须点击校验才能取得该版本的实际解析结果。" : "尚未找到此平台检查器，不能开始原生校验。"}</p>}
      {value.actions.download.allowed && <p role="status" className="text-sm font-medium text-primary">此版本目前可以交付配置文件，尚未验证或应用到目标设备。</p>}
      {messages.length > 0 && <ul className="space-y-1 text-xs leading-6 text-muted-foreground">{messages.map(code => <li key={code}>{reasons[code] ?? "当前条件未满足，请重新检查或联系管理员。"}</li>)}</ul>}
      {stale && value.actions.generate.allowed && <Button size="sm" variant="outline" disabled={busy || loading} onClick={onGenerate}>按当前输入生成新版本</Button>}
      <p className="border-t pt-3 text-xs leading-6 text-muted-foreground">{activation[value.activationMode] ?? activation.unavailable}</p>
      {value.activationMode === "quagga-local-watchdog" && <p className="text-xs leading-6 text-muted-foreground">GR、TCP-MD5 握手和生产目标仍待验收。确认前请核对对端会话和实际路由。</p>}
    </>}
    <p className="text-xs leading-6 text-muted-foreground">检查结果仅对应上述时间，不授予发布权限。校验、批准和实际下载会再次核对当前会话与版本，设备端仍需独立校验、确认和回退保护。</p>
  </section>;
}
