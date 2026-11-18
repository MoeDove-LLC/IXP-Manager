export type Entity = { id: string; [key: string]: unknown };
export type Field = {
  key: string;
  label: string;
  section?: string;
  readOnly?: boolean;
  type?:
    | "text"
    | "email"
    | "password"
    | "number"
    | "textarea"
    | "prefixes"
    | "select"
    | "checkbox"
    | "multiselect"
    | "date";
  required?: boolean;
  placeholder?: string;
  reference?: string;
  options?: string[];
  min?: number;
  max?: number;
  step?: number | string;
  hint?: string;
  default?: unknown;
};
export type Resource = {
  key: string;
  label: string;
  singular: string;
  description: string;
  fields: Field[];
  columns: string[];
  adminOnly?: boolean;
};
const name: Field = { key: "name", label: "名称", required: true };
const notes: Field = { key: "notes", label: "备注", type: "textarea" };
const ref = (
  key: string,
  label: string,
  reference: string,
  required = true,
): Field => ({ key, label, reference, required });
const text = (key: string, label: string, required = false): Field => ({
  key,
  label,
  required,
});
const num = (
  key: string,
  label: string,
  min: number,
  max: number,
  required = true,
  value?: number,
): Field => ({
  key,
  label,
  type: "number",
  min,
  max,
  required,
  default: value,
});
const select = (
  key: string,
  label: string,
  options: string[],
  value = options[0],
): Field => ({ key, label, type: "select", options, default: value });
const member = ref("memberId", "成员", "members");
const location = ref("locationId", "数据中心", "locations");
const vlan = ref("vlanId", "VLAN", "vlans");
const asn = num("asn", "自治系统号 (ASN)", 1, 4294967294);
const resourcesBase: Resource[] = [
  {
    key: "members",
    label: "成员管理",
    singular: "成员",
    description: "管理接入组织、自治系统与成员信息。",
    columns: ["name", "asn", "email", "status"],
    fields: [
      { ...name, section: "成员信息" },
      asn,
      select("type", "成员类型", [
        "full",
        "probono",
        "internal",
        "associate",
        "reseller",
      ]),
      select("status", "状态", ["active", "pending", "suspended", "closed"]),
      { ...text("statusReason", "状态原因"), type: "textarea" },
      {
        key: "email",
        label: "联系邮箱",
        type: "email",
        section: "注册与联络资料",
      },
      { key: "website", label: "网站", placeholder: "https://" },
      text("phone", "电话"),
      text("address", "注册地址"),
      text("city", "城市"),
      text("country", "国家 / 地区"),
      text("postalCode", "邮政编码"),
      text("registrationNumber", "注册编号"),
      text("vatNumber", "税号"),
      { ...text("billingName", "账单抬头"), section: "账务信息" },
      { key: "billingEmail", label: "账务邮箱", type: "email" },
      { ...text("billingAddress", "账单地址"), type: "textarea" },
      {
        key: "joinedAt",
        label: "加入日期",
        type: "date",
        section: "生命周期与备注",
      },
      { key: "leftAt", label: "离开日期", type: "date" },
      notes,
    ],
  },
  {
    key: "locations",
    label: "数据中心",
    singular: "数据中心",
    description: "维护机房位置与现场信息。",
    columns: ["name", "city", "country", "address"],
    fields: [
      name,
      text("city", "城市"),
      text("country", "国家或地区"),
      text("address", "地址"),
    ],
  },
  {
    key: "infrastructures",
    label: "交换网络",
    singular: "交换网络",
    description: "将不同的交换平面独立管理。",
    columns: ["name", "shortName", "country", "isPrimary", "excludeIxf"],
    fields: [
      name,
      text("shortName", "简称"),
      text("country", "国家 / 地区"),
      text("website", "网站"),
      num("ixfId", "IX-F 交换点编号", 1, 2147483647, false),
      {
        key: "isPrimary",
        label: "主交换网络",
        type: "checkbox",
        default: false,
      },
      {
        key: "excludeIxf",
        label: "暂不导出 IX-F",
        type: "checkbox",
        default: true,
      },
      notes,
    ],
  },
  {
    key: "switches",
    label: "交换机",
    singular: "交换机",
    description: "管理网络设备及其所属机房与机柜。",
    columns: ["name", "locationId", "rackId", "managementIp"],
    fields: [
      name,
      location,
      {
        ...ref("infrastructureId", "交换网络", "infrastructures", false),
        hint: "保留为空可继续使用原有未绑定网络；已有连接请使用交换网络批量迁移工具。",
      },
      ref("rackId", "机柜", "racks", false),
      text("managementIp", "管理 IP 地址"),
      text("model", "型号"),
      ref("vendorId", "厂商", "vendors", false),
      {
        ...num("asn", "核心互联本地 ASN", 1, 4294967294, false),
        section: "核心路由库存",
      },
      text("loopbackIpv4", "Loopback IPv4 / Router ID"),
      text("loopbackIpv6", "Loopback IPv6"),
      text("loopbackName", "Loopback 接口名"),
      {
        key: "active",
        label: "交换机计划启用",
        type: "checkbox",
        default: true,
        hint: "控制核心邻居与 floodlist 的配置计划，不代表实际设备在线；监测开关另行管理。",
      },
    ],
  },
  {
    key: "vlans",
    label: "VLAN 与地址",
    singular: "VLAN",
    description: "集中规划 VLAN 与 IPv4 / IPv6 交换网段。",
    columns: ["name", "tag", "ipv4Prefix", "ipv6Prefix"],
    fields: [
      name,
      num("tag", "VLAN ID", 1, 4094),
      ref("infrastructureId", "交换网络", "infrastructures", false),
      { key: "ipv4Prefix", label: "IPv4 网段", placeholder: "192.0.2.0/24" },
      { key: "ipv6Prefix", label: "IPv6 网段", placeholder: "2001:db8::/64" },
      {
        key: "prefixes",
        label: "额外互联子网",
        type: "prefixes",
        hint: "每行一个规范 CIDR，可同时登记多个 IPv4 / IPv6 子网；保留上方已有网段，无需聚合或扩大发布范围。",
      },
      {
        key: "excludeIxf",
        label: "从 IX-F 导出中排除此 VLAN",
        type: "checkbox",
        default: false,
      },
    ],
  },
  {
    key: "ports",
    label: "物理端口",
    singular: "端口",
    description: "管理交换机端口、带宽和端口状态。",
    columns: ["name", "switchId", "speed", "status"],
    fields: [
      name,
      ref("switchId", "交换机", "switches"),
      num("speed", "速率 (Mbps)", 1, 10000000, false),
      select("status", "状态", ["active", "inactive"]),
      ref("memberId", "所属成员", "members", false),
      {
        key: "coreReserved",
        label: "保留为核心用途",
        type: "checkbox",
        default: false,
        hint: "核心工作台保存时会自动预留；移除链路后仍保留此标记。仍有核心引用时不能清除，解除后也需先确认设备与线缆已处理，才能用于成员接入。",
      },
    ],
  },
  {
    key: "connections",
    label: "成员接入",
    singular: "接入",
    description: "关联成员、单端口或 LAG 聚合链路、交换 VLAN 与对等地址。",
    columns: [
      "name",
      "memberId",
      "portIds",
      "linkMode",
      "speed",
      "vlanId",
      "status",
    ],
    fields: [
      name,
      member,
      {
        ...ref("portIds", "物理端口", "ports"),
        type: "multiselect",
        hint: "至少选择一个端口。多个端口组成 LAG；跨交换机时，请确认设备侧已配置 MLAG。",
      },
      {
        ...select("linkMode", "连接模式", ["single", "lag"]),
        hint: "单端口模式选择一个端口；LAG 可保留单个成员，多个端口时自动采用 LAG。",
      },
      {
        ...num("mtu", "MTU", 1280, 9216, false),
        hint: "可选，需与成员及交换网络的实际 MTU 一致。",
      },
      vlan,
      { key: "ipv4", label: "IPv4 地址", placeholder: "192.0.2.10" },
      { key: "ipv6", label: "IPv6 地址", placeholder: "2001:db8::10" },
      num("speed", "速率 (Mbps)", 1, 10000000, false),
      select("status", "状态", ["active", "pending", "disabled", "quarantine"]),
    ],
  },
  {
    key: "reseller-fanouts",
    label: "经销商分流",
    singular: "分流关系",
    description: "登记成员接入端口与直属经销商端口之间的本地配线关系。",
    columns: ["name", "memberId", "peeringPortId", "resellerId", "fanoutPortId"],
    fields: [
      name,
      ref("memberId", "接入成员", "members"),
      ref("resellerId", "直属经销商", "members"),
      ref("peeringPortId", "成员接入端口（左）", "ports"),
      ref("fanoutPortId", "经销商端口（右）", "ports"),
      notes,
    ],
  },
  {
    key: "routers",
    label: "路由服务器",
    singular: "路由服务器",
    description: "管理路由服务器、只收集路由的收集器与 AS112 服务节点，校验后再发布。",
    columns: ["name", "role", "asn", "routerId", "vlanId"],
    fields: [
      name,
      select("platform", "路由平台", [
        "bird2",
        "bird3",
        "bird1",
        "quagga",
        "frr",
        "openbgpd",
        "routeros7",
        "junos",
      ]),
      {
        ...select("role", "路由角色", ["route-server", "route-collector", "as112"]),
        hint: "路由服务器交换成员路由；收集器不导出；AS112 仅宣告权威 DNS 服务本源前缀，固定拒收路由。",
      },
      { ...select("as112Service", "AS112 服务", ["both", "direct-delegation", "dname"]), default: "both" },
      { key: "as112IPv4", label: "AS112 IPv4 服务", type: "checkbox", default: true },
      { key: "as112IPv6", label: "AS112 IPv6 服务", type: "checkbox", default: true },
      { ...text("as112NextHop4", "Junos 的 DNS 节点 IPv4 下一跳"), hint: "启用 IPv4 时必填；Junos 将固定服务地址 /32 指向此 DNS 节点，不在路由器上运行 PowerDNS。" },
      { ...text("as112NextHop6", "Junos 的 DNS 节点 IPv6 下一跳"), hint: "启用 IPv6 时必填；必须是明确的 DNS 后端单播地址，不接受 AS112 anycast 服务地址。" },
      {
        ...select("collectorPolicy", "收集策略", [
          "observe",
          "authorized-only",
        ]),
        hint: "观察模式保留被路由策略拒绝的路由供本机诊断，授权路由表仍按策略筛选；仅授权模式只收取符合策略的路由。协议安全检查拒绝的路由不保证保留。",
      },
      {
        key: "rfc1997Passthrough",
        label: "RFC 1997 社区处理",
        type: "select",
        options: ["native", "true", "false"],
        default: "native",
        hint: "控制 NO_EXPORT、NO_ADVERTISE 等众所周知社区由服务器执行还是保留给客户端处理，不是通用社区过滤器。",
      },
      {
        ...select("rpkiPolicy", "实时 RPKI 策略", ["off", "require-valid"]),
        hint: "BIRD 2/3 和 FRR 支持；启用后运行时必须为 ROA_VALID 才接受路由，不接受缓存失效后的未知结果。",
      },
      {
        ...text("rpkiHost", "RPKI 缓存 IP"),
        hint: "RPKI-RTR 服务的 IPv4 / IPv6 地址。",
      },
      num("rpkiPort", "RPKI-RTR 端口", 1, 65535, false, 323),
      num("rpkiExpireSeconds", "缓存有效期（秒）", 60, 86400, false, 600),
      asn,
      {
        key: "routerId",
        label: "Router ID",
        required: true,
        placeholder: "192.0.2.1",
      },
      vlan,
      { key: "localIpv4", label: "本地 IPv4 地址", placeholder: "192.0.2.1" },
      { key: "localIpv6", label: "本地 IPv6 地址", placeholder: "2001:db8::1" },
    ],
  },
  {
    key: "peers",
    label: "BGP 对等会话",
    singular: "对等会话",
    description: "设置邻居地址、最大前缀数与明确的路由前缀白名单。",
    columns: ["name", "routerId", "memberId", "address", "enabled"],
    fields: [
      name,
      ref("routerId", "路由服务器", "routers"),
      member,
      asn,
      {
        key: "address",
        label: "邻居 IP 地址",
        required: true,
        hint: "必须匹配此成员在服务器 VLAN 上的已启用接入连接。",
      },
      num("maxPrefixes", "最大前缀数", 1, 1000000, true, 1000),
      {
        key: "enabled",
        label: "启用此对等会话",
        type: "checkbox",
        default: false,
      },
      {
        key: "allowedPrefixes",
        label: "允许的路由前缀",
        type: "prefixes",
        required: true,
        hint: "每行一个 CIDR，地址族须与邻居一致。可先停用会话并留空，再到策略工作台生成白名单；启用前必须填写允许的前缀。",
        placeholder: "198.51.100.0/24",
      },
    ],
  },
  {
    key: "contacts",
    label: "联系人",
    singular: "联系人",
    description: "维护成员的技术、NOC 与业务联系人。",
    columns: ["name", "memberId", "email", "role"],
    fields: [
      name,
      member,
      { key: "email", label: "邮箱", type: "email", required: true },
      text("phone", "电话"),
      select("role", "职责", ["technical", "noc", "billing", "admin"]),
    ],
  },
  {
    key: "racks",
    label: "机柜",
    singular: "机柜",
    description: "维护机柜与机房的归属关系。",
    columns: ["name", "locationId", "units"],
    fields: [name, location, num("units", "高度 (U)", 1, 100, false)],
  },
  {
    key: "vendors",
    label: "设备厂商",
    singular: "厂商",
    description: "维护设备厂商信息。",
    columns: ["name", "website"],
    fields: [name, text("website", "网站")],
  },
  {
    key: "mac-addresses",
    label: "MAC 地址",
    singular: "MAC 地址",
    description: "登记成员接入的链路层地址。",
    columns: ["name", "connectionId", "address"],
    fields: [
      name,
      ref("connectionId", "成员接入", "connections"),
      text("address", "MAC 地址", true),
    ],
  },
  {
    key: "ip-addresses",
    label: "IP 地址分配",
    singular: "IP 地址",
    description: "查看交换 VLAN 的地址分配。",
    columns: ["name", "vlanId", "address", "memberId", "dnsName"],
    fields: [
      name,
      vlan,
      text("address", "IP 地址", true),
      ref("memberId", "成员", "members", false),
      {
        ...text("dnsName", "DNS 主机名"),
        hint: "仅分配给成员的预留地址可设置完整域名；释放地址会清除主机名。同成员接入未填写主机名时可继承此值，更换成员归属前请先清空。",
      },
    ],
  },
  {
    key: "tags",
    label: "成员标签",
    singular: "标签",
    description: "创建组织分类标签。",
    columns: ["name", "description"],
    fields: [name, text("description", "说明")],
  },
  {
    key: "member-tags",
    label: "标签关联",
    singular: "标签关联",
    description: "为成员分配标签。",
    columns: ["name", "memberId", "tagId"],
    fields: [name, member, ref("tagId", "标签", "tags")],
  },
  {
    key: "notes",
    label: "成员备注",
    singular: "备注",
    description: "记录成员相关的工作信息。",
    columns: ["name", "memberId", "visibility"],
    fields: [
      name,
      member,
      { key: "body", label: "内容", type: "textarea", required: true },
      select("visibility", "可见范围", ["internal", "member"]),
    ],
  },
  {
    key: "equipment",
    label: "成员设备",
    singular: "成员设备",
    description: "维护成员设备与机柜位置。",
    columns: ["name", "memberId", "rackId"],
    fields: [
      name,
      member,
      ref("rackId", "机柜", "racks", false),
      text("description", "说明"),
    ],
  },
  {
    key: "console-servers",
    label: "控制台服务器",
    singular: "控制台服务器",
    description: "登记控制台设备、管理地址与资产位置；配置状态只表示库存意图。",
    columns: [
      "name",
      "hostname",
      "managementIp",
      "rackId",
      "active",
      "connectionCount",
    ],
    fields: [
      { ...name, section: "设备与地址" },
      {
        ...text("hostname", "主机名"),
        hint: "支持国际化域名，保存时离线转换为标准 ASCII；不解析 DNS。与管理 IP 至少记录一项。",
      },
      text("managementIp", "管理 IP"),
      location,
      ref("rackId", "机柜", "racks", false),
      ref("vendorId", "厂商", "vendors", false),
      { ...text("model", "型号"), section: "资产与备注" },
      text("serialNumber", "序列号"),
      {
        key: "active",
        label: "配置启用",
        type: "checkbox",
        default: true,
        hint: "只记录启用意图，不代表设备在线或已执行配置。",
      },
      notes,
      {
        ...text("legacyConsole", "原系统待核对字段"),
        type: "textarea",
        readOnly: true,
        hint: "导入时保留的原值，只作来源核对，不代表当前设备配置。",
      },
    ],
  },
  {
    key: "console-connections",
    label: "控制台连接",
    singular: "控制台连接",
    description:
      "关联成员、串口标识和已记录的串口参数；归属记录不授予远程终端访问。",
    columns: ["name", "consoleServerId", "memberId", "portLabel", "autobaud"],
    fields: [
      { ...name, section: "归属与串口" },
      text("description", "连接设备说明"),
      ref("consoleServerId", "控制台服务器", "console-servers"),
      member,
      {
        ...text("portLabel", "串口标识", true),
        hint: "例如 01、ttyS0 或 slot/2；保留前导零，不是 TCP 端口号。同一服务器内不区分大小写，不能含首尾空白。",
      },
      {
        key: "autobaud",
        label: "自动波特率记录",
        type: "select",
        options: ["unknown", "true", "false"],
        default: "unknown",
        section: "串口参数",
      },
      {
        ...num("baudRate", "波特率（baud）", 300, 230400, false),
        options: [
          "300",
          "600",
          "1200",
          "2400",
          "4800",
          "9600",
          "14400",
          "19200",
          "28800",
          "38400",
          "57600",
          "115200",
          "230400",
        ],
      },
      {
        ...select("parity", "校验位", ["even", "odd", "none"], ""),
        required: false,
      },
      { ...num("stopBits", "停止位", 1, 2, false), options: ["1", "2"] },
      {
        ...select("flowControl", "流控", ["none", "rts-cts", "xon-xoff"], ""),
        required: false,
      },
      notes,
      {
        ...text("legacySerial", "原系统串口 / 关联字段"),
        type: "textarea",
        readOnly: true,
        hint: "未自动转换的来源值；不会替代当前参数，也不用于建立设备连接。",
      },
    ],
  },
  {
    key: "patch-panels",
    label: "配线架",
    singular: "配线架",
    description: "登记机柜中的配线架与端口容量。",
    columns: ["name", "rackId", "portCount"],
    fields: [
      name,
      ref("rackId", "机柜", "racks"),
      num("portCount", "端口数量", 1, 1024),
    ],
  },
  {
    key: "patch-ports",
    label: "配线架端口",
    singular: "配线端口",
    description: "跟踪配线端口、线路编号与成员归属。",
    columns: ["name", "panelId", "number", "status"],
    fields: [
      name,
      ref("panelId", "配线架", "patch-panels"),
      num("number", "端口编号", 1, 1024),
      ref("memberId", "成员", "members", false),
      select("status", "状态", ["available", "reserved", "connected"]),
      text("circuitId", "线路编号"),
      notes,
    ],
  },
  {
    key: "core-links",
    label: "未归组核心链路",
    singular: "未归组核心链路",
    description: "兼容库存仍占用端口；聚合与 ECMP 请在核心互联工作台显式归组。",
    columns: ["name", "portAId", "portBId", "type"],
    fields: [
      name,
      ref("portAId", "端点 A", "ports"),
      ref("portBId", "端点 B", "ports"),
      select("type", "链路类型", ["layer2", "layer3"]),
      notes,
    ],
  },
  {
    key: "irr-sources",
    label: "IRR 数据源",
    singular: "IRR 数据源",
    description: "登记路由注册数据源连接信息。",
    columns: ["name", "host", "port"],
    fields: [
      name,
      text("host", "主机", true),
      num("port", "端口", 1, 65535, true, 43),
    ],
  },
  {
    key: "contact-groups",
    label: "联系组",
    singular: "联系组",
    description: "组织技术、值班与业务联系人，便于后续按职责联络。",
    columns: ["name", "description"],
    fields: [name, text("description", "说明")],
  },
  {
    key: "contact-group-memberships",
    label: "联系组成员",
    singular: "联系组关联",
    description: "将联系人加入所需的联系组。",
    columns: ["name", "contactId", "groupId"],
    fields: [
      name,
      ref("contactId", "联系人", "contacts"),
      ref("groupId", "联系组", "contact-groups"),
    ],
  },
  {
    key: "users",
    label: "用户与权限",
    singular: "用户",
    description: "管理工作空间的账户与访问权限。",
    adminOnly: true,
    columns: ["name", "email", "role", "memberIds"],
    fields: [
      name,
      { key: "email", label: "邮箱", type: "email", required: true },
      select("role", "角色", ["viewer", "operator", "admin", "member"]),
      {
        ...ref("memberIds", "可访问的成员网络", "members", false),
        type: "multiselect",
        hint: "成员账号至少关联一个网络；登录后可在所属网络之间切换。",
      },
      {
        ...ref("memberAdminIds", "可管理联络资料的网络", "members", false),
        type: "multiselect",
        hint: "必须是可访问网络的子集；不授予路由、基础设施或工作人员权限。",
      },
      {
        key: "password",
        label: "密码",
        type: "password",
        hint: "新账户必须设置至少 12 字符的密码；编辑时留空保留原密码。",
      },
    ],
  },
];
// Keep optional legacy business and asset metadata in readable form sections.
const memberSchema = resourcesBase.find((r) => r.key === "members")!;
memberSchema.fields.push(
  { ...text("registeredName", "注册实体名称"), section: "注册实体补充" },
  text("jurisdiction", "注册司法辖区"),
  { ...text("registrationNotes", "注册备注"), type: "textarea" },
  { ...text("shortName", "成员简称"), section: "网络联络与公开信息" },
  text("abbreviatedName", "短名称"),
  text("noc24hPhone", "NOC 24 小时电话"),
  { key: "nocEmail", label: "NOC 邮箱", type: "email" },
  text("nocHours", "NOC 服务时间"),
  text("nocWebsite", "NOC 网站"),
  text("irrASSet", "IPv4 IRR AS-SET"),
  text("irrASSet6", "IPv6 IRR AS-SET"),
  text("peeringPolicy", "对等策略"),
  { ...text("billingCity", "账单城市"), section: "账务补充与开票偏好" },
  text("billingPostalCode", "账单邮编"),
  text("billingCountry", "账单国家 / 地区"),
  text("billingPhone", "账务电话"),
  { ...text("billingNotes", "账务备注"), type: "textarea" },
  {
    key: "vatRate",
    label: "税率（%）",
    type: "number",
    min: 0,
    max: 100,
    step: "any",
  },
  {
    key: "purchaseOrderRequired",
    label: "需要采购订单",
    type: "checkbox",
    default: false,
  },
  text("purchaseOrderNumber", "采购订单编号"),
  select("invoiceMethod", "账单发送方式", ["email", "post"]),
  { key: "invoiceEmail", label: "发票邮箱", type: "email" },
  select(
    "billingFrequency",
    "账单周期",
    ["monthly", "2monthly", "quarterly", "halfyearly", "annually", "none"],
    "none",
  ),
  {
    key: "isReseller",
    label: "作为转售商",
    type: "checkbox",
    default: false,
    section: "转售关系与数据公开",
  },
  ref("resellerId", "所属转售商", "members", false),
  {
    key: "excludeIxf",
    label: "从公开 IX-F 数据中排除",
    type: "checkbox",
    default: false,
  },
);
resourcesBase
  .find((r) => r.key === "contacts")!
  .fields.push(text("mobile", "手机"), text("position", "职位"), notes);
