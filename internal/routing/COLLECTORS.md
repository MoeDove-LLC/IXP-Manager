# 路由收集器

本文件主要解释 BIRD 2/FRR 的共同策略。BIRD 3 已通过独立原生解析和控制主版本检查，见 [BIRD3.md](BIRD3.md)；OpenBGPD 的原始 RIB 计数、无转发 RIB 和本机监督器边界见 [OPENBGPD.md](OPENBGPD.md)。两者也支持 observe/authorized-only，不能用另一个平台的解析结果替代。

BIRD 1 的两种单族 collector 见 [BIRD1.md](BIRD1.md)：使用实际 bird/bird6 1.6.8 解析，observe 保留过滤项并限制包含过滤项的接收量；不生成 BIRD 2/3 的 `import table`，不支持 runtime RPKI 或 live apply，也不声称完成原始报文/长期归档功能。

`Router.Role` 为空或 `route-server` 且未启用运行时 RPKI 时保留既有配置字节；显式选择 `route-collector` 才生成收集器配置。BIRD 2 与 FRR 支持该角色，仍使用现有审批、授权下载及本机执行器。配置头记录角色和收集策略，切换角色或策略会改变不可变配置校验和，必须重新校验和审批。启用 RPKI 的旧路由服务器也需要新增包含过滤路由的缓存限额，因此升级后必须重新生成和审批；历史快照字节和散列不变，旧版本按已有过期检查阻止重新下载下发。

`CollectorPolicy` 仅适用于收集器，空值等同于 `observe`：

| 策略 | 授权 RIB | 被导入策略拒绝的路由 | 白名单 |
|---|---|---|---|
| `observe` | 仍按精确前缀、首 ASN、路径长度、可选前缀/来源 ASN 配对及 RPKI 筛选 | 在本机预策略/过滤视图保留，用于诊断 | 可以为空；此时全部收到的路由都不能进入授权 RIB，只用于过滤观察 |
| `authorized-only` | 使用相同授权规则 | 不承诺保留拒绝项；启用 RPKI 重评估时可能有内部预策略缓存 | 必须非空 |

两种策略都禁止向所有邻居导出路由。BIRD 明确使用 `export none` 且没有 kernel 协议；FRR 使用不向内核安装路由的独立 `MOEDOVE` view，并为每个激活的地址族邻居绑定只有拒绝动作的出站 route-map。收集器不使用 route-server-client，不生成 network/redistribute。TCP-MD5、精确邻居地址/ASN 和首 ASN 检查仍生效；未知角色、未知策略和路由服务器上的 collectorPolicy 会直接拒绝。

观察模式不是“所有坏包都能保存”：BGP 自身因首 ASN、AS 环路、无效属性等原因拒绝的更新，不能保证进入可观察 RIB。它面向成员直接 eBGP 会话，不默认允许不携带服务器 ASN 的透明 route-server feed。默认路由、白名单外路由或 RPKI 拒绝的路由可以作为观察数据保留，但不会因此获准导出或转发。观察内容仍可能含敏感路由策略，当前仅提供设备本机诊断，不自动公开成员 Looking Glass。

BIRD 2/3 观察模式同时启用 `import table yes` 和 `import keep filtered yes`。前者保留导入过滤前的 Adj-RIB-In，后者保留过滤状态；`receive limit N action disable` 将保留的拒绝路由也计入数量上限，达到上限时关闭协议，需操作员处理。普通 `import limit` 只计算接受的路由，不足以单独限制观察存储。FRR 使用 `soft-reconfiguration inbound` 和 `maximum-prefix N force`，`force` 将过滤路由计入上限，超限关闭会话。`authorized-only` 若启用 RPKI 也需要预策略缓存，因此同样启用这些包含过滤路由的限额机制；没有 RPKI 时只使用接受路由的限额。数量限制不是精确的内存字节配额；观察模式保留额外副本，须根据主机容量设置每个会话的 MaxPrefixes。关闭或重启会话可能清空观察数据，这不是永久 MRT/BMP 归档服务。

本机只读诊断示例（替换为生成文件中的协议名及实际邻居）：

```sh
# BIRD：授权视图、被过滤视图、导入过滤前视图
birdc 'show route protocol peer_HASH all'
birdc 'show route filtered protocol peer_HASH all'
birdc 'show route import table peer_HASH.ipv4 all'

# FRR：授权与导入过滤前视图；view 名固定为 MOEDOVE
vtysh -d bgpd -c 'show bgp view MOEDOVE ipv4 unicast neighbors 192.0.2.2 routes json'
vtysh -d bgpd -c 'show bgp view MOEDOVE ipv4 unicast neighbors 192.0.2.2 received-routes json'
```

旧 `resources/views/api/v4/router/collector/bird2*/neighbors.foil.php` 接受路由并添加诊断 Large Community；本实现使用分开的授权/过滤/预策略视图，未逐项复制旧的 bogon、transit、next-hop 和 Large Community 原因标签，也未实现这些数据的 Web Looking Glass。不能把本次角色支持描述成全部旧收集器诊断功能已经迁移。

Junos 和 RouterOS 的当前适配器明确拒绝收集器角色。现有 Junos 模板使用默认路由实例，单独拒绝 BGP 导出不足以证明不安装转发表；RouterOS 虽有 `fib=no` 表，尚未验证收集器的过滤留存与安全更新契约。`Capabilities.roles` 和 `collectorPolicies` 暴露准确支持范围，不会默默生成路由服务器配置。

测试：`TestCollector*` 覆盖旧默认 SHA 不变、无导出、空观察白名单、授权/RPKI/密钥保留、代理拒绝安全机制缺失，以及 server→collector 暂存与回退。`TestNativeCollectorProfiles` 在 BIRD 2.17.5 / FRR 10.3 对双栈、两种策略、空白名单与 RPKI 组合进行离线原生解析；`TestNativeCollectorFRRReloadPlan` 只运行 `frr-reload.py --test --input`，不连接运行中的守护进程。原生解析和官方命令语义不替代设备实测或大规模路由/内存负载测试。

参考：[BIRD 2.17.1 channel limits、filtered routes 与 import table](https://bird.nic.cz/doc/bird-2.17.1.html)、[FRR 10.3 views、maximum-prefix force、received-routes](https://docs.frrouting.org/en/stable-10.3/bgp.html)。
