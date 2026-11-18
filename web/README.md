# MoeDove IXP 控制台

React + TypeScript + shadcn/ui（Radix、Tailwind CSS）。界面通过同源 `/api` 连接 Go 后端，不含模拟业务数据。

```sh
npm ci
npm run dev
```

开发服务器会将 `/api` 转发至 `127.0.0.1:8080`。生产构建使用 `npm run build`，Go 服务读取 `web/dist`。

## 浏览器集成验证

测试必须使用**空的独立测试数据目录**。它会创建管理员、成员、设备及路由配置，并验证编辑、关联删除保护、BIRD 原生校验与批准、JSON 导入导出和移动端导航。

1. 构建前端并以临时数据目录启动 Go 服务，监听 `127.0.0.1:18080`，设置 `IXP_SETUP_TOKEN=moedove-browser-test-token`，提供 BIRD 2 原生校验程序。
2. 安装 Chromium：`npx playwright install --with-deps chromium`。
3. 执行 `npm run test:e2e`。

另建一个空的测试数据目录并启动服务后，可执行 `npm run test:security-documents`，验证 TOTP 二维码绑定与登录、恢复码轮换、API 令牌权限与撤销、会话管理、密码修改，以及文档上传/替换/文本冲突、成员隔离与公开访问。两个测试套件均要求从空数据开始。

可使用 `TEST_URL`、`TEST_SETUP_TOKEN` 和 `CHROMIUM_PATH` 指向不同的测试服务、初始化令牌和本地浏览器。截图输出到 `/tmp/moedove-dashboard.png` 与 `/tmp/moedove-mobile.png`。测试不会向生产路由器应用配置。

## 界面约定

- 中文资源元数据位于 `src/lib/schema.ts`；服务端 `/api/schema` 校准必填项、枚举、引用及数值范围。
- 不同路由平台的生成、校验和应用能力来自 `/api/router-platforms`，未支持的操作显示原因并禁用。
- 账号、权限和关联完整性由 Go API 强制验证；前端同步隐藏无权操作。
- 用户提供的 MoeDove 标识缓存于 `public/moedove-logo.png`，来源为 https://moedove.com/MoeDove-Logo.png。

账号安全页面只在创建时显示 API 令牌和恢复码。验证器二维码由浏览器本地生成，密钥不会发送至二维码服务。文档范围由目录继承；修改目录权限时，界面会说明对子目录和文件的影响。文本与文件替换都提交校验和，避免覆盖并发修改。

`npm run test:lag` 在独立空数据实例中验证跨交换机端口选择、LAG 与单成员 LAG、MTU、真实物理速率汇总、旧 `portId` 编辑，以及跨成员端口复用与停用端口的保护。端口名称和速率来自服务器资源；设备上的 LACP/MLAG 仍需与登记信息保持一致。

`npm run test:operations` 使用另一空实例，验证带路由器绑定的采集器令牌、真实计数器/速率/重置/未采样状态、24 小时查询、IRR/ROA 导入与 AS 路径模拟、AS-SET 起源绑定和并发审查保护，以及多成员切换、成员管理员自助资料、暂停/激活与只读策略访问。

监测曲线只使用服务器采样，未知速率留空。成员视图不展示物理接口的累计计数。策略快照不会自动下发；候选白名单保存会核对审查版本，实时 RPKI 需在路由服务器表单中设置 RPKI-RTR 缓存。多成员切换会重新加载当前页面，避免保留上个成员的数据。

`npm run test:patching-ipam` 验证双芯双方版本校验、预留到释放的完整配线流程、私有资料关联、真实 PDF/HTML LOA、旧授权失效及跨成员隔离，并检查地址自动预留、使用中地址保护、归属冲突和 IPv6 精确容量。使用新建的空测试数据目录，邮件可保持关闭。

