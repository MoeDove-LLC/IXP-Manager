# DNS 和监测配置生成器

`internal/provision` 只接受 [types.go](types.go) 的 typed 输入，输出一个确定性的工件。`Render` 成功表示生成完成，`Validate` 成功表示相应级别的验证完成；两者都不安装配置、不重载服务、不代表实际健康。控制面负责库存投影、不可变版本、审批、保留期和加密备份。

| 类型 | 输出 | 验证边界 |
| --- | --- | --- |
| `dns-arpa` | 单个完整反向区域：SOA、NS、IPv4/IPv6 PTR | 固定参数调用真实 `named-checkzone`，缺工具或失败即拒绝；不验证上级委派、正向记录或当前线上 SOA |
| `nagios` | 受限 host/service/hostgroup include | 仅结构验证；目标完整配置还必须定义模板、固定命令，并执行 `nagios -v` |
| `smokeping` | Targets 子树 include | 仅结构验证；目标完整配置还必须定义 probe，并执行 `smokeping --check --config=...` |
| `switch-core` | 单台交换机的类型化核心接口、物理成员与双栈 BGP JSON 计划 | 跨字段结构验证；需目标厂商适配、原生校验及独立应用，不是 native CLI |

`ValidatorOptions.Nagios/Smokeping/MainConfig` 不会启动验证：检查一个无关的已安装主配置无法证明当前片段已包含在其中。调用者不能借这些字段将片段升级为 native 验证。

## DNS 范围

普通 `Options.DNS.Prefix` 必须是单个 canonical CIDR。IPv4 只支持 /8、/16、/24、/32；IPv6 支持 /4 至 /128 的 nibble 边界。其他前缀明确拒绝，不能将一个部分网段伪装为整个父区。独立的 [RFC 2317 离线流程](../../docs/RFC2317.md) 接受另一份 typed 输入，生成完整子区与明确标记为不完整的父区片段；它尚未接入 App 的父子区审批/序列账本，不会绕过这些普通反向区限制。

`PrimaryNS`、`ResponsibleMailbox`（DNS RNAME，例如 `hostmaster.example.net.`）、`NameServers` 是明确的 DNS 名称；不接受模板、路径或任意指令。区内 NS 需要 glue，当前版本要求区外 NS。默认 TTL/refresh/retry/expire/minimum 为 86400/43200/7200/1209600/7200 秒。没有显式 `DNSName` 时，只在配置 `NameSuffix` 后生成确定性名称；否则给出跳过提示。一个地址出现不同 PTR 名称会拒绝生成。

`Serial` 必须是调用者提供的非零固定值，`Render` 不读取当前时间。Web 控制面对已有区要求明确的当前 SOA 基线；`dnsSerialFloor: 0` 是合法的显式基线，与未提供不同。只有显式选择“全新区”且该区没有已分配账本时，才能选择初始序列。CLI 不安装、不读取当前权威区。

