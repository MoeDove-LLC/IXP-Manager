# MoeDove IXP Manager 全项目迁移清单

目标是将原 IXP Manager 的服务端和运行工具迁移为 Go，以 shadcn/ui 重做管理和成员界面，并简化配置发布。**当前是进行中的重写，不是功能等价替代版。** 原 Laravel 源码仍是业务规则和迁移验收的依据，不能因为基础 CRUD、界面构建或少量测试通过，就认定整个项目已重写完成。

用户补充要求：便利和安全的配置流程必须覆盖其他 router，不限于 BIRD。所有路由平台都需要按其真实能力完成配置建模、校验、审阅、下发、回滚与可观测性；平台列表或预览器本身不是完整适配。

## 范围和验收依据

盘点来源：`routes/*.php`、`app/Models`、`app/Http/Controllers`、`app/Console/Commands`、`app/Console/Kernel.php`、`app/Services`、`database/migrations`、`tools/runtime`、`resources/views/api`、`tests`。数据库截至 2026 年的迁移包含成员用户关联、应用密码、独立 IPv6 最大前缀数以及 IRR 数据结构变化；不能只按早期数据库 schema 导入。

以下表格里的「基础」只表示新代码已经定义相关数据资源，**不代表旧业务流程、权限或集成完成**。所有「待补齐」项目仍属于整体目标，未经用户明确决定不应删除或缩小范围。