resourcesBase
  .find((r) => r.key === "tags")!
  .fields.push({
    key: "internal",
    label: "仅工作人员可见",
    type: "checkbox",
    default: false,
  });
for (const kind of ["equipment", "switches"]) {
  resourcesBase
    .find((r) => r.key === kind)!
    .fields.push(
      {
        ...num("unitStart", "机柜起始 U", 1, 1024, false),
        section: "资产位置",
      },
      num("unitHeight", "占用高度（U）", 1, 1024, false),
      text("serialNumber", "序列号"),
      text("assetTag", "资产标签"),
    );
}
resourcesBase
  .find((r) => r.key === "members")!
  .fields.push({
    ...num("peeringdbId", "PeeringDB 网络 ID", 1, 2147483647, false),
    readOnly: true,
    section: "外部资料标识",
  });
resourcesBase
  .find((r) => r.key === "connections")!
  .fields.push({
    ...num("peeringdbNetixlanId", "PeeringDB 接入 ID", 1, 2147483647, false),
    readOnly: true,
  });
resourcesBase
  .find((r) => r.key === "ports")!
  .fields.push({
    ...num("snmpIfIndex", "SNMP 接口索引", 1, 4294967295, false),
    readOnly: true,
    hint: "由设备发现审查维护；普通资料编辑不会改变映射。",
  });