分配、批准、启动/备份校验和资源导出统一使用 [RFC 1982 第3、7节](https://www.rfc-editor.org/rfc/rfc1982.html) 的32位环形比较。通常前进1；`4294967295`后为`1`，以合法的+2跨过0，保持区文件的非零约束。数值变小不等于回退；相差`2147483648`的顺序未定义，必须拒绝，不能用普通整数大小决定。已分配/已批准的每区账本独立保留，不随profile删除、版本清理或重建而重置。

新草稿只前进已分配序列，不改变当前批准文件；批准必须在RFC顺序上前进，旧批准序列不能重新下载。锁内下载再次核对当前批准账本。保留的序列不可复用，候选必须与明确基线、已分配/批准head和保留历史处于可比较窗口；歧义或跨度过大时拒绝，而不是自动重置区或猜测线上状态。资源导出携带回绕后最新可比较基线，完整备份保留原账本和加密工件。

回滚内容需要重新生成、校验并批准在RFC顺序上更新的版本，不能直接恢复旧文件。上述规则不证明主/从服务器已经加载该版本；部署前仍须核对实际SOA和各从服务器同步。RFC还要求留意SOA expire期间累计前进量，不能把多次大幅跳跃当作自动安全的重同步。


默认 `ActiveOnly=false` 保留旧系统所有已分配主机名的 PTR 覆盖，包括当前禁用的地址族。`Monitor`、`CanPing` 不控制 DNS。独立保留的地址必须仍有真实成员归属；控制面迁移和 IPAM 在解除/改变归属时清除前任成员的 DNS 偏好。

## 监测范围与实际观测

Nagios 使用 `Active && Monitor` 的目标；只有 `CanPing=true` 才生成 ping 服务，`Busy` 选择忙主机模板。禁 ping 主机保留为关联 BGP 服务的容器，主机主动检查关闭，不应把容器的主机状态解释为实测连通性。Smokeping 使用 `Active && Monitor && CanPing`。这些布尔值是配置意图，不是测量结果。

成员可以属于多个交换机、机架、位置，生成对应组；VLAN 范围会包括选中成员依赖的交换机。交换机供应商/型号、基础设施、协议族及实际 BGP router ID 也产生稳定分组。启用且允许监测的 BGP session 使用实际 peer ID；缺地址关联会提示并跳过。路由服务器、采集器和 AS112 的 session 角色受支持。不存在真实 core-bundle 对象时不从 desired core-link 捏造对象。

同一个监测地址若跨基础设施、VLAN 或成员复用，必须缩小档案范围；生成的 ping/probe 没有 VRF/netns 选择器，不能声称可区分它们。此限制不会静默丢弃库存。普通组织名中的标点会转为可显示的全角字符，阻止配置注释、宏与指令注入；控制字符和换行拒绝。

所有模板和 probe 选项只能是安全标识符。Nagios 服务使用 `specificTemplate,serviceTemplate`，前者优先，遵循 [Nagios 多模板继承规则](https://assets.nagios.com/downloads/nagioscore/docs/nagioscore/4/en/objectinheritance.html)。没有用户可编辑命令、shell 或 executable path。

## 独立 CLI

`ixp-provision` 提供 `render`、`check`、`fetch`、`probe`；没有 install/reload/rollback 子命令。

```sh
ixp-provision render --input inventory.json --output reviewed.zone
ixp-provision check --kind dns-arpa --zone 2.0.192.in-addr.arpa. \
  --file reviewed.zone --checksum ACTUAL_FILE_SHA256 \
  --named-checkzone /usr/bin/named-checkzone
ixp-provision fetch --server https://ixp.example.net \
  --profile PROFILE_ID --revision APPROVED_REVISION --kind dns-arpa \
  --zone 2.0.192.in-addr.arpa. --token-file /etc/moedove/pull.token \
  --output downloaded.zone
```

`render` 的输入是 `Input` JSON（8 MiB 上限；不认识的字段拒绝）；输出文件始终以 0600、临时文件 fsync + 不覆盖原子发布的方式保存。标准输出给出实际文件 SHA-256、级别和提示。`render` 不执行原生检查；用 `check` 验证具体文件字节。Nagios/Smokeping 的 `check` 成功结果始终是 `structural`。

最小 DNS 输入示例：

```json
{
  "options": {
    "kind": "dns-arpa",
    "vlanId": "vlan-1",
    "dns": {
      "prefix": "192.0.2.0/24",
      "primaryNS": "ns1.example.net.",
      "responsibleMailbox": "hostmaster.example.net.",
      "nameServers": ["ns1.example.net.", "ns2.example.net."],
      "serial": 2026110701
    }
  },
  "targets": [{
    "id": "connection-1-v4", "kind": "member", "name": "Example member",
    "address": "192.0.2.10", "dnsName": "router.example.net.",
    "memberId": "member-1", "asn": 64501, "vlanId": "vlan-1",
    "active": true, "monitor": true, "canPing": true
  }]
}
```

`fetch` 只接受 HTTPS origin，拒绝重定向，绑定响应的 profile ID、revision ID、kind 及真实文件校验和；DNS 还要求期望 zone。令牌必须是有对应 `ProfileIDs` 的 `provisioning:pull` 令牌，只能从私有常规文件或 `--token-stdin` 读取。不会输出令牌或错误响应体。Web 的审阅标识不能作为文件 SHA-256；使用下载的 `X-Config-SHA256` 或 CLI 返回值。下载后仍需校验、结合目标上下文审阅并由运维部署。

### Nagios 固定命令接入

在受控的目标 Nagios 主配置定义下列命令，令牌文件只对实际 Nagios 运行用户开放，并使用最小的 `monitoring:read` 和/或 `devices:read` scope。

| 生成命令 | 本地实现 |
| --- | --- |
| `check_moedove_ping4/6` | 目标安装的 Monitoring Plugins `check_ping -4/-6`，地址使用 `$HOSTADDRESS$`，阈值由站点固定 |
| `check_moedove_ping_busy4/6` | 同上，使用站点固定的忙主机阈值 |
| `check_moedove_bgp` | `ixp-provision probe --kind bgp --id '$ARG1$' --server https://ixp.example.net --token-file /etc/nagios4/moedove.token` |
| `check_moedove_router` | 同上，`--kind router` |
| `check_moedove_switch` | 同上，`--kind switch` |
| `check_moedove_core` | 同上，`--kind core`；缺实际观测时返回 UNKNOWN |

例如一个固定 ping 命令可以使用 `/usr/lib/nagios/plugins/check_ping -4 -H '$HOSTADDRESS$' -w 100,20% -c 200,40% -p 5 -t 10`，但插件路径、告警阈值和运行权限必须在目标站点确认。IPv6 显式使用 `-6`。命令格式参见 [Monitoring Plugins 文档](https://www.monitoring-plugins.org/doc/man/check_ping.html)。这些主动 ICMP 命令由目标监测系统运行；生成器和本次验证没有运行它们。

`probe` 通过固定 API 只读获取真实观测，最多等待 15 秒，默认样本年龄不超过 120 秒。服务端 stale 状态、超过 30 秒的未来样本、缺采样时间、身份未验证或重复记录一律 UNKNOWN（退出码 3）。BGP 已建立返回 0，真实非 Established 的已知状态返回 CRITICAL（2）。明确禁用/不监测的 peer 不计入 router 新鲜性；零个有效样本仍 UNKNOWN。旧 API 缺布尔元数据时保守保留样本检查。

Router 检查只证明被选择的 BGP 观测新鲜，不能证明独立 daemon 健康；switch 检查只证明 SNMP 采集成功且新鲜，不推断所有组件健康。core 检查读取 `/api/core/bundles/{id}/observations`，逐条验证全部启用链路两端的真实身份、新鲜度和物理 Up/Down，不评估 LACP/BFD/BGP 或转发。零启用链路、缺失/过期数据及明确停用均为 UNKNOWN；`core:read` 令牌必须绑定两侧交换机。任何采集失败、过期、数据不足都不能被库存 `Active`/`Enabled` 变成 OK。顶层 API 查询时间绝不替代样本时间。[Nagios 插件退出码规范](https://assets.nagios.com/downloads/nagioscore/docs/nagioscore/3/en/pluginapi.html) 区分这些结果。

`switch-core` 规则使用 `options:{kind:"switch-core",switchId:"..."}`，只输出该交换机的完整双栈计划，不允许 VLAN/fabric/单地址族截取。离线输入的 `coreConfig` 必须通过 `core.ValidateSwitchPlan`，包含固定的 inventory-only、非 native、需 target adapter 声明。`fetch --kind switch-core` 还必须提供 `--switch`，核对响应头与计划内物理 switch ID；其它私密文件、实际 SHA、profile/revision 绑定和不覆盖规则不变。完整流程见[核心互联说明](../../docs/CORE.md)。

## 离线验证

包合同测试覆盖双栈反向名边界、完整 owner 形状、注入、模板/probe 限制、旧禁用地址 PTR、LAG/范围选择、同 IP 跨域歧义、native 缺失拒绝与稳定输出。设置 `NAMED_CHECKZONE_BIN` 可使用真实 BIND 验证双栈正反例；设置 `NAGIOS_BIN` 可对隔离的完整临时 Nagios 配置执行 `-v`，不运行任何插件。Smokeping 当前仅做结构验证，未宣称已对目标完整配置原生验证；目标检查用官方 [`--check`](https://oss.oetiker.ch/smokeping/doc/smokeping.en.html)。

CLI 测试使用本地 HTTPS fixtures，覆盖目标/校验和固定、拒绝重定向、令牌权限、FIFO 拒绝、并发原子发布，以及真实采样新鲜性/身份规则。没有联系真实路由器或外部业务系统。

本次离线验证使用 Debian 官方包提取的 BIND 9.20.26 和 Nagios 4.4.6，没有安装或启动 daemon。[BIND `named-checkzone` 文档](https://bind9.readthedocs.io/en/latest/manpages.html#named-checkzone-zone-file-validation-tool) 说明了区文件检查的范围；原生检查成功不等于服务已应用。
