# PeeringDB 只读资料同步与人工审核

默认关闭外部抓取。启用时，以 `0600` 私密 JSON 配置指定受信任的来源，并传给 `ixp-manager --provider-config /etc/moedove/provider.json`：

```json
{
  "enabled": true,
  "baseURL": "https://www.peeringdb.com/api",
  "allowedOrigins": ["https://www.peeringdb.com"],
  "requestIntervalSeconds": 30
}
```

适配器仅调用 `GET net`、`GET netixlan`、`GET ix/{id}` 和 `GET fac/{id}`，不使用 OAuth、API Key、外部写入或网络扫描。ASN 来自本地成员记录，调用者不能通过 API 提供另一 ASN 或任意 URL。每次抓取最多四个固定请求，使用证书校验的 HTTPS，不跟随重定向；单请求超时十秒，总抓取超时三十秒。每份响应上限 8 MiB、每类最多 1000 行。

默认允许官方 PeeringDB origins；自建兼容镜像必须在本机配置 allowlist 中明确列出。只有 `allowLoopbackHTTP: true` 且 baseURL 使用字面 loopback IP 时，才允许用于隔离测试的 HTTP。配置文件不经管理 API 修改。

数据类型保留 ASN、网络名称/网站/IRR 集、netixlan 地址和声明容量、交换中心与机房参考数据。支持当前公开 netixlan 状态 `ok` 与 `not-operational`，不把后者误判为已删除；可空的建议前缀数量保持 null。设施参考数据本身不证明该网络在该机房有接入关系。

## 同步工具

```sh
ixp-sync --provider-config /etc/moedove/provider.json \
  --asn 64501 --ix 301 --facility 501 --out ./network.json

ixp-sync --provider-config /etc/moedove/provider.json \
  --asn 64501 --out ./network-next.json \
  --submit https://ixp.example.net/api/integrations/proposals/import \
  --member-id MEMBER_ID --connection-id CONNECTION_ID \
  --token-file /etc/moedove/integration.token
```

工具生成私密 JSON 和 `.sha256`，拒绝覆盖已有文件。可选提交只能创建审核提案，不能应用变更；令牌必须具备 `integrations:write`，通过 `0600` 文件或 stdin 提供。管理端上传使用 HTTPS 并拒绝重定向，不把凭据放在命令参数、日志或 URL 中。

离线导入接受 `{memberId, connectionId?, document, checksum}`，其中 `document` 是原始 UTF-8 文件文本，`checksum` 是这些原始字节的 SHA-256。API 文本上限 6 MiB（另受全局 JSON 请求大小限制）。导入只校验结构和内容摘要，摘要不证明来源，界面明确标记 `offline-import`；来源直取提案保存实际配置的 providerOrigin。提案源码文件位于 `integrations/snapshots/<proposalID>.json`，总容量 128 MiB、最多 2000 条提案，备份须包含所有引用文件。

## 审核和应用

提案绑定本地成员 ASN、可选连接归属、VLAN/交换网络及本地基准摘要。提案摘要同时绑定来源、目标、完整字段 diff 和不可变文件摘要。列表按最近记录分页，只返回元数据和 diff；详情按需读取单份快照，避免把大型原始数据全部加载到列表。

管理员在浏览器中选择具体字段并提交两个审核摘要后才能应用；API Token 只能抓取/导入提案。允许字段是成员名称、网站、PeeringDB network ID，以及通过本地地址唯一匹配的 netixlan 外部关联 ID。已有交换网络配置了 PeeringDB IX ID 时，还必须匹配该 IX，防止跨交换网络误关联。

ASN、IPv4/IPv6 分配、物理/LAG 容量和 IRR 策略差异明确列为受保护项，不从第三方快照覆盖。它们应在各自的本地资源或路由策略流程中显式修改。未知速度显示为空，不等同于零容量。

本地基准变化、文件被修改、来源资料超过七天或选中了受保护字段，应用均返回冲突；必须重新生成并审核提案。已应用记录及历史快照仍可查看，不因时间经过而阻止启动。没有可应用差异或决定保留本地时，管理员可使用 dismiss 操作关闭提案，保留原快照与 diff。

`ValidateExternalArtifacts` 可在离线恢复前验证引用文件、摘要和 ASN/时间绑定。快照创建与引用元数据在控制存储锁内协调；网络请求不占用该锁。不会产生静默的本地地址分配或路由下发。

参考：[PeeringDB API 规范](https://docs.peeringdb.com/api_specs/)、[官方数据序列化定义](https://github.com/peeringdb/peeringdb/blob/master/src/peeringdb_server/serializers.py)。本次测试只使用内存 HTTP fixtures，没有向真实 PeeringDB 发起数据请求。


## 配额回收

已应用或已关闭的提案可以由浏览器管理员显式执行 `POST /api/integrations/proposals/{id}/prune`，提交原审核 checksum 与 acknowledge:true。操作保留元数据、diff 和所有摘要，只清理原始文件；详情返回 snapshotUnavailable:true。需要详细原始数据时，应先导出或完成整目录备份。pending 提案不能清理。

清理意图先持久化，再删除匹配摘要的文件；崩溃后的下次启动会继续已获授权的删除，离线验证本身不修改文件。snapshotPruned 标记允许恢复/启动时不要求原始文件存在。

如需释放 2000 条记录数量配额，管理员还可对已经清理的终态提案执行 DELETE，提交 checksum、acknowledge:true 和原因。完整审核元数据、diff、原始摘要与应用字段会转存到审计详情，随后移除提案行；不会删除 pending 提案，也不会再次修改业务资源。这是明确的保留期操作，不替代原始快照归档。
