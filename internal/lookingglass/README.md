# 受限路由详情查询

Looking Glass 采用控制台请求队列与本机采集器分离的方式。控制台只提交结构化目标和视图；本机操作员明确启用 worker 后，采集器才领取短期 lease、运行固定只读命令并回传有界 JSON。没有远程 shell、自由 CLI、任意表名或配置文件读取接口，不读取 running-config，也不上传其普通散列或原始 stdout/stderr。

## 支持范围

| 平台 | accepted | filtered | pre-policy | exported | table |
|---|---|---|---|---|---|
| BIRD 2.17.5 / BIRD 3.4.0 | 指定实际协议的路由 | 必须从原生统计证明保留 filtered | 必须存在指定协议/地址族的 import table | 原生 export 视图 | 本机明确允许，固定 master4/master6，核对 router-id 和 Local AS |
| FRR 10.3 | 指定邻居；可按精确前缀读取详细路径 | 原生 filtered-routes，需 soft-reconfiguration | 原始 received-routes，需 soft-reconfiguration | 原生 advertised-routes | 本机明确允许，固定 MOEDOVE，核对实际 view/router-id/AS |
| OpenBGPD 8.8 | 固定 MOEDOVE 中指定邻居 | 不支持：原生状态不能证明 filtered Loc-RIB 留存开关 | 原生 Adj-RIB-In | 原生 Adj-RIB-Out | 不支持：该 JSON 接口不足以独立证明本机 router-id/AS |

Junos、RouterOS 等平台没有此适配器。能力不存在、缓存未启用、身份未知、格式改变、超时或输出过大都会明确失败，不会变成“0 条路由”。BIRD filtered 的可用性来自 `show protocols all` 当前 channel 的 `N filtered` 统计列；不是把控制台期望配置当成运行事实。FRR 的实际返回必须属于 `MOEDOVE`，不会默默读默认 VRF。

每次请求在查询前后分别核对实际邻居 IP/ASN。BIRD/FRR 还核对 lease 固定的本机 router-id/ASN；OpenBGPD 只声明已经证明的邻居身份，不补造本机字段。任一身份或相关运行上下文改变都会丢弃路由结果。本机映射与套接字仍须由操作员正确配置，原生元组不是设备的密码学身份。

`complete` 仅表示原生命令完成且行数未截断，**不代表实时 RIB 的原子快照**；路由可以在扫描期间正常变化。`exported` 表示守护进程的出站/导出视图，不证明远端已经收到或接受更新。控制台在结果读取时再次执行归属版本检查，并显示采样时间与过期状态。

## 数据与边界

查询固定 `routerId`、`peerId`、`platform`、实际邻居地址/ASN、地址族、所有权版本；视图只有 `accepted`、`filtered`、`pre-policy`、`exported`、`table`。可选 `prefix` 必须是该地址族的规范 CIDR，按精确前缀筛选。表级请求没有 peer，只允许员工在控制台发起，并且本机 `lookingGlass.allowTable` 必须显式为 true。

返回仅包含 prefix、数值 IP next-hop、结构化 AS path、标准/large communities 和已知的 filtered 状态。AS_SET、AS_CONFED_SEQUENCE 和 AS_CONFED_SET 分别保留为独立 segment，不能伪装普通 sequence。`filtered: null` 或省略表示未知，不能从未验证的 Large Community 声称一个权威拒绝原因。旧 `FilteredPrefixes` 的 1101 原因标签仅作为原始数值 large community 保留；此版本不重建全部旧诊断原因。

FRR summary 命令没有完整 community 属性，因此对应字段为 null，并在 `unavailableAttributes` 明确标记，不能看作“已知没有 community”。accepted 提供精确 prefix 时可以取得详细路径并只保留目标邻居的路径。FRR 的 received-routes detail 路径会经过当前入站 modifier，因此 pre-policy 特意使用非 detail 的原始字段，避免把修改后的属性当成原始收到的数据。暂不返回 extended communities、每路由年龄或任意动态属性。

默认最多 50 行，允许 1–200 行。一个原生响应最多 4 MiB，一条路由最多 512 个 ASN、64 个 path segments、256 个标准和256个 large communities。命令最多运行10秒，一次查询最多25秒；超过这些界限会失败，行数超过 maxRows 则明确 `truncated=true` / `complete=false`。大 RIB 应指定精确 prefix，不能把输出字节超限当成完整空表。

