# 路由诊断与 Looking Glass

管理服务不持有路由主机的控制权限，也不接收用户填写的命令。浏览器创建固定类型的查询，显式开启 Looking Glass 的本机采集器领取短期租约，执行有界只读命令，再提交类型化结果。没有采集器、没有对应原生视图、输出无法解释或超过限额时，界面会保留等待、失败、过期或不支持状态，不伪造空路由表。

查询视图按各平台的实际 `capabilities` 提供：接收后已允许路由、保留的过滤路由、策略处理前路由、发往指定邻居的路由，以及工作人员可用的固定受管表。表级查询不接受任意表名，采集器还需显式允许；缺独立 router-id/ASN 证据的平台不能执行表级查询。支持一个精确规范化前缀及最多 200 行，默认 50 行。

## 归属与结果含义

成员只能选择当前组织拥有且仍有活跃接入的启用 peer。目标选择器由专用安全投影提供，不依赖管理员资源列表。请求固定 router、peer、邻居地址/ASN 和归属修订；采集器在命令前后核对实际身份，服务器在接收结果及成员读取时再核对当前归属。重新分配或 A→B→A 的变化不会把旧结果重新归给成员。工作人员可以查看历史快照，但目标已经改变时不会标记为新鲜。

AS path 保留 sequence/set/confederation 的区别，不将 AS_SET 展平。标准/大型社区是原生属性，不被当作不可伪造的过滤原因。原生视图未提供的属性用未知标记表示；策略前视图没有确认过滤状态时，不会被涂成“已拒绝”。动态 RIB 的一次命令完成也不证明整张表在同一原子时刻冻结。

队列的 `complete` 表示请求执行结束，与结果的 `complete`、`truncated` 和新鲜度分别显示。结果只是一份采样，30 秒后转为过期；服务器接收时间也限制新鲜度，不能用客户端快时钟延长。没有路由不代表会话健康，存在路由也不代表实际转发或部署成功。

## 本机采集器

`ixp-collector --looking-glass` 在已有采样循环中显式启用诊断；`--looking-glass-only` 只处理诊断任务。令牌需要 `looking-glass:run`，且必须绑定允许的 `routerIds`。本地 JSON 决定受信任的可执行文件、socket 和 peer 映射；服务器租约不包含可执行程序、路径或自由命令。表级查询另需本地 `lookingGlass.allowTable=true`。

只有读请求的用户令牌使用 `looking-glass:read`；成员令牌仍固定创建时的组织。工作人员和成员均不能使用普通令牌冒充采集器。所有客户端请求使用固定 HTTPS origin、拒绝重定向，认证令牌由私密文件或 stdin 读取，不写入命令参数或结果。

## HTTP 流程

- `GET /api/looking-glass/capabilities` 返回安全 peer/router 选择器和平台视图。
- `POST /api/looking-glass/queries` 提交 `{peerId,view,prefix?,maxRows?}`；表级请求使用 `{routerId,view:"table",family}`。返回 `{item,cached}`。
- `GET /api/looking-glass/queries` 与 `GET .../queries/:id` 读取状态；列表有 `limit`、`before`、`nextCursor`。
- `GET .../queries/:id/result` 返回独立的 `query`、类型化 `result` 和 `freshness`。
- 采集器通过 `POST /claim` 领取租约，通过 `POST /complete` 返回租约令牌和完整类型化结果。

普通相同查询可复用 30 秒内结果；管理员浏览器可显式 `force:true` 重新采样，仍不会重复已在执行的相同请求。队列总等待期限为 2 分钟，单租约最长 60 秒；每个路由器只发一个活跃租约。每用户最多 4 个待处理请求、每路由器最多 8 个，每分钟每用户最多创建 20 个。过期租约可有界重发，但旧令牌、错目标、晚到或未来异常时间结果会被拒绝；重复提交同一已接收结果不会重复写入。

取消使用 `{expectedRevision}`。管理员清理终态快照使用 `{expectedRevision,acknowledge:true,reason?}`，保留审计和元数据；随后删除元数据还需非空原因。待处理任务不能直接清理。最多保留 2000 条请求，快照采用独立加密文件及容量上限，纳入完整备份。写入快照后若控制状态未能提交，会回收未引用文件，允许同一有效租约安全重试。

这些流程均不修改路由配置、不发送 ping/traceroute，也不把原始控制台输出或配置秘密写入页面。各平台支持的视图、完整本机配置及启动命令见 [采集器说明](../internal/lookingglass/README.md)。
