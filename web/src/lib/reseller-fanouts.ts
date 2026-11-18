type InventoryRow = { id: string; [key: string]: unknown };
export type FanoutInventory = Record<string, InventoryRow[]>;
export const fanoutDependencies = ["members", "ports", "switches", "connections", "patch-ports", "core-links", "reseller-fanouts"];
export const fanoutReferencesReady = (refs: FanoutInventory) => fanoutDependencies.every(kind => Array.isArray(refs[kind]));

const value = (row: Record<string, unknown> | undefined, key: string) => typeof row?.[key] === "string" ? row[key] as string : "";
const connectionPorts = (row: InventoryRow) => Array.isArray(row.portIds) && row.portIds.length
  ? row.portIds.filter((id): id is string => typeof id === "string")
  : value(row, "portId") ? [value(row, "portId")] : [];
export function fanoutAccessOwners(portId: string, refs: FanoutInventory): string[] {
  return [...new Set((refs.connections ?? []).filter(row => connectionPorts(row).includes(portId)).map(row => value(row, "memberId")))];
}
export function fanoutDirectReseller(memberId: string, refs: FanoutInventory) {
  const member = refs.members?.find(row => row.id === memberId);
  const reseller = refs.members?.find(row => row.id === value(member, "resellerId"));
  return reseller && reseller.id !== memberId && reseller.isReseller === true ? reseller : undefined;
}
export function fanoutPortLabel(portId: string, refs: FanoutInventory) {
  const port = refs.ports?.find(row => row.id === portId);
  if (!port) return portId || "未选择";
  const sw = refs.switches?.find(row => row.id === value(port, "switchId"));
  return `${value(sw, "name") || value(port, "switchId") || "交换机未登记"} / ${value(port, "name") || port.id}`;
}
export function fanoutPortOwner(portId: string, refs: FanoutInventory) {
  const port = refs.ports?.find(row => row.id === portId), owner = value(port, "memberId");
  return owner ? value(refs.members?.find(row => row.id === owner), "name") || owner : "未指定";
}

export function fanoutPortProblem(
  side: "peering" | "fanout",
  portId: string,
  memberId: string,
  resellerId: string,
  refs: FanoutInventory,
  editingId = "",
) {
  const port = refs.ports?.find(row => row.id === portId);
  if (!port) return "端口不存在或尚未读取。";
  if (port.coreReserved === true || (refs["core-links"] ?? []).some(row => row.portAId === portId || row.portBId === portId))
    return "端口已保留或用于核心互联。";
  if ((refs["reseller-fanouts"] ?? []).some(row => row.id !== editingId && (row.peeringPortId === portId || row.fanoutPortId === portId)))
    return "端口已被另一条分流关系占用。";
  const owner = side === "peering" ? memberId : resellerId;
  if (!owner) return "请先选择成员及其直属经销商。";
  if (value(port, "memberId") && port.memberId !== owner) return "端口库存归属与该侧组织不一致。";
  if ((refs["patch-ports"] ?? []).some(row => row.switchPortId === portId && value(row, "memberId") && row.memberId !== owner))
    return "端口关联的已分配配线属于其他组织。";
  const owners = fanoutAccessOwners(portId, refs);
  if (side === "peering" && (owners.length !== 1 || owners[0] !== memberId))
    return "左侧必须是该成员已登记接入中的实际物理端口。";
  if (side === "fanout" && owners.length) return "右侧端口已承载成员接入。";
  return "";
}

export function fanoutDraftProblem(form: Record<string, unknown>, refs: FanoutInventory, editingId = "") {
  if (!fanoutReferencesReady(refs)) return "端口与成员资料尚未完整读取，请刷新端口选项。";
  const memberId = value(form, "memberId"), resellerId = value(form, "resellerId");
  const reseller = fanoutDirectReseller(memberId, refs);
  if (!reseller) return "请选择已关联有效直属经销商的成员。";
  if (reseller.id !== resellerId) return "草稿经销商与成员当前直属关系不一致，请核对后重新选择。";
  const left = value(form, "peeringPortId"), right = value(form, "fanoutPortId");
  if (!left || !right) return "请选择左侧成员接入端口和右侧经销商端口。";
  if (left === right) return "分流关系的两端必须是不同物理端口。";
  return fanoutPortProblem("peering", left, memberId, resellerId, refs, editingId) ||
    fanoutPortProblem("fanout", right, memberId, resellerId, refs, editingId);
}
