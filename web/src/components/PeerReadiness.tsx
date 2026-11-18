import { useEffect, useState } from "react";
import { RefreshCw } from "lucide-react";
import { api } from "@/lib/api";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogTitle } from "@/components/ui/dialog";
import { ErrorNotice, message, time } from "@/components/shared";

type Readiness = {
  peerId: string;
  routerId: string;
  enabled: boolean;
  canGenerateConfiguration: boolean;
  issues: string[];
  checkedAt: string;
};

const explanations: Record<string, string> = {
  "router-missing": "选择一个已登记的路由器。",
  "router-role-unsupported": "根据路由平台支持的能力调整路由器角色。",
  "member-missing": "选择此邻居所属的成员。",
  "member-inactive": "成员尚未激活，请先核对成员状态。",
  "member-asn-mismatch": "将邻居 ASN 核对为成员登记的 ASN。",
  "local-address-family-missing": "请在路由器上登记与邻居同一地址族的本地地址。",
  "active-attachment-missing": "在路由器所在 VLAN 上，为此成员登记地址与邻居相同的已激活接入。",
  "as112-has-inbound-policy": "AS112 只宣告本源前缀。请移除此会话的导入白名单。",
  "allowlist-required": "审阅要接收的路由并保存明确的前缀白名单。",
  "peer-settings-invalid": "检查邻居地址、路由器地址族和会话字段。",
  "credential-invalid": "在 BGP 凭据管理中重设此会话的凭据。",
  "router-render-failed": "该路由器的配置生成失败。请在配置中心查看该路由器及其他会话的报错。",
};

export default function PeerReadiness({ peerId, name, onClose, onEdit }: {
  peerId: string;
  name: string;
  onClose: () => void;
  onEdit?: () => void;
}) {
  const [revision, setRevision] = useState(0);
  const [value, setValue] = useState<Readiness | null>(null);
  const [failure, setFailure] = useState("");
  const [loading, setLoading] = useState(true);
  useEffect(() => {
    const controller = new AbortController();
    setValue(null);
    setFailure("");
    setLoading(true);
    const check = async () => {
      try {
        const current = await api<Readiness>(`/peer-readiness/${encodeURIComponent(peerId)}`, { signal: controller.signal });
        if (current.peerId !== peerId || !Array.isArray(current.issues) || current.canGenerateConfiguration !== (current.issues.length === 0)) {
          throw new Error("检查结果与当前会话不符，请重新检查。");
        }
        if (!controller.signal.aborted) setValue(current);
      } catch (error) {
        if (!controller.signal.aborted) setFailure(message(error));
      } finally {
        if (!controller.signal.aborted) setLoading(false);
      }
    };
    void check();
    return () => controller.abort();
  }, [peerId, revision]);

  return <Dialog open onOpenChange={open => { if (!open) onClose(); }}>
    <DialogContent>
      <DialogTitle className="break-words">检查 BGP 配置条件</DialogTitle>
      <DialogDescription className="break-words">{name} · 按当前库存和策略试算配置</DialogDescription>
      {loading && <p role="status" className="text-sm text-muted-foreground">正在检查会话配置…</p>}
      <ErrorNotice message={failure} />
      {value && <div className="space-y-3 rounded-lg border p-4">
        <div className="flex flex-wrap items-center gap-2">
          <Badge variant={value.enabled ? "secondary" : "outline"}>{value.enabled ? "已启用" : "已停用"}</Badge>
          <Badge variant={value.canGenerateConfiguration ? "default" : "outline"}>{value.canGenerateConfiguration ? "配置生成检查通过" : "待补齐配置条件"}</Badge>
        </div>
        {value.issues.length > 0 && <ul className="list-disc space-y-2 pl-5 text-sm leading-6">
          {value.issues.map(issue => <li key={issue}>{explanations[issue] ?? "请刷新后重新核对会话。"}</li>)}
        </ul>}
        {value.canGenerateConfiguration && <p className="text-sm leading-6">
          {value.enabled ? "可在配置中心生成版本，继续校验与审批。" : "核对后可编辑并启用此会话，再到配置中心生成版本。"}
        </p>}
        <p className="text-xs text-muted-foreground">{time(value.checkedAt)} · 库存快照检查</p>
      </div>}
      <div className="flex flex-wrap justify-end gap-2">
        <Button variant="outline" disabled={loading} onClick={() => setRevision(n => n + 1)}><RefreshCw />重新检查</Button>
        {onEdit && <Button onClick={onEdit}>编辑会话</Button>}
      </div>
    </DialogContent>
  </Dialog>;
}
