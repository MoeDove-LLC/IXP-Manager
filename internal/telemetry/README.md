# 实测流量与 BGP 会话

采样与控制面分离：每次观测写入 `telemetry/journal.bin`，使用长度和 SHA-256 帧并 fsync 后应答。启动时丢弃未完成的最后一帧；已完成记录校验失败则停止启动，不把损坏数据当成有效观测。每 128 个批次进行快照压缩，先原子写入并同步 `snapshot.json`，再截断日志；重启会跳过快照已经包含的日志前缀。

保留窗口为 24 小时，每个实体最多 2880 个样本，总计最多 100000 个样本、4096 个实体；规模较大时会优先移除较老的数据，因此实际历史可能少于 24 小时。备份须在服务停止后同时复制 `snapshot.json`（若存在）与 `journal.bin`，不要复制运行时 `.lock` 或临时 `.snapshot-*`。

计数器以十进制字符串接收与返回，按 uint64 计算差值后再转换为 bits/s，避免 JavaScript 的 2^53 精度边界。第一个点、计数重置、Linux boot/ifindex 改变、所有者变化及超过 120 秒的采样间隔不产生速率。32 位计数器仅在端口速度与时间间隔证明至多一次回绕时提供速率，否则留空。计数器下降只有在边界附近才识别为单次回绕，其余当作重置。缺失和过期数据分别标记 unknown/stale；过期汇总速率为空，不生成假的零流量。

采集时间必须单调前进至少 1 秒，并处于服务器时间之前 10 分钟至之后 30 秒内。相同时间和内容的重试幂等；相同时间的不同内容被拒绝。每批最多 2048 个端口/会话观测。

## 本机采集器

在控制台创建独立 API Token，仅勾选 `monitoring:write` 并绑定允许采集的路由器。Token 以私密文件或 stdin 传入，不放在参数或 URL 中。配置示例（ID 必须换成控制台中的实际 ID）：

```json
{
  "endpoint": "https://ixp.example.net/api/monitoring/ingest",
  "routerId": "router-record-id",
  "platform": "bird2",
  "birdc": "/usr/sbin/birdc",
  "birdSocket": "/run/bird/bird.ctl",
  "ports": { "port-record-id": "eth0" },
  "peers": { "peer-record-id": "peer_bird_protocol_name" }
}
```

```sh
ixp-collector --config /etc/moedove/collector.json \
  --token-file /etc/moedove/collector.token --once

ixp-collector --config /etc/moedove/collector.json \
  --token-file /etc/moedove/collector.token --interval 30s
```

Token 文件必须为 `0600` 普通文件；采样间隔允许 5–60 秒。配置文件由主机管理员管理，其中自定义程序必须是本机绝对路径。

- BIRD 2 / BIRD 3：分别选择 `bird2` / `bird3`，按实际主版本核对原生输出；固定调用 `birdc [-s socket] show protocols all`；映射值为真实协议名。MoeDove 生成的名称为 `peer_` 加 peer ID 的 SHA-256 前 20 位。
- FRR：`platform` 设置为 `frr`，可设置 `vtysh` 绝对路径，固定调用 `vtysh -c 'show bgp summary json'`；映射值为邻居 IP。
- OpenBGPD：选择 `openbgpd`，本机可设置 `bgpctl` / `bgpSocket`；固定 `bgpctl -j show neighbor` JSON，默认私密socket为 `/run/moedove-openbgpd/bgpd.sock`，映射值为邻居IP。
- FRR managed view：设置 `frrView: "MOEDOVE"` 才查询该固定 view；未设置保持旧普通 summary，另可设置 `vtySocket`。
- 仅流量：`platform` 设置为 `none`，`peers` 为空。
- 端口：从映射接口的 `/sys/class/net/<iface>/statistics/{rx_bytes,tx_bytes}`、`operstate`、`ifindex` 读取；boot ID 加 ifindex 标记计数器世代。不会自动枚举主机全部接口。

BGP 的 `receivedPrefixes` 是当前导入/接收的路由数量，不是累计 UPDATE 消息数量；BIRD 使用各 channel 的 imported 数量汇总。找不到映射会话时报告 unknown；本机命令或计数器读取失败则跳过该次采集，保留上次观测直至标记 stale。当前无 Junos/RouterOS 采集适配，不以配置中的 enabled 伪造 Established。