| 领域 | 原项目证据 | 新版目前覆盖 / 待补齐 | 完成所需证据 |
| --- | --- | --- | --- |
| 初始化与部署 | `SetupWizard.php`、[旧安装脚本快照](https://github.com/MoeDove-LLC/IXP-Manager/tree/8161e185b9a155f6b297f1487a2d0c6ed920e27e/tools/installers) | Go 服务、初始化令牌和设置界面已实现。提供源码构建、容器、可搬运的 Linux 发布包、专用账号的 systemd 服务文件和离线备份恢复工具。发布包独立启动、首次初始化、重启和合成数据恢复已验证。新版 `ixp-backup verify` 已从上一 Go 构建的真实备份完成隔离检查、恢复和新服务读取。生产主机安装、正式版本间升级与生产数据恢复仍待完成 | 空环境安装、重启持久化、升级和恢复演练 |
| 管理员认证 | `Auth/*`、`User/User2FAController.php`、`ApiKeyController.php`、`AppPasswordController.php` | 已实现 TOTP、本地二维码、一次性恢复码、会话管理、带范围/有效期的 API 令牌、密码修改及离线账号救援；已增加保留 MFA 的邮件自助重置、令牌到期提醒，以及 HTTPS 上明确选择的 30 天设备恢复许可、轮换和逐台/全部撤销；备份恢复取消旧许可 | 登录暴力尝试、CSRF、会话撤销、角色矩阵、2FA/恢复测试 |
| 成员门户与租户隔离 | `CustomerToUser.php`、`Auth/SwitchCustomerController.php`、`ProfileController.php`、`UserController.php` | 已实现一人多成员、会话租户切换、成员管理员资料/联系人自助维护、成员令牌租户固定及撤权。成员邀请复核邮箱、已有账号本人身份和发起者权限。成员管理员可在「我的网络」查看账号、打开邀请、审阅并移除当前组织关系。旧账号和组织关系支持私密归档、批量审核和本人接受邀请。其余授权策略和生产数据迁移验收仍待补齐 | 两个成员互相不可读取或修改数据，各门户流程端到端测试 |
| 成员资料 | `Customer.php`、`Customer/*`、`CompanyBillingDetail.php`、`CompanyRegisteredDetail.php` | 已实现成员类型、加入/离开日期、注册/账务资料和暂停/关闭/开通流程；已增加无 ASN 关联组织、经销商关系循环检查及账务/注册资料迁移。成员 Logo 已接通有界图片解码、实体/CAS 绑定、显式公开、旧图片离线预览导入和备份；经销商完整接入流程和其余历史资料迁移仍待补齐 | 所有存量资料迁移及成员生命周期测试 |
| 联系人与通讯 | `Contact/*`、`ContactGroup.php`、`MailingList/*` | 已实现联系人、联系组关联和成员管理员自助维护、SMTP加密发件箱、恢复邀请与到期提醒；新增账号本人邮件列表偏好、管理员定义、版本冲突、邮箱/实体绑定、专用导出令牌及旧只读订阅/退订接口，界面与真实备份恢复通过。Mailman3同步桥已完成实际隔离Core验证，旧偏好有独立来源包/明确映射/本人接受及浏览器流程；周期同步已有固定目标的首次明确授权、异常停机和显式恢复，已提供按联系组名称或类型及成员筛选的私密 JSON/CSV 导出。旧联系人导出已有专用只读 `contacts:export` 自动化入口，控制台同步回执和旧地址回执清理仍待完成 | 分组权限、订阅变更、邮件服务集成测试 |
| 标签和内部备注 | `CustomerNote.php`、`CustomerTag.php`、API `Customer/Note/*` | 已保留内部标签/备注隐私并迁移旧资料；现有staff自己的订阅偏好、实体绑定、CAS、未读游标和加密通知。旧正文与两种prefs格式现有独立受限来源包、只读归档、管理员显式账号关联及本人短效预览/CAS接受；未知值不授予权限，旧账号/邮箱不自动映射，旧阅读记录不替代当前游标。生产资料对应关系仍须核对 | 来源保真与秘密排除、成员隔离、订阅冲突、撤权、本人接受和完整恢复测试 |
| 机房与设备资产 | `Location.php`、`Cabinet.php`、`Switcher.php`、`Vendor.php`、`CustomerEquipment.php` | 已补齐机柜 U 位重叠/容量检查、序列号、厂商与 IXP 自有设备迁移；完整设备历史与 SNMP 发现仍待补齐 | 关联校验、资产导入、删除限制、实际 UI 验证 |
| 多交换基础设施 | `Infrastructure.php`、`NetworkInfo.php` | 已实现多网络隔离、按网络复用 VLAN 编号、成组事务迁移、主网络、IX-F 排除、多网段及旧资料转换；生产拓扑切换仍需现场核对 | 多基础设施、多 VLAN 的地址与导出边界测试 |
| 端口与二层接入 | `Interfaces/*`、`SwitchPort.php` | 已实现单端口/LAG、同组多 VLAN、MTU、真实端口容量和冲突约束。经销商物理分流支持登记、更换、释放及无独立 VLAN 的旧端口转换，配对会保留分流端口。混合用途旧端口、设备实际 provisioning、完整物理/逻辑生命周期仍待补齐 | 单端口与聚合接入、同端口多 VLAN、隔离状态测试 |
| 核心互联 | `CoreBundle.php`、`CoreLink.php`、`CoreInterface.php` | 已实现 ECMP/L2-LAG/L3-LAG 聚合事务、双端接口、子网/BFD/STP/成本偏好、核心端口预留、旧链路显式归组、双端 SNMP 物理观测和独立流量、旧库闭合迁移及受审批的交换机 JSON 配置计划。独立 LACP/BGP/BFD 观测、厂商原生配置适配/应用/撤回、真实设备验收仍待完成；不从 cost/preference 推断不存在的主备字段 | 核心拓扑迁移、占用/CAS、观测归属、双栈计划、审批绑定、备份和真实目标测试 |
| IPAM 与 MAC | `IpAddressController.php`、`Layer2AddressController.php`、`MacAddressController.php` | 已实现事务预留/释放、归属检查、IPv6 大整数容量、地址池与已登记l2address迁移；SNMP FDB学习与登记对照现有唯一物理接口/VLAN/成员审阅、短期摘要、冲突拒绝及事务登记。禁用但仍分配的旧地址保留成员/PTR；sFlow学习候选持久化、旧学习历史和OUI数据仍待补齐 | 冲突、回收、双栈、成员可见性和学习数据测试 |
| 配线架与交叉连接 | `PatchPanel/*`、`PatchPanelPortHistory.php`、文件/LOA 控制器 | 已实现双工、预接线、状态流程、只追加历史、不可变中文 LOA/PDF/HTML 和当前租户附件检查；旧库存/分配快照/附件现有受限离线包、原保存键与内容摘要核对、事务导入、整面板待核对和只读归档。当前附件按实际分配与成员实体绑定，历史仅staff，私有物理附件保留。管理员配线邮件现支持可编辑To/CC/BCC、正文、当前LOA/PDF与授权附件，先预览再明确排队，交接前复核；暂停/关闭成员仍可收到当前分配的拆线/资料通知。生产资料核对与真实SMTP联调仍待完成 | 预留→接线→拆线状态、LOA、附件授权、迁移、通知与备份恢复测试 |
| 控制台服务器 | [旧 PHP 控制器](https://github.com/MoeDove-LLC/IXP-Manager/tree/8161e185b9a155f6b297f1487a2d0c6ed920e27e/app/Http/Controllers/ConsoleServer) | Go 管理控制台库存、串口参数和成员关联。两张旧表的创建和更新时间按原文保存，支持按成员和服务器联合筛选。已退役 PHP 的库存界面及模型，保留旧数据库结构供离线迁移 | 字符串端口和前导零、IDNA、外键、角色隔离、CAS、导入与浏览器流程测试 |
| 路由器与配置发布 | `RouterController.php`、`Router/GenerateConfiguration.php`、`tools/runtime/router-reconfigure-scripts` | BIRD 2/3、FRR、OpenBGPD 已有独立原生检查和受控本机发布/恢复，Junos 使用 NETCONF 事务。RouterOS 已有 typed 分代计划、严格双通道观察、持久监督器及 review/plan/stage/confirm/cancel/recover/discard-prepared CLI；固定 CHR7.24.6 已验证双栈生成策略、千前缀准备取消及小规模准备物清理，完整发布仍待完成，生产门保持关闭。前四者支持两种收集器角色；BIRD2/3/OpenBGPD有显式RFC1997选择。AS112已新增五平台精确本源策略及双栈DNS配置/探针；BIRD2/3、FRR专用实例的安装、DNS失效撤回/恢复、同主机离线更新及ixp-as112入口已实现并分别验证。AS112另有受审阅的历史清理与明确重绑定，保留原工件并通过状态/故障回归；可审阅的 systemd unit 支持 watchdog。OpenBGPD/Junos 运维适配、地址安装、自动启动恢复和目标机 systemd 运行验收继续推进。BIRD 1 已接入独立 IPv4/IPv6 实例的 RS/收集器生成、实际解析及审批下载；v2 专用本机事务已验证 native 限时确认、精确读回、失败回退和显式恢复，v1 仍仅预览/校验。Quagga 1.2.4 已接入专用 RS 生成、原生校验、审批下载、目标机 watchdog 发布/回退和会话状态采集。旧收集器完整原因标签、Quagga 生产目标及 TCP-MD5/GR 验证、其他厂商收集器、Cisco、旧模板语义、双栈配对及真实设备验收仍待补齐 | 各平台原生解析、凭据隔离、版本不可变、配置差异、失败回滚和真实设备演练 |
| 路由安全策略 | `Irrdb/*`、`RsFilterController.php`、`RouteServerFilter*.php`、路由模板 | 已实现有界 IRR AS-SET 递归采集、带来源/期限的快照、精确前缀-起源配对、ROA 模拟、审阅后更新允许列表及 BIRD/FRR 严格运行时 RPKI；自动调度、完整 bogon/community 与例外策略、其他平台 RPKI 适配仍待补齐 | 允许/拒绝路径测试、IRR/RPKI 失效处理、原策略逐项对照 |
| BGP 运行状态与 Looking Glass | `Services/LookingGlass*`、`FilteredPrefixesController.php`、`Diagnostics/Suites/RouterBgpSessionsDiagnosticSuite.php` | 已实现 BIRD2/3、FRR、OpenBGPD 会话采集及按原生能力提供的类型化路由视图；短期只读租约、前后身份核对、成员归属、AS_SET/多下一跳/社区、新鲜度、截断和加密快照保留已接通。Quagga 会话状态已由本机采集器核对身份后入库。完整过滤原因、OpenBGPD 过滤/表级视图、Quagga 路由表与其他厂商仍待补齐 | 原生客户端/模拟控制端、超时、命令限制、租约重放、成员隔离、未知属性及状态展示测试 |
| 对等管理与矩阵 | `PeeringManagerController.php`、`PeeringMatrixController.php`、`BgpSession*.php` | 已实现双边请求/决定/撤销、ASN 同意快照、独立可见性及矩阵；全网双边实际会话学习仍待补齐 | 邀请/确认、双边关系、采集到矩阵展示测试 |
| SNMP 与物理诊断 | `Switches/SnmpPoll.php`、`Switcher.php`、`Services/Diagnostics` | 已实现只读 SNMPv3 采集、人工审阅索引绑定、接口/硬件/标准传感器/FDB、身份及租户隔离；厂商私有光模块 MIB 和生产设备联调仍待补齐 | SNMP fixture/设备集成、断连与计数回绕、调度测试 |
| 流量、图表和容量 | `Services/Grapher*`、`Statistics*`、`TrafficDaily*.php`、`P2pDailyStats.php` | 已实现独立持久遥测、同步 WAL 派生的 UTC 5m/hour/day 长期归档、精确整数/覆盖率/回绕下界、冻结成员实体与历史维度、单一来源选择及计数/采样分离页面。旧 MRTG/PhysInt/P2P 表 JSON 和 RRD xport JSON/XML 现有独立预览、精确摘要审批、历史归属证明、原窗口/单位查询和备份；生产旧数据对账、Smokeping结果兼容、告警邮件及更高容量存储仍待完成 | 真实数据采集到图表、日聚合/保留周期、旧单位/时间窗口、恢复与权限测试 |
| sFlow 接收与映射 | `SflowReceiver.php`、`tools/runtime/sflow`、API `SflowReceiverController.php` | 已实现有界 v5 FLOW/CNTR 解码、固定私密采集器、入口归属/跨fabric边界、持久序列去重、估算P2P/质量/活动证据及受限成员视图。接收器现有申请/审批/停用/删除、逐包短授权、显式本地策略下的净化FLOW relay和旧VI/VLI闭合迁移；静态plan/pretag与迁移记录不授权执行。SNMP FDB可审阅登记，sFlow学习证据、旧学习历史及生产流量验证仍待完成 | 收发 fixture、接口映射、成员实体与switch scope、恢复/重绑、导出格式、错误和容量测试 |
| RIPE Atlas | `RipeAtlas/*`、`Atlas*.php`、`Services/RipeAtlas` | 已实现显式探针、单向及同 VLAN 有向互测矩阵、预算/私有性审批、创建/停止、逐项恢复、晚到结果、加密回执和保留清理；不确定创建不自动重发，过期审批仅续批未尝试余项。真实 Atlas 账户/额度联调、旧历史数据迁移仍待补齐 | 本地提供者模拟、限额和独立预算账本、持久创建意图、所有权核对、续批恢复、取消、结果与备份测试 |
| 文档与文件 | `Docstore/*`、`DocstoreCustomer/*`、`DocstoreLog.php` | 已实现目录继承、成员授权、附件下载、文件替换、文本冲突编辑、SHA-256校验和访问审计；旧配线附件按当前分配绑定。通用Docstore现有显式磁盘映射的受限离线包、逐文件原权限、成员管理员层次、成员实体绑定、只读来源目录/访问记录和备份恢复；未知权限与归属仅staff，旧日志不伪造成成功下载或文件版本。生产数据逐份核对仍待完成 | 路径穿越、上传大小、媒体类型、授权下载、迁移哈希、并发撤权和恢复测试 |
| 外部数据与业务集成 | `Services/PeeringDb.php`、`RipeRestApi.php`、`Helpdesk/Zendesk.php`、`InManrs.php` | 已实现 PeeringDB 公开数据获取、审阅提案与选择性关联更新；OAuth、RIPE whois/对象、Zendesk、MANRS、注册服务仍待迁移 | 外部 API contract fixture、超时重试、凭据保密、错误展示 |
| 公开与自动化 API | `routes/apiv4*.php`、`publicapi.php`、`apiv1-aliases.php`、`Services/IXF.php` | `internal/ixf` 已实现 IX-F 0.6/0.7/1.0 类型化格式、显式版本选择及原成员导出路径；HTTP 公共发布需显式开启，私有导出要求staff或绑定权限令牌。三个官方schema、双栈/LAG/排除规则、版本权限及真实浏览器下载通过。旧v4其他路由、PHP可选资料字段、历史数字ID关联及调用方迁移仍待补齐 | IX-F schema、私有数据排除、旧调用方契约和版本迁移说明 |
| 其他配置下发 | API `DnsController.php`、`NagiosController.php`、`Provisioner/YamlController.php`、`tools/runtime` | 已实现 typed DNS 完整反向区、Nagios/Smokeping 受限片段、交换机核心接口/BGP JSON 计划、规则 CAS、加密版本、校验审批、绑定规则下载与真实观测探针；核心计划下载还核对物理 switch ID。RFC 2317 可联合审批，并通过显式本机命令顺序安装同一静态 BIND 实例的父子区。AS112现有受审阅PowerDNS/BIND配置包、精确路由/节点绑定、安全提取及本机双栈UDP/TCP检查。RFC 2317 的跨权威服务器发布、glue、其他目标服务安全安装/持续健康撤回、TACACS、交换机厂商原生适配仍待补齐 | 每种生成器 fixture、语法校验、安全发布与撤回 |
| 搜索、审计、设置和运维验证 | `SearchController.php`、`LogController.php`、`ValidationController.php`、`Services/Validation` | 已有审计/设置基础、通用资源强ETag和冲突核对；全局搜索现按可见字段及逐域scope查询库存/文件/核心/接收器/学习MAC，保留旧查询语法、签名分页和精确页面定位。旧 `log` 表可提取来源 ID、模型、操作、时间并在管理员历史归档分页查看。系统验证已覆盖后台调度、监控配置交付和当前主数据目录的写入检查。其余健康诊断和设置/自助接口版本保护待补齐 | 审计不可被普通用户修改、敏感字段不记录、并发修改、搜索授权、健康检查 |
| 定时任务与异步工作 | `app/Console/Kernel.php`、`app/Jobs`、`ScheduledTasks.php` | 已实现持久固定任务调度、崩溃租约、通知重试、到期提醒和过期数据清理。通知页显示本进程的调度健康、最近完成时间和任务逾期及租约状态。外部数据刷新、统计汇总等任务随对应功能继续迁移 | 并发互斥、幂等、超时、重试、重启恢复、最近成功状态 |

## 数据迁移工具

`cmd/ixp-migrate` 是纯 Go 离线转换器。它读取表形式的 JSON，将受支持的记录转换成新版 `/api/import` 的 JSON 格式，不连接生产数据库、不读取 `.env`、不启动或重载 BIRD。

输入结构：

```json
{
  "tables": {
    "cust": [{"id": 1, "name": "Example Network", "autsys": 64496, "status": 1}],
    "location": [{"id": 1, "name": "Example DC", "city": "Example"}]
  }
}
```

表名和字段名使用当前 Laravel 模型对应的实际数据库名称。数字可用 JSON 数字或整数文本；ID 以整数精度读取，不能先由 JavaScript 转成浮点数。输入不是 SQL dump，也不是 MySQL Shell 的逐行 JSON 文件；需要先按上面的 `tables` 结构组合。工具只能对输入中存在的数据报告遗漏，缺失的表无法推断。必须另外保存完整数据库和文件备份。

```sh
go run ./cmd/ixp-migrate -input legacy.json -output import.json
# 审核模式：存在任何未迁移字段/表/记录时失败，不写输出。
go run ./cmd/ixp-migrate -strict -input legacy.json -output checked-import.json
go test ./cmd/ixp-migrate
```

不指定输入或输出文件时使用 stdin/stdout。文件输出权限为 `0600`，使用同目录临时文件写完后发布，拒绝覆盖已有文件。输入上限 128 MiB，拒绝额外 JSON 文档、无效/重复主键。按原主键生成 `legacy-<resource>-<id>`，避免新旧记录 ID 混淆；相同数据重复导入由服务端冲突检查拒绝。

当前转换范围：

| 原表 | 新资源 | 规则和限制 |
| --- | --- | --- |
| `cust` | `members` | ASN、名称、联系邮箱、网站；状态 1/2/3 对应 active/pending/suspended，未知状态降为 suspended 并报告 |
| `company_registration_detail`、`company_billing_detail` | `members` 扩展字段 | 注册、账务、税率、采购订单、NOC、日期与角色；关联/内部组织可无 ASN |
| `cust_tag`、`cust_to_cust_tag`、`cust_notes` | 标签、成员关联、备注 | 组合主键稳定重映射，保留内部可见范围 |
| `contact_group`、`contact_to_group` | 联系组与成员关联 | 保留类型、启用和权限级别元数据 |
| `vendor`、`custkit` | 厂商与资产 | 厂商关联及机柜设备，设备可属于 IXP 自身 |
| `infrastructure` | `infrastructures` | 主网络、国家、IX-F/PeeringDB ID、公开排除标志及交换机/VLAN 绑定 |
| `location` | `locations` | 名称、城市、国家、地址 |
| `cabinet` | `racks` | 关联机房、名称、高度 |
| `switch` | `switches` | 通过机柜解析机房；名称、型号、一个管理 IP、合法 DNS 主机名、管理 active、ASN、IPv4/IPv6 loopback 与接口名；不复制 SNMP 密钥 |
| `switchport` | `ports` | 关联交换机、端口名、Mbps 速率、active/inactive |
| `vlan` + `networkinfo` | `vlans` | VLAN tag、保留每个地址族的规范化 CIDR 与额外网段、所属网络及私有/导出标志 |
| `routers` | `routers` | 保留有明确来源的平台、角色、ASN、Router ID、单地址族 peering IP 和 VLAN。逐行报告未知策略与同交换网络内的标识冲突。[路由器迁移说明](LEGACY-ROUTERS.md) |
| `contact` | `contacts` | 姓名、邮箱、电话、成员关联 |
| `vlaninterface` + `virtualinterface` + `physicalinterface` + `ipv4address`/`ipv6address` | `connections`、IPAM 池 | 支持单端口/多物理端口 LAG、双栈、DNS 名、可 ping 与 busyhost；禁用但仍被接口引用的地址保留真实成员归属及 DNS-only 预留，不当作空闲或监控目标。歧义、无有效 owner 或不能表达的已分配地址阻止输出 |
| `l2address` | `mac-addresses` | 仅迁移已登记地址，按 `vlan_interface_id` 精确映射接入；规范化48位单播MAC，同VLAN重复/冲突或缺引用拒整份输出。旧 `macaddress` 是学习记录，明确排除并报告，不能提升为登记 |
| `sflow_receiver` + VI/VLI 闭合关系 | 顶层 `sflowReceivers` | 保留明确目的IP/UDP端口与VI下全部已转换接入/VLAN，不能只取第一条。标准旧表所有行表达启用意图；显式 `active` 扩展仅接受布尔/0/1。新App启用项须重新审阅，停用项保留停用；不迁移审批或采样授权 |
| `corebundles` + `corelinks` + `coreinterfaces` + 对应 PI/VI/端口 | 顶层 `coreBundles` 与 `ports.coreReserved` | 三种类型、稳定 link/endpoint ID、两侧 VI/PI 属性、双栈子网/BFD/STP、cost/preference、禁用状态全部保留；从成员连接转换中排除。孤儿/复用/混入成员等占用歧义阻止 strict 与非 strict 输出。尚未连接的 type=3 端口保留预留用途 |

所有非空的未转换字段、非空的未支持表、无法解析的关系以及跳过的记录会进入 `warnings`，同时写 stderr。诊断只输出表、字段、主键和原因，不复制原密码或字段值。已分配地址若无法保留唯一有效归属，即使非严格模式也拒绝输出可导入 JSON；仅跳过地址仍会让 IPAM 把整个网段中的缺失记录视作空闲，因此不能继续。合法归属配有坏 DNS 名时仍保留地址/成员，并对被舍弃的名称给出警告。旧账户密码、2FA、API token、SNMP/BGP 密钥、模板源码和任意命令都不进入输出；需要在相应 Go 功能完成后单独迁移或重新设置。默认转换只新增路由器库存记录。显式使用 `--peer-drafts` 可为有明确来源依据的会话生成停用草稿，启用前需审阅接入、过滤策略、认证和目标配置。[邻居草稿迁移说明](LEGACY-PEER-DRAFTS.md)

输出结构为 `{ "version": 1, "source": "ixp-manager-legacy", "resources": { ... }, "coreBundles": [...], "sflowReceivers": [...], "warnings": [...] }`，无相应对象时可省略对应顶层数组。服务端导入需要管理员会话及 CSRF token，使用与普通资源写入相同的 schema 和关联校验，以单个事务接受或拒绝整个批次。核心稳定 ID 保留，但创建新的采样归属与时间，不恢复旧采样授权；接收器也不转移旧审批。先在设置页上传资源 JSON 进行预检。服务端在库存副本上运行完整导入规则，通过时返回资源数量和来源警告，失败时返回发现的校验问题。确认导入会重新按最新库存校验同一文件并在单一事务中保存。资源 JSON 请求大小上限为 8 MiB。核心迁移的闭合关系及拒绝条件见[转换器说明](../cmd/ixp-migrate/CORE-MIGRATION.md)。

## 切换前必须完成的核对

1. 盘点当前部署使用的旧功能和外部调用方；将本表所有待补齐项目保持可见，不能用未使用的假设宣布全项目完成。
2. 备份完整数据库、文档附件、旧配置和依赖版本；在隔离环境运行转换器，审阅每条 warning，并逐表核对记录数量和关系。
3. 导入空的新实例，核对持久化、重启、导出/恢复、成员权限和网络资源关系；未迁移资料有独立可查询的保留方案。
4. 对每个路由器对照旧配置与新策略，分别执行目标 BIRD 版本的原生解析，再进行隔离的会话与路由测试。配置生成通过不等于运行行为等价。
5. 对外部 API、告警、采集器、配置下发、定时任务和成员日常流程做端到端验证。只有这些验收通过后，才可移除旧运行依赖和声明整体迁移完成。

原项目 GPL-2.0 许可及版权声明保持有效；Go 重写不会改变原项目的许可要求。

## 本地验证记录（2026-11-06）

- Go 工具链 1.27.3：`go test -race ./...` 与 `go vet ./...` 通过；`ixp-manager`、`ixp-agent`、`ixp-netconf`、`ixp-migrate` 均可构建。模块声明最低 Go 1.25，当前环境尚未单独运行最低版本矩阵。
- BIRD 2.17.5：双栈生成配置原生解析通过，无效配置负例被拒绝；birdcc 0.1.2 对生成的独立配置检查结果为 0 errors / 0 warnings。birdcc 最新包依赖未发布，验证使用可安装的固定版本，并不宣称验证了最新工具链。
- FRR 10.3：原生 dry-run 接受生成的双栈配置，拒绝错误命令；临时运行目录在 bubblewrap tmpfs 中隔离，未启动 BGP 服务或操作内核路由。
- NETCONF：模拟传输覆盖能力协商、真实协议 URI、消息编号、XML 命名空间、候选锁、已有未提交配置拒绝、校验失败清理、持久确认/取消及私密回执。不等于真实 Junos 设备验证。
- 前端：TypeScript/Vite 生产构建通过；Chromium 在独立测试数据目录验证初始化、登录、资源关联 CRUD、删除限制、BIRD 预览/差异/校验/批准/下载、多平台能力限制、Junos 结构校验、IX-F 和 JSON 导入导出、成员隔离及移动端导航，未捕获页面异常为 0。
- 尚未验证：Docker 实际构建/运行、生产旧库全量迁移、物理路由设备发布、BGP 真实会话与路由传播。剩余 28 个领域仍按上表继续推进，不能由以上测试推导整个项目已完成。

第二轮补齐后的验证：整套 `go test -race ./...`、`go vet ./...` 和前端生产构建通过；新增 `ixp-admin` 与 `ixp-backup`，六个 Go 工具均可构建。账号安全/文档库专项 Chromium 流程与 LAG 专项 Chromium 流程通过，覆盖 MFA、恢复码、令牌、会话、公开/成员文档隔离、编辑冲突、移动端、聚合容量和成员安全端口视图。完整备份恢复验证包含实际附件与匹配的加密密钥，并拒绝损坏、路径穿越、链接、追加压缩流和覆盖已有目标。FRR 看护服务通过受控子进程强制结束/重新启动的恢复测试，以及官方 FRR 工具的离线差异检查；仍未连接实际 FRR 守护进程或物理路由设备。旧单端口 fixture 的 8 条记录和新增跨交换机 LAG/双 VLAN fixture 的 12 条记录均通过转换、事务导入和 IX-F 校验。


第三轮补齐后的验证：整套 `go test -race ./...` 和 `go vet ./...` 通过，八个 Go 可执行程序构建成功。原生测试实际使用 BIRD 2.17.5 与 FRR 10.3 / RPKI 模块，在隔离网络命名空间验证严格 RPKI、前缀–起源 ASN 配对以及错误配置拒绝。监控/策略/多成员专项 Chromium 流程通过，包括大计数器精度、过期/重置、成员历史隔离、AS-SET 配对审阅、并发策略更新拒绝、成员自助维护、联系组和生命周期操作；390/320px 窄屏视觉检查通过。备份恢复已验证策略快照及独立遥测日志，不丢失大整数计数。Docker 运行环境改为 Debian trixie 并包含 FRR RPKI 模块；Docker 构建和实机联调仍未验证，网络命名空间被运行环境禁止时 FRR RPKI 校验会明确失败。

第四轮补齐后的验证：整套 `go test -race ./...`、`go vet ./...`、Go 可执行程序构建与前端生产构建通过。专项浏览器流程验证了双工配线生命周期、伙伴版本冲突、中文 PDF/HTML 下载、授权撤销/失效、成员附件隔离、IPAM 预留与释放保护、邀请和 MFA 密码恢复、脱敏发件箱及固定任务。SMTP 测试只使用内存管道或隔离 loopback 假服务器，没有发送外部邮件。扩展业务 fixture 的 20 条记录通过严格转换和真实 API 导入，保留公司资料、税率、隐私标签/备注、组合主键、无 ASN 关联组织和未分配地址池；原单端口及 LAG fixture 仍兼容。备份恢复同时验证了不可变 LOA blob 和加密邮件正文。新 PDF 依赖固定为 `codeberg.org/go-pdf/fpdf v0.12.0`，字体及许可证本地内嵌，当前明确不支持 Unicode 补充平面字符。Docker 文件已补充 go.sum/依赖下载，但实际 Docker 构建和生产数据/设备验证仍未执行。


当前存储与发布仍需继续完善：路由版本正文已移到独立密文文件，BGP 凭据采用单独加密及目标/修订冲突保护，通用资源已启用强 ETag；专用多资源流程继续使用各自摘要或修订，更高容量持久化及其余设置/自助接口的版本保护仍未完成。接下来继续补齐其余路由角色/平台、其他配置生成器和部署升级验证；这些仍属于原始完整重写目标，不以现有页面数量替代验收。旧 BGP 密钥尚不自动导入，现有迁移器仍明确排除秘密值，不会以缺省无认证的会话替代旧策略。

第五轮补齐后的验证（2026-11-07）：整套 `go test -race ./...`、`go vet ./...`、十个 Go 可执行程序构建及前端生产构建通过。新增 SNMP、PeeringDB/双边互联、多网络、多网段及权限状态的专项浏览器流程通过，包含发现审阅、ifIndex/设备身份复核、存储清理、选择性外部资料应用、协议同意与隐私矩阵、跨标签页 CSRF/界面同步。采集器已采用“获取归属修订→采集→验证后上传”；补测了延迟/未来时钟旧数据、A→B→A、接口重新映射、实测邻居地址/ASN不符及直接分配端口，拒绝将旧值误记为当前成员数据。旧多网络 fixture 通过严格转换和 IX-F 导出：同一 VLAN 编号跨网络保持独立，多网段不伪造聚合。备份覆盖外部/设备快照，合法清理后的元数据仍可恢复；关闭后的旧 Store/清理任务不能覆盖或删除新实例数据。SNMP/外部资料验证使用受控传输与模拟服务，未访问真实交换机或公共提供者接口，也未发送外部邮件。厂商私有 MIB、真实设备/数据联调和生产部署仍需继续验证。

第六轮补齐后的验证（2026-11-07）：整套 `go test -race ./...` 在隔离网络命名空间通过，`go vet ./...`、十个 Go 可执行程序构建及前端生产构建通过。新增 BGP 认证流程验证了重新认证/MFA、凭据与目标双重冲突检查、移除修订、错密钥拒绝启动、管理接口不回显与资源导出排除。BIRD 2.17.5 / FRR 10.3 实际解析接受边界长度及兼容字符认证配置；四平台脱敏、修订变化、预览占位符不可应用、原生诊断不回显及本地目标/回退文件权限均通过专项检查。Junos 预览的命名空间重编码另经专项回归核对，实际发布文件保持独立。

配置版本现在使用独立 AES-GCM 文件，公开审批标识与授权下载 SHA-256 分开；旧内嵌配置迁移保留原内容与审批状态，备份恢复验证了密文文件、清理历史、损坏拒绝及合法清单中多余配置文件的拒绝。`ixp-agent fetch` 验证 HTTPS、禁止重定向、路由器/平台/版本绑定、真实摘要和私密文件发布，不自动应用。Chromium 专项覆盖凭据生成/手填/移除、重认证失败保留登录、并发冲突、按需脱敏详情/差异、审批下载、查看者隔离、pull 令牌路由器绑定、旧基线清理提示及 390px 布局。未连接真实设备或发送外部消息；RouterOS 仍只有预览能力，Junos 仍需目标设备校验，生产设备/完整数据升级验证及其他迁移领域仍未完成。

额外的存储恢复 race 专项通过：迁移中断后复用完整密文、原子发布不覆盖、孤儿及暂存文件回收、容量上限拒绝、FIFO/符号链接拒绝、关闭后旧实例无法读取或删除替代实例文件、当前批准版本保留及已删除路由器历史的显式清理。

第七轮补齐后的验证（2026-11-07）：整套 `go test -race ./...` 在隔离网络命名空间通过（app 411.041s，backup 31.160s），`go vet ./...`、11 个 Go 工具及前端生产构建通过。BIRD 2.17.5 / FRR 10.3 验证了 collector 两种策略、双栈、空观察白名单、认证/RPKI、禁止导出与本机事务切换；实际离线 FRR reload planner 通过。所有预策略缓存现在限制含过滤项的总接收量，因此旧 RPKI 服务器配置升级后需重新生成审批，旧文件内容不被静默修改。收集器不会在 IX-F 或成员矩阵中被标为 route-server 服务。

新增 DNS/Nagios/Smokeping typed 生成器、`ixp-provision` 和规则/版本界面。真实 BIND 9.20.26 验证双栈区文件；Nagios 4.4.6 在完整隔离 fixture 中验证对象引用与缺固定命令的负例，未执行探测。监测片段的产品状态仍仅为结构校验，不以 fixture 结果冒充任意目标配置已验证。专项覆盖域名/标签注入、PTR owner 边界、范围及监测开关、跨网络重复 IP 歧义、私密原子文件、HTTPS/目标摘要绑定、真实观测新鲜性和 UNKNOWN 语义。

HTTP/Chromium 验证了规则 CAS、按需详情与差异、native DNS 成功与缺检查器拒绝、只读权限、profileIds 令牌绑定、授权下载真实 SHA、旧基线清理提示与 390px 布局。DNS 要求显式新建或现有 SOA 基线；已分配/已批准 serial 独立保留，乱序批准、旧序列下载、删除重建重置均被拒绝。共享密文文件实现的跨 namespace 复制不能解密，完整备份恢复包含配置工具工件与序列映射，额外孤儿文件即使 manifest 散列正确也拒绝。

旧库 DNS/监测迁移专项经过真实 CLI→HTTP 导入：启用族主机名/canping/busyhost、交换机 DNS 名保留；禁用但仍引用的地址保留真实 owner/PTR、不进入监控或空闲池。同 owner 接入可继承 DNS，名称冲突拒绝，释放/换 owner 清理旧名；无法保留唯一归属时非严格导出同样失败。没有连接生产设备、重载服务或发送外部消息。Docker 实际构建、真实目标安装/撤回、RFC2317/glue/serial 回绕、旧 collector 原因标签和其他迁移领域仍按上表继续完成。

第八轮补齐后的验证（2026-11-07）：完整 `go test -race ./...` 在既有非 root 网络隔离环境通过（app 426.542s），`go vet ./...`、12 个 Go 工具构建及前端生产构建通过。BIRD 3 使用实际 3.4.0 验证 server/collector、双栈、origin 配对、认证、RPKI 和语法负例；真实 3.4.0 birdc 对私密模拟控制端验证 status/configure timeout/confirm。主版本错配、回滚候选校验、版本绑定恢复 marker 及保密错误经过专项检查，不使用 BIRD2 解析替代。

OpenBGPD 使用实际 8.8 离线 parser；配置模型的完整重建、双栈/collector、前缀-origin、认证及 RFC1997 三态均通过。独占实例看护经过模拟 host 的 stage/confirm/cancel/timeout、带外磁盘冲突、并发单赢家、强杀后恢复以及准确 reload 回应检查；root socket/进程身份另外在映射当前用户的 root 命名空间通过，包括真实 PID1 容器边界专项。FRR RPKI 在该 root 命名空间下失败，在既有非 root 隔离环境复核及全量回归通过；因此没有用一个权限环境的结果替代另一种运行条件。没有启动真实路由 daemon、改变内核路由或联系生产设备。

通用资源的强 ETag 已覆盖读取、条件更新和删除，缺失/弱标签/旧版本被拒绝，两个同基线写入只有一个成功；写事务还检查当前权限及安全代次。HTTP元数据不进入存储和导出，私密字段改变会影响标识但不回显摘要源。Chromium 验证了保留草稿、明确的非重叠合并建议、逐字段冲突选择、合并后二次并发、数组、引用刷新、密码清空不重发、删除核对与390px布局。旧测试夹具仅以明确读取当前版本适配新协议，生产界面不会保存前自动刷新标识绕过审阅。

新平台 UI 原生校验→审批→文件下载/真实SHA、能力驱动 fetch、缺检查器提示和冻结 RFC1997 语义验证通过。标准管理容器仅新增 Go CLI，BIRD3/OpenBGPD 原生解析器仍需单独提供；Docker实际构建、目标daemon发布/负载、其他平台及清单中的业务领域仍未完成，不能由本轮测试宣布整个重写完成。

第九轮补齐后的验证（2026-11-07）：Looking Glass 已接通控制台队列、短期只读租约、类型化路由快照与加密保留。真实 BIRD 2.17.5 / BIRD 3.4.0 birdc 对私密模拟控制端验证五种视图、命令限制及前后身份核对；真实 FRR 10.3 vtysh 对模拟 VTY 验证固定 view 的 JSON 读取；OpenBGPD 8.8 验证实际 CLI 参数语法，JSON 样例对照原生源码。OpenBGPD 的过滤及表级能力明确不支持，没有以模拟结果宣称真实路由守护进程已验收。

控制面专项验证了成员目标投影、相同的跨成员/不存在错误、表级权限、路由器绑定令牌、租约重发与幂等、A→B→A 隔离、AS_SET、未知属性和截断。写入文件后提交状态失败会回收未引用工件并允许有效租约重试。样本与服务器接收时间共同限制新鲜度；补测确认已经显示过期的快照不会阻止成员再次查询。完整备份恢复验证了 Looking Glass 密文/类型化结果及清理状态，以及 Atlas 实例身份、探针快照和独立额度账本。

Atlas 使用本地 HTTP fixture 与 fake provider 验证单向/有向矩阵、显式探针、默认私有、总预算审阅、持久创建意图、不确定响应只读核对、所有权停止、晚到结果和初始化错误。过期审批以原 checksum 重新认证，只续批从未尝试的余项，再显式 resume；既有尝试及回执保持不变。等待锁、读取请求体及发现请求期间发生的权限代次/会话撤销会阻止迟到授权，已经实际发送的创建仍保存回执。最终 Atlas 整组 race 专项通过（68.427s），Looking Glass 整组 race 专项通过（23.279s）。未创建真实 Atlas 测量或消费真实 credits。

Chromium 的 Looking Glass、Atlas 和续批专项均通过，覆盖按需详情、权限隔离、桌面/窄屏、错误/丢包/多路径和未知 RTT。页面沿用 shadcn 与 MoeDove 视觉样式；所有测试管理服务及本地假提供者均已关闭。全部 Go 包的 `-race` 分组回归通过（app 513.644s、backup 42.687s，其余包单独并行验证）；末尾的权限复核与缓存修复另经过上述完整领域专项。最终前端生产构建、`go vet ./...` 和 12 个 Go 工具构建通过。完整项目仍有上表中的核心互联、长期流量、其他 provisioning、历史数据、业务集成、存储规模和实机部署验收待完成。

第十轮核心互联验证（2026-11-07）：全部 Go 包的 `-race` 分组回归通过（app 567.646s、backup 54.559s，其余包单独验证）。覆盖 ECMP/L2-LAG/L3-LAG 聚合事务、同时争用同一端口的单一赢家、依赖交换机变化后的 CAS 拒绝、旧链路采用、最后链路保护、成员及双端 switch token 隔离。端口预留的 null/空串绕过被堵住，持久化拓扑要求用途标记正确，旧平面链路迁移只推进一次并保存归属版本。末尾新增的保留 ASN 及配置速率不能抬高物理容量的边界另经相关领域 race 专项通过。

SNMP discovery→审核映射→取当前绑定→上传→核心观测/曲线的实际 API 流程通过。两侧独立速率、缺端口计数时聚合 null、过期、A→B→A、零启用链路与旧历史隔离均有回归；未由端口 Up 推断 LACP/BFD/BGP 正常。`ixp-provision probe --kind core` 逐端检查实际样本、身份和启用集合，类型化交换机计划的 render/check/fetch 也通过，下载绑定明确物理 switch ID。BIND/Nagios 既有原生结构验证及 CLI 的本地 HTTPS fixture 回归通过。

核心迁移整包 race 与 vet 通过；实际严格 CLI 转换得到 3 个 bundle、6 条 link、1 个成员连接、13 个核心预留端口、1 个 inactive switch，0 warnings。三种模式、两侧 PI/VI 属性、IPv6 loopback、成本偏好和稳定 ID 保留；再经当前 Go API setup→import→export 验证新采样归属。约 30 类孤儿、复用、混入成员、跨关系及非法字段负例，均证实 strict/非 strict 不写 stdout 或输出文件。完整备份恢复保持核心拓扑、采样与审阅标识、预留用途及已批准的加密交换机计划。

交换机配置页面的独立 Chromium 专项通过：明确目标、生成、结构检查、审批、实际 SHA 下载、响应头/JSON 错目标拦截、版本差异、库存变动后拒绝旧审批/下载及 390px 布局。此计划仍需目标厂商适配及原生验收；本轮未操作真实交换机。

核心工作台 Chromium 专项也已通过：双栈 ECMP 新增/读取/整体编辑、依赖库存陈旧时拒绝保存并保留完整草稿和新增链路、明确选择类型后采用旧 L2 链路、链路增删和退役后的预留用途、viewer/member/两侧 token 权限，以及工作台和编辑器的 390px 布局。浏览器按采样时间主动降级过期数据；故障注入验证旧 Up 响应和刷新 500 不会保留当前在线声明或当前速率。最终前端生产构建、全站 Go vet 和 12 个 Go 工具构建通过，所有自管测试服务已关闭。当前完整重写继续推进长期流量、sFlow、其他原生设备适配、历史数据和业务集成；本轮不是生产设备验收。

第十一轮流量实现与专项验证（2026-11-07）：计数器的三层归档、同 WAL 重放、整数跨桶守恒、原始数据裁剪后的恢复、epoch/实体代次、组覆盖和对齐峰、32位条件下界、HC混合方向及名义速率不裁切64位差值经过 race 专项。App 回归验证真实 HTTP 摄入覆盖客户端伪造维度、原成员历史保留、新持有人不能继承旧值、来源 CAS/暂停及历史密钥缺失/替换拒绝。备份专项保留了9007199254741003的精确总量、24次下界，以及36893488147419103230的采样估算和持久序列；正确散列但未引用的分区同样被拒绝。

sFlow parser、CLI、WAL/游标和接收计划验证使用专用严格沙箱：无外网，限定只读源码/工具链/已有依赖，空环境，独占512MiB临时文件系统，以及虚拟内存4GiB、CPU120s、进程128、文件64MiB、壁钟150s等限制；没有暴露主机用户目录或凭据。已验证恶意长度/截断/扩展、精度、无载荷上传、权限目录、恢复/去重、HTTP失败后重绑、增量事务和小规模增长。该专项按内存边界运行非 race 测试，不把它称作整个项目的安全审计。App 则验证了入口归属、VLAN/接口约束、跨映射游标、私密对端别名、限定交换机目录和旧包不得取得新映射后重新归属。

长期流量、来源选择和sFlow三个 Chromium 专项通过真实本地接口：精确差分/真实零、单位切换、UTC窗口、缺覆盖时rate=null、核心A/B、成员转归历史、读取令牌、409保留来源草稿、Counter32下界、typed采样估算、AF/整日quality区别、受限别名和目录、过期/503降级及390px布局。所有测试服务均关闭，没有采集真实交换机或向成员目标转发数据。旧 MRTG/RRD/P2P 历史导入、receiver 审批/转发适配与 MAC 学习审阅继续保留为未完成范围。

完整控制面 race 回归通过（app 565.592s），完整 backup race 通过（48.806s），新归档备份专项另通过（8.920s）；其余 Go 包分组 race 通过，sFlow parser/CLI 使用上述专用严格沙箱验证。末尾的服务器时钟锚、映射最早时间与收包租期专项通过，覆盖±48小时墙钟偏差、非固定TTL、耗尽租期及丢弃旧队列后重绑。最终前端构建、Go vet 和13个工具构建通过，发布物已确认晚于最后一次源码修改。

第十二轮接收器与旧统计验证（2026-11-07）：接收器新增/修改/审批/停用/删除、短期逐包授权和本地socket回执已接通。审核冻结具体导出器、交换机、物理入口/出口、VLAN、接入及注册MAC；统计可使用的端口归属或远端目的MAC不扩大包头披露范围。首次授权不能覆盖旧版本回执，禁止“未发送”状态携带正发送计数，重复包和失败回执不重发UDP。完整sFlow库和CLI通过严格隔离non-race测试；实际UDP另在仅loopback且CapEff/CapBnd均为零的命名空间验证，各成员只获授权的净化FLOW，应用载荷/CNTR/vendor记录不转发。没有向真实成员目标发包，socket成功仍不代表远端到达。

旧表与RRD导入保留原单位/窗口、超过2^53的精度及null/NaN语义；原生RRDtool 1.7.2实际完成导出→预览→摘要导入→查询，不继承RRDCACHED daemon配置。导入的成员发布需完整窗口和显式历史归属证明，实体代次/共享批次别名/删除撤销通过HTTP专项，成员投影不含内部归属或提取窗口证明原文。备份往返保留原始压缩批次和接收器审批，拒绝损坏及未引用批次；普通资源导入则重置接收器审批。当前完整控制面race通过（641.325s），完整backup race通过（74.310s）；末尾的跨switch目的tuple和窗口证明投影另经独立受限专项通过。前端构建及接收器、sFlow、旧统计、计数器四套真实浏览器测试通过，Go vet和14个工具构建通过。生产数据对账、MAC学习的其余来源及实机联调仍未完成。

第十三轮MAC与搜索验证（2026-11-07）：SNMP发现→审核接口→当前绑定采集→FDB对照→审阅登记的实际HTTP链路通过race专项，覆盖唯一归属、同VLAN冲突、多VLAN歧义、移动/接口换代、更新采集、过期、switch token隔离和成员拒绝。通用MAC资源写入也拒绝零/组播地址及同VLAN重复；MAC/sFlow/receiver组合专项通过（59.213s），另验证已认证但在等待期间注销的请求不能登记（4.181s）。观察和登记保持分离，没有把学习证据自动变成授权。

旧sflow_receiver和已登记l2address转换器全包race通过，真实CLI→App导入通过（3.568s），验证多VLAN LAG完整范围、启用项重新进入draft、停用项无转发绑定、重复导入事务拒绝；旧macaddress学习记录明确排除并警告。缺成员/端口/VLAN或不完整VI关联即使非strict也不输出部分文件。

全局搜索8组race通过（33.406s），API配线投影对齐既有GET后相关scope/Unicode专项再通过（7.840s）；涵盖当前租户、撤销会话、成员隐私、隐藏字段不影响可见revision、文档祖先权限、用户账号管理员限制、逐域token scope、签名分页、旧语法、sFlow接收器实体代次和Core双端switch范围。搜索只索引白名单可读字段，不读文档正文或原始设备身份。

MAC学习与全局搜索两套真实Chromium流程通过，含409保留登记名后重新审阅、跨分页与重复目标定位、按角色显示搜索类型、延迟旧响应、分页失效清空及390px布局。新增导航专项确认文档/嵌套目录/配置规则/非默认核心互联/配线端口定位准确，未触发修改、生成或下载，校验和与修订保持不变。前端构建、Go vet及14个Go工具实际输出构建通过。仍未连接生产设备；RouterOS原生检查与安全发布、sFlow学习持久化/旧历史、其余业务集成和完整迁移清单继续推进。

第十四轮RouterOS基础与原生验证（2026-11-07）：新增 `internal/routeros` 类型化分代计划、目标/基础状态/规则正文及顺序摘要、凭据修订、保守动作预算、严格快照和只允许计划内操作的API桥接；支持准备、激活和确认后清理的独立表示。`api` 子包使用真实TLS、固定菜单/属性、严格有界sentence/tag、明确trap/empty/done处理与不确定写入拒重试；`terminal` 子包使用单持久通道、固定SSH参数、分片控制序列处理、当前owner检查与不冒称回滚完成的取消接口。三个完整包的race测试在无网络、只读二进制、独立限额scratch及OS数据内存/CPU/进程上限下通过，原生用例默认明确跳过。

已验证官方固定SHA的CHR7.24.6镜像，QEMU/TCG全namespace、零capabilities、仅loopback环境中实际运行Go：API-SSL证书/主机名/登录、控制台current owner与API非owner、API写表及原生ID读回、Ctrl-D后原API连接读回名称/ID真正消失。新增仓库harness独立复现，并明确不连接生产设备、不保留guest磁盘或凭据。

原生属性探针发现并纠正CLI `fib=no`非法、API同名值仍会开启开关、连接默认main而不继承实例表、空AF-limit Set拒绝、filter没有invalid getter，以及MD5未设置与明确空值的区别。现编译器省略FIB开关、连接和实例显式同表、独立typed unset、准备阶段显式初始化认证，并按成功getter证据处理精确7.24.6的未设置值。凭据依旧用独立opaque证据，受限用户结果不当成“无认证”。旧预览修复两处表语义，历史正文保持不变且严格脱敏继续支持旧/新格式。

最终隔离原生用例通过：取消读回约2.55s、关键属性约2.94s；仓库 `--properties` 入口也独立通过。上述时间不包含虚拟机启动/证书准备。修正后的routing渲染/认证/脱敏/角色专项race通过（1.167s）。详细事实、镜像摘要和边界见[原生记录](../internal/routeros/NATIVE-7.24.6.md)及[复现工具](../tools/testing/routeros/README.md)。这些不是SSH owner、Confirm、断线/重启/溢出、完整BGP数据面或全发布监督器的验收；RouterOS公开能力仍为preview，后续继续补齐执行流程。

最后的控制面多平台流程race通过（2.988s），仍拒绝把RouterOS预览提升为已校验版本；Go vet、14个工具输出构建与10项harness契约测试通过，构建产物已核对晚于生产源码修改。

第十五轮RouterOS执行层验证（2026-11-07）：已新增固定目标的真实API/SSH观察器、持久发布监督器及`ixp-routeros`本地CLI。监督器在写入前同步保存意图，逐步独立读回，检查实际Safe Mode历史动作数量，并绑定原始owner、计划摘要和确认截止时间；重启恢复只读核对，不自动重发不确定写入或接管丢失会话。日志完整性、凭据保密、独占锁、超时取消、丢失响应、并发提交和失败后重新检查均有回归。CLI使用私密配置文件及同UID本地socket，调用方不能更换目标或传入任意命令。

官方CHR7.24.6隔离虚拟机已实际验证Go持久SSH取消后原API读回对象消失，以及确认后配置在释放Safe Mode及关闭SSH后均保留；最新复测分别约1.98s和1.16s。SSH主机公钥与可信串口导出的公钥核对，API证书、主机名和SPKI独立核对，两个通道读取相同稳定设备标识。真实只读完整观察（约1.54s）和显式重建空闲SSH会话（约2.30s）通过，关闭owner后仍可独立API读回。SystemID按有界opaque文本处理，包含`+`或`/`的合法值不再被误拒绝，且从不插入文件路径或原生命令。

真实准备测试发现未设置的`connect`属性不能获得已知布尔值；编译器现显式配置`connect=yes`和`listen=yes`。有效实例下的连接AS由print和getter返回实际ASN，不能当作空值处理；模型现包含LocalASN，新增和更新均显式设置，并在变更实例前固定连接ASN。仍待清理的禁用旧连接可以保留旧ASN，但必须有同peer历史代次证明，不能据此放宽已启用连接。未扩大未知值的默认推断。

双栈bootstrap的完整监督器取消（约63.64s）与确认（约70.75s）实际通过：准备阶段浮动历史为0，三次过滤规则修改及两次连接启用各产生1个撤回动作；取消后完整配置和opaque凭据与准备完成时的checkpoint相同，确认后Strict目标快照、重新生成计划无差异及关闭SSH后保留均通过。清理只发生在证明完成之后，不能用手动删除冒充撤回。全新Runtime及独立API/SSH连接重开HMAC日志的只读恢复另通过（约58.75s），旧确认被拒绝，Bind/Reset/Take/Apply/Cancel/Confirm调用数为0；这不是设备重启验收。

恢复所需的敏感getter授权来自已验证日志中的精确对象ID、名称及完整属性摘要，并在读取前后重新匹配，不把名称前缀当作归属。过滤链内顺序现纳入状态完整性核对，最终状态必须通过Strict目标快照。SSH实际剩余寿命必须覆盖确认截止时间及操作余量。状态查询使用不可变原子快照，准备或激活被阻塞时仍可立即返回已持久化的阶段和当前操作。

仓库`--ssh --bgp-cache`已独立实际复现固定单peer IPv4样例：两条文档前缀的允许/拒绝随root切换而改变，SSH取消后恢复，期间仅1次BGP OPEN和1次UPDATE、没有refresh；禁用连接后3秒该表为空。该样例不是完整生成策略或双栈BGP验收。独立`--ssh-disconnect`入口也通过（约4.08s），直接结束本地SSH进程后，原API连续两次确认对象名称/ID消失且owner释放，等待约3014ms；没有Ctrl-D、Ctrl-X、重连或手工清理，不把这个时间推广为半开网络故障的上限。

最终RouterOS父包、API、终端、监督器、live观察器和CLI六组完整race在无外网、只读二进制及受限scratch命名空间中通过；全项目`go vet ./...`和15个Go工具的CGO关闭构建通过。没有改动前端或重新运行此前的完整App回归。真实事务样例只有两个地址族和约13条生成规则，不能据约一分钟的结果声称千条规则同样可用；大配置准备效率、完整生成策略的BGP行为、更新与确认后清理、设备重启及完整动作预算仍继续推进。代码内Prepare/Activation能力门保持关闭，RouterOS公开状态仍为预览。

仓库`--ssh --live`最终整体复现也通过：六个live用例均实际执行并通过，完整取消71.49秒、确认70.84秒、全新Runtime恢复80.99秒；前置串口与SSH取消/确认同样通过。27项harness契约检查另在只读源码、无外网及限额scratch环境下通过。所有本轮自管虚拟机已退出并销毁来宾状态，未连接生产设备。

第十六轮继续推进（2026-11-07，进行中）：真实双对端IPv4实验发现仅指定`ebgp-rs`仍会追加本地ASN，现生成器显式设置`output.default-prepend=0`，并同步受限API字典、读回要求、动作估算和旧预览脱敏。修正后的生成策略实际通过122.28秒：来源与origin配对、透明AS_PATH/下一跳、不向来源回送、仅切根规则的缓存撤回及取消恢复均成立；双方仅一次OPEN、来源仅一次通告且没有refresh。这个结果限定IPv4，使用当时的API激活路径，不代表IPv6或包转发。

激活路径已改为同一Safe Mode owner SSH流的封闭类型化命令，API只负责该阶段的独立读回，防止独立API命令在取消后迟到。八类owned命令、`$`字面量、认证设置/清空、AF unset、enable/disable及取消后的checkpoint恢复在CHR中通过4.67秒；没有使用API进行激活写入。排队命令/断线次序的专门原生验证仍在补齐。

批量Prepare和确认后自动Cleanup已落代码，并保留逐条持久化、完整差异证明、未知结果不重发及独立清理状态。自动清理原生实验通过127.64秒：按原批准计划删除10个旧目标，已确认状态、新Strict配置和零差异重建均保留，没有再次请求审批。千条前缀批量场景正在进行原生验证；各新增生产能力门仍关闭。旧配线历史和附件迁移已开始独立实施，使用受约束的离线文件包及当前分配绑定权限。

当前owned SSH激活路径的双对端完整策略再次通过147.64秒，保持透明路径/下一跳及策略取消恢复，不依赖重连或源路由重发。仓库独立owned命令入口通过7.97秒；同流延迟实验通过6.23秒：固定本地代理保留加密命令与EOF顺序，释放前仍观察到owner持有，未把此时的baseline冒称回滚；释放后owner关闭、checkpoint恢复，并经过有界稳定观察未发现晚写。此证据限定该延迟流场景，不是任意网络或设备故障的保证。

千条前缀实验已确认全部2004条generation规则写入及逐条日志回执完成，随后完整inventory读回失败，尚未达到可用性验收。正在加入不含响应内容的固定错误分类与字节/记录/时限诊断；没有因测试失败直接增加超时或降低回读要求。

后续诊断已定位为单次Observe的30秒总期限：批量写入已完成，规则`disabled` getter在累计2864次查询、批后30000ms处遭遇context deadline，随后client-closed只是结果。正在改为每批最多64项的只读getter流水线，保持独立tag、每项完成/缺值语义与两轮完整一致性核对，不纳入凭据读取或写操作。

第一版64项流水线原生复测仍未通过：第二轮filter-rule print读到550239字节、1478条记录时耗尽同一Observe剩余context，单次Inventory约3318ms，没有触发容量界限。正在按Inventory/getter/本地处理分别计时，并以独立只读detail探针确认设备是否能明确返回布尔属性；正式观察仍不根据省略字段推断默认值，原30秒/15秒限制保持。

新增`ixp-routeros review`只读规划入口：从已认证日志恢复已确认且清理完成的KnownState，重新核对设备完整状态、凭据指纹和Strict快照后返回，不输出密码、不重绑或接管owner。初始空namespace也可读取；未完成事务、清理中间态、日志损坏和状态漂移不提升为新基线。返回值为独立副本；控制器及私密socket CLI专项race已通过。旧配线迁移的受限离线bundle、专用事务导入、分配绑定权限、备份恢复和真实浏览器流程已完成专项验证，完整App/Backup回归正在接续。

完整Backup race已在独立无外网、源码/工具链/依赖只读、白名单环境与限额scratch中通过。完整App race保持同一测试进程继续运行；该回归针对启动时编译快照，后续RouterOS改动另由其专项覆盖，不能混称为同一版本的全项目原生验收。

完整App race随后也以原进程完成并通过，未筛选或重启。App/Backup/迁移源码在该快照期间保持不变；并行RouterOS变更不在这一预编译快照范围。BIRD2专门生命周期及BIRD3/OpenBGPD/named-checkzone的可选原生成功分支未在清空环境中启用，其余普通合同与缺失检查器的拒绝行为仍执行，相关平台原生证据见此前独立轮次。

新增离线`ixp-routeros plan`：读取私密review响应和较小的期望配置，生成规范计划、操作序列、摘要及原子发布的0600 stage文件；输出不会覆盖既有文件，stdout不含凭据。未提供authentication的现有peer保留原认证引用，显式新凭据与清空意图冲突时拒绝；规划不会开启native能力或代替独占维护前提。父包、CLI隔离race与vet通过。控制台库存和串口参数的Go模型/旧表迁移继续独立推进，目标仍是库存管理，未新增设备会话或凭据保存。

`review --output`现直接原子保存0600规划输入，拒绝覆盖和符号链接目标，stdout只报告已保存状态。私密socket端到端、无写入副作用、认证引用省略保留、显式清空/轮换及输出冲突专项再次通过。Driver的一次性完整观察证据复用正在原生1000前缀场景验证；证据由真实成功Observe在runtime内部生成，绑定计划/操作/会话和至多1秒新鲜度，写入口消费，过期或失配回到完整读取，未引入调用方跳过验证的开关。

该去重版本的1000前缀复测仍失败，2004条规则均收到回执，后续10轮完整Observe每轮约17.6–19.7秒，最终在批后约219589ms耗尽上层context；不能报告容量验收完成。其逐Apply诊断因字段名不匹配未被harness接受，本轮不能据此宣称原生逐项零额外读取，字段已修正留待后续验证。下一实现正在采用明确的目标对象读回与全局事务边界核对，保留独立owner/history/WAL和最终双轮Strict完整证明，未提高默认deadline。

最低版本兼容补验：从[Go官方归档清单](https://go.dev/dl/?mode=json&include=all)取得Go1.25.12 linux-amd64，核对59856753字节及SHA256 `234828b7a89e0e303d2556310ee549fbcf253d28de937bac3da13d6294262ac1`后仅解压到私有构建目录。该工具链实际完成全部15个CLI的CGO关闭构建及`go vet ./...`；这不是Go1.25下完整runtime/race套件的通过声明。初次构建因/tmp空间不足失败，移走本轮可重建工具链/缓存后原代码重跑通过。CI新增Go1.25.x/1.27.x矩阵与25分钟测试期限，CI配置本身尚未在远端运行。

控制台库存补齐后的shared/app/CLI迁移专项与race/vet、前端构建和真实浏览器均通过。保留串口标签前导零，13种baud及旧枚举有明确映射，未知值保留来源且不伪造成9600/none；仅hostname的IDNA资产可用，机柜/位置/厂商约束、autobaud保留、412草稿、关联删除保护、viewer/member边界和390px布局经过验证。浏览器renderer崩溃仅作一次环境重试后通过，不记作业务缺陷；服务及临时数据已关闭。更多字段和迁移边界见[控制台迁移说明](../cmd/ixp-migrate/CONSOLE-MIGRATION.md)。

工作人员可从「成员」打开对应的「控制台接入」，查看设备说明、位置、服务器和串口。详情按原文显示历史时间。备注按纯文本显示，原 Markdown 字符随库存保留。Go 通过工作人员角色控制访问，删除仍有关联的成员或服务器时提示先处理连接。定向 Go 竞态、转换器到实际 HTTP 导入及导出、跨实例重导和 390px Chromium 测试均通过。对应的 PHP 控制器、成员 OOB 页面、模型、菜单、开关和 Dusk 测试已退役。 [控制台迁移说明](../cmd/ixp-migrate/CONSOLE-MIGRATION.md)

通用 Docstore 迁移已接通：显式 disk→离线目录映射、原 path 与真实字节/原 SHA 核对、受限 manifest-first tar.gz、事务导入、独立只读来源与访问记录页面。保留逐文件 0/1/2/3 权限和当前成员管理员层次；成员文件另绑定成员实体创建时间，未知权限/归属只供工作人员核对。原目录没有 ACL，不能直接套新系统目录继承；原树保留在来源档案，每份文件进入独立不可变容器。旧访问记录不合成成功下载、当前账号或历史文件版本。迁移库/CLI race、App 的 Docstore/既有配线与文档回归、完整备份恢复专项、前端构建和隔离 Chromium 导入/权限/只读文本/390px 流程已通过。复查补齐上传期间 logout 的事务会话核对，以及四个文档写入口的事务内只读绑定检查；实际 HTTP 并发导入与替换回归通过。详情见[Docstore 迁移说明](../cmd/ixp-migrate/DOCSTORE-MIGRATION.md)。

RouterOS 两层读取的首次 1000 前缀实测仍未通过：2004 条 generation 规则收到回执，三个后续 Prepare Apply 的内部完整 Inventory 调用为 0，耗时约 199/232/189ms；首次对象激活在 owner guard 被拒，未继续写入。已将失败原因拆成固定类别和有界时间诊断，正在同一固定场景复测，没有提高 1 秒证据新鲜度或 90 秒确认窗口，生产能力门继续关闭。

迁移隐私补验：旧备注 `private` 与标签 `internal_only` 不再把数字解析失败的零值当作公开。只有明确的 0/false 才允许成员可见；未知、缺失或 null 保留内部并产生核对提示，strict 模式拒绝这类未决资料。19 组标志表示、普通/严格输出及原业务 fixture 已通过隔离 race 回归。Docstore 最终成员实体失效投影与最终服务/界面的 Chromium 流程再次通过，测试进程和合成服务均已退出。

文档迁移与当前 Logo 后端接入后的 Go1.25.12 集成检查再次完成：`go vet ./...` 与全部 15 个 CLI 的 CGO 关闭构建通过，逐个核对有实际输出。该检查不代表所有包完整 race 或 Docker/生产部署验收；后续备注通知开发与 RouterOS 诊断仍单独推进。

两层读取的同场景诊断复测随后通过：真实 1000 前缀生成 2004 条 generation 规则，单批全部 ACK；Stage 265637ms，完整 Inventory 43 次，实际对象读取与身份读取各 16 次，最终全局 Strict 校验通过。原 90 秒确认窗口不变，取消后的完整 prepared checkpoint 与凭据精确相等；整个用例 366.42 秒，证明前无手动清理。三个普通 Prepare Apply 的内部额外 Inventory 为 0，约 207/202/289ms。本轮只新增固定失败分类与有界计时，没有改变执行行为，因此不能据这次成功反推此前 owner guard 的具体原因。该证据限定一次固定千前缀取消流程，生产能力门仍关闭，IPv6 生成策略验证继续进行。

备注通知与未读管理已接通：staff账号默认不订阅，可选择全部或指定成员/备注，订阅绑定实体创建时间，失效对象不会随同编号新建而恢复。真实内容、归属或可见性改变产生同事务事件与每收件人独立加密快照，no-op不发；读取游标独立于订阅。SMTP停用时不入队或补发过去变化。实际投递前复核账号、权限、实体、邮箱和订阅版本，手工重试也不能绕过；共享清理或其他邮件入队清掉旧outbox后，其绑定在Store事务出口同步清理，重开仍严格校验。正文为有标识的8KiB摘录，SMTP结果保持至少一次语义，已交给传输的邮件无法撤回。后端专项race和Chromium CAS/草稿、旧摘要拒绝、实体替换、普通成员隔离、错误恢复与390px流程通过；Go1.25.12完整App/Backup race已完成：App 228项通过、1项因未设置BIRD_BIN跳过，Backup 22项通过，均无失败或race报告，450个输入文件前后哈希一致；证据在.cache/note-final/results.json。此22项不包含独立的备注通知备份专用用例，该专项正在补充。旧订阅prefs/user_pref档案和显式本人接受映射尚未迁移，API明确返回legacyPreferencesImported=false。

2026-11-14补齐备注通知专门备份证据：新增 `TestNoteNotificationsBackupPreservesStateAndRevocation`，Go1.25.12 race下1个主测试及3个恢复分支通过（16.26秒）。真实HTTP建立的订阅实体、已读游标、备注修订、待发绑定与加密邮件经Create/Restore精确保留；恢复后有效重试正对照成功，退订和角色撤销均拒绝旧邮件重试且队列未改变。未启动邮件worker或外发。本次是额外定向测试，不扩大此前228项App/22项Backup全量回归的版本范围。

2026-11-14真实IPv6生成策略实验通过：Go用例59.84秒（不含来宾启动），两端实际IPv6 socket、MP协商及CHR session真实地址/ASN双轮读回成立。来源/origin配对、透明AS_PATH与下一跳、自反抑制、三条root规则的缓存撤回/切换、owned SSH取消及完整checkpoint/凭据恢复均通过；双方一次OPEN，来源一次通告且零refresh。固定接口地址属于隔离拓扑准备，没有手工BGP策略，未验证包转发。前两轮来源读取失败未当作成功；最终as-value读回适配CR/LF行分隔后通过，没有减少应验证的两对端或放宽期限。生产能力门继续关闭。

CLI能力记录现单独标记已通过的千前缀及双对端IPv6固定实验，并移除对应过时的未验证提示；Preparation/Activation/RuleBatch/Cleanup/ObjectObservation生产开关及MaxHistoryActions仍保持关闭/0。固定pin与配置不能授予执行权限的既有回归通过，剩余操作预算、身份/认证转换矩阵和设备故障恢复仍需验证。

旧备注与订阅来源迁移已接通：`--note-archive`在普通转换前独立选择来源，保留原正文、空/孤儿记录、null及两种prefs格式，排除凭据和其他偏好；源128MiB/包32MiB，并在解码前拒绝非法UTF-8、重复键和未配对surrogate。App导入不改live notes/prefs/reads/outbox，管理员显式关联当前staff实体，本人以五分钟会话绑定预览和CAS明确接受新选择；源账号/邮箱/旧时间不授予新权限。库8组及CLI7组race、App6组专项和UI交互链通过。新增LegacyNotes完整备份专项9.15秒通过，保留大int64来源、已接受回执和pending review，旧会话与旧token失效而新预览可正常接受；未覆盖旧正文/游标，未新增邮件。详情见[旧备注迁移说明](../cmd/ixp-migrate/NOTE-ARCHIVE-MIGRATION.md)。

本轮最终集成检查完成：Go1.25.12全仓库`go vet ./...`通过，15个CLI以CGO关闭构建并核对实际ELF/大小/SHA；469个输入前后摘要一致。证据位于`.cache/final-integration-20261114/results.json`。旧备注迁移的最终Chromium流程亦通过，现有Noto字体用于只读测试环境核对中文390px布局；未改产品全局字体。下一优先工作是RouterOS取消后的fresh-runtime review/显式准备物清理闭环、大规模更新确认与清理，以及操作预算和故障恢复矩阵；不能把已有固定实验或可配置私有预算视为完整生产发布已完成。

RouterOS取消后闭环新增：已管理更新可用fresh runtime按原rollback检查点做严格read-only review，复用完整prepared generation；bootstrap保留reject roots时仍拒partial基线，新增独立PreparedDiscard状态、原Add回执授权、逆依赖Remove与逐项WAL/不确定结果只读恢复，完成后精确回到原Known/Baseline。修复Stage把原Baseline与工作状态共享map的缺陷。控制器全包51项race、Review 6组、CLI13组及parent/live回归通过；原生小样例67.08秒通过，11个原新增对象被精确单次删除、0额外控制调用，原基线/凭据恢复后新bootstrap确认成功。已管理认证/AF模型覆盖与真实凭据HMAC分开陈述；千条删除性能及更完整故障矩阵仍待验证，生产执行门保持关闭。

2026-11-14已管理更新取消后的复用原生验证通过（53.89秒）：一个使用非空合成MD5的peer仅改变前缀策略，取消后完整checkpoint/凭据一致；关闭旧Runtime/Controller后，新连接只读Review保留native IDs及认证引用，并以零Prepare、零AddRule完成新事务确认和最终Strict核对。旧确认在Review前及新事务待确认期间均被拒绝。批量删除及cleanup未启用，旧inactive代仍保留；未据此声称认证轮换、AF转换或大规模更新清理已完成。隔离来宾已退出并销毁。

包含批量删除控制器与适配器的最新冻结快照已通过集成检查：Go1.25.12模块校验、全仓库vet、15个CLI的CGO关闭构建、前端类型检查及Vite生产构建；control整包race为60项通过，live为65项通过及15项原生场景跳过，没有失败。485个Go源码、587个总输入均有摘要记录，证据位于`.cache/integration-current-20261114/results.json`。前端首次打包误报依赖无法解析，诊断证实为测试沙箱文件描述符上限引起的EMFILE；与已有runner对齐到1024后通过，依赖7443项摘要不变。此快照不包含随后开发的跨平台发布就绪检查，也不把跳过的原生场景算作通过。

千前缀取消后批量清理首次原生验证未通过：Stage257001ms、2004条generation规则/2009个对象创建、取消及完整checkpoint/凭据核对成功。fresh runtime的discard阶段183777ms，只完成11次Remove/11次ACK（一个connection及十条generation规则），禁止操作为0；18次Inventory、126次显式presence读取及34489个属性请求后，出现getter transport-failed、随后client-closed，最终全量读回无法证明。getter日志的context deadline标志为false，尚不把关闭根因归为超时。没有完成最终原基线/Strict/Review验证，没有放宽期限或手动清扫；隔离来宾已销毁，大规模准备物清理仍未达标。

跨平台发布就绪检查已接通：BIRD2/3、FRR、OpenBGPD、Junos和RouterOS预览共用只读`GET /api/deployments/:id/readiness`，直接复用当前生成结果与已有加密工件完整性检查，兼容旧版本，不引入新的迁移签名。响应只给固定状态、原因及当前操作权限，不返回配置、真实摘要或底层错误，不执行外部检查器。页面显示检查时间、平台实际校验层级、下一步及目标侧确认/回退要求；输入变化时可直接生成新版本。慢响应、版本切换、列表刷新失败和错误结果不会恢复旧操作按钮，实际写入/下载仍独立复核会话、权限、版本和工件。

该流程补齐了生成、校验、审批、保留清理以及下载的最终会话复核。7组后端race、23个子例通过，app vet与服务构建通过；最终前端类型/生产构建和5组Chromium流程通过，包括真实控制台Junos结构校验→审批→SHA核对下载、过期恢复、缺失检查器、RouterOS预览、迟到响应及390px布局。测试用状态与fake checker不构成其他平台原生或设备发布证据，未连接真实路由器。证据位于`.cache/readiness-checks-final`及`.cache/deployment-readiness-ui`。

RouterOS下一轮诊断版本已冻结：逐getter输出独立命令deadline及回复计数，新增每批进入/退出、剩余context、完整阶段与未计耗时；71项Python合同检查及最终13项专项、3个TLS截止分类race子例通过。TLS测试说明子命令读截止可以在父context仍未结束时发生，因此第一轮的父context标志不足以判定关闭根因。未修改正式算法、预算或生产开关，也未重跑千前缀原生流程。另一个身份/AF/认证转换及逐操作撤回预算fixture已编译并通过纯守卫测试，尚未原生执行。

2026-11-15配线通知闭环已完成：保留旧接通、拆线、资料及LOA四类人工邮件用途，支持手填To/CC/BCC、可编辑主题/纯文本、真实LOA PDF与当前有权读取的附件。十分钟审阅绑定会话、当前分配/实体/物理资料、来源地址及工件；明确确认后把邮件内容、所有收件人与附件字节加密入队，未知HTTP结果只允许同一已审阅请求取原回执。最多10个收件人、4份附件（LOA加3份资料）、附件合计2MiB，队列密文总计32MiB；密送仅进入SMTP信封，不放进邮件头或通知审计。SMTP关闭时仍可编辑预览，不能入队；旧历史不会重放为邮件。

投递与人工重试会重新检查发起者实体/权限代次、当前分配、地址来源、LOA和资料权限。修复初版将非active成员一律拦截的问题：实际HTTP暂停/关闭成员后，当前分配的cease/info可以发送；释放/重分配后旧邮件停止。connect/loa仍不对停用成员生成新授权；已接通后的旧LOA按原Go授权状态失效，不通过邮件恢复。notify整包12组race、App专项/相关回归、完整备份往返及追加生命周期测试通过；最终TypeScript/Vite与6组真实Chromium通过，包括真实MIME/PDF/工单字节、BCC信封、CAS保稿、明确重试、角色和390px内部布局。所有SMTP投递只进入隔离loopback假服务。证据在`.cache/patch-notifications-inactive`、`.cache/patch-notifications-backup-final`及`.cache/patch-notifications-ui`；完整App/Backup集成另行记录。

RouterOS短只读大规则实验通过81.62秒：固定创建2008个惰性对象（含2004条generation规则），两个单次全量扫描分别8.853/8.793秒，完整双轮Observe17.454秒。6次精确规则presence读取为1985ms，另6个getter仅61ms，表明昂贵部分包括按ID的print查询本身。独立from查询变体将相同6次现存规则读取降至83ms，但不存在ID返回command-failed而不是已完成空结果，故整个变体的缺失验证失败，不能将trap解释为absence。现只把已验证的FilterRule现存读取接到from路径；缺失查询保持原完整协议。

批量清理的适配器现在可一次性复用控制器刚完成的双轮全量观察，最多1秒、绑定目标/会话/原计划/模式/游标/epoch，过期或失配回到完整读取。仍执行当前owner、每条规则及保护对象实际读回、逐项WAL和控制器批次头尾完整证明；1秒指进入批次时的复用上限，不是批内每次写入都有1秒内的全局快照。已知外部owner或撤回历史变化会废弃该证据。最终API42组及live75组race通过，17个原生用例按空环境跳过，两个包vet通过。上述优化后的千前缀清理尚未原生复跑，原失败的连接关闭根因仍未由这些短读实验直接证明，生产开关保持关闭。

配线通知的完整集成已完成：冻结源的全Go vet、15个CLI的CGO关闭构建、Backup整包25项race通过；App原全量244项通过、1项BIRD可选原生跳过，5项因runner漏挂同一381字节旧流量JSON夹具而失败。保留原日志，仅补原fixture后使用完全同一race二进制定向5项全部通过，最终249通过/1跳过/0剩余失败/0race，没有重启完整套件。App/Backup及迁移工具的257个运行依赖Go输入前后相同；随后RouterOS的现存读取优化不包含在该15CLI/vet冻结快照结论内，已由其独立API/live检查覆盖。详见`.cache/patch-full-integration-20261115/results.json`。

最后的仅ID原生探针通过55.34秒：同规模2008个惰性对象，仅一次完整Inventory核对全部实际ID/生成规则，再查询3个存在ID和2次已排除的缺失ID。存在查询1012ms、缺失查询666ms，限制`.proplist=.id`并未减少原query的扫描成本。此方法只产生布尔存在性，未读取凭据/执行Remove/改变owner，未并入生产API。下一步仍需改善批量读回流程并完成千前缀完整清理，不能用该探针通过代替删除或回滚验收；来宾状态已销毁。

2026-11-15新增明确的`global-tail`清理模式：旧逐对象缺失模式及旧日志继续兼容。新模式逐项核对实际目标、保护对象和owner，持久记录Intent/Result；ACK只推进适配器的私有尝试游标，不能成为已删除证明。完成批次后仍须真实双轮全量Observe、完整HMAC/凭据/外部状态和规则顺序核对，才能推进正式游标；任一ACK目标仍在或出现额外变化会保持未决，未知结果不重发。控制器69组、live80组race及vet通过，小原生完整生命周期58.97秒通过：6条规则一个批次、1次真实完整尾读、0逐对象Absent回调；原11对象精确删除、原基线/凭据恢复，后续新bootstrap确认/Strict也通过，生产gate仍关闭。

同预算千前缀global-tail场景仍未完成：首次正式证明149/2009对象已删除后读连接中断，总测试505.257秒，生命周期和Discard独立420秒预算都尚有余量。保持同一binary和CPU/墙钟预算追加只读资源诊断后，再次正式完成167对象；最终getter仍是无deadline的transport-failed。停止前采样明确QEMU已因SIGKILL成为zombie，实际CPU软硬限制360秒，而当时`/proc`快照为336.99秒；该快照不足以直接判定CPU超限原因。原有输出已保留，不把这类读错误写成配置已漂移或全部清理成功。正在增加最终wait4计数，未放宽运行预算或省略状态证明。

AS112新增Go端到端配置与检查流程：DD/DNAME/both和显式地址族、固定ASN112及四个服务聚合前缀；BIRD2/3、FRR、OpenBGPD、Junos只向手工启用邻居通告精确本源并拒收邻居路由。Junos按已启用AF的明确DNS下一跳生成固定NS主机路由，主机路由不出口。PowerDNS Authoritative+BIND后端服务包含24个双栈both模式区域、身份/可选LOC及固定健康计划，先审阅批准，再校验实际SHA/目标并提取至新私有目录；提取不安装或覆盖配置。生成、校验、批准和下载重新绑定当前router实体、服务/AF、全部节点地址；健康探针要求服务监听地址和全部启用AF节点地址都在本机，不能用一侧健康替另一节点背书。

AS112原生证据分开记录：四个软件平台各3个DD/DNAME/双栈配置解析通过；PowerDNS真实设置检查、全部24区与坏区负例通过。最终可信bootstrap先配置固定地址，再全cap归零/NNP运行Go/PowerDNS；384次真实IPv4/IPv6 UDP/TCP、长UTF8 TXT/EDNS0、negative SOA、缺第二节点零查询和停服拒绝均通过。另一套两私有netns/veth、全cap归零/NNP的真实BGP会话验证了BIRD2/3与FRR各4个双栈对端、精确本源/AS_PATH=[112]/下一跳，以及实际恶意UPDATE已处理但不进入已接收RIB。OpenBGPD解析不经过其正常privsep启动，当前缺可用多UID映射工具，不能把解析结果当wire验证；Junos真实设备亦未验收。

AS112执行入口另有本地控制端/NETCONF合同验证：BIRD应用与恢复、FRR/OpenBGPD暂存/确认/取消及精确认证peer reset、Junos实际RPC默认namespace及嵌套host-route读回。发现并修复PowerDNS空resolver导致崩溃、JunosXML继承NETCONF namespace、较长身份TXT缺EDNS，以及双NH/uniqueIP不能逐步编辑的问题。Web主流程7组和节点绑定增量3组真实Chromium通过，含CAS编辑期间拒旧批准、新版本修复、精确下载和390px布局；Backup实际Create/Restore保留AS112字段、BGP认证和DNS密文。持续DNS健康→BGP自动撤回/恢复、自动原子安装、生产返回路径/地址归属及真实设备仍是完整目标的剩余范围，详见[AS112说明](../internal/as112/README.md)。

RouterOS中断原因的下一轮证据已补齐：同一冻结binary/原预算复现，正式证明161次删除后失去连接；应用生命周期尚余32.212秒、Discard预算尚余265.906秒。停止前QEMU已SIGKILL，实际软硬CPU限制360秒，而wait4最终CPU为371.451996秒、最大RSS376076KiB，proc本轮也一致；属于夹具CPU额度不足，不能据此声称路由配置漂移。仅对原明确LONG_CASES把CPU限额修正为600秒，与既有Go600秒预算一致；外层900、工作540、Prepare420、API15、Observe180、Confirm90及内存/进程/文件界限均不变。后续还需在修正环境下完成大规模完整验收，生产写门仍关闭。

新的只读布尔分区路径采用独立证据而非伪造getter：仅固定版本、两次已完成谓词和完整原生ID清单严格一致时可得到disabled值，任何缺失/重叠/重复/打印值或getter冲突均拒绝。两轮观察及私有缓存/projection边界已联合race验证，父包49/API45/control69/live85组通过，相关原生用例默认明确跳过；只读优化gate仍关闭，待大表与逐项getter原生对照后再决定启用。

AS112最终统一快照已完成全部集成回归：App整包race 254通过/1项可选BIRD原生跳过/0失败，Backup整包26通过且执行了AS112真实PowerDNS与全部区文件的offline检查；全Go vet与15个CGO关闭CLI实际构建通过。运行依赖和生产源前后摘要一致，未重启长测试；后续新增RouterOS native-only用例不冒称已由该冻结二进制覆盖。最终双节点绑定与长UTF8/EDNS的cap0 DNS定向复验又确认384次实际查询、缺任一后端地址零查询及停服拒绝。Web主流程7组和节点绑定增量3组通过，根代理检查了390px渲染与源码摘要。证据位于`.cache/as112-full-integration-20261115/results.json`、`.cache/as112-static-final-20261115/go-results.json`及`.cache/as112-evidence/results.json`；上述未完成的部署/故障联动/实机范围仍保留。

RouterOS大表布尔分区原生对照随后通过101.44秒：2004条generation规则、2008个惰性Add，明确default false/explicit false/true及两次原ID切换；两个状态的getter/selection/完整Observe全部属性、ID、正文、链序与摘要一致。原路径每轮2004次disabled getter，新路径为0、两次完成的查询形成false2005/true1精确分区；单轮9051→5739ms、9426→5678ms。未创建connection或激活root，无凭据读取和非授权调用。这证明只读等价性及该规模的性能，不代表发布、回滚或完整千前缀清理完成；下一步将此已验证路径与修正CPU夹具一起接回完整清理，生产写门仍关闭。


2026-11-15 RouterOS只读优化已接回完整清理：固定7.24.6大表选择器gate开启，生产写门仍关闭、MaxHistoryActions仍0。修正CPU夹具后的千前缀global-tail在共享540秒context截止处停止，QEMU仍运行且CPU仅采样322.66/600秒；Discard自身420秒预算尚余136.347秒。正式提交进度为732/2009（731规则+1连接），797次Remove ACK中的其余65个没有完整尾证明，不能报完成；最终原基线/凭据/Strict/Review未达到。9分钟是固定实验预算，不是产品必须一次清完的要求；剩余验收优先转向有界请求、安全进度、fresh只读恢复及显式继续，吞吐优化后置。原证据位于`.cache/readonly-selection-global1000-checks/native-result.json`，未据此放宽预算或开启写门。

RouterOS小型身份/AF/认证转换已实际通过：`transition-budget`用例51.38秒（root终端81571，exit0），基线4操作/22379ms，更新9操作/24481ms、实际history最大值与delta总和均9，新增6条prepared规则；取消3085ms后原native IDs、属性、IPv4、ASN/router ID、非空MD5 A及认证引用恢复，新prepared代保留，0禁止调用。仅修正fixture将IPv4显示标签`ipv4`误作typed AF的断言为`ip`，旧binary保留；3项race守卫、105项Python合同及live vet通过。这个组合不证明所有转换参数或history容量。`.cache/native-transition-budget-v2-checks/native-result.json`明确标记为根代理终端输出摘要转录，未伪装成raw log；源码、binary与差异保存在独立冻结snapshot，生产gate不变。

2026-11-15补齐IX-F旧格式入口：0.6/0.7/1.0共用严格类型化清单，0.6缺注册ID时省略、0.7/1.0要求真实ID；服务标记按各版结构输出，不从成员peer关系猜服务身份。新增原v4和apiv1成员导出路径，公开开关、字段隐私、staff权限与exports:read范围保持一致；设置页按所选格式下载已保存资料。官方0.6/0.7夹具与上游Git blob逐字节一致。冻结检查中IX-F包5项、App相关8项race通过：原旧多网络导入项因runner漏挂JSON失败，补同一夹具后仅定向补跑通过，生产代码未改；受影响包vet、服务构建、前端类型/构建及真实Chromium三格式下载、关闭公开不泄漏、390px布局通过。证据`.cache/ixf-versions-completion-checks/results.json`，不代表旧v4全API或PHP全部可选元数据已迁移。


2026-11-15 RouterOS显式分次清理原生闭环通过：`discard-bounded`用例59.12秒（root终端73339，exit0，fixture_completed=true）。产品CLI/socket默认每请求一个已核实工作单元，可显式选择1..16；只有实际完整尾读、凭据/HMAC与WAL进度提交后才可正常返回Pending，输出已验证X/Y、尚未完成和明确继续命令，不在后台自动继续。原生第一次MaxBatches=2返回7/11；关闭旧服务后fresh Open→只读Recover仍7/11且0写、partial Review拒绝；四次MaxBatches=1分别到8/9/10/11，最后才Complete。总计11个原新增对象恰好11次Remove/11次ACK、0禁止调用，六条规则为1批/1次真实完整尾读/0Absent回调；原基线与凭据恢复，fresh Review空基线、新bootstrap确认和Strict均通过，没有手工清扫。停止前来宾仍运行且CPU360秒限额匹配，随后已停止并销毁。29项controller相关race、16项CLI全包race、3项live守卫、107项Python合同、三包vet和CLI构建通过。证据`.cache/discard-bounded-checks/native-result.json`明确来自根代理终端摘要，不伪作raw log；冻结snapshot保持不变。这个结果完成小规模安全分次可用闭环，不能扩大为千前缀吞吐、全部故障路径或生产写门开放；RS只参与BGP控制平面，不要求数据包经RS转发。


2026-11-15 DNS SOA的RFC1982回绕已贯穿生成/批准/下载和持久账本：相差2^31的未定义顺序明确拒绝；保持非零工件约束，4294967295按合法+2前进到1。已有区继续要求明确当前SOA，显式0基线与缺失区分；profile/版本删除重建不重置每区issued/approved账本，新草稿不改变当前批准文件，乱序批准与旧序列下载在锁内拒绝。资源导出携带最新可比较基线，歧义账本不能导出或通过启动校验，保留serial不可复用。RFC数学/renderer 6项、App 9项及真实Backup Create/Restore 3项race通过，无跳过；App专项使用真实本地named-checkzone完成Max-1/Max/1/2生成、批准、下载、CAS拒绝、重开、清理/重建与坏账本拒绝。未查询生产DNS、安装或重载服务，也不推断从服务器已同步；RFC2317/glue和目标发布仍另行处理。证据在`.cache/dns-serial-checks`。

2026-11-15新增AS112专用运维入口：BIRD2/3、FRR实际PowerDNS健康→BGP撤回→连续健康恢复完成；同Host离线Replace绑定完整前后计划/原current/新四份摘要、保留旧版本并经独立WAL发布，6种崩溃位置及写intent不确定时保留staging有专项证据。ixp-as112提供prepare/install/run/status/reconcile/replace/replace-reconcile及仅供审阅的unit，5项CLI race、vet与实际CGO0构建通过，已接为第16个工具。status不冒充live健康，unit不自动安装启动；既有专用UID与预配地址契约、跨boot/namespace和独立防挂死能力缺口仍保留。证据`.cache/as112ops-evidence/results.json`与`.cache/as112-cli-replace-checks/results.json`；后续保留清理不包含在这些已冻结结论内。

邮件列表新增个人/管理员全流程：个人偏好有list及preference双修订和当前邮箱复核，邮箱改变/账号删除不会继承旧同意，旧地址保留退订记录，新当前账号明确同意优先于旧tombstone。管理员定义key/email不可重定向；停用不能导出假空同步列表，普通exports:read不获得地址名单，新增admin-only mailing-lists:read只读范围及旧v4只读表示。App3项领域race+participants.json公开门1项、Backup真实Create/Restore1项、app/backup vet与服务构建通过；最终Web类型/构建和6组Chromium流程通过，含全部角色、CAS核对、邮箱重选、JSON下载、刷新失败及390px长URL。证据`.cache/mailing-lists-checks/race-results.json`和`.cache/mailing-lists-ui/results.json`。保存/导出仍明确external-not-observed/未应用，未向外发信；Mailman桥与旧偏好迁移继续推进。

2026-11-18恢复工作后收取上轮统一回归终态：冻结源74e585cd的App整包263通过/1项可选BIRD native跳过/0失败/0race，1039.154秒；Backup整包28通过/0跳过/0失败，156.988秒。两条长测试各执行一次、未重启。全Go mod verify/vet与16个CGO0工具构建通过。测试时全部迁移JSON、381字节旧流量及LOA/testdata均已挂载；DNS wrap真实named-checkzone与Backup PowerDNS离线分支执行。冻结后Mailman与AS112 retention增量不冒称包含于原16工具快照，见`.cache/stage-full-integration-20261115/results.json`。

AS112历史清理已完成明确review/apply/reconcile/resume：仅有私密历史plan与逐文件身份/摘要证明的owned版本可删除，保护current和replacement恢复端点，未知unlink须只读核对或再次明确审阅剩余实物；本地文件系统挂载/链接/权限变化拒绝。增量9项backend+2项CLI及21子例、vet/build通过。此后新增status绑定与rebind-plan/apply/confirm：保持原plan与工件摘要，区分匹配稳定本机身份的新boot与明确源隔离迁移；不把旧PID当stop权限，不处理pending/unknown状态，不借重绑定更换工具。后续Replace和跨boot历史清理通过回归；原工具升级不阻止只读绑定核对，运行新工具仍需重新批准。35项backend+9项CLI及80子例race、vet/build通过，1项真实DNS/BGP集成按原开关跳过；这是合成身份/状态验证，未重启宿主或VM。见`.cache/as112-retention-final/results.json`、`.cache/as112-rebind-final/results.json`。

Mailman桥实际Core验证发现并修复模拟合同的两处错误：stock3.3.10版本字符串含Tom Sawyer，POST/members不接受admin_notify_mchanges逐请求参数。新增前现读取目标该设置并要求明确false，在记录写意图前拒绝不满足条件；已有成员/退订不受无关新增前提阻断，绝不临时改全局设置。隔离stock Core实际11条CLI操作/3次成员变更完成；SMTP捕获器先收到4封原生通知作正向对照，adapter操作0封且队列空。保留无关成员/其他列表/owner/moderator，同名列表删除重建使旧计划失效，重复apply/status/reconcile无重复写。Core551个Python文件与官方发布逐字一致；Python3.13使用standard-nntplib、SQLAlchemy2.0.58和指向原模板的显式URI，兼容条件及原始失败记录保留。9项库测试/15子例+4项CLI、race/vet/build通过。未连接生产Mailman或对外发信，详见`.cache/mailingsync-native/results.json`。

旧邮件偏好迁移已接通后端/转换器/备份和shadcn页面：只提取确证的user.prefs.mailinglist及user_pref.mailinglist键，int64来源编号在浏览器按字符串保留，未知值及冲突不会成为订阅。管理员手动映射当前账号/列表实体，本人短期审阅与明确接受绑定会话/邮箱/修订；当前邮箱已有选择不可覆盖，部分接受只占用实际已接受keys，剩余可重新映射。首轮12项及增量7项Go race通过；最终tsc/Vite和真实Chromium转换→原字节下载→映射→接受/冲突/撤权通过。旧同邮箱不授权、unknown原值不泄给本人、名单/邮箱变化拒旧确认、刷新失败禁写和390px标题/弹窗均验证；根检查了最终确认与成功截图。见`.cache/legacy-mailing-final-checks/race-results.json`与`.cache/legacy-mailing-ui/results.json`，旧来源和回执进入完整私密备份，普通库存导出不含这些资料。

2026-11-18统一静态整合补验完成：在AS112与Mailman代理确认稳定后冻结990c9a79，共654个输入、589个Go文件，完整包含internal/cmd/tools/migrate、LOA嵌入字体/图片及全部testdata。Go1.25.12的go mod verify、全包go vet和16个CGO0/trimpath CLI构建一次通过（33.289秒）；实际核对全部预期文件名、非空ELF、大小与SHA256，构建产物随限额scratch销毁。三组新功能源码分别与已验收的AS112 retention/rebind、Mailman Core和旧邮件迁移冻结源逐字匹配；本轮源码/工具/依赖只读、无外网、未启动服务或运行完整App/Backup测试。完成后差异清单明确列出随后开展的AS112 watchdog入口/单元及新增文件，后续watchdog与mailing-watch均不包含在本次通过范围；此前App263/Backup28的完整结果仍仅覆盖原74e585cd快照，新功能运行行为依赖各自已记录的定向回归与真实浏览器/原生证据，不由这次静态构建补算。精确输入、16工具摘要和并行差异见`.cache/integration-20261118/results.json`。

AS112 已接入 systemd watchdog，完成每轮 Tick 后才发送心跳，审阅生成的 unit 将期限设为 180 秒。定向 race、vet、CLI 构建及 systemd 257 的 unit 检查通过。隔离环境中的真实 CLI、PowerDNS 和 BIRD 2 验证了 DNS 失效后的撤回恢复，以及 CLI 被 SIGSTOP 后独立监视器到期终止、两个所属进程退出。该环境无法启动 stock systemd manager，其按服务组终止的行为仍需目标机验收。证据 `.cache/as112-watchdog-final/results.json` 和 `.cache/as112-watchdog-final-native/results.json`。

Mailman 周期同步 `mailing-watch` 已通过隔离 stock Core 3.3.10 验证。首次审批绑定来源、目标列表实体、周期和每轮变化上限，普通用户之后的订阅/退订在下一周期应用。真实测试包含两次成员变更和零 SMTP 通知，验证独占 worker、正常停机重启、源权限撤销、错误目标凭据与目标列表重建。未决操作只能只读核对，未确认完成前重启不会继续写入。已结束历史有带校验的保留和明确清理流程。库和 CLI 定向 race、vet 与构建通过。证据 `.cache/mailingsync-watch/results.json`。

记住设备已接入登录页和账号安全页。账号启用 MFA 时，必须完成当前双重验证后才可选择创建 30 天硬期限许可；服务端只保存摘要。恢复轮换浏览器许可，但保留原到期时间。逐台与全部撤销都会结束从该许可建立的会话。旧恢复响应迟到时，完整新登录可撤销同一浏览器的旧许可，浏览器对登录/恢复/退出共用跨标签页锁。普通重启保留仍有效的许可，从备份恢复实例时清除旧许可。服务端 33 项定向测试及 4 项增量 race、vet、实际构建通过。真实 HTTPS Chromium 验证默认不选择、Secure/HttpOnly/SameSite Cookie、双标签只轮换一次、撤销联动、密码会话保留与 390px 账号安全布局。证据 `.cache/remembered-devices-final/results.json` 和 `.cache/remembered-devices-ui/browser-results.json`。

BIRD 1 已接入配置中心，以独立 IPv4 `bird` 和 IPv6 `bird6` 实例生成 RS/收集器配置，按工件绑定的地址族选择真实 1.6.8 解析器。界面可修复旧双栈记录，保留另一个地址族的单独登记方式。routing、App 和 agent 共 13 个顶层及 24 个子例 race 通过。Chromium 以只配置 `bird6` 的 Go 服务验证 IPv4 检查器缺失、IPv6 原生通过并审批下载、单族约束和 390px 布局。这一早期快照只覆盖 agent 的 `fetch/check`；v2 专用在线应用与回退由后续独立证据补齐。证据 `.cache/bird1-app-checks/results.json`、`.cache/bird1-browser/browser-results.json`。

另以 BIRD 1.6.8 两个实际守护进程验证了后续在线发布依赖的命令行为。私有规范地址的静态路由试验在零能力、无外网的 namespace 内执行。错地址族的解析读回拒绝，限时切换读回候选，超时读回原路由，确认后候选跨越期限仍有效，显式 undo 恢复原路由。原生切换命令返回 0004，须等状态和路由读回。两进程均正常退出。试验范围是守护进程控制行为，BGP 通告和实际部署流程仍单独验收。证据 `.cache/bird1-control-native/results.json`。

联系人导出接入「联系人 → 按联系组导出」，工作人员可按精确组名或类型取并集、可再选成员，保留原七列及成员日期边界。JSON 保留字段原值，CSV 用可见文本前缀保护电话和公式触发字段。路由和选项元数据使用同一会话权限，成员和令牌无此导出入口。输出基于一致快照并在编码后复核会话，响应写入前释放库存锁。定向 race、vet 及 Go 构建通过，真实浏览器验证列表并集、角色、JSON/CSV 下载、出错刷新和 390px 对话框。证据 `.cache/contact-export-final/browser-results.json`、`.cache/contact-export-final/checks.log`。

可搬运 Linux 发布包已接入 `make bundle` 和 CI。包内为 16 个 CGO0 Go 工具、前端静态资源、Logo 和许可。服务默认识别二进制旁的前端，保留开发目录与明确 `-web` 的用法。冻结 App/routing 和前端离线构建及全包 vet 通过。抽取压缩包后在另一个 cwd 运行真实服务，对初始令牌拒绝及初始化、登录重启、原资产与 Logo HTTP 字节、停止后创建备份并恢复到新目录、账号和成员数据读回均通过。构建和验证在零能力、无外网的 namespace 中执行，三个服务进程都正常退出。该快照早于 Quagga 控制面接入与 BIRD 1 在线应用，生产跨版本升级仍须演练。证据`.cache/portable-release-check/results.json`。

新版 `ixp-backup verify` 已复用实际恢复校验，在私密临时目录加载归档、关联文件和数据状态后清理副本。定向 backup race 和 vet 通过。另在无外网环境中运行了两个不同 Go 构建：旧管理服务创建账号、成员和加密路由版本，旧工具产生备份；新版检查并恢复后，新管理程序可登录、读取成员并得到同一配置版本正文与审批标识。检查正常和错误路径均自动清理临时数据，旧状态及原归档散列保持一致。范围为合成 Go 快照的跨构建演练。证据 `.cache/upgrade-rehearsal/results.json`。

Quagga 1.2.4 已完成 Go 的 RS 配置生成、原生版本及策略校验、审批、授权下载与代理拉取检查。独立客户端 RIB 的 import 按来源邻居分流并调用对应策略。原版 bgpd 完成双族、单族和空邻居解析，链接原生路由库的 21 项检查覆盖前缀、AS 路径、来源与下一跳。App 通过 IPv4/IPv6 生成、真实校验、批准和限数据范围下载，agent 执行拉取及本机检查。定向 race、vet、manager/agent 构建通过。后续仍需活跃 BGP 会话、实际发布与故障撤回验证。证据 `.cache/quagga-app-checks/run-results.json`。

旧路由器库存转换已通过实际 Go HTTP 导入。按 `routers` 的原声明保留独立 IPv4/IPv6 实例及精确的旧行 ID。外部模板、未确认的安全控制、未知平台和同交换网络冲突逐行报告，同一交换网络的重复 Router ID 不自动重编号。可表示的 Cisco 与 Quagga RS 来源保留为库存。导入范围仅为库存，BGP 会话草稿按旧模板、归属和上限证据独立验证。证据 `.cache/router-migration-final/results.json`。

资源 JSON 导入增加 `/api/import/preview`。管理员浏览器将同一份原文件用于预检和确认，保留原字节。预检在库存副本中运行与提交相同的导入逻辑，校验资源、关联、接收器、核心互联和归档引用。预检不会更改数据文件或创建导入审计记录。提交复核当前会话并按最新状态重新运行校验。Go race 通过并验证撤销会话、重复 ID 和原子性。真实 Chromium 验证初始冲突禁提交、预检后并发冲突整批拒绝、确认有效文件和 390px 界面。证据 `.cache/resource-import-preview/browser-results.json`。

BGP 对等会话新增只读配置条件检查，可对已启用和停用记录执行。Go 服务在库存副本中核对成员状态与 ASN、实际地址族、同 VLAN 活跃接入、白名单和目标平台角色，并由正常配置生成路径检查结果。权限限定为工作人员浏览器会话，响应只包含检查状态和原因。实际 Chromium 在 Quagga 草稿上验证缺少接入和策略、补齐后刷新、直接进入编辑及 390px 布局。最终草稿仍停用且无新部署。Go race、vet 与真实浏览器检查通过。证据 `.cache/peer-readiness/browser-results.json`。

旧邻居草稿已通过实际 Go HTTP 导入。转换使用显式来源日期、列布局和已验证的模板语义，从旧 VLAN、成员、接入、协议地址和源码明确的 max-prefix 约束推导草稿。草稿全部处于停用状态，白名单为空，凭据与部署授权独立核对。无新选项时，原 CLI 转换 JSON 和警告逐字保留。定向 race、vet、CLI 构建通过。证据 `.cache/peer-drafts/results.json`。

旧 `log` 表已提供独立审计摘要转换与管理员历史视图。`ixp-migrate --audit-archive` 只提取有界的行号、来源用户、模型身份、操作与原时间。原自由文本和对象属性留在旧源中。新服务按来源与旧行号拒绝重复归档，并分页返回历史摘要，同时单独记录实际导入动作。Go race、vet 和 CLI/manager 构建通过。真实 Chromium 使用真实转换器导入一条带模拟密钥及超出 JavaScript 精确整数范围的旧记录，检验输出省略敏感属性、原 ID 与原时间、导入回执刷新及 390px 布局。证据 `.cache/legacy-audit/browser-results.json`。

RouterOS 的双栈生成策略用例已增加原生 RIB 隔离检查。在真实对端已确认通告、切换和撤回后，API-SSL 每次核对专用表的 `fib` 未设置，独立 SSH 从实际 `/routing/route` 查找两个前缀。六次均确认获准前缀只存在于专用表，两个前缀均未出现在 `main` 或外部表。CHR 7.24.6 的 IPv4 和 IPv6 用例分别 64.69 秒、65.68 秒通过。初次新增探针断言未通过，原日志已保留。Go 探针解析定向用例及 107 项 Python 契约检查通过。证据 `.cache/routeros-rs-rib/results.json`。生产写入能力仍待完成。

Quagga 1.2.4 的真实双栈 BGP 会话已通过。隔离环境的 stock bgpd 与 6 个对端验证 2 次允许的更新、11 次策略拒绝及 2 次原生 First-AS 错误通知。接收端只保留获准前缀，AS_PATH 与 IPv4 和 IPv6 下一跳原样保留，内核路由保持一致。在线应用和恢复仍待完成。证据 `.cache/quagga-wire-final/final-results.json`。

BIRD 1 的 v2 专用本机 apply/recover 已完成独立验证，未把 BIRD2 的事务假设套入旧版本。实际 BIRD1 的 `configure status` 返回 9001；本路径改用 0013 精确 ready 文本、私密配置指纹重复读回、0018 确认以及同连接 socket 凭据/进程/工具绑定。候选先限时加载，确认后才发布启动配置。旧 v1 保持原字节 preview/check，初始 v2 基线须由运维离线建立；不接管共享目录/default socket，也不运行任意旧模板。所有 live 动作由本机 agent 明确接收私密 target/socket、base/next 文件摘要与独占声明，控制台不远程执行。

两族真实 native/CLI 的 11 顶层、32 子例及额外 RS→collector 1 顶层、2 子例通过 race，覆盖限时确认、丢失响应、原生错误、实际 daemon 终止及 agent 五阶段退出后的恢复。最终 routing/App/agent 的 13 顶层、14 子例、四包 vet、CGO0 agent/manager 构建通过，HTTP preview/diff 不暴露密码或运行指纹。readiness 暴露 `bird1-confirmed-configure`。证据 `.cache/bird1-apply-native/results.json`、`.cache/bird1-apply-collector/results.json`、`.cache/bird1-apply-final/results.json`；最后冻结为 `114984347e7b6b7446ea6b2ae9faeb02c74ae48da8273c7cbfb5b9672b04b8d2`，事务核心与真实 native 证据逐项核对一致。试验仅在隔离 namespace、零 target capability、无 kernel 协议及无生产转发条件下运行；这些配置控制证据不替代正常 BGP wire/远端收敛或生产升级演练，其他旧模板语义与更广厂商支持仍继续完成。

配置中心已接通 RouterOS 草稿到本机规划的交接。管理员和操作员浏览器可取得当前草稿的类型化 JSON。服务端复核会话、草稿标识、现行输入和加密工件；输出保留双栈、精确前缀与起源、上限和认证版本。`ixp-routeros plan --handoff` 将下载原文和本机 `review` 匹配到同一库存目标，另读私密密钥文件，生成 0600 阶段文件。Go 新增权限/完整性/旧凭据拒绝以及跨平台配置、AS112、凭据 race 回归通过。隔离 Chromium 使用真实服务与 CLI 验证了双栈下载、缺密钥拒绝、迟到响应不保存、并发输入变化拒绝、390px 布局和原配置中心回归。使用合成 `review` 验证离线规划，生产准备和激活 gate 保持关闭。证据 `.cache/routeros-planning-handoff/results.json`。

RFC 2317 已提供可审阅的离线生成和检查。`ixp-provision` 支持 IPv4 /25 至 /32，分别生成完整子区和带委派及 CNAME 的父区片段。原生 BIND 9.20.26 验证 /25、/26、/31 和 /32 的子区与合成父区样例，拒绝把裸片段当完整父区及 PTR/CNAME 冲突。Go race、vet 和实际 CLI 生成、检查、提取通过。真实父区快照核对、合并、SOA 竞争保护与控制台父子区审批仍待接入。证据 `.cache/rfc2317/results.json`。

RouterOS 在待确认更新期间硬重启的原生测试已复现。隔离 CHR 7.24.6 在 Safe Mode 的原会话仍持有时收到真实 QMP 重启；新 TLS/SSH 连接、稳定设备身份及较短 uptime 证明设备已重新启动。原会话已释放，但设备完整保留未确认候选，配置与认证证据均吻合。监督器现以持久 `unconfirmed-candidate-retained` 在 `rollback-pending` 明确报告，拒绝旧确认和规划审阅，两个验证标志仍为 false。正式入口原生 61.44 秒通过，Go 控制器和 CLI 整包 race、vet、构建通过。恢复已审阅基线的受控操作仍需验证，生产写入门关闭。证据 `.cache/routeros-device-reset/results.json`。

RFC 2317 的完整父区离线合并已通过验收。CLI 核对父区原始 SHA-256、SOA 串行和 owner 声明，仅替换 SOA token 并追加缺少的 NS/CNAME。完整父区与原子区通过 stock BIND 9.20.26 和原生 RRset 差分检查，其他原字节保留。12 项顶层和 61 个子例、Go race、vet 和真实 CLI 流程均通过。实时 CAS 和成对审批发布仍待实现。证据 `.cache/rfc2317-parent/results.json`。

Quagga 原版进程切换的证据已补齐。隔离环境的 fresh replacement 通过 A→B→A、候选就绪前退出后恢复基线、双地址族监听、TERM Cease、会话断开清 RIB 和进程身份校验。独立实测确认 HUP 会让两条已删邻居重新连接并收到候选路由。在线适配采用进程替换，生产能力由真实适配器验证后决定。证据 `.cache/quagga-lifecycle/{replacement,hup}/results.json`。

RouterOS 已有受管基线的显式恢复已在隔离 CHR 中验证。`restore-review` 从同一认证日志推导原基线，独立核对当前未确认候选。`restore-stage` 重新校验精确摘要和凭据，新事务在确认前可回到保留候选但不能普通发布。原生 CHR 7.24.6 在实际 QMP 重启保留候选之后，恢复交易确认、原策略代及 TCP-MD5 和非 FIB 表严格读回、日志重开均通过，耗时 74.37 秒。CLI、Go 控制器和 114 个测试基础设施合同检查通过。现有原生写入门继续约束执行。证据 `.cache/routeros-baseline-restore/results.json`。

RFC 2317 的成对审批已接入配置工具。操作员从原字节合并包导入、校验。管理员批准一个精确审阅标识，两区 RFC1982 审批在同一写事务中推进。已校验的旧配对在父区或子区任一推进后拒绝，已批准父区在相同 SOA 但原字节变化时拒绝，普通反向区生成不会覆盖其委派。预览仅显示完整子区、父区 SOA 和实际新增记录。真实 BIND 9.20.26、Go race、备份回归及真实 Chromium 通过。目标侧 `rfc2317-preflight` 已验证给定本机路径的原文件/显式新建状态与候选 BIND 结果。实际安装和重载仍待实现。证据 `.cache/rfc2317-paired-app/results.json` 和 `.cache/rfc2317-target-preflight-final/results.json`。

Quagga 的本机监督器和配置中心引导已完成隔离验收。stock 1.2.4 双栈 BGP 进程替换、基线过期拒绝、取消回退、候选启动失败与未确认重开恢复通过。实际 `ixp-agent` + 私密同 UID socket 在另一套无邻居 fixture 验证本机动作。Chromium 配置中心使用 `quagga-local-watchdog` 展示固定目标、SHA、BGP 会话中断，并核对真实审批下载。通用远程应用门保持关闭。证据 `.cache/quagga-watchdog-core/native/results.json`、`.cache/quagga-agent-watchdog-final/results.json`、`.cache/quagga-readiness-guide/results.json`。

Quagga 已覆盖候选通告期间监督进程突然退出。实际 `ixp-agent` 与 stock bgpd 在隔离双栈网络中运行。SIGKILL 只发送给监督进程。候选进程退出、双族监听释放、旧 RIB 清除。故意加入外来监听器时重开被拒且原日志不变。去掉外来监听器后重新启动，最后确认配置的字节、VTY 运行指纹和实际双栈路由均吻合，原候选确认被拒。29.27 秒原生测试和 race 通过。证据 `.cache/quagga-supervisor-sigkill/results.json`。

RFC 2317 本机 BIND 安装与中断恢复已在隔离环境验收。`rfc2317-install` 绑定已批准合并包、目标 master 路径和回环 BIND 控制及数据地址。先完成并读回子区，再更换父区。目录 inode 锁、原子文件操作及私密日志保护精确持久阶段，失败后用同一批准包前向恢复。真实 BIND 9.20.26 覆盖未加载的新子区、原 SOA 为零的已有子区和子区加载回执丢失。后一个场景里父区仍提供旧内容，实际 CLI 续跑只重载父区。三种场景均核对真实权威 TCP CNAME、PTR、SOA 与精确文件。Go race、vet、构建和跨进程目录锁回归通过。范围是一台本机静态未签名 BIND 主服务器。证据 `.cache/rfc2317-install-root/results.json`。

RouterOS 已确认版本的清理已支持按已验证批次继续。`cleanup --max-batches 1..16` 停在完整读回后的边界。CLI 保留原不设限请求，私密 `supervisor.manualCleanup` 可在确认后等待明确开始。重开只读恢复原交易进度，旧审批和未知写入结果不会产生继续命令。控制器全包及 CLI/live 配置 35 主例、57 子例 race、vet 和构建通过。证据 `.cache/routeros-bounded-cleanup/results.json`。

Quagga 的真实单播会话状态已接入监控。固定私密 bgpd.vty 与本机 Router ID、ASN 和进程一致，读取原生双栈 unicast summary，仅返回指定邻居。采集器从同一次受限绑定请求获取期望本机身份，管理端入库时再次核对，并保留旧用途的绑定响应形状及成员限定。真实双栈原生观测与控制面角色/身份变化回归通过。实际 `ixp-collector --once` 经 HTTPS 和路由器限定令牌连接真实 Go 管理端。stock Quagga 两族完成 OPEN，真实 IPv4 UPDATE 贡献 1 条前缀，IPv6 已建立且为 0。改动登记的本机 Router ID 后，第二次采集被拒绝且没有上传。10.15 秒原生端到端、Go race、vet 和静态构建通过。证据 `.cache/quagga-monitoring/results.json`。

已重建当前 Linux amd64 便携候选包。冻结 Go 源码、锁匹配的本机前端依赖和原始 Logo，离线 `make bundle` 生成 16 个 CGO0 工具及前端。隔离环境在另一个工作目录实际解包启动，验证初始化密码令牌、登录后重启、Logo 和静态文件精确字节、备份 create/verify 清理临时状态以及 restore 保留成员和加密工件。已核对实际包里的 RouterOS cleanup 参数、Quagga 目标限定和 RFC 2317 安装入口。证据 `.cache/portable-release-current/results.json`。

联系人分组导出已开放专用自动化入口。管理员及操作员可用独立 `contacts:export` 令牌请求精确名称或类型的 JSON/CSV，仍可按成员限定。普通资源和网络导出令牌不能调用此接口，专用令牌不能枚举浏览器选择器、访问其他资源或修改联系人。真实 HTTP 精确字节、权限、撤销、Go race/vet 和前端类型检查通过。证据 `.cache/contact-export-automation/results.json`。

旧 `POST /api/v4/customer/by-vlan-and-protocol` 保留表单及 JSON 查询、IPv4/IPv6 筛选、`listCustomers` 和精确数字编号。转换器从旧接口与同 VLAN 的路由器声明保存查询所需事实。浏览器请求需要 CSRF，令牌需要 `resources:read`，成员请求限当前有效组织。普通编辑保留迁移事实，改变归属或移除地址时移除相应资格。旧源转换、实际 Go 导入和 HTTP 已联合验证。早期 Go 库存中的记录需要回到旧源重新导出转换，在隔离目标审阅，现有库存暂未回填。

旧按月总流量的两个 API 路径已接入。服务对审阅后的 `ixp001` 年度 Grapher 快照校验 SHA-256，从原始五列采样计算最高观测值。响应保留旧字段与已观测零值，缺测月份省略。匿名访问需显式启用，更新快照后需要重启。定向 race、vet、构建和隔离 HTTP 验证通过。 [配置与数据来源](LEGACY-OVERALL-TRAFFIC.md)

「我的网络」已接入旧组织账号名册的日常查看流程。当前活跃组织的成员管理员可通过浏览器查看姓名、登录邮箱、该组织内角色、当前两步验证状态与账号创建时间。接口按当前会话复核权限并只返回必要字段。切换组织请求期间隐藏名册，晚到响应仍需匹配当前组织。独立 Go race 与真实 Chromium 验证覆盖两组织切换、旧会话和令牌拒绝、真实两步验证、刷新失败恢复、邀请入口及 390px 布局。旧账号停用状态与批量接入仍在迁移范围内。

`ixp-migrate --account-archive` 已支持从旧 `user` 和 `customer_to_users` 表提取受限私密来源。精确记录原编号、停用状态、组织角色和可证明的时间。身份或权限有歧义的行保留供审阅。转换器测试与实际 CLI 合成数据导出通过。批量授权与身份确认仍需接入邀请流程。 [数据格式和白名单 SQL](../cmd/ixp-migrate/ACCOUNT-ARCHIVE.md)

组织关系移除已接到名册。确认绑定同一账号、组织与预览版本，提交时重查权限和最后管理员规则。移除会使目标的旧会话、令牌及记住设备失效。其他组织关系及账号两步验证保留。最后一条关系移除后，账号进入等待新邀请的受限状态，可登录和退出。Go 竞态测试覆盖并发移除与实际两步验证重邀。Chromium 验证了 390px 审阅、预览过时、只提交一次确认，以及受限登录和退出。

旧账号归档可在「成员邀请」批量审核。管理员选择关系、关联当前组织并复核角色。邮箱碰撞时需明确确认当前账号。邀请把一个账号的多个组织及角色一起呈现，管理员和现有账号接收方均沿用当前密码及已启用的两步验证。接收方核对后一次写入获准关系，保留其他组织权限，并为新账号保留已选的旧默认组织。已知停用、歧义或权限不明的记录须继续审阅。 [操作说明](../cmd/ixp-migrate/ACCOUNT-ARCHIVE.md)

后台调度健康已接入「通知与后台任务」。浏览器管理员可通过 `GET /api/jobs` 核对本进程的最近完成时间、当前阶段和上一轮失败阶段。距上次完成超过 10 分钟时显示停滞，首次启动至首次完成期间从启动时间起算。任务卡显示到期、逾期、活动租约或租约过期。刷新失败时页面标记上次快照，恢复观测后可以提交任务。重新提交会让固定任务进入待执行状态，清除旧租约。在新进程中，调度健康从实际启动开始记录。HTTP 存活通过 `/healthz` 检查，邮件结果在投递队列中查看。

「配置工具 → 监控配置检查」补上旧版 Nagios 配置刷新检查的日常用途。浏览器管理员可以分页核对 Nagios 和 Smokeping 规则的最新同类型批准版本、文件完整性，以及规则和源数据是否仍与批准内容匹配。服务在成功写出完整批准内容后，按版本分别记录绑定规则的 API 拉取和人工下载，同类重复记录以 5 分钟为间隔。距最近记录的 API 拉取超过 24 小时时提示检查。人工下载和拉取记录都随原版本通过备份保留。目标监控服务装载状态保持未观测。

「交换中心设置 → 数据目录检查」覆盖旧基础验证中的在线文件和目录写入检查。浏览器管理员显式触发 `POST /api/system/storage-check` 后，服务核对当前目录与已持有的锁，再创建私有临时文件和目录，执行小型写入、同步、重命名、读回和清理。最终重新核对目录与锁。结果只给出阶段、固定错误分类和检查时间。检查范围是本进程当前主数据目录的一次小型文件操作，正式状态文件、密钥、锁和业务修订保持原样。

Go Linux 发布包增加了 systemd 服务和账号声明。它用专用账号运行前台 Go 进程，监听回环地址，固定私有状态目录，并支持同一发布目录中的 UI。发布流程覆盖停止、离线备份、归档校验、新目录恢复与切换。旧 PHP 安装器、Vagrant 测试环境、旧 Docker 开发环境和 PHP IDE 工具已退役，可从原版提交查阅。 [Go 主机安装](SYSTEMD-INSTALL.md) · [原版树](https://github.com/MoeDove-LLC/IXP-Manager/tree/8161e185b9a155f6b297f1487a2d0c6ed920e27e)

旧版站点的 `public/` 入口及其静态资源已退役。源码方式从 `web/dist` 提供控制台。Linux 发布包从二进制旁的 `web/` 提供控制台，容器使用 `/app/web`。已在隔离环境解包启动，验证控制台、Logo、初始化、重启和备份恢复。迁移旧成员 Logo 时仍可用 `ixp-migrate --logo-bundle --logo-public-root` 指向归档中包含 `logos/` 的旧 `public/`。 [旧版站点目录](https://github.com/MoeDove-LLC/IXP-Manager/tree/8161e185b9a155f6b297f1487a2d0c6ed920e27e/public)

原管理员成员欢迎邮件迁至 Go「成员 → 欢迎邮件」。草稿汇总当前成员、端口和 LAG、VLAN、双栈地址及成员管理员资料。管理员可在当前成员和支持邮箱范围内编辑 To、CC、BCC、主题及正文。排队前绑定预览内容和成员资料，投递与重试前复核当前归属。正文与抄送信息使用加密队列，BCC 使用 SMTP 信封。独立 Go 竞态、备份恢复和 Chromium 测试验证了完整流程，含响应丢失后的同一审阅重放。对应的 PHP 路由、表单、邮件类和模板已退役。 [原版欢迎邮件](https://github.com/MoeDove-LLC/IXP-Manager/blob/8161e185b9a155f6b297f1487a2d0c6ed920e27e/resources/views/customer/emails/welcome-email.blade.php)

「经销商分流」保存唯一的成员接入端口与直属经销商端口配对。管理员及操作员可按现有版本更换或释放配对。右侧端口在配对有效期间保留给经销商，核心候选和成员接入提交复核该占用。关闭成员后物理关系持续保留。旧库转换按 `physicalinterface.fanout_physical_interface_id` 还原配对，保留原成员的多 VLAN 和 LAG。源数据需要证明被动端是独立物理端口、属于该成员的直属经销商且无自身 VLAN 或聚合。孤儿引用、归属冲突和混合用途的旧端口阻止生成导入文件。 [旧库样例](../tools/migrate/example-legacy-fanouts.json)