resourcesBase
  .find((r) => r.key === "members")!
  .fields.push(
    {
      key: "peeringManagerEnabled",
      label: "允许成员互联发现与请求",
      type: "checkbox",
      default: true,
      section: "成员互联可见性",
    },
    {
      key: "peeringMatrixEnabled",
      label: "在成员对等矩阵中可见",
      type: "checkbox",
      default: true,
    },
  );
resourcesBase
  .find((r) => r.key === "vlans")!
  .fields.push(
    {
      key: "private",
      label: "私有 VLAN（禁止成员互联发现）",
      type: "checkbox",
      default: false,
    },
    {
      key: "peeringManager",
      label: "启用成员对等请求",
      type: "checkbox",
      default: true,
    },
    {
      key: "peeringMatrix",
      label: "显示成员对等矩阵",
      type: "checkbox",
      default: true,
    },
  );
resourcesBase
  .find((r) => r.key === "infrastructures")!
  .fields.push({
    ...num("peeringdbId", "PeeringDB IX ID", 1, 9007199254740991, false),
    hint: "用于核对外部接入所属交换点，与 IX-F 导出标识分别管理。",
  });
resourcesBase
  .find((r) => r.key === "ports")!
  .fields.push(
    { ...text("snmpIfName", "已确认的 SNMP 接口名称"), readOnly: true },
    {
      ...num("snmpIfType", "已确认的 SNMP 接口类型", 1, 2147483647, false),
      readOnly: true,
    },
    { ...text("snmpPhysicalAddress", "已确认的接口物理地址"), readOnly: true },
    { ...text("snmpEngineIdentity", "已确认的设备身份摘要"), readOnly: true },
  );