仅向 HTTPS 上传，使用系统信任根且不跟随重定向。没有远程 shell、任意网络探测或实际配置写入。服务端检查 Token 的路由器绑定、peer 所属关系，以及端口是否通过连接接入该路由器 VLAN。

## 成员隔离与接口

`GET /api/monitoring/summary` 返回所有可见端口和对等体，包括尚无样本的 unknown 实体。`GET /api/monitoring/series?portId=...&from=...&to=...` 返回时间序列；默认最近一小时。`POST /api/monitoring/ingest` 只接受专用采集 Token，不接受浏览器 Cookie 写入。

样本所有者由服务端根据当前连接和 peer 关系写入，忽略采集器自报的 memberId。成员只能查看当前选中会员拥有的端口和 peer，历史点同时按采样时所有者过滤；端口重新分配后不暴露前一成员的历史。成员响应不返回物理接口生命周期累计 octet 计数，只返回属于本成员采样期的速率和状态。员工可查看完整原始计数器。

测试覆盖大整数速率、重置/回绕、采样间隔、崩溃尾部恢复、压缩重放、保留界限、BIRD/FRR 格式样例、内核文件样例、上传重定向保护及 API 租户隔离。本次验证未访问真实路由器。

参考：[BIRD 2 控制接口](https://bird.nic.cz/doc/bird-2.17.1.html)、[FRR BGP summary](https://docs.frrouting.org/en/latest/bgp.html#clicmd-show-ip-bgp-all-summary-wide-json)。


BGP 观测还必须携带从守护进程读取的 `neighborAddress` 与 `neighborAsn`。BIRD 读取 Neighbor address/AS，FRR 读取邻居键与 remoteAs；不能用期望配置补齐缺失身份。未知或缺失映射可以不带身份，但不能声明已建立。服务端在当前所有权版本之外再检查邻居元组；不匹配时写入 unknown 观测清除旧的 Established，同时返回 identityMismatches。合法端口观测仍可接收；采集器会把身份不匹配回执作为需要检查部署/映射的错误。

新端口和会话样本都保留已验证的所有权版本。当前汇总和成员历史必须匹配当前版本，因此即使旧样本时间有允许范围内的未来偏差，快速 A→B→A 重分配也不能重新显示旧周期数据。员工仍可查看历史原始序列。缺少身份的旧会话日志可恢复，但当前汇总保持 unknown；不匹配的原始邻居地址/ASN 只允许员工诊断查看，成员不会看到另一邻居的身份或前缀数量。

自定义采集器也必须遵循“先取绑定、后采样、再上传”的顺序：每个周期先用同一受限 Token 调用 `GET /api/monitoring/bindings?routerId=...`，再读取真实接口/邻居状态，把返回的每项 `ownershipRevision` 原样放入对应样本后 POST ingest。不能先采样再取新绑定来包装旧数据。回执出现 skippedBeforeOwnership 时，丢弃旧批次并重新取绑定、重新采集；出现 identityMismatches 时检查真实运行配置与映射，不得复制期望地址/ASN伪装观测。`ixp-collector` 已执行这个顺序。

### Quagga 1.2.4 会话采集

使用 `platform: "quagga"` 和专用实例的绝对 `quaggaSocket`，例如
`/run/moedove-quagga/bgpd.vty`。采集器与 bgpd 使用同一 UID。`peers`
映射从管理端对等会话 ID 指向明确且互不重复的 IPv4 或 IPv6 邻居地址。
使用现有的路由器绑定 `monitoring:write` 令牌。

采集器在同一次绑定请求中取得管理端登记的 BGP Router ID 和本机 ASN，
核对 stock Quagga 1.2.4 的私密套接字、进程和原生单播汇总，再上传选择的
会话。状态和前缀数量以实际原生结果为准。管理端入库时再次核对本机身份，
并沿用现有的邻居地址、ASN 和成员归属限制。调整本机 Router ID、ASN 或平台
会使原采集结果失去当前会话绑定。Quagga 的 Looking Glass 路由视图仍待适配。


按需路由详情通过显式 `--looking-glass` 或 `--looking-glass-only` 启用，默认不轮询请求。token还需绑定RouterIDs的 `looking-glass:run` scope；严格视图、实际身份、截断/未知语义与本机配置见 [Looking Glass说明](../lookingglass/README.md)。