`TEST_SERVER_BIN=/path/to/ixp-manager npm run test:mail` 自行启动临时 Go 实例和仅监听 `127.0.0.1` 的假 SMTP 服务，不连接外部邮件服务器。它验证邮件关闭时的行为、邀请新/旧账号、MFA 密码恢复、令牌从 URL 片段移除、安全投递摘要与固定任务。重试场景使用已停止服务后的有效终态失败夹具，避免等待完整退避时间；实际退避逻辑由 Go 队列测试覆盖。测试结束会关闭自行启动的服务。

`npm run test:fabrics-devices` 使用空的独立实例，验证多交换网络的 VLAN 编号隔离、保持连接/LAG 完整的批量归属迁移、多子网精确分配，以及交换机绑定令牌、只读发现提案、显式接口索引/身份替换、成员诊断脱敏。采集器夹具先取得服务端 ownershipRevision，再提交当前时间的观测；旧归属修订不能借未来时间戳进入新成员的数据。

`TEST_SERVER_BIN=/path/to/ixp-manager npm run test:integrations-peering` 自行启动临时管理服务与只监听回环地址的假 PeeringDB 服务。验证在线只读请求、精确 UTF-8 快照校验、受保护资料差异、并发审查保护、选择性应用/关闭，以及成员提案、接收方管理员确认、矩阵隐私和拓扑失效。不会访问真实 PeeringDB，也不会发送邮件。

新的交换网络默认暂不导出 IX-F。设备发现和外部资料更新只改变明确审查的本地库存/资料，不向设备发送配置；成员间的“已同意对等”也不作为 BGP 已建立的证据。

监测浏览器夹具使用当前归属修订和当前时间样本；历史过期与时钟边界由 Go 测试的专用时间夹具覆盖。已知 BGP 状态样本还必须携带匹配的实际邻居地址与 ASN。

`npm run test:tenant-tabs` 在已初始化的独立测试实例中创建专用成员，验证同源标签页之间的无秘密 BroadcastChannel 失效通知、服务端会话重新读取、成员切换 CSRF 更新、旧标签页写入拒绝，以及退出登录后迟到响应不会恢复旧上下文。使用 `TEST_ADMIN_PASSWORD` 指定测试管理员密码；广播内容不含账号、成员标识、CSRF 或其他令牌。

`TEST_SERVER_BIN=/path/to/ixp-manager npm run test:credentials-deployments` 自行创建临时数据并启动仅监听 loopback 的测试服务，验证 BGP 凭据生成、手填、移除、重认证与并发冲突，部署列表按需加载、服务端脱敏预览和差异、审批后私密下载的真实 SHA、只读权限、拉取令牌的路由器绑定，以及 390px 布局。测试只做 Junos 结构校验，不连接或应用到路由设备；退出时自动关闭服务。截图在 `/tmp/moedove-credentials-*.png`，不包含实际凭据或 API 令牌。

BGP 凭据管理入口位于会话行的钥匙按钮，只有管理员和操作员可用。生成值只在本次管理窗口显示，授权下载的生产配置仍包含实际凭据。配置预览和差异使用服务端脱敏内容；版本校验标识用于审批，下载文件的真实 SHA-256 仅在授权下载并核对字节后显示。`deployments:pull` 令牌必须选择允许拉取的路由器，目标机的 `ixp-agent fetch` 命令只下载文件，不应用配置。

`TEST_SERVER_BIN=/path/to/ixp-manager npm run test:provisioning-collectors` 自行启动空数据的 loopback 服务，检查路由收集器角色、观察模式空白名单、无导出生成结果，以及配置工具的规则修订冲突、按需预览、片段审核、文件校验、历史清理、只读权限和规则绑定令牌。DNS 校验能力按实际服务返回判断，缺少真实检查器时会验证拒绝批准，不使用替代脚本假装原生校验。 可用 `TEST_NAMED_CHECKZONE_BIN` 指定真实 BIND 检查器来执行原生成功路径。测试不会运行目标服务重载，结束时关闭自建服务。

“配置工具”保存固定类型的 DNS/Nagios/Smokeping 规则。DNS 的 SOA 序列号由服务端递增分配，回滚内容应重新生成；Nagios 和 Smokeping 只生成需要目标模板或探测器的片段，审批不代表目标完整配置已通过校验。自动化生成或拉取令牌必须绑定具体 `profileIds`。收集器观察模式与授权路由表分别说明，始终禁止向邻居导出或写入内核转发表。