resourcesBase
  .find((resource) => resource.key === "connections")!
  .fields.push(
    {
      ...text("dnsName4", "IPv4 DNS 主机名"),
      section: "DNS 与探测",
      hint: "已登记的完整域名，例如 member1.example.net.；地址为空时不生成该地址族的目标。",
    },
    text("dnsName6", "IPv6 DNS 主机名"),
    {
      key: "canPing4",
      label: "允许 IPv4 Ping 探测",
      type: "checkbox",
      default: true,
    },
    {
      key: "canPing6",
      label: "允许 IPv6 Ping 探测",
      type: "checkbox",
      default: true,
    },
    {
      key: "monitorBusy",
      label: "使用繁忙主机 Ping 阈值",
      type: "checkbox",
      default: false,
      hint: "Nagios 使用规则中的繁忙主机模板；不代表当前正在繁忙。",
    },
  );
resourcesBase
  .find((resource) => resource.key === "switches")!
  .fields.push(
    {
      ...text("dnsName", "DNS 主机名"),
      section: "DNS 与监测",
      hint: "完整域名，例如 switch1.example.net.。",
    },
    { key: "monitor", label: "纳入监测配置", type: "checkbox", default: true },
  );
resourcesBase
  .find((resource) => resource.key === "routers")!
  .fields.push(
    { ...text("dnsName4", "IPv4 DNS 主机名"), section: "DNS 与监测" },
    text("dnsName6", "IPv6 DNS 主机名"),
    { key: "monitor", label: "纳入监测配置", type: "checkbox", default: true },
  );
