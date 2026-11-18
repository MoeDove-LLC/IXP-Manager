# RIPE Atlas

本适配器迁移旧版 `AtlasRun → AtlasMeasurement → AtlasResult` 工作流，保留同 VLAN、同地址族成员间的有向互测矩阵，也支持单个 ping/traceroute 提案。它不会随机更换来源探针或目的地址。每个矩阵方向都是独立的、可恢复的子测量；多路径、丢包和未知结果保留原意，不推断“对称”“健康”或 0ms。

本轮只使用本地 fake provider 和 fixture。没有读取真实 `.env`/Atlas key，也没有创建、停止或查询真实外部测量。启用后的生产客户端包含实际 create/read/stop API，不是只读占位实现。

## 本地配置

省略 `ixp-manager --atlas-config` 时禁用外部 Atlas 操作。启用时传入私有常规 JSON 文件（0600、不接受符号链接/FIFO、最多 64 KiB）：

```json
{
  "enabled": true,
  "baseURL": "https://atlas.ripe.net",
  "apiKey": "REPLACE-WITH-YOUR-PRIVATE-ATLAS-UUID"
}
```

生产 origin 固定为 `https://atlas.ripe.net`，路径由代码固定生成，拒绝重定向和用户提供 URL。API key 只通过 `Authorization: Key …` 发送，不放在 query、状态、审计或错误响应里，遵循 [官方认证格式](https://atlas.ripe.net/docs/apis/rest-api-manual/authentication/api-keys/)。配置文件不进入业务 JSON 导出；其路径和 key 不通过 API 返回。

测试可明确设置 `allowLoopbackHTTP:true`，且 baseURL 必须是带端口的 literal loopback origin，例如 `http://127.0.0.1:18080`；不接受 `localhost`、非 loopback 地址或任意 HTTPS 替代站点。不要在 baseURL 后追加 `/api/v2`。测试 key 也须为 16–256 个受限 ASCII 字符。

## 有界提案与审核

探针发现按选定成员当前 ASN、IPv4/IPv6、connected/public 和可选 anchor 筛选；只取一页最多 200 条，不跟随 provider 返回的 `next` URL，未完整列出时标记 `truncated`。缓存一小时后不能用于新的创建。探针 API 的 [ASN/status/public 字段](https://atlas.ripe.net/docs/apis/rest-api-reference/probes/probes_list/) 是来源证据；connected 不保证探针一定接受测量。

Single 模式明确来源成员及 probe IDs，目的为另一成员的指定公共 Atlas probe 地址，或该成员当前 active connection 的地址。Matrix 模式选择同 VLAN/AF 的 2–10 个 active 成员，每个成员明确选择来源 probe IDs 与一个目的 probe，产生 `N*(N-1)` 个方向。来源和目的、成员 ASN、地址族及当前 VLAN 关系在审批、执行前重新核对；地址被冻结，不自动追随之后的 DNS 或探针地址变化。

每个定义只允许 ping 或 ICMP traceroute、固定 3 包和 64 字节、traceroute 最多 32 hops；不允许 HTTP/DNS 扩展、任意端口、bill-to 或 provider 模板。使用 [明确的 probe ID 列表](https://atlas.ripe.net/docs/apis/rest-api-manual/measurements/creating-measurements/probe-selection/)；没有 ASN 随机挑选或自动补充探针。

| 限制 | 值 |
| --- | --- |
| 每个方向来源探针 | 1–20 |
| 周期测量间隔 | 300–3600 秒 |
| 周期持续时间 | 至少一个间隔，最多 86400 秒 |
| 计划开始时间 | 现在或未来 7 天内 |
| 每个 run 子测量 | 最多 90 |
| 整个 run 预计结果/credits 上界 | 1000 / 20000 |
| 已尝试 active/uncertain 子测量 | 最多 100 |
| 本地创建额度预留 | 保守 24–25 小时窗口最多 100000 |
| 审批可开始执行窗口 | 15 分钟 |

成本依据 [RIPE Atlas 官方计算规则](https://atlas.ripe.net/docs/getting-started/credits/)：固定参数下周期 ping/traceroute 每个结果为 3/30 credits，one-off 加倍。周期上界使用 `probeCount * (ceil(duration/interval)+1)`，矩阵累计所有方向。本地预留从创建意图时开始，包含不确定或失败请求，使用独立小时桶；prune/delete 历史不会消除预留。时钟后退导致无法安全容纳新的桶时，在任何外部 POST 前拒绝。

这些值是保守估算与本地准入限制，不是实际扣费、Atlas 账户余额或 provider 执行日账单上限。实际收到结果、探针可用性、计价变更和 provider quota 仍由 RIPE Atlas 决定。

默认显式发送 `is_public:false`。官方 API [支持私有 measurement](https://atlas.ripe.net/docs/apis/rest-api-manual/measurements/creating-measurements/definitions/)；不会在拒绝后偷偷改为 public。只有管理员明确审阅公开范围并确认，才发送 `true`。已创建测量返回与所审私有性/时间配置不符时，暂停剩余工作；若已由创建意图证明属于本实例，排队停止，而不把不符合的结果当作成功。

## 执行与恢复

状态大致为 `draft → approved → creating → scheduled/running → completed/stopped`，异常为 `uncertain`、`failed`，矩阵混合进度可显示 `partial`。审批和执行分离：管理员浏览器确认准确 checksum、总 credits、公开性，并用当前密码及已启用的 MFA 重认证。随后 execute 授权按项执行；后台每次最多推进一个子项。

每次创建先写入独立 intent、不可猜测的 instance/run/nonce 描述、实际开始/停止时间及额度预留，再调用 [POST measurements](https://atlas.ripe.net/docs/apis/rest-api-reference/measurements/measurements_create/)。只有标准 201 且一个明确 ID 才是有效回执。超时、读取失败、坏 JSON、非预期 2xx/3xx、408 或 5xx 都可能已经执行，进入 uncertain；不会自动重发收费 POST。

创建回执和后续观察以加密快照绑定本地 run/child、provider ID、intent 和完整 spec。响应无法持久化或进程中断时，既有 creating 在启动/下一次工作前转 uncertain 并暂停余项。恢复只读查询固定 origin 的已知 ID，或按不可猜测描述查询 [当前账户 measurements/my](https://atlas.ripe.net/docs/apis/rest-api-reference/measurements/measurements_my_list/)；必须唯一匹配目标、类型、AF 和原意图，才能建立本实例所有权。不能通过 API 手填一个 provider ID 接管外部测量。

`resume` 是显式继续剩余且从未尝试的 approved 项；不会重发 creating/uncertain/failed 项。停止只对已验证属于当前实例的 child 使用 [DELETE measurement](https://atlas.ripe.net/docs/apis/rest-api-reference/measurements/measurements_destroy/)，不停止共享 Atlas 账户中其他系统创建的测量。204 仅表示停止请求已确认；继续读取 provider 状态，不能立即假装已停止。

若执行较慢导致十五分钟审批窗口过期，管理员可对同一冻结 checksum 重新认证，并再次确认剩余 credits 和公开范围，续批仅未尝试余项。已尝试项的状态、intent 与回执保持不变；续批清除执行授权，返回 `resumeRequired:true`，随后必须显式 `/resume`，不会自动创建或通过 `/execute` 绕过。creating/uncertain 必须先核对，stop/pruned 不可续批；原明确 `startAt` 已过时必须新提案，不能修改原计划时间。DTO 的 `remainingEstimatedCredits/remainingEstimatedSamples` 是余项上界，`approvalExpired` 表示需续批，`canReapprove` 也可在新鲜余项上为 true，表示可选的再次授权。

请求等待 Atlas 串行锁后、授权 mutation 事务内，以及探针发现外部读取前都重新核对当前角色、安全代次、有效期及浏览器会话是否仍存在；密码重置或撤会话期间迟到的发现不能替换缓存。已经持久授权并实际发生的创建仍保存回执，不能因随后 logout 丢失付费操作的证据。接受 `/resume` 后会准确清除 `resumeRequired`；已有审批/执行任务不因普通 logout 自动撤回。

矩阵各方向的首次停止不等待普通同步周期；已经尝试的异步停止按一分钟退避，避免重复发送 DELETE。整个 run 从未尝试创建时，stop 是纯本地取消，provider disabled 时也可取消并清理；存在 creating/uncertain/owned 项时仍需原 provider 身份和所有权证据。

周期请求总是携带 provider `stop_time`；one-off 按 [官方要求](https://atlas.ripe.net/docs/apis/rest-api-manual/measurements/creating-measurements/global-fields/) 不携带 stop_time。后台还按本地有界计划停止已验证的超期子测量。应用关闭时序列化 Atlas 操作，关闭后的旧实例不得继续外部调用或修改替代实例文件。

## HTTP 合同

管理员/操作员/查看者可以读取。管理员和操作员浏览器可发现、提案和安全刷新；审批、执行、resume、stop、保留期清理仅管理员浏览器。Member 无此 staff 工作流访问；API token 仅支持 `atlas:read`，不能创建、批准或停止。

| 路由 | 用途 |
| --- | --- |
| `GET /api/atlas` | enabled、limits、probeSnapshots 与 metadata-only runs |
| `POST /api/atlas/probes/discover` | `{memberId,af,anchorOnly?}`，保存有界探针快照 |
| `GET /api/atlas/probes?memberId=&af=` | `{snapshot,items,stale}` |
| `DELETE /api/atlas/probes/{snapshotId}` | `{checksum,reason,acknowledge:true}` 清缓存；不停止已创建测量 |
| `POST /api/atlas/runs` | 创建 typed single/matrix draft，返回服务端预算上界 |
| `GET /api/atlas/runs/{id}` | 固定 spec、checksum 和全部子项 metadata |
| `POST /api/atlas/runs/{id}/approve` | `{checksum,acknowledgeCredits:true,acknowledgePublic,currentPassword,code?}` |
| `POST /api/atlas/runs/{id}/execute` | `{checksum}`，首次授权执行，202 返回真实进度 |
| `POST /api/atlas/runs/{id}/resume` | `{checksum}`，只继续未尝试项 |
| `POST /api/atlas/runs/{id}/stop` | `{checksum}`，取消未发项并停止已证明的所有权项 |
| `POST /api/atlas/runs/{id}/refresh` | `{}`，每次只读推进一个方向，不创建或停止 |
| `GET /api/atlas/runs/{id}/results?measurementId=` | 可选本地 child ID；typed results、方向摘要和快照 metadata |
| `POST /api/atlas/runs/{id}/prune` | 仅所有子项终态；`{checksum,reason,acknowledge:true}` |
| `DELETE /api/atlas/runs/{id}` | 仅已 prune 的终态 metadata，同样明确确认与审计 |

Single body 字段见 `app.AtlasProposal`：`name,sourceMemberId,targetMemberId,targetType,targetId,type,af,probeIds,isOneoff,intervalSeconds?,durationSeconds?,startAt?,isPublic?`。targetType 为 `probe` 时 targetId 是十进制 probe ID 字符串，`connection` 时为当前资源 ID。

Matrix 使用 `mode:"matrix",vlanId,participants:[{memberId,probeIds,targetProbeId}]` 及共同的测量参数，不携带 single 字段。顶层 detail 有 `measurementCount,remainingCount,uncertainCount,canResume,stopRequested`；每个 item 的 `id` 是本地 child ID，`providerId` 是已收到的 provider 编号，`ownershipConfirmed` 独立表达能否安全停止。

时间字段统一返回 RFC3339；结果 RTT 缺失为 absent/null，不变成零。丢包保留 sent/received，traceroute 每 hop 保留全部有界 replies/timeout。矩阵方向摘要中的 `currentVLANAddressesSeen` 只表示在实际回复地址中匹配到当前登记 VLAN 地址，不能证明全部路径经过该 IXP、更不能证明对称性。

Provider 已终止后，若结果数仍低于审阅的保守样本上界，会在计划结束后的一小时宽限期内继续按分钟读取晚到结果；不能保证上界所含样本全部到齐。宽限期后停止自动读取，仍可手动刷新受约束的结果窗口；不会重新 POST 测量或把缺失样本填零。

探针初始化/解析错误可以缺少实际 `dst_addr`。此时只保留绑定 measurement/probe/AF/type/time 的明确 `probe-error`，`targetAddress` 为空，不能回填计划目标。依据 [官方结果格式](https://atlas.ripe.net/docs/apis/measurement-result-format/version-5000/) 与 [RIPE NCC ping 实现](https://github.com/RIPE-NCC/ripe-atlas-software-probe/blob/master/probe-busybox/eperd/ping.c)、[traceroute 实现](https://github.com/RIPE-NCC/ripe-atlas-software-probe/blob/master/probe-busybox/eperd/traceroute.c)，错误可位于 result 项或 hop，而不一定是顶层字段。ICMP error reply 保留地址与错误标志，其 RTT 不展示为成功 RTT；timeout 与成功回复相矛盾的输入会被拒绝。原始错误时序仍保存在加密快照中。

## 存储与备份

`State.Atlas` 只保存计划、进度、小型预算桶和文件 metadata。原始响应、typed probe/result 数组及所有权回执存于 `dataDir/atlas/<opaque-id>.enc`，复用 AES-GCM、0600、原子发布与 fsync，AAD 为 `atlas:<id>:<plain-checksum>`。目录限额 128 MiB；响应最多 8 MiB、结果最多 1000 条。刷新替换当前快照，旧不再引用的文件安全清理；terminal prune 和独立 probe-cache 删除提供配额释放入口。

`ValidateAtlasArtifacts` 在启动和离线备份校验时核对密文、内容摘要以及回执/观察/结果的身份绑定；缺文件、错误 security.key 或篡改 provider ID 都失败。备份必须同时保留 State、security.key 和 `AtlasArtifactIDs` 列出的所有文件。独立 API key 配置另按私密运维流程保存，恢复时默认 disabled，不能拿旧的任意 Atlas measurement ID 冒充本实例创建证据。

Uncertain 项不可静默删掉；需要先只读核对创建是否发生，或在 provider 侧进行人工处置。没有“重试所有失败创建”或“停止账户所有测量”的快捷路径。

## 验证

`go test -race ./internal/atlas ./internal/app -run '^TestAtlas'` 使用本地 HTTP fixture 与 fake provider。覆盖 HTTPS/key-header/无redirect、实际 POST/DELETE 参数、默认私有和明确probe IDs、2xx/408创建不确定性、single/矩阵成本、原子创建意图、只读reconcile、重启/Close代次、并发执行、审批/CSRF/token权限、member ASN变更、receipt重绑定拒绝、错误public策略停止、加密快照、保留期与预算窗口。

本轮没有把 fixture 成功标记为真实 Atlas 测量已执行，也没有扣用真实 credits。
