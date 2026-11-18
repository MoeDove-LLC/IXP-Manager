# 核心互联离线迁移

`ixp-migrate` 接受离线、按表组织的 JSON。核心来源为 `corebundles`、`corelinks`、`coreinterfaces`，以及关联的 `physicalinterface`、`virtualinterface`、`switchport`、`switch` 和库存引用表。转换器不连接数据库，也不配置真实设备。

完整示例在 [testdata/core-topology.json](testdata/core-topology.json)，涵盖 ECMP、L2-LAG、L3-LAG、双栈、禁用交换机、独立接口属性与未归组的预留核心端口：

```sh
ixp-migrate --strict --input core-topology.json --output reviewed-core-import.json
```

输出使用顶层 `coreBundles` typed 数组；核心结构不会被压平成普通成员 connection 或旧的两端 port-only `resources.core-links`。

| 旧字段/关系 | 新模型 |
| --- | --- |
| corebundle type 1/2/3 | `ecmp` / `l2-lag` / `l3-lag` |
| description、graph_title、enabled、stp | name、graphTitle、enabled、stp |
| bundle bfd、ipv4_subnet、ipv6_subnet | L3-LAG bundle 级策略 |
| corelink bfd、ipv4_subnet、ipv6_subnet | ECMP link 级策略 |
| cost、preference | nullable uint32；NULL 与明确 0 保持区别 |
| link 两侧 coreinterface→PI→VI | link 的物理 Endpoint 与 bundle 两侧逻辑 Side |
| VI custid、name、description、channelgroup、mtu | ownerMemberId、interfaceName、description、channelGroup、mtu |
| VI trunk、fastlacp、lag_framing | 独立布尔字段，不从 bundle 类型猜测 |
| PI speed、duplex、autoneg、status、rate_limit、notes | 独立的 desired endpoint 属性，保留原值 |
| switch asn、loopback_ip、loopback_name、active | asn、按实际族分入 loopbackIpv4/6、loopbackName、active |
| switchport type=3 | `coreReserved:true`，包括尚无 PI/CI 的预留端口 |

同一 bundle 每侧必须解析到同一个 VI 和同一交换机。Core Interface、PI、物理端口在整个核心图中只能有一个归属；不能把不同 VI 或不同设备静默折叠成一侧。两侧 custid 是逻辑所有者，**不会**写成核心端口的 `memberId`，也不会生成成员接入 connection。

ECMP 使用各 link 子网，L3-LAG 使用 bundle 子网；L2-LAG 不带 L3 子网/BFD。缺必需子网不生成默认值，带主机位的 CIDR 不自动掩码修正。合法 IPv6 大小写/零压缩会规范化。旧模型没有独立 backup/standby 字段，不从 cost、preference 或 disabled 推断此角色。

MTU 保留旧核心范围 0..64000，不套用成员接入的更小范围。LAG 的 channelGroup 必须明确提供，0 是合法值；缺失不能填成 0。PI duplex 必须可表示为 full/half，autoneg 以及 bundle/link enabled 必须有明确值。PI status 保留 connected、disabled、not-connected、awaiting-cross-connect、quarantine 的区别；NULL 表示未知，未知的非空代码不会猜成 disabled。Rate limit 的 NULL 与 0 分别保留；0 表达无限制。

PI.speed 代表旧配置意图，`switchport.ifHighSpeed` 是独立的端口容量来源，二者不相互覆盖。switch.active=false 原样保留，不用 SNMP oper status 推断启用或健康。原始业务/观测时间保留在源备份中，输出不带可复用的 bindingRevision 或旧观测 lineage；真正导入时生成新时间与端口绑定代次。

以下情况即使没有 `--strict` 也返回错误，stdout 不包含导入 JSON，也不创建输出文件：

- CI/PI/link/bundle 引用缺失、重复端点、同一 CI 跨 A/B 复用、一个物理端口被多个 PI 占用。
- 有 PI 占用的核心预留端口缺少完整 core 关联，或核心 VI 混入普通 PI/VLI。
- 核心与 member fanout 互相引用，核心用途与明确的非核心端口类型冲突。
- 必需关联库存未转换、一个侧面跨不同 VI/交换机、无完整 link 的 bundle。
- LAG channel group 冲突，核心子网与同作用域核心/peering VLAN/loopback 冲突，L2 跨不同 infrastructure。
- 核心字段类型/范围无法忠实映射；不会靠静默丢弃危险字段或虚构接口属性继续转换。

仅 `type=3`、尚未绑定 PI 的端口是合法预留，不会被错误判为断链。其他未知表、额外观测字段和密钥字段仍沿用通用迁移警告；strict 模式要求逐项处理这些警告。旧 SNMP/BGP 等专用凭据字段的值不会拷贝到输出或诊断。

验证命令：

```sh
go test -race ./cmd/ixp-migrate -count=1
go vet ./cmd/ixp-migrate
```

测试同时覆盖真实 Go `/api/import` 与 `/api/export`：保留稳定 legacy IDs、三个 typed bundle、正常成员接入分离、inactive switch、预留核心端口，并确认导入生成新 binding/timestamps。所有测试使用仓库构造 fixture 和临时数据目录，不读取生产数据、不使用旧 SNMP 密钥或执行配置下发。
