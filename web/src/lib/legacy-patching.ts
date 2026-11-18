export type LegacyPatchBatch = {
  id: string; importedAt: string; importedBy: string; sourceTimezone: string;
  panels: number; reviewPanels: number; reviewPorts: number; histories: number; attachments: number;
};
export type LegacyPatchSource = {
  id: string; legacyId: number; facts: Record<string, string>;
  name?: string; prefix?: string; panelId?: string; portId?: string; parentId?: string;
  number?: number; state?: number; kind?: "current" | "history";
  private?: boolean; size?: number; checksum?: string;
};
export type LegacyPatchRecord = {
  source: LegacyPatchSource; batchId: string; nativeId?: string; nativePortId?: string;
  documentId?: string; directoryId?: string; review?: string | string[];
};
export type LegacyPatchListing = {
  batches: LegacyPatchBatch[]; panels: LegacyPatchRecord[]; ports: LegacyPatchRecord[];
  histories: LegacyPatchRecord[]; attachments: LegacyPatchRecord[];
};
export type LegacyPatchDocument = {
  id: string; name: string; visibility: string; legacyPatch?: boolean; readOnly?: boolean;
  allocationAccessActive?: boolean;
};
export const legacyPatchStates: Record<number, string> = {
  1: "空闲", 2: "等待交叉连接", 3: "已连接", 4: "等待终止", 5: "已终止",
  6: "故障", 7: "已预留", 8: "已预布线", 999: "其他（需核对）",
};
export const legacyFacts: Record<string, string> = {
  id: "原记录 ID", name: "原名称", cabinet_id: "原机柜 ID", patch_panel_id: "原配线架 ID",
  patch_panel_port_id: "原配线端口 ID", patch_panel_port_history_id: "原历史快照 ID",
  customer_id: "原成员 ID", cust_id: "原历史成员 ID", customer: "原成员名称（来源文字）",
  switch_port_id: "原交换机端口 ID", switchport: "原交换机端口（来源文字）",
  number: "原端口编号", state: "原状态码", notes: "原备注", private_notes: "原私有备注",
  uploaded_by: "原上传人字段", uploaded_at: "原上传时间", created_at: "原创建日期 / 时间",
  updated_at: "原更新日期 / 时间", assigned_at: "原分配日期", connected_at: "原接通日期",
  cease_requested_at: "原终止申请日期", ceased_at: "原终止日期", last_state_change: "原状态更新时间",
  installation_date: "原安装日期", storage_location: "原保存键（非内容摘要）",
  size: "原文件大小（字节）", is_private: "原私有标记", type: "原文件类型",
  duplex_master_id: "原双芯主端口 ID", colo_circuit_ref: "原机房电路编号", ticket_ref: "原工单号",
  description: "原描述", location_notes: "原位置说明", port_prefix: "原端口前缀",
  colo_billing_ref: "原机房账单编号", colo_reference: "原机房标识", owned_by: "原所有者",
  chargeable: "原计费标记", internal_use: "原内部使用标记", active: "原启用标记",
};
export const fileBytes = (size: number) => size < 1024 ? `${size} B` : size < 1024 * 1024
  ? `${(size / 1024).toFixed(1)} KiB` : `${(size / 1024 / 1024).toFixed(1)} MiB`;
export const sourceTimezone = (zone?: string) => !zone || zone === "unknown" ? "未声明源时区" : `源时区：${zone}`;
