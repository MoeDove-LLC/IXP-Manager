import { ArrowRight } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import type { Row } from "@/lib/resources";
import {
  fanoutDirectReseller,
  fanoutPortLabel,
  fanoutPortOwner,
  fanoutPortProblem,
  fanoutReferencesReady,
} from "@/lib/reseller-fanouts";

export default function ResellerFanoutFields({
  form, refs, editingId, onChange,
}: {
  form: Record<string, unknown>;
  refs: Record<string, Row[]>;
  editingId?: string;
  onChange: (next: Record<string, unknown>) => void;
}) {
  const memberId = String(form.memberId ?? ""), resellerId = String(form.resellerId ?? "");
  const members = refs.members ?? [], ports = refs.ports ?? [];
  const eligibleMembers = members.filter(member => fanoutDirectReseller(member.id, refs));
  const member = members.find(row => row.id === memberId), reseller = members.find(row => row.id === resellerId);
  const directReseller = fanoutDirectReseller(memberId, refs), ready = fanoutReferencesReady(refs);
  const choices = (side: "peering" | "fanout") =>
    !ready || !memberId || !resellerId ? [] : ports.filter(port =>
      !fanoutPortProblem(side, port.id, memberId, resellerId, refs, editingId),
    );
  const portField = (side: "peering" | "fanout", key: "peeringPortId" | "fanoutPortId") => {
    const selected = String(form[key] ?? ""), options = choices(side);
    const problem = selected ? fanoutPortProblem(side, selected, memberId, resellerId, refs, editingId) : "";
    return (
      <div className="field">
        <label htmlFor={`field-${key}`}>{side === "peering" ? "成员接入端口（左）" : "经销商端口（右）"}<span className="ml-1 text-destructive">*</span></label>
        <select id={`field-${key}`} required value={selected} disabled={!ready || !memberId || !resellerId} onChange={event => onChange({ ...form, [key]: event.target.value })}>
          <option value="">请选择端口</option>
          {selected && !options.some(port => port.id === selected) && <option value={selected}>{fanoutPortLabel(selected, refs)}（当前不可用）</option>}
          {options.map(port => <option key={port.id} value={port.id}>{fanoutPortLabel(port.id, refs)} · 库存归属：{fanoutPortOwner(port.id, refs)}</option>)}
        </select>
        {problem && <p className="text-xs leading-5 text-destructive">{problem}</p>}
        {!options.length && ready && memberId && resellerId && <p className="text-xs text-muted-foreground">当前没有符合条件的端口，请核对接入、占用及归属。</p>}
        {selected && <p className="text-xs text-muted-foreground">端口库存归属：{fanoutPortOwner(selected, refs)}</p>}
      </div>
    );
  };
  return (
    <div className="space-y-4 md:col-span-2">
      <div className="field">
        <label htmlFor="field-memberId">接入成员<span className="ml-1 text-destructive">*</span></label>
        <select id="field-memberId" required value={memberId} disabled={!ready} onChange={event => {
          const id = event.target.value;
          onChange({ ...form, memberId: id, resellerId: fanoutDirectReseller(id, refs)?.id ?? "", peeringPortId: "", fanoutPortId: "" });
        }}>
          <option value="">选择已有直属经销商的成员</option>
          {memberId && !eligibleMembers.some(row => row.id === memberId) && <option value={memberId}>{String(member?.name ?? memberId)}（当前关系不可用）</option>}
          {eligibleMembers.map(row => <option key={row.id} value={row.id}>{String(row.name ?? row.id)}</option>)}
        </select>
        {ready && !eligibleMembers.length && <p className="text-xs leading-5 text-muted-foreground">请先在成员资料中配置“所属转售商”，并登记成员接入。</p>}
      </div>
      <div className="field">
        <label htmlFor="field-resellerId">直属经销商</label>
        <Input id="field-resellerId" readOnly value={String(reseller?.name ?? resellerId)} placeholder="选择成员后显示" />
        {memberId && directReseller?.id !== resellerId && <div className="space-y-2"><p className="text-xs text-destructive">草稿与当前直属经销商关系不一致。</p>{directReseller && <Button type="button" variant="outline" size="sm" onClick={() => onChange({ ...form, resellerId: directReseller.id, fanoutPortId: "" })}>采用当前经销商并重选右侧端口</Button>}</div>}
      </div>
      <div className="grid gap-4 md:grid-cols-[minmax(0,1fr)_auto_minmax(0,1fr)]">
        <div className="min-w-0 space-y-3 rounded-lg border p-4"><h3 className="break-words text-sm font-medium">左侧 · {String(member?.name ?? "接入成员")}</h3>{portField("peering", "peeringPortId")}<p className="text-xs leading-5 text-muted-foreground">选择该成员接入记录中的物理端口。</p></div>
        <ArrowRight className="hidden self-center text-muted-foreground md:block" />
        <div className="min-w-0 space-y-3 rounded-lg border p-4"><h3 className="break-words text-sm font-medium">右侧 · {String(reseller?.name ?? "直属经销商")}</h3>{portField("fanout", "fanoutPortId")}<p className="text-xs leading-5 text-muted-foreground">选择归属为空或属于此经销商的空闲端口。</p></div>
      </div>
    </div>
  );
}