`TEST_SERVER_BIN=/path/to/ixp-manager npm run test:resource-conflicts` 在独立 loopback 服务中验证通用资源的强 `If-Match`、并发修改与删除、逐字段核对、二次并发保护、数组和新引用的合并、密码冲突清空，以及已删除记录的非密码草稿保存。截图输出到 `/tmp/moedove-resource-conflict-*.png`。生产编辑器始终使用打开或明确核对时的 `_etag`，不会在保存前自动刷新版本，也不会自动提交合并结果。已有浏览器脚本中的库存夹具更新会明确读取当前版本，这仅用于构造测试数据；配置工具规则仍使用独立的 `expectedRevision`。

`npm run test:router-platforms` 使用 `BIRD3_BIN`、`OPENBGPD_BIN` 指向真实解析器，验证新平台的原生校验、审批与下载、基于 `capability.fetch` 的拉取命令、检查器缺失提示和 RFC 1997 三态选项。服务运行在独立临时数据目录，不启动路由守护进程或应用配置。未显式设置 RFC 1997 的记录保留平台默认；已有版本显示生成时冻结的语义，不从后来修改的路由器设置推测。原生语法校验成功不代表实网会话健康。

`npm run test:looking-glass` 在独立服务中通过真实 router-bound worker lease 提交明确标记的本地 typed fixture，不连接路由设备。它验证排队、缓存提示、AS_SET 保留、未知过滤状态和原生未提供属性、采样自然过期、取消/清理、成员表级与跨成员限制，以及切换成员后旧上下文失效。页面只接受后端列出的固定视图，队列完成与采样新鲜度分开展示。

`npm run test:atlas` 自行启动仅监听 loopback 的假 Atlas HTTP provider 与临时管理器，不访问 RIPE Atlas、不消耗真实 credits、不执行网络测量。覆盖公开探针有界快照、私有默认、单方向与有向矩阵、额度和公开范围明确审批、独立执行、真实状态停止、uncertain 不重发与归属核对、缺失观测目标、丢包/超时/多回复展示、只读角色和本地保留清理。未发送项可以取消；未知创建不能被当作失败后重新发送。结果与额度界面不把未知值补成零，也不推断双向对等、Established 或网络健康。

`ATLAS_REAPPROVAL_ONLY=1 npm run test:atlas` 单独验证过期审批恢复：仅在自有临时管理器停止后回拨测试提案的审批时间，重启后通过真实接口重新认证、审核剩余额度并独立续行，证明已尝试方向不重发。续批保持冻结目标与公开范围；开始时间或绑定失效时仍须新提案。该夹具没有生产时钟绕过入口。

`npm run test:core-workspace` 自行启动空数据的 loopback 管理器，验证核心 Bundle 的三步双端预览/保存/编辑、专用端口版本与整体 CAS、冲突草稿保留、链路增删、旧 flat links 显式归组、核心端口预留保留，以及 staff/member/token 边界和 390px 布局。陈旧观测和刷新 500 使用明确的浏览器故障注入验证显示降级，不假装执行真实 SNMP 或设备配置。生产页将 desired、physical、LACP、BFD、BGP 分开；过期或读取失败时隐藏当前速率，历史曲线不补零。截图在 `/tmp/moedove-core-workspace-*.png` 与 `/tmp/moedove-core-editor-mobile.png`。

`npm run test:core-provisioning` 验证 `switch-core` 固定双栈 JSON 计划的规则、结构检查、审批与下载，核对 profile、revision、kind、switch ID 和真实文件 SHA，同时拒绝错误目标及陈旧配置。该计划仍需要目标平台适配，不是 native CLI，也不会自动下发设备。

设备与外部资料提案只允许显式清理已处理数据。界面提供审查导出、待审查保护、快照清理以及需要确认的元数据移除；设备最新观测缓存的清理还核对当前摘要，避免删除刚上报的新版本。历史遥测保留独立处理。