## 本机 worker

配置由主机管理员管理；可执行程序和 socket 路径只能来自本地文件。lease 不能覆盖这些值，也不能选择 native protocol 名。实际 peer ID 到 BIRD protocol / FRR、OpenBGPD 邻居 IP 的映射必须显式登记。

```json
{
  "endpoint": "https://ixp.example.net/api/monitoring/ingest",
  "routerId": "router-record-id",
  "platform": "bird3",
  "birdc": "/opt/bird3/sbin/birdc",
  "birdSocket": "/run/bird3/bird.ctl",
  "ports": {},
  "peers": {"peer-record-id": "peer_actual_protocol_name"},
  "lookingGlass": {"allowTable": false}
}
```

```sh
# 专用 worker：只需要绑定该 RouterID 的 looking-glass:run token
ixp-collector --config /etc/moedove/collector.json \
  --token-file /etc/moedove/looking-glass.token --looking-glass-only --interval 5s

# 与已有 telemetry 合并：token 同时需要 monitoring:write 和 looking-glass:run
ixp-collector --config /etc/moedove/collector.json \
  --token-file /etc/moedove/collector.token --looking-glass --interval 30s
```

未传这两个选项时，不会领取 Looking Glass 请求。`--once` 在专用模式只处理一次领取。token 仅经私密普通文件或 stdin 提供，不接受 argv/URL 中的 token，不跟随 HTTPS 重定向。示例 endpoint 复用现有控制面地址；专用 Looking Glass 模式只调用 `/api/looking-glass/claim` 和 `/complete`，不调用 monitoring ingest。

FRR 可设置本地 `vtysh`、`vtySocket`；Looking Glass 固定查询 MOEDOVE。常规 telemetry 为兼容旧配置仍默认普通 summary，使用本项目 managed view 时设置 `frrView: "MOEDOVE"`。FRR 客户端必须具备已有的非交互身份及 socket 权限；若 PAM 要求密码或出现诊断输出，worker 会失败，不会把空响应当成成功。测试的私密 mock 客户端认证配置不是生产认证部署范例。

OpenBGPD 使用本地 `bgpctl`、`bgpSocket`，默认 socket 为 `/run/moedove-openbgpd/bgpd.sock`，peer 映射值为 IP。BIRD3 也已接入真实会话采集，必须读取到 major3 的原生标识。OpenBGPD 会话采集读取实际 remote_addr、remote_as、state 和可用的 prefixes 统计；缺计数时保持未知，不补造零。

## HTTP 合同与验证

worker POST `/api/looking-glass/claim` `{routerId}`，获得 `{lease: {id,token,expiresAt,query}}` 或 `{lease:null}`。执行后 POST `/complete` `{leaseId,leaseToken,result}`；要求 `{ok:true,duplicate?:boolean}` 应答。Query 原样回显且必须匹配 lease；结果失败也有非零尝试时间，但不包含路由。错误码为固定枚举，没有原始诊断文本。API 端负责 token 的 RouterIDs 绑定、lease 生命周期、当前归属版本、结果加密持久化及成员访问权限。

测试覆盖 typed 格式、AS_SET 保真、行数截断、输出超限、未知格式、身份变更、保留能力缺失、HTTPS lease 回传及重定向保护。真正 BIRD2/3 客户端连接私密 mock Unix socket 验证五个视图；真正 FRR10.3 vtysh 连接 mock VTY 验证 JSON 读路径；OpenBGPD8.8 验证固定命令参数语法，JSON fixture 对照官方源码。没有启动真实 daemon、建立真实 BGP 会话或修改路由/内核状态。

参考：[BIRD 3 控制接口与 filtered/import table](https://bird.nic.cz/doc/bird-3.4.0.html)、[BIRD 2 filtered 统计源码](https://github.com/CZ-NIC/bird/blob/v2.17.5/nest/proto.c)、[FRR 10.3 BGP 查询](https://docs.frrouting.org/en/stable-10.3/bgp.html)、[FRR 原生 Adj-RIB 查询实现](https://github.com/FRRouting/frr/blob/frr-10.3/bgpd/bgp_route.c)、[OpenBGPD bgpctl](https://man.openbsd.org/bgpctl)。
