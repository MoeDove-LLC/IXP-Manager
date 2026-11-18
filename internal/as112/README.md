# AS112 服务

本实现保留两类 AS112 服务：Direct Delegation（DD）和 DNAME redirection。节点 ASN 固定为 112；可以明确选择一种服务或两种，以及 IPv4、IPv6 或双栈。地址与空区行为依据 [RFC 7534](https://www.rfc-editor.org/rfc/rfc7534.html) 和 [RFC 7535](https://datatracker.ietf.org/doc/html/rfc7535)。DNAME 由需要重定向的源区管理员设置；AS112 节点托管 `empty.as112.arpa.`，不会在该空区创建 DNAME 环。

| 服务 | 宣告前缀 | 固定 DNS 地址 |
| --- | --- | --- |
| DD IPv4 | `192.175.48.0/24` | `192.175.48.1`、`.6`、`.42` |
| DD IPv6 | `2620:4f:8000::/48` | `2620:4f:8000::1`、`::6`、`::42` |
| DNAME IPv4 | `192.31.196.0/24` | `192.31.196.1` |
| DNAME IPv6 | `2001:4:112::/48` | `2001:4:112::1` |

DD 包含 RFC 1918 反向空区和 `254.169.in-addr.arpa.`，并保留旧实现已有的 `home.arpa.`、`service.arpa.`。后两者的委派依据分别为 [RFC 8375 §7](https://www.rfc-editor.org/rfc/rfc8375.html#section-7) 和 [RFC 9665 §9](https://www.rfc-editor.org/rfc/rfc9665.html#section-9)。两个 `hostname.as112.net.` / `hostname.as112.arpa.` 区提供节点 TXT 标识；可选 LOC 使用完整的“度 分 秒 方位”格式。

## 路由与 DNS 的职责

当前有 BIRD 2、BIRD 3、FRR、OpenBGPD 和 Junos 的专用生成器与严格配置识别。它们只允许所选服务的精确聚合前缀从本源导出，拒收邻居路由，不复用 route-server 的透明转发语义。BIRD 只导出指定静态协议的路由，FRR 使用精确 prefix-list 与本源 AS_PATH 条件，OpenBGPD 使用精确前缀及 AS_PATH 长度条件，Junos 只导出固定静态聚合。启用的 peer 是管理员明确配置的接入选择，不会给所有 IXP 成员自动建立邻居。

Linux 的 BIRD/FRR/OpenBGPD 模式用于与 DNS 同机的节点，不向内核安装邻居路由。主机返回路径需要另行配置和审阅。Junos 模式需要为每个启用地址族填写明确的 DNS 后端 next hop，只为固定服务地址生成 `/32`、`/128` 主机路由；主机路由、默认路由及邻居路由均不在 export 范围内。next hop 不能属于 AS112 anycast 前缀，不能是路由器自身 local 地址，不能使用错误地址族。

RouterOS、Cisco、Quagga、BIRD 1 没有本功能适配，不能用其他平台输出冒充支持。Junos 的控制台检查仍是结构检查，必须通过目标 NETCONF validate 与 confirmed-commit；真实转发并未由生成成功证明。

## DNS 配置规则与 CLI

创建 `as112-dns` 配置规则，明确绑定当前 AS112 router。服务模式和地址族来自该 router，不在 DNS 规则中另选。`serverName` 与 `responsibleMailbox` 是 DNS FQDN/RNAME；`uniqueIP` 对 Linux 模式必须匹配本机 transport 地址，对 Junos 模式必须匹配明确的 DNS 后端 next hop。一个规则绑定一台 DNS 节点，`nodeAddresses` 固定保存全部已启用地址族的本机 transport/后端 next hop；`requiredLocalAddresses` 还包含全部服务监听地址。不能用节点 A 的 IPv4 健康替节点 B 的 IPv6 next hop 作证明。

规则定义和 router 的 next hop 可以分别通过 CAS 修改意图；在两边完整匹配之前，生成、校验、审批和下载都拒绝。旧批准不会因为尚未完成的重定向而继续有效。规则仍使用既有加密版本、管理员审批和绑定下载流程。

下载内容是受约束的 JSON 多文件包，包含 `pdns.conf`、`named.conf`、区文件以及尚未执行的健康检查计划。文件路径和内容都由固定模型重生成核对；不接受额外配置语句、任意文件或伪造 `executed:true`。PowerDNS 使用 Authoritative Server 的 [BIND backend](https://doc.powerdns.com/authoritative/backends/bind.html)，仅监听所选固定地址，禁用 API、Web、AXFR、DNS UPDATE、secondary/autosecondary 和递归代理。`resolver=no` 与实际验证的 [PowerDNS 4.9.17 源码默认及开关判断](https://github.com/PowerDNS/pdns/blob/auth-4.9.17/pdns/auth-main.cc) 一致；不能改成空值并把 settings check 当作运行健康证明。

`ixp-manager --pdns-server` 与 `--named-checkzone` 只由本机管理员选择校验程序。校验会在私密临时目录中运行 PowerDNS 的配置检查，并逐一使用 `named-checkzone` 检查全部区；不会启动服务。生产能力和路径必须以实际安装版本为准。

```sh
ixp-provision fetch --server https://control.example.net --profile PROFILE \
  --revision REVISION --kind as112-dns --router ROUTER \
  --token-file /private/pull-token --output as112.json
ixp-provision check --kind as112-dns --file as112.json --checksum SHA256 \
  --named-checkzone /usr/bin/named-checkzone --pdns-server /usr/sbin/pdns_server
ixp-provision as112-extract --file as112.json --checksum SHA256 \
  --router ROUTER --output /new/private/as112
ixp-provision as112-probe --file as112.json --checksum SHA256 --router ROUTER
```

SHA256 是下载头中的实际文件摘要，不是界面的 review identifier。extract 只创建新的私密目录，不覆盖现有安装，也不启动或重载服务。PowerDNS 的工作目录必须是解包目录，以正确解析相对 `named.conf` 和 zone 路径。系统服务的权限、生命周期和实际安装由目标管理员控制。

probe 仅在本机具有全部 `requiredLocalAddresses` 后，才向固定服务地址的 53 端口发包；后端 node 地址只检查本机存在，不作为探测目的地址。它分别检查 IPv4/IPv6、UDP/TCP、两个节点 TXT 标识，以及所选空区的 NXDOMAIN、AA、RA 和 SOA。查询使用 EDNS0 的 1232 字节 UDP 广告，避免合法长 TXT 被传统 512 字节上限截断；响应读取仍有独立大小上限。成功返回 0，不健康返回 2，无法建立本机地址前提或输入无效返回非零；结果不是长期健康租约，也不会自动允许路由宣告。

## 运维前置与真实剩余

先配置服务地址，再启动并检查 DNS，最后发布 BGP。撤去服务时先撤回相应 BGP 前缀，再移除 DNS 和地址。DNS 或地址失效时也必须撤回相应前缀，这一顺序来自 RFC 7534 的运维要求。

专用 Linux BIRD 2/3、FRR 节点现在可使用 [ixp-as112 运维 CLI](../../cmd/ixp-as112/README.md)：固定原审批 artifact、native ELF 和当前 boot/netns 的私密计划，经明确批准后原子安装；前台 owner 根据持续 DNS/地址健康控制唯一受管 BGP 进程，unknown 不自动重放。当前模型显式关闭 GR/LLGR；历史 v1 只作精确脱敏读取，不能直接进入新运维流程。实际接口与保证见 [as112ops](../as112ops/README.md)。

这不是覆盖任意既有实例的安装器，也没有替管理员配置接口地址或主机返回路径。OpenBGPD 正常 privsep、远端 Junos、跨 boot 重新绑定、仍活但挂死的 supervisor 独立 guard、实际 systemd 部署和真实设备验收仍有明确边界。解析通过、生成健康计划和一次健康检查不替代持续运维证明；各平台的原生解析、模拟执行器入口与实际 BGP wire 行为应分别记录。