resourcesBase
  .find((resource) => resource.key === "peers")!
  .fields.push({
    key: "monitor",
    label: "纳入 BGP 监测配置",
    type: "checkbox",
    default: true,
    hint: "控制监测配置生成，不改变 BGP 会话是否启用。",
  });
resourcesBase.push({
  key: "sflow-exporters",
  label: "sFlow 导出器",
  singular: "sFlow 导出器",
  description:
    "登记受允许的采样来源与交换机、VLAN 范围；保存库存不会配置设备或启动采集器。",
  columns: ["name", "switchId", "sourceAddress", "agentAddress", "enabled"],
  fields: [
    name,
    ref("switchId", "交换机", "switches"),
    {
      ...text("sourceAddress", "UDP 传输来源地址", true),
      hint: "采集器允许接收的来源 IP；地址白名单不等同于加密身份认证。",
    },
    {
      ...text("agentAddress", "sFlow Agent 地址", true),
      hint: "数据报中声明的 Agent IP，须与登记值一致。",
    },
    num("subAgentId", "子 Agent ID", 0, 4294967295, true, 0),
    { ...ref("vlanIds", "允许的 VLAN", "vlans"), type: "multiselect" },
    {
      key: "enabled",
      label: "允许接收此导出器的采样",
      type: "checkbox",
      default: true,
    },
  ],
});
export const resources = resourcesBase;
export const byKey = Object.fromEntries(
  resources.map((resource) => [resource.key, resource]),
);
export const valueLabels: Record<string, string> = {
  email: "电子邮件",
  post: "邮寄",
  monthly: "每月",
  "2monthly": "每两个月",
  quarterly: "每季度",
  halfyearly: "每半年",
  annually: "每年",
  none: "不定期 / 无自动周期",
  bird3: "BIRD 3",
  openbgpd: "OpenBGPD",
  cisco: "Cisco（待适配）",
  quagga: "Quagga（遗留）",
  bird1: "BIRD 1（遗留）",
  bird2: "BIRD 2",
  frr: "FRRouting",
  routeros7: "RouterOS 7",
  junos: "Junos",
  "route-server": "路由服务器",
  "route-collector": "路由收集器（不导出）",
  as112: "AS112 权威 DNS 服务",
  both: "直接委派与 DNAME",
  "direct-delegation": "直接委派 DD",
  dname: "DNAME 接收服务",
  observe: "观察被策略过滤的路由",
  "authorized-only": "仅收取授权路由",
  single: "单端口",
  lag: "链路聚合（LAG）",
  full: "正式成员",
  probono: "公益成员",
  associate: "关联成员",
  reseller: "转售商",
  closed: "已关闭",
  off: "关闭",
  "require-valid": "只接受 ROA 有效路由",
  active: "已启用",
  pending: "待开通",
  inactive: "已停用",
  suspended: "已暂停",
  disabled: "已禁用",
  quarantine: "隔离",
  technical: "技术",
  noc: "NOC",
  billing: "财务",
  admin: "管理员",
  viewer: "只读",
  operator: "运维",
  member: "成员",
  "member-admin": "成员管理员",
  internal: "内部",
  draft: "草稿",
  validated: "已校验",
  published: "已发布",
  failed: "失败",
  available: "可用",
  reserved: "已预留",
  connected: "已连接",
  prewired: "已预布线",
  "awaiting-connect": "待连接",
  "awaiting-cease": "待终止",
  ceased: "已终止",
  broken: "故障",
  layer2: "二层",
  layer3: "三层",
};
export const initialValues = (resource: Resource) =>
  Object.fromEntries(
    resource.fields.map((field) => [
      field.key,
      field.default ??
        (field.type === "checkbox"
          ? false
          : field.type === "prefixes"
            ? []
            : ""),
    ]),
  );
