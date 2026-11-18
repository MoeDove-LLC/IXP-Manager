# BIRD 1：单地址族的受管配置

`bird1` 现在有独立的 Go renderer、严格配置重建/脱敏和实际 BIRD 1 原生解析路径。支持 `route-server`、`route-collector` 的 `observe` 与 `authorized-only`；可生成、审阅、原生校验及下载。v2 受管配置另有专用本机 apply、限时确认、回退及显式恢复；不将 BIRD 2/3 的事务入口借给 BIRD 1。

每个路由器只允许 `localIpv4` 或 `localIpv6` 之一，启用的 peers、白名单和 prefix-origin 配对必须同族。IPv4 用 BIRD 1 `bird`，IPv6 用 BIRD 1 `bird6`；一个双栈部署应拆成两个独立路由器、文件和进程。两种可执行文件的 `--version` 都显示 1.6.8，因此版本字符串不能证明编译族；`ValidateBIRD1` 还要求对应族的最小 `-p` 探针成功、另一族探针失败，再解析已审阅文件。没有另一族或新版本回退。

配置直接输出 BIRD 1 的顶层 import/export 语法，不通过删改 BIRD 2 channel 块转换。RS 使用 `rs client` 与 `next hop keep`，IPv6 显式 `missing lladdr ignore`；不追加 AS_PATH、不设置 next-hop-self。输入要求非空且至多 64 的 AS_PATH、首 ASN 等于邻居、NEXT_HOP 等于来源，以及精确前缀白名单和已有 origin 配对。输出只允许 BGP 来源。`rs client` 的透明语义和 IPv6 行为见 [BIRD 1.6.8 BGP 文档](https://bird.network.cz/doc/bird-6.html#ss6.3)。

两种 collector 都 `export none`。observe 用 `import keep filtered yes` 留下过滤项，并以 `receive limit … action disable` 限制包含过滤项的接收量；空白名单仍拒绝进入授权 RIB，不转成 import-all。authorized-only 不保留过滤项。生成文件只有 BGP 和 device 协议，没有 kernel、direct、static、pipe 或 FIB 同步，不修改宿主路由、接口或地址。没有把原生解析称为真实 peer 行为验收。

配置头中有无密码的结构化模型。读取时提取受限密码行，再从模型完全重建并逐字核对，才准许脱敏、选择 family checker 或解析。改动策略、增补 include/kernel 指令、移除 collector export deny/接收限制都会拒绝；错误不返回配置正文。密码仍只存在于需要私密保存的原始配置和控制面的加密 artifact 中，预览不能拿去校验或执行。

## 控制面和本机检查

管理进程独立配置两条路径：

```sh
ixp-manager --bird1 /opt/bird1/usr/sbin/bird \
  --bird1-ipv6 /opt/bird1/usr/sbin/bird6
```

`--bird1` 默认查找 `bird1`，以免把普通 `bird` 的 BIRD 2 安装当成旧版本；`--bird1-ipv6` 默认 `bird6`。缺少所选族的工具时该部署不能校验，不借用另一个配置路径。平台概览提供 `singleAddressFamily` 和 `nativeCheckerFamilies.ipv4/ipv6`；平台总体 `nativeCheckerAvailable` 仅在两者都存在时为 true。单个部署的 readiness 使用该已验证模型的 `addressFamily`，只检查它需要的那一个工具，所以只安装 bird6 也可以校验 IPv6。路径存在仅表示可调用，实际版本和族仍须原生解析。

新建/明确编辑 router 时拒绝双栈 BIRD 1；新启用 peer 必须匹配该单族。以前保存或导入的未支持双栈 BIRD 1 inventory 仍可读取、启动并通过编辑器修复，但不能生成安全部署。可以先禁用旧的不匹配 peer，再拆分/修复，不能由新字段校验使整个管理服务无法启动。

```sh
ixp-agent fetch --server HTTPS_ORIGIN --deployment REVISION --router ROUTER \
  --platform bird1 --output approved.conf --token-file private-token
ixp-agent check --platform bird1 --validator /opt/bird1/usr/sbin/bird6 \
  --file approved.conf --checksum AUTHORIZED_FILE_SHA256
```

fetch 保留既有 HTTPS origin、平台/router/revision、校验和与私密不覆盖输出限制。check 只解析；v1 历史配置保持原字节预览/校验，在线变更必须使用下方 v2 专用流程。

## v2 专用本机 apply / recover

生成器现在生成 model v2，加入绑定完整配置字节（仅排除该常量自身）的私密运行指纹。原 v1 仍按原格式完全重建，不会静默重写历史审批；自动 apply 不把 v1 当成可读回的运行基线。该指纹含密码相关字节的摘要，预览与 diff 必须替换为 `[REDACTED-RUNTIME-DIGEST]`，不能作为公开审阅 ID。

初次接管必须由运维离线建立一个已重新生成、审阅并批准的 v2 基线，启动独占 BIRD 1 实例。目标和 socket 的父目录必须已存在、由同一运行账号拥有且为 `0700`；基线、候选及恢复文件为该账号拥有的单链接 `0600` 常规文件。daemon 需以明确的 `-c TARGET -s SOCKET` 启动，可带 `-f` 和私密目录中的 `-P PIDFILE`；不接管默认 socket、共享目录、任意原生配置或另一运行账号。IPv4/IPv6 仍各自使用实际 `bird`/`bird6`，运行 agent 的账号与 network namespace 必须与该 daemon 相同。该工具不创建地址、服务或运行权限。

```sh
ixp-agent apply --platform bird1 \
  --validator /opt/bird1/usr/sbin/bird6 \
  --target /var/lib/moedove-bird1/v6/current.conf \
  --socket /var/lib/moedove-bird1/v6/control.sock \
  --base-checksum REVIEWED_BASE_FILE_SHA256 \
  --file /var/lib/moedove-approved/next-v6.conf \
  --checksum REVIEWED_NEXT_FILE_SHA256 \
  --exclusive-instance-acknowledged --timeout 120
```

base 与 next 必须是两个明确审阅的实际文件 SHA256，不能用控制台 HMAC review identifier。目标、socket、native executable 必须显式使用规范绝对路径，没有族或主版本回退。BIRD 1 使用固定 Unix 控制协议，不调用外部 birdc；每个连接先核对同一连接的 `SO_PEERCRED`、daemon 可执行文件路径/摘要、启动参数、进程 generation 及 namespace。锁只约束合作的写入者；`--exclusive-instance-acknowledged` 要求连续独占，不能代替对特权管理员并发带外变更的控制。运行指纹也不是防范这种刻意伪造的内存证明。

BIRD 1 的 `configure status` 实际返回 9001，而且 birdc 仍可退出 0，因此本适配器不使用它。0004 仅表示异步变更开始；`show status` 必须返回精确的 `0013 Daemon is up and running`，并重复 `eval md_managed_checksum` 核对目标指纹。确认必须得到 0018，随后再次核对 readiness 和指纹。控制语义见 [BIRD 1 远程控制文档](https://bird.network.cz/doc/bird-4.html)。这些检查证明配置阶段，不证明 BGP 已收敛。

事务复用已有 flock、原子文件替换和 fsync 工具，但使用独立 BIRD1 JSON WAL。先保存精确 `.moedove-previous` 和 intent，再持久化私密 `.moedove-next` 候选；daemon 限时加载候选，确认成功后才原子发布启动目标。确认前 agent/daemon 崩溃，启动目标仍是旧基线。`.moedove-pending` 记录准备、候选、configure、confirm、发布和回退意图；文件与活进程仍不是一个全局原子事务。正常成功才按精确候选 hash 清理候选并移除 pending，不能删除未知材料。

错误或丢失响应会先重新读取实际旧/新端点，再以独立有时限上下文恢复精确基线。若 native timeout 已恢复基线，不重发候选，也不重复无必要的运行配置命令。回退使用不带新 undo timer 的基线加载，避免之后又自动回到被拒绝的候选。第三种磁盘字节、运行指纹、改变的 snapshot、socket/process/tool 身份都会拒绝覆盖并保留恢复记录。native undo 依赖 daemon 继续运行，不是独立主机 watchdog。

中断后再次 apply 不会重放。明确恢复会回退到原基线，即使候选曾确认而最终结果未记录：

```sh
ixp-agent recover --platform bird1 \
  --validator /opt/bird1/usr/sbin/bird6 \
  --target /var/lib/moedove-bird1/v6/current.conf \
  --socket /var/lib/moedove-bird1/v6/control.sock \
  --base-checksum REVIEWED_BASE_FILE_SHA256 \
  --checksum REVIEWED_NEXT_FILE_SHA256 \
  --exclusive-instance-acknowledged
```

recover 可在同工具的专用 daemon 重启后重新核对当前进程，不依赖旧 PID 发信号；启动或杀死 daemon 均不由它执行。旧/新 checksum 对、family、snapshot 与运行读回必须对应同一记录。不要在未处理的事务期间更换 pinned native 工具、手工删除 journal，或把不确定结果当作已恢复。readiness 的 `activationMode=bird1-confirmed-configure` 表示此本机流程；控制台不远程执行这些动作。

## 仍须完成的旧功能

原 `resources/views/api/v4/router/{server,collector,as112}/bird` 模板已逐项对照。这是可验证的第一增量，尚不等于旧模板完整迁移：

- 每客户端 table/transparent pipe、标准/large community 的客户选择策略、RFC8326 降低 local-pref、旧 martian/前缀长度规则及自定义 skin 没有被隐式导入。
- 关闭 IRR、放宽 more-specific、无完整 origin 配对时的旧 ASN 集等选项不由当前严格 typed 模型推断；这里继续使用明确 exact allowlist/origin 规则。
- 明确 RFC1997 passthrough 值与 runtime RPKI 均拒绝。省略 RFC1997 选项保留 BIRD 1 原生默认，不声称与所有平台一致。
- AS112 不在此适配器范围；旧 AS112 模板会生成 kernel/static 行，不能当成当前健康门控、无不明重放的 AS112 操作器。真实 BGP wire session 行为仍需独立验证；下方本机事务验证不能替代邻居策略或端到端连通性验收。

## 原生工具来源

fixture 使用 [Debian 官方 bookworm 下载页](https://packages.debian.org/bookworm/amd64/bird/download) 的 `bird_1.6.8-2.1+b1_amd64.deb`，大小 602668 字节，SHA256：

```text
a7c5c4ac9eae071be75ea05c86ff6ae52c3c4aecdbf0709321a1722ae3269106
```

下载记录在 `.cache/bird1-native/input/provenance.json`。仅在只读根、隔离网络、零 capability 的受限 namespace 解包，没有安装 package、运行 maintainer script 或修改宿主服务。`bird` 与 `bird6` 的独立 SHA256 为：

```text
bird  f00b7b499517f8d1f96c64ac928f984ecd50e523bfad8c9e8942d8d685776cdc
bird6 bde0b510f224dfe086409a5d6b7163944b02c254a1d82106064a042a23ba70d9
```

二者只需要常规 libc 和 ELF loader。`birdcc` 在本环境未安装，且其主要目标是 BIRD 2/3；这里没有拿它或 BIRD 2 的结果替代真正的 BIRD 1 解析。

## 冻结验证记录

`.cache/bird1-routing-checks/results.json` 记录首轮 renderer/真实 parser 与相关回归：11 个顶层测试、24 子例通过 race，routing vet 通过。实际 1.6.8 IPv4/IPv6 各验证 server、observe、authorized-only、空观察白名单和无 peer 五种配置；每个都拒绝另一族真实可执行文件，并使用实际 BIRD 2 作主版本负对照。

控制面/CLI 的后续冻结是 `.cache/bird1-app-checks/results.json`，aggregate：

```text
7288178032c55096a560aa14fbc6957d275a318dfd30b68a501d797647c62d03
```

该记录覆盖 routing/app/agent 的定向 race、四包 vet 与 CGO0 agent 构建；实际 checker 贯通 v4/v6 的生成、秘密脱敏、校验、审批及精确下载，并验证只有当前族工具也可工作、反族拒绝、旧双栈记录重启后仍可修复，以及 agent fetch/check 不授权 live apply。管理二进制复用联系人增量已构建的 `.cache/contact-export/ixp-manager`，BIRD1 相关 production 文件逐一核对相同，来源与 SHA 记录于 `manager-reuse.json`，没有重复编译。

本轮所有原生操作都是 `--version` 或 `-p`，在零 capability、隔离网络、只读源码/工具的受限 namespace 执行；没有启动 daemon、建立 BGP session、安装软件包、改宿主 FIB/服务或启动 VM。后续独立的浏览器或 daemon 控制实验应另记证据，不归入本次解析结果。

v2 专用 apply/recover 的真实 daemon 与真实 CLI 证据在 `.cache/bird1-apply-native/results.json`（冻结 `cb4ad5c9775fc1a2c054d758f4ed5890d3f295b49a38705ebff60128336b987d`）以及 `.cache/bird1-apply-collector/results.json`。IPv4/IPv6 都验证确认跨越 undo 期限、configure/confirm 回执丢失、实际 8002 响应、daemon 被终止后重新核对恢复、agent 在五个持久化阶段真实退出、未知文件/运行态拒绝，以及 RS→collector 切换。所用 daemon 仅在隔离 namespace 运行，无 kernel 协议或生产转发。

最后的公开投影/能力/CLI 检查在 `.cache/bird1-apply-final/results.json`（冻结 `114984347e7b6b7446ea6b2ae9faeb02c74ae48da8273c7cbfb5b9672b04b8d2`）：v1 精确兼容、秘密与运行 digest 不进入 HTTP preview/diff、族选择、readiness 和 CLI 参数均通过定向 race；四包 vet 以及 CGO0 agent/manager 构建通过。`activationMode` 为 `bird1-confirmed-configure`。该收尾没有重跑已经通过的 daemon 故障矩阵；结果记录核对本机事务核心源码与真实 native 快照一致。对应新二进制保留在该目录，不属于之前可搬运发布包的旧快照。
