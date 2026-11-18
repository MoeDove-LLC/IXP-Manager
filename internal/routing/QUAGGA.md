# Quagga 1.2.4 迁移适配与受管进程

本适配器生成独立 route-server view，支持 IPv4、IPv6 或双族的结构化邻居、精确前缀、前缀/origin 配对、首 AS、最长 64-AS 路径和 TCP-MD5。可生成、原生校验、审批下载及通过 `ixp-agent fetch/check` 取得并核对文件。专用 Go 监督器采用有中断的前台进程替换。控制台及通用部署入口的在线应用和回退能力保持关闭。

旧项目仍有 `app/Models/Router.php` 的 `SOFTWARE_QUAGGA = 2` 平台定义，但当前仓库没有可核对的 Quagga 配置模板。本轮不把任意旧模板或其他厂商配置解释为 Quagga 授权；路由迁移须明确重建当前结构化策略。Quagga collector、AS112、运行时 RPKI 和显式 RFC1997 覆盖没有适配，直接拒绝。未设置 RFC1997 覆盖时保留 stock Quagga 的 RS 社区语义，不声称与 BIRD 默认处理相同。

## 与 FRR 不同的策略路径

Quagga 1.2.4 的 RS-client 有独立 Loc-RIB。即使主 RIB 的入站策略拒绝一条更新，原生代码仍会继续处理客户端 RIB；仅给邻居设置 `in` 不足以约束 RS 转发。此外，`match peer` 仅在 `import`/`export` 上下文生效。[官方源码 `bgp_route.c`](https://github.com/Quagga/quagga/blob/quagga-1.2.4/bgpd/bgp_route.c)、[官方 route-map 实现](https://github.com/Quagga/quagga/blob/quagga-1.2.4/bgpd/bgp_routemap.c)

生成器为每个来源建立完整策略，在同一个 permit 条目内同时匹配精确前缀与对应 AS 路径，再以共享 `mq_import` 的实际 `match peer` 调用该来源策略。每个 RS-client 都挂载此 import 策略；来源自身的 main-RIB `in` 和 RS `export` 也使用该来源完整策略。最终 deny 始终存在。同 ASN 的另一邻居、错误 origin、交叉配对或更具体前缀不能借其他条目的授权通过。若输入没有 `AllowedRoutes`，只保留明确前缀和首 AS约束，不猜测 origin。

本轮采用直接邻居下一跳 profile：IPv4 下一跳必须等于来源 IPv4 地址；IPv6 全局下一跳必须等于来源 IPv6 地址，并显式保留合法 link-local 下一跳。第三方下一跳不被接受，也不重写成 RS 地址。Quagga 原生 MP_REACH 解码器会将非 link-local 的第二地址排除出 32-byte 下一跳语义；合法数据进入 route-map 后，global 地址匹配不会被第二地址替代。RS-client 路径保留 AS_PATH，并且生成配置不包含 network、redistribute、set next-hop 或 AS prepend。[原生属性解码与编码](https://github.com/Quagga/quagga/blob/quagga-1.2.4/bgpd/bgp_attr.c)

`router bgp ... view MOEDOVE_QUAGGA` 使用有名字的独立 view，Quagga 的 FIB 更新分支明确跳过这种 view。校验器同时使用 `--no_kernel`。[原生 FIB 判断](https://github.com/Quagga/quagga/blob/quagga-1.2.4/bgpd/bgp_route.c)

AS 正则使用该版本真正的 `ip as-path access-list`，不套用 FRR 的 `bgp as-path access-list`。普通 AS 序列长度限定 1..64，AS_SET/混合 confederation 字符串不获得隐式授权。route-map 序号连续递增，满足 Quagga 的 `<1-65535>` 语法；不因习惯性的十位间隔使合法大输入溢出。总输入仍受通用 20000 个前缀/origin 条目与 16 MiB 产物限制。

## 原生校验与凭据边界

```sh
ixp-manager --quagga-bgpd /private/native/quagga-1.2.4/bgpd

ixp-agent fetch --server https://manager.example.test --deployment REVISION \
  --router ROUTER --platform quagga --output approved.conf \
  --token-file private-pull-token

ixp-agent check --platform quagga --validator /private/native/quagga-1.2.4/bgpd \
  --file approved.conf --checksum APPROVED_FILE_SHA256
```

管理端默认寻找 `quagga-bgpd`，不把系统 `bgpd` 或 FRR 自动当作 Quagga。校验器固定要求 1.2.4 的版本与项目身份输出，并实际检查一个错误命令应被拒绝，再检查候选配置。其他版本、缺失程序、redacted 预览和未知配置格式均失败。`nativeCheckerAvailable` 只表示已配置的程序存在；版本和隔离条件仍要通过实际校验。

`--dryrun/-C` **本身不是隔离措施**：stock Quagga 在配置解析期间可能创建 BGP 监听 socket，且 `--bgp_port 0` 会回到默认 179。每次检查必须创建私有 user/network namespace，固定使用高端口 65535、127.0.0.1、无内核安装、关闭 VTY TCP端口，并把 Zebra/PID 路径指向私有临时目录；没有命名空间能力就失败，不退回宿主网络。环境只保留明确的 PATH、私有 HOME/TMPDIR 和 C locale。整个校验限时 15 秒，诊断有界且不返回可能回显密码的原始输出。[官方 `bgp_main.c`](https://github.com/Quagga/quagga/blob/quagga-1.2.4/bgpd/bgp_main.c)

带密码的真实配置只进入已有私密加密工件和经审批、限定路由器的下载流程。模型注释不包含密码或显示名称，按短行分块以满足旧 VTY 行边界；原生指令只含解析后的地址、数字与受限 MD5 字节串。验证和脱敏都先从模型及受限密码行重建完整生成结果，逐字比较；额外指令、移除 view、改宽前缀、替换来源匹配或改变 import/export 方向都拒绝。预览的密码占位符不能再次作为原生输入。

## 已取得的证据与未覆盖范围

实际使用 [官方 Quagga 1.2.4 发布归档](https://github.com/Quagga/quagga/releases/tag/quagga-1.2.4)，大小 2,925,847 字节，SHA256 `e364c082c3309910e1eb7b068bf39ee298e2f2f3f31a6431a5c115193bd653d3`，与 [Buildroot 官方配方记录的发布校验值](https://github.com/buildroot/buildroot/blob/2019.11.x/package/quagga/quagga.hash) 一致。这里核对了 SHA256，未另行声称本地验证了该 tarball 的 PGP 签名。构建需要的两个 Debian 工具包则核对了签名 InRelease、完整 Packages 摘要、包大小及 SHA256。

748 个原始源码文件没有补丁。构建只在只读源/工具和有资源限额的离线 scratch 中进行，使用 out-of-tree 配置、`-O2 -fcommon`、静态链接 Quagga 库，并禁用未使用的守护进程、VTY shell 和 capabilities；未执行系统安装。此前因私有 state 目录、解压时间戳和产物路径而失败的构建日志保留，不算原生通过。

真实 bgpd 通过双族、各单族、空邻居和 80-byte MD5配置的正向解析及错误命令负向探针。另有 [离线 C 夹具](../../tools/testing/quagga/policy-check.c) 直接链接同一次未修改的 stock libbgp/libzebra，使用真正的配置读取器、route-map 引擎、AS 正则和属性编码器完成 21 项匹配/拒绝及 AS_PATH/NEXT_HOP 编码检查。夹具明确设置不监听、不安装 FIB，且不运行事件循环；这不是模拟 FRR，也不是完整 BGP wire/设备故障测试。

构建和原生证据分别位于 `.cache/quagga-native/build4/results.json` 与 `.cache/quagga-checks2/go-results.json`。控制台和 CLI 接线、审批下载、权限、审批标识及后续整合另有定向证据；任何后续 BIRD1 apply、其他路由迁移或便携发布改动都不自动算入此冻结源。真实多对端会话、max-prefix 断连过程、长时间运行和自动发布/回滚仍未覆盖。

控制台/agent补验已通过：14项顶层、33条通过记录，0失败/0跳过，包含路由器两族真实原生校验、缺失/错误程序拒绝、预览脱敏、审批标识及绑定令牌下载、私密fetch与所有Quagga执行模式拒绝。4包vet及manager/agent的CGO0构建通过；精确产物摘要与冻结后并行差异见`.cache/quagga-native/results.json`。

### 后续真实 BGP wire 增量

同一未修改的官方源码以标准 `--enable-vtysh` 重编，开启只读 Unix VTY 观测；routing `libbgp.a` 与上述解析构建逐字相同。实际 stock bgpd SHA256 `9f758d79ce302c188b42d252ebd3b51044f803767db55c048574e8d4b60eaf67`。新网络命名空间只含固定文档地址的 loopback；Go 与 bgpd 都实测全部 capabilities 为零且 no_new_privs=1。

真实双族六个 TCP BGP peer 通过：每族一个合法发送者、接收者和同 ASN 的另一来源。两条正例的精确前缀、AS_PATH、IPv4/IPv6 global NEXT_HOP 及 IPv6 link-local 原样传递；11条策略负例均由原生接收计数确认处理，接收者的真实 RS-client RIB 只含正例。同 ASN 来源越权、交叉 origin 配对、第三方下一跳、更具体或未批准前缀都拒绝；把授权 global 地址塞入非法 non-link-local secondary 的 IPv6 绕过尝试也拒绝。两个错误 first-AS 更新各收到真正的 UPDATE/Malformed-AS-PATH（3/11）通知。内核路由前后相同，未运行 Zebra、没有 kernel export。用例10.08秒，含编译总26.487秒，0race；证据`.cache/quagga-wire-final/final-results.json`。

初次 wire 失败与修复保留：关闭VTY的解析构建没有运行时Unix控制口；开启VTY后又观察到 `zclient_start` 对不存在的Zebra socket同步sleep(1)重试五次，阻塞初始事件循环。现在在原12秒startup窗口中重新读取真正的peer状态，未以socket文件存在当ready，也未提升权限或放宽总预算。这个运行条件来自[stock zclient源码](https://github.com/Quagga/quagga/blob/quagga-1.2.4/lib/zclient.c)和受控进程的syscall/wchan证据。

此次只验证固定2-byte-AS合成会话；AS4 wire协商、TCP-MD5握手、max-prefix断连、长时间运行和故障恢复不由它覆盖。原解析/App/CLI回归没有重跑；生产Apply/Rollback能力仍保持关闭。

### 后续部署调查：仍未开放发布

源码检查支持先验证**明确有中断的、受监督的独立进程替换**，不能把 SIGHUP 或“graceful restart”字样当成无中断配置提交。

- 默认 SIGTERM/SIGINT 路径与 SIGHUP 都先进入 `bgp_terminate`，向 Established 邻居发送 Cease/Peer Unconfigured，再清路由处理队列。`-r/--retain` 是另一条路径，初始控制器应拒绝它，也拒绝后台 daemonize，保留真实子进程身份与 wait 回执。[bgp_main.c](https://github.com/Quagga/quagga/blob/quagga-1.2.4/bgpd/bgp_main.c)、[bgpd.c](https://github.com/Quagga/quagga/blob/quagga-1.2.4/bgpd/bgpd.c)
- SIGHUP 后调用的 `bgp_reset` 只列出 VTY、Zebra client、access-list 和 prefix-list 重置，并没有完整删除 BGP 实例/邻居和 route-map。新文件中删掉一个邻居，不等于旧 runtime 定义已消失；这个删除风险已由后续原生 HUP 反例证实（见下文），当前不选 SIGHUP 作为发布或回滚机制。[bgp_route.c](https://github.com/Quagga/quagga/blob/quagga-1.2.4/bgpd/bgp_route.c)
- stock OPEN 无论配置开关都会通告基础 GR capability；只有显式 `bgp graceful-restart` 才加 AF 元组，其 F-bit 固定为零。基础 capability 的出现不能证明本机转发/RIB保存、会话不断或配置回退；GR peer-helper、进程替换与 native confirmed-commit 是不同能力。本轮 wire 邻居未协商 GR，不能据它声称 GR 成功。[bgp_open.c](https://github.com/Quagga/quagga/blob/quagga-1.2.4/bgpd/bgp_open.c)

可复用现有 OpenBGPD 监督器的私密有界文件读取、fsync/原子文件、独占锁、旧/新 checksum、操作意图先落盘、精确 transaction/deadline 决策，以及未知第三版本不覆盖的原则。不能复用其 `bgpctl reload` 双回应屏障、固定 OpenBGPD socket、root privsep parent 假设或绑定旧 PID 的 verify 闭包。Quagga 是自己的 VTY 协议，Unix socket 路径由构建确定，且替换后 PID/starttime 本来就应改变；需要独立绑定 binary hash、启动参数、预期 UID、boot/process generation 和 SO_PEERCRED。

已有两种原生进程行为实测，另有监督器的专用替换流程：

1. **SIGHUP 反例**。同一进程重读 B 后，B 中已删除的 IPv4 和 IPv6 接收者仍能重新连接，并收到只允许在 B 中传播的路由。用例 17.10 秒，通过表示反例已实测。证据 `.cache/quagga-lifecycle/hup/results.json`。
2. **完整进程替换**。原版 bgpd 完成双族 A→B→A。TERM 发出 Cease / Peer Unconfigured，非 GR 对端会话清空 RIB。候选 B 在就绪前终止后，确认其退出再启动 A，原策略从真实对端恢复。用例 32.30 秒，无 Zebra 与内核路由变化。证据 `.cache/quagga-lifecycle/replacement/results.json`。
3. **独立 GR 验收**。需另行核对实际 OPEN 的 R/F/AF 字节、helper RIB、EOR 与超时。当前渲染器保持原 GR 行为，发布方案按会话会断开设计。

### 专用 Go 监督器

`OpenQuaggaWatchdog` 只面向明确允许会话中断的独占前台实例。管理器需同一 UID、全部 capabilities 为零并启用 NoNewPrivs。需提供固定 Quagga ELF、其 SHA256、编译时指向同一私密状态目录的 `bgpd.vty` 和明确 BGP 端口。生产部署环境应预先准备独立路由服务器的网络与该端口的非特权绑定条件。首次启动另需已审阅的 0600 配置和预期摘要。

监督器先校验当前已确认槽和候选摘要，再持久化候选意图。仅停止自己启动并验证的前台进程，绑定 Linux UID、启动时间、可执行文件、固定参数、Unix `SO_PEERCRED` 与两族 BGP 监听。原生只读 VTY 取得稳定的前台运行配置指纹，并确认每个已配置地址族至少有一个邻居进入初始活动状态。此时远端会话和策略仍须在确认前检查。

启动文件通过私有描述符交给进程。候选启动及会话观察期间，原已确认槽继续作为重启目标。确认只接受当前事务 ID 与候选摘要；成功后原子切换重启槽。候选失败、取消、到期或未确认后重开时，恢复最后确认的槽。发现第三方文件或陌生 socket 会停在需要核对的状态。重启后只有之前持久化了 inode 且核实已失去监听的旧 VTY 可清理；若中断发生在创建 VTY 与落盘之间，需在独占维护窗口核对该 pathname。监督器退出时停止其子进程，主机服务负责重启监督器。

在线路径的本机预检只绑定 `127.0.0.1:65535`，使用 stock `-C` 的 VTY/PID 创建前退出路径。控制台及 `ixp-agent check` 的通用验证保留独立 user/network namespace。[Quagga 1.2.4 进程初始化源码](https://github.com/Quagga/quagga/blob/quagga-1.2.4/bgpd/bgp_main.c)

Go 监督器的原生双栈用例 57.58 秒通过，并发竞态检测未报错。实测拒绝陌生 VTY 和过期基线摘要，替换进程后的两族策略及 Cease，取消后恢复原策略，候选就绪前异常退出后恢复，两次启动间持久使用最后确认的配置。结束后核对内核路由保持一致。证据 `.cache/quagga-watchdog-core/native/results.json`。[专用 CLI 操作说明](../../docs/QUAGGA-WATCHDOG.md)

## 单播会话观测

`routing.ObserveQuaggaSessions` 从管理员指定的私密 `bgpd.vty` 读取
Quagga 1.2.4 的固定 IPv4 和 IPv6 单播会话摘要。它在读取期间验证同
UID 的进程身份和 socket inode，再逐个核对有数据的地址族中的本机
Router ID 与 ASN。仅返回请求选定的邻居、原生远端 ASN、会话状态与
已接收前缀数。已建立会话的原生零计数可用，未建立的计数未知。空地址族
只能在同一进程的另一地址族提供本机身份时归属到路由器。

原生六邻居夹具验证了双栈源会话各自的真实计数、零计数和未知计数的
区别、邻居筛选及错误本机身份拒绝。另验证了读取期间的 Unix socket
替换拒绝、长 IPv6 地址换行与摘要汇总。所有操作均限固定只读查询，内核
路由保持一致。证据 `.cache/quagga-observation/verified/results.json`。

### 原版进程替换与 HUP 删除语义验收

两个独立 cap0/NNP、无外网、无 Zebra 的 namespace 运行同一冻结 race 二进制和 stock 1.2.4 bgpd。没有修改产品 renderer、发布入口或能力开关。

**Fresh replacement** 用例32.30秒通过。相同私有 `bgpd.conf` 路径依次运行 A→B→A，再让一个 B 候选在真实 PID/VTY/监听已创建、尚未确认 ready 时被有意 SIGKILL，并重新启动 A。每次正常停止都观察到真正的 Cease/Peer Unconfigured（6/3）、会话断开及不协商 GR 的客户端 RIB 清空；旧进程 wait 回收和双族监听消失之后才启动新进程。`/proc` 的 PID/starttime/exe/完整 argv、PID文件、Unix SO_PEERCRED 与两个 TCP监听 inode 在每次切换前核对。B 实际只接受新前缀并拒绝原策略，删除的 peer 无法连接；回到 A 后拒绝 B-only 前缀并恢复 A 的双族路由。最终私密文件逐字等于 A，内核路由不变。这个结果限定测试夹具中的明确中断替换和故障注入，未实现持久监督器或无缝 GR。

**SIGHUP 反例** 用例17.10秒通过取证：候选文件已删除 `192.0.2.2` 与 `2001:db8::2` 两个接收者，但同一 PID/starttime 在 HUP 后仍接受它们重新建立 BGP，并向它们发送只存在于候选新策略的前缀。因此“文件已替换 + HUP已送达”不能证明被删除邻居失去 runtime 授权，也不能作为本项目的发布/回滚方法。这个是原版 Quagga 的实测 reload 限制；本产品没有启用这样的 HUP 适配器。

精确证据在 `.cache/quagga-lifecycle/results.json`，冻结源 `dcef8f96c264bbccbe992001de1cbcfebbe2b1d062ad1c17f3560f691ae15158`。两个运行互不共享网络、监听或可写目录，均0race，进程和句柄已关闭。仍需另行实现并验证持久日志、控制器崩溃恢复、外来进程/socket/文件冲突、TCP-MD5 wire与GR/EOR等范围；`Apply`/`Rollback`仍为false。
