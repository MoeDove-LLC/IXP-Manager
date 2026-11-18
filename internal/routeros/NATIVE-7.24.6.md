# CHR 7.24.6 原生验证记录

这些记录来自一次性官方 CHR 虚拟机，限定已测版本与路径。它们不是生产设备验收，也不把结构化计划、协议可编码或“命令已接收”升级为完整发布授权。

这里的角色是Route Server：验收BGP路由通告/撤回、来源策略、AS_PATH和下一跳透明性，以及配置发布/恢复。RS不承载成员间数据流量；非FIB检查只验证RS路由不装入转发表、不把数据流量引向RS，数据包经RS转发的性能不是验收要求。

## 来源与隔离

- 镜像来自[MikroTik 官方 CHR 下载页](https://mikrotik.com/download/chr)的7.24.6 long-term条目。下载ZIP的公开SHA-256为 `692672e6cac9cfaa4f37c6334d07760d49e074b50befc61f0904270553f17126`；验证后仅解出预期的128MiB raw文件。
- raw SHA-256为 `c6abf87622c5bdbd86f7b5730c8e83f86f2335ee2fbe97d008da176ffbf97878`。基础镜像只读，全部写入临时overlay。
- 本轮使用私有解包、校验过的Debian QEMU10.0.13，TCG、1vCPU、768MiB RAM。没有KVM或生产网络。
- OS隔离提供独立namespace、仅loopback、零effective/bounding capabilities、空环境、只读工具/镜像/代码、受限scratch tmpfs、CPU/内存/进程/文件及wall时限。退出销毁来宾状态，不保留凭据或故障磁盘。
- 可复现的可信入口及精确限额见[CHR harness](../../tools/testing/routeros/README.md)。普通 `go test` 默认跳过原生用例，Skip不算原生验证。

## IPv4 和 IPv6 RS 路由表隔离

两套生成策略用例现已在初始通告、仅切换策略、取消恢复三个阶段核对设备路由表。每次先用 API-SSL 读回专用表，并通过原生 getter 确认 `fib` 开关未设置，再以固定主机密钥的独立 SSH 只读查询，从 `/routing/route` 的全部表中查找两个测试前缀。每次都确认当前获准前缀存在于专用表，两个前缀均未出现在 `main` 或其他表中。接收端仍从真实会话独立核对通告、撤回、透明 AS_PATH 和下一跳。探针使用独立 SSH 调用，Safe Mode 仍由原会话持有。

隔离 CHR 7.24.6 的 IPv4 和 IPv6 用例分别在 64.69 秒和 65.68 秒通过，均不含来宾启动。两个固定拓扑的六次检查全部通过，来宾状态均已销毁。测试证据见 `.cache/routeros-rs-rib/results.json`。

## 待确认更新期间硬重启

`--ssh --case device-reset` 先在隔离 CHR 7.24.6 确认基线 A，再用 Safe Mode 激活不同策略的候选 B。原会话仍持有确认权时，测试以私有 QMP 执行一次 `system_reset`，核实实际 RESET 事件。同一 QEMU 进程和临时磁盘覆盖层保留。随后新连接使用原 TLS 证书和 SSH 主机密钥核对设备身份，独立读回的 uptime 小于重启前。

**CHR 保留了未确认的候选 B。** 原 Safe Mode 会话已释放，重启后完整路由配置与认证证据匹配候选，和已审阅的回退检查点不同。重新打开同一事务日志后，监督器返回 `rollback-pending` 和 `unconfirmed-candidate-retained`，拒绝旧确认和新的规划审阅。恢复过程没有执行写入、重新取得 Safe Mode 或绑定新计划。两项验证标志均为 false。

仓库入口实测退出 0，Go 用例 61.44 秒，来宾状态已销毁。证据见 `.cache/routeros-device-reset/results.json`。

### 已有受管基线的显式恢复

`--ssh --case device-restore` 独立复现硬重启后 B 保留的场景。它随后重新打开固定设备和原事务日志，确证候选配置，从日志推导到 A 的计划。测试用当前交易标识、精确计划摘要和独占声明启动一笔新交易。旧确认被拒绝。确认新交易后，严格读回证实原策略代恢复、TCP-MD5 状态匹配、专用表仍为非 FIB，恢复记录重新打开后仍能验证。

该固定 CHR 7.24.6 用例 74.37 秒通过，临时覆盖层已销毁。实际场景使用 IPv4 策略更新和原 TCP-MD5 密钥；切换密钥、取消恢复后继续和过期摘要另由 Go 控制层回归覆盖。生产写入仍受原生能力门约束，首次引导还没有已验证的受管基线可以恢复。证据见 `.cache/routeros-baseline-restore/results.json`。

## 已通过的真实路径

`TestNativeCHRSafeModeRollback` 实际通过：Go API-SSL客户端验证来宾证书与主机名并登录；串口控制台进入Safe Mode，控制台读取current=yes，另一个API连接读取enabled=yes/current=no。API新增测试表并精确读回原生ID；Ctrl-D终止owner会话后，原API连接轮询确认该名称及ID均消失，Safe Mode也已释放。用例不手工删表，不重连制造成功。可信脚本和仓库入口分别复现，Go用例约2.5秒（不含虚拟机启动与证书准备）。

这证明串口owner与API-SSL组合下的该类撤销。终端层仍只报告session-ended，`RollbackVerified`保持false；独立读回才构成回滚证据。

`TestNativeCHRSSHSafeModeRollback` 和 `TestNativeCHRSSHConfirmRetains` 也已实际通过。仓库 `--properties --ssh` 入口分别以独立 Go 进程复现：串口取消2.51秒、属性测试2.90秒、SSH取消2.70秒、Confirm保留1.29秒，整体退出码0。后续独立实验再次通过SSH取消1.98秒及Confirm保留1.16秒。SSH与已校验证书的API通过版本和同源System ID指纹核对同设备；SSH读取current=yes，API读取current=no。

SSH取消要求原API连接读回新建的disabled、非FIB测试表名称与ID均消失。Confirm要求精确 `Releasing Safe Mode... Success!`、普通prompt和新owner查询enabled/current均关闭；之后原API连接确认同名同ID的disabled、非FIB表仍存在，关闭SSH后再次读回仍存在。只有这些保留观察完成后才显式清理，因此清理不制造Confirm证明。终端的`CommitVerified`与`RollbackVerified`仍保持false，独立API读回由测试验证。

SSH使用随机Ed25519用户密钥。该固定7.24.6镜像实际导出的公共host key文件为 `mdlab_host_rsa_pub.pem`，转换后的OpenSSH公钥类型为 `ssh-rsa`。harness通过可信串口只读取这一PUBLIC PEM，与私有loopback上的keyscan算法及解码公钥字节精确比对后再pin；private host key留在销毁的来宾中。7.24.6没有可依赖的host-key-fingerprint getter。此文件名/类型约束仅针对该实验镜像，生产SSH预置算法支持不变。

原生诊断曾得到长度11且包含`/`的System ID，SSH frame只有一条数据行。`LicenseSystemID`因此按有界opaque字母表 `[A-Za-z0-9+/]{1,128}` 处理，首字符也可为`+`或`/`；不解码或正规化，拒绝空白、控制、引号、分号、padding及多条数据行，只比较或计算SHA256，不拼接到路径/命令。Software ID和RouterBOARD serial保留各自的较严格规则。临时字符诊断已移除，原生测试只保留固定布尔/版本/错误分类输出。

`/system/history/print count-only where floating-undo` 已在该版本上实际使用：新建pending表后得到已知正计数，release后为0。固定查询只接受一条规范十进制0..100数据行；不支持、缺值或无法解析时保持未知。这不是每种配置操作的undo成本或历史溢出验证。

独立live实验中，`TestNativeCHRLiveObserve`与`TestNativeCHRLiveResetIdle`已分别验证双通道身份、严格空namespace、关闭owner后的读回和新未持有owner会话。最新固定实验分别耗时1.51秒、2.38秒。

`TestNativeCHRLiveGeneratedPrepare`已在同一固定版本实际通过（12.46秒）：对固定IPv4/IPv6 bootstrap计划逐条执行规范Prepare命令，并按精确原生ID、属性、凭据和unset结果读回。两种AF的connect/listen print存在且为true，getter已知且为true；连接仍disabled，表保持非FIB，生成规则可用。该探针未激活peer、未进入Safe Mode，也未证明Supervisor事务、每操作undo预算或数据面行为，不能据此开放生产发布门。

`TestNativeCHRLiveSupervisorCancel`及`TestNativeCHRLiveSupervisorConfirm`已分别通过完整独立实验（63.64秒、70.75秒），最终仓库`--live`组合入口再次通过（71.49秒、70.84秒）。准备阶段floating-undo为0，三个filter Set和两个connection Enable各增加1，总计5；取消后完整配置与opaque凭据精确回到prepared checkpoint。确认要求Strict目标快照、重新生成计划无差异，且关闭SSH后配置仍保留。只有证明完成后才清理。测试二进制为固定fixture临时覆盖能力门，0..80仍是拒绝上限，实际5个动作不推广为所有操作的预算证明。

`TestNativeCHRLiveSupervisorRestartReadOnly`已独立实际通过58.75秒：原owner与Runtime关闭后，新建Runtime并重新加载同一HMAC journal，只读恢复到精确prepared checkpoint；fresh身份核对通过，旧确认请求被拒绝，禁止的mutation/owner/rebind/reset调用计数为0，证明前没有手工清理。该场景不等于网络半开或设备重启恢复。这个精确名字已明确加入`--live`许可清单，总数六项；typed阶段只额外允许`restart`，所有成功证据必须齐全，其他未知名字仍拒绝。

最终仓库`--ssh --live`六项组合入口已整体实际通过，退出码0：Observe 1.62秒、GeneratedPrepare 15.62秒、ResetIdle 2.68秒、SupervisorCancel 71.49秒、SupervisorConfirm 70.84秒、RestartReadOnly 80.99秒；前置串口及SSH取消/确认也通过。原480秒总wall和资源限制未改变，`guest_process_stopped`、`guest_state_discarded`及`fixture_completed`均为true。所有时间不包含虚拟机启动及证书准备；小规模fixture通过不代表千条规则性能或所有发布分支已验收。

`TestNativeCHRSSHDisconnectRollback`在独立隔离实验中通过（4.10秒）：主动终止本地SSH进程后，原API连接连续两次读回测试表名称与原生ID均消失且Safe Mode关闭，测得等待3014毫秒。测试没有Ctrl-D、确认Ctrl-X、Remove或API重连。这个结果只覆盖本次明确关闭SSH传输的场景，不能推断网络半开、黑洞或设备故障也会在3秒内恢复。

断开用例默认跳过，需要额外`MOEDOVE_CHR_FAILURE_TEST=1`及原固定fixture开关。保留10分钟独立读回上限、11分钟总context；初始预算参考[官方Safe Mode说明](https://manual.mikrotik.com/docs/getting-started/configuration-management/#safe-mode)对连接丢失可能等待约9分钟TCP超时的描述，不是把手册期限当作已测SSH行为。它未加入默认`--ssh`流程；仓库提供互斥的`--ssh-disconnect`独立模式，只跑该selector，总wall900秒、Go12分钟/父730秒，CPU/内存/进程等限制不变。该仓库入口已独立实际复现，退出码0：用例4.08秒、等待3014毫秒，原API精确表名/原生ID消失、owner关闭及无手工清理证据均通过，来宾状态已销毁。此结果不缩短最坏等待预算，也不自动改变生产超时或其他能力门槛。

独立手写单peer IPv4 BGP缓存实验及仓库`--ssh --bgp-cache`入口均已通过，后者独立复现退出码0且来宾状态已销毁。固定guestfwd把来宾`10.0.2.100:179`连接到私有namespace的`127.0.0.1:11790`，speaker仅发送两个文档前缀。起初允许`203.0.113.0/24`并拒绝`198.51.100.0/24`，切换filter root后既有缓存转为允许198、拒绝203；SSH owner Ctrl-D后独立串口确认恢复原结果。全过程只有一次OPEN、一次前缀UPDATE和零refresh，禁用connection三秒后该表无路由。仓库入口的baseline/switch/rollback/disabled-empty及单次通告证据全部为true，generated-policy/dual-stack标志保持false；其前置串口2.51秒、SSH取消2.70秒、Confirm1.18秒也通过。该结果不代表生成策略、双栈、所有缓存重评估或数据面转发已经通过。

## 实际属性差异

以下来自独立 `TestNativeCHRAPIProperties` 探针。它会按新建对象的精确ID和唯一标识显式清理，不能混作自动回滚证明。

| 项目 | 7.24.6实际结果 | 实现约束 |
| --- | --- | --- |
| FIB开关 | CLI `fib=no`被拒；API传`fib=no`或`fib=yes`都表示开关存在，get均为true | 非FIB创建省略字段；传输层拒绝false/no，开启编码为bare switch，关闭使用显式unset并读回 |
| 未设置FIB | print没有字段，成功get没有ret；CLI typeof为nil。开启时print字段存在但值空，get为true | 只有精确版本与已完成的显式getter证据才能解释unset；print空值不能当false，矛盾读集必须拒绝 |
| AF limit清除 | Set空字符串被拒；先设置非空值再Unset成功，回到原未设置表示 | 编译计划使用独立typed unset操作，不悄悄遗漏旧地址族限制 |
| 版本字符串 | API返回7.24.6并带long-term渠道后缀 | 精确版本规范化，不宽泛截断未知版本文本 |
| filter有效性 | 正确但未引用的reject规则inactive=false；不存在invalid getter；语法错误/未知属性的负规则被拒且不保存 | filter单独解释inactive；instance/connection因禁用而inactive不代表配置错误或会话健康 |
| MD5原始未设置 | full与read/api受限用户getter均Unknown | 不推断“无认证”，也不把Unknown与空密钥比较为相等 |
| MD5显式空/设置/清除 | full用户得到可匹配的已知值，受限用户始终Unknown | 新禁用连接先显式初始化认证，再核对；密钥只走私密getter，普通inventory和日志不含值 |
| connect/listen | 未设置connect无法取得已知布尔；显式yes后两AF的print/get均为true | 新增/更新明确写入两个字段，不推断缺省true |
| 连接本地AS | 有效实例fixture中，即使未显式写连接as，print/get也返回与实例相同的已知ASN | 模型保留LocalASN并显式Add/Set，不能归一为空；先固定连接ASN再更新实例，历史禁用连接需要旧代次证明 |

另已确认：未显式指定时，连接routing-table为main，即使实例已绑定独立表。显式Set以及新增时直接指定routing-table后，print/get都精确匹配实例的独立表。编译器与旧预览现在都显式绑定两层，Inspector不容忍main或空值来冒充隔离。修正后的属性矩阵实际通过，Go用例约2.94秒；这仍不代表整个规划器或发布流程已可应用。

原生控制台还使用固定终端参数、精确的 `Taking Safe Mode session... Success!`、prompt前的CSI9999B以及退出时的save/restore/home/clear/RIS。终端解码器仅增加已观察到的受限形式，并有分片和假marker反例；不泛化为忽略未知控制字节。

## 仍需完成

当前执行路径追加的实测：仅`ebgp-rs`仍会给AS_PATH加本地ASN，显式`output.default-prepend=0`修复后，生成的双peer IPv4策略及透明AS_PATH/下一跳通过；改为同一owned SSH流激活后再次通过147.64秒。来源/origin绑定、禁止自身回送、缓存withdraw/announce和取消恢复均检查实际接收UPDATE，双方只建立一次会话、来源只通告一次且没有refresh。

八种封闭owned命令的仓库入口通过7.97秒；加密流延迟并保持data→EOF次序的独立实验通过6.23秒，未在owner释放前误报回滚，释放后恢复并做有界晚写观察。确认后自动清理通过127.64秒，实际删除10个原批准目标，保持Confirmed和新Strict配置。原先API激活路径的历史成功标志不自动证明这些新代码分支；新增事实各自记录为fixture能力，生产发布门仍关闭。千条前缀的容量路径随后取得下述独立通过结果，未增加原时限。

## 千条前缀的分层读回与取消

仓库固定 `--ssh --case batch1000` 用例 `TestNativeCHRLiveRuleBatch1000Cancel`
在同一7.24.6镜像实际通过，完整Go用例366.42秒，入口退出码0，来宾进程停止且临时状态销毁。
1000个固定文档用途前缀生成2004条新generation规则；一次Prepare批次得到2004个ACK，
仍逐条记录durable intent/result并证明完整批次差异。Stage耗时265637毫秒，
包含准备阶段，并非声称整个Stage在90秒内完成；**原90秒确认窗口未改变**，
在该窗口内完成Safe Mode激活、最终Strict全局检查并到达待确认状态。

实测Stage完整Inventory调用43次，另有16次精确目标读取及16次身份读取。
每个激活目标实际做前后、各两轮属性/凭据读取；不把缓存拼成全局Snapshot。
三个普通Prepare写入复用了驱动刚完成的私有全局证据，内部额外Inventory均为0，
分别耗时207、202、289毫秒。完整全局Observe约17.3–19.7秒。
激活前、进入待确认前仍检查全局状态、凭据、规则顺序和external HMAC；
随后原owner取消，独立全量读回确认prepared checkpoint及opaque凭据精确相等，
证明前没有手工清理，最后才删除已收到原生ID的fixture对象。

此前逐条getter曾耗尽单次30秒观察期限；64项只读getter流水线保留全部真实返回，
但单靠流水线或Apply内部去重仍不足以完成原确认窗口。`print detail`没有补齐disabled，
固定布尔查询也未通过完整集合对照，均没有被用于猜测缺省false。
首次分层路径曾在首条写入前返回合并的owner校验错误；本次只拆固定诊断后便通过，
没有放宽1秒证据时限，也未实际捕获该失败的细分原因。因此不能将前次失败断言为
证据过期或宣称所有时序边界已经消除。超时/未知仍失败关闭，不自动重发。

本结果是生成配置、分层读回和取消的固定规模证明，不是1000条实际BGP通告的
数据面性能证明，也没有在这个大规模fixture中执行Confirm。
目标外变化在下一个全局边界检测；独占配置窗口仍是前提，不承诺逐步全设备原子性。
`ObjectObservationVerified`、`RuleBatchVerified`及生产发布门仍不因该单个fixture自动开启。

固定双栈 bootstrap 的准备、激活、完整状态和凭据核对、取消、确认及全新 Runtime 只读恢复已有上述原生证据。双栈生成策略与设备硬重启的固定结果见相应章节。完整发布仍需核对认证轮换、历史容量、各步 undo 预算、确认后大规模清理和设备故障恢复，其中网络半开和历史容量溢出仍待验证。

能力记录逐项标识上述已通过的具体fixture；生产`PreparationVerified`、`ActivationVerified`仍为false，`MaxHistoryActions`仍为0，计划的`NativeValidated`和终端配置读回标志不会因为这些实验自动开启。旧预览与结构化计划均不直接授予执行权。

## 2026-11-14: generated policy over true IPv6 transport

The standalone `--ssh --bgp-generated-ipv6` fixture passed with QEMU10.0.13,
Go1.25.12 and the same pinned CHR7.24.6 image. The Go test took 59.84 seconds;
this excludes guest bootstrap. A second emulated interface used the fixed
`2001:db8:ffff::15/64` test topology and the separately confined passt stream.
Both active IPv6 clients bound distinct documentation addresses. Two actual
CHR session-menu reads verified their address/ASN pairs before announcement.

The successful proof includes source/origin pairing, transparent AS_PATH and
IPv6 next-hop (including preserving the distinction between 16-byte global and
32-byte global+link-local next hops), no echo to the sender, three generated
root-entry changes, withdrawal and activation of already cached prefixes, and
owned-SSH cancellation restoring the complete prepared checkpoint/credentials
and original cached route. Both peers opened once; the sender announced once;
no refresh or manual BGP policy changes were used. Fixture interface addressing
was prepared explicitly. Packet forwarding was not tested. The guest exited and
all guest state was discarded; production capability gates remain false.

The first native attempt failed the source read via session getters. A later
as-value attempt identified only one output row. Switching the exact two-row
parser to handle CR/LF/CRLF and leading terminal whitespace, without consuming
the next record delimiter, allowed both independent reads to pass. The earlier
getter failure is not assigned a stronger cause than the recorded read failure.
No execution deadline or expected source/ASN count was relaxed.

## Prepared-object discard and a new bootstrap

`TestNativeCHRLivePreparedDiscardCycle` passed in 67.08 seconds on the fixed
isolated CHR. After an independently verified cancellation, the old runtime and
controller were closed. A fresh runtime rejected a bootstrap review while the
prepared reject roots remained, then used the authenticated original journal to
remove exactly its 11 recorded additions: 11 attempts, 11 acknowledgements and
zero forbidden operations. The complete original baseline and credential state
were restored. Another fresh service then returned a strict empty review and
completed a different bootstrap transaction with verified confirmation and a
zero-difference strict round trip. No manual cleanup manufactured either proof.

Discard had its own status/cursor and never took Safe Mode, confirmed, enabled,
set or unset configuration. The wrapper refused unknown IDs, duplicate removes
and non-Remove API operations before sending. The guest was disposed afterwards.
This is a small one-peer fixture; it does not establish thousand-rule deletion
performance, packet forwarding, or broad native fault recovery. The production
PreparedDiscard capability remains false.

## Explicit object absence

`TestNativeCHRLiveRuleObjectPresence` passed in 1.96 seconds on the pinned guest.
One unreferenced reject rule was added, its actual native ID and complete selected
identity fields were read, and exactly one Remove was sent for that ID. Two
successful, completed exact-ID queries then returned no object; an independent
full filter-menu read confirmed absence. Both device channels and idle ownership
were checked again. This validates the fixed rule-query presence/absence
primitive; it does not establish batch GC, publication, or rollback semantics.

`ReadObject` continues to require one existing object. The new
`ReadObjectPresence` returns an absent result only on a successful completed
query; traps, deadlines, premature EOF, duplicate/wrong rows and unexpected done
attributes remain errors. Missing properties of an existing row remain unknown.

## Managed rollback review and prepared-generation reuse

`TestNativeCHRLiveRollbackReviewReuse` passed in 53.89 seconds on the pinned
CHR7.24.6 guest with Go1.25.12. A confirmed one-peer baseline used a real,
non-empty synthetic MD5 key. A prefix-only policy update was staged and cancelled;
the complete prepared checkpoint and opaque credential evidence matched. The old
controller and runtime were closed before a fresh pair opened the same journal.

The fresh review performed no mutating or owner-control calls, preserved every
baseline native ID and authentication reference, and returned both generations
with the original one active. Replanning the same desired update required no
Prepare operations. The new transaction confirmed successfully with zero new
rule additions and a final Strict snapshot. The old confirmation was rejected
both before review and while the new transaction awaited confirmation.

Batch removal and cleanup were disabled throughout this fixture. Inactive old
rules remained; no manual cleanup manufactured the result. This does not prove
authentication rotation, AF changes, large-scale updates or packet forwarding.
The guest was stopped and discarded, and production capability gates remain
closed.

## Thousand-prefix prepared discard: incomplete

The first `TestNativeCHRLiveRuleRemoval1000Discard` run did not pass. Its fixed
1000 prefixes produced 2004 generation rules and 2009 total additions. Stage
completed in 257001 ms with 2011 API writes (2009 Add, one AF Unset and one
authentication Set), 43 Inventory calls and four owned SSH writes. Cancellation
and the complete prepared checkpoint/credential comparison passed.

After a fresh connection, discard ran for 183777 ms and issued 11 Remove
attempts, all acknowledged: one connection and ten generation rules. No
forbidden operation occurred. Two rule-batch calls were attempted. This phase
recorded 18 Inventory calls, 126 explicit presence reads and 34489 property
requests in 649 getter batches. A filter-rule disabled getter returned
`transport-failed`; the next Inventory returned `client-closed`, and the final
controller error was `rule-removal-global-readback-unavailable`. The log did not
establish the original transport-closure cause. The reported getter context
deadline flags were false, so they are not presented as a proven deadline cause.

These counters measure wrapper interface calls/request items, not total wire
commands. Connection establishment and the intermediate rejected Review are not
fully included in the phase counters. No final original-baseline, Strict or
empty-review proof was obtained. The guest was discarded, and no manual cleanup
or deadline increase was used. Large prepared discard remains unverified.

## Large read costs and direct selection

The standalone `read-scale` probe passed in 81.62 seconds. Its fixture setup
created 2004 compiled generation rules plus four inert protected objects, with
2008 real Add acknowledgements. Measurement used no writes, credentials or owner
mutations. Two individual full reads took 8853/8793 ms; the two-pass full Observe
took 17454 ms. Six exact filter-rule presence queries took 1985 ms, while their
six supplementary getters took 61 ms. Four protected-object presence reads took
697 ms. This is read-performance evidence, not publication or rollback proof.

The console documentation describes `print from` for selecting an item; the
API documentation only establishes the general print/query protocol. These
documents alone do not prove its missing-ID behavior in API-SSL.
[Console parameter documentation](https://help.mikrotik.com/docs/spaces/ROS/pages/47579229/Scripting),
[API protocol documentation](https://help.mikrotik.com/docs/spaces/ROS/pages/47579160/API).

An isolated `read-scale-from` source overlay changed only the selector to
`=from=<native-ID>`, preserving the fixed full property list and protocol checks.
Six present-rule reads took 83 ms, and four protected-object reads took 52 ms.
However, an ID excluded by the actual complete inventory produced
`command-failed` (98 reply bytes, three reply sentences), not a successful empty
result. The experiment therefore failed its absence requirement and the guest
was discarded. There was no reinterpretation of a trap as absence.

Production code uses this verified direct selection only for FilterRule reads
that require one existing object. Those reads still require the exact native ID,
complete tagged response and actual properties. The general presence/absence
method retains its query and zero-row completion requirements. Before-removal
and protected-rule reads may use the direct path; after-removal queries keep the
independent absence protocol. No production write gate was opened. Later full-lifecycle results and the
separately verified read-only gate are recorded below.

The following `presence-id-scale` probe passed in 55.34 seconds. It added a
test-only boolean reader using the original exact-ID query with `.proplist=.id`,
and compared its results with one actual complete inventory of the same 2008
fixture objects. Three existing-ID queries took 1012 ms; two completed missing-ID
queries took 666 ms. Thus limiting the requested properties did not eliminate
the expensive query. The reader returned no attributes, performed no removal or
owner mutation, and was not added to the production API. This is presence/absence
primitive evidence, not deletion, publication or rollback verification. The
guest was stopped and discarded.

## Final exit accounting and long-case CPU policy

A further exact-binary rerun added non-blocking wait4 accounting before cleanup.
The test proved 161 removals before losing its read connection, with 32212 ms of
lifecycle context and 265906 ms of the separate discard allowance still left.
QEMU had already exited on signal 9. Its actual soft/hard CPU limits were both
360 seconds; final wait4 usage was 367172139 user + 4279857 system microseconds
(371.451996 CPU seconds), with maximum RSS 376076 KiB. The proc snapshot in this
run agreed at 37144 ticks / 100 Hz. This identifies an insufficient fixture CPU
allowance terminating the emulator before application deadlines, rather than
proving a configuration drift or a successful complete discard.

The reviewed long-case list therefore receives a 600-second hard CPU allowance,
matching its existing 600-second Go test budget. Its 900-second outer wall limit,
540-second lifecycle context, 420-second preparation/discard allowance,
15-second API command limit, 180-second observer limit and 90-second confirmation
window are unchanged, as are memory, process and file bounds. Other cases remain
at 360 CPU seconds. This is a test-environment correction; it does not validate
the remaining large lifecycle or open a production capability gate.

## Global-tail discard

`TestNativeCHRLivePreparedDiscardGlobalTailCycle` passed in 58.97 seconds.
Six generation-rule acknowledgements remained provisional until a real full
Observe cleared the private pending delta and the supervisor verified its
complete HMAC/credentials. No per-object Absent callback was manufactured.
All 11 original additions were removed exactly once; the original baseline and
credentials were restored, and a subsequent fresh bootstrap confirmed Strictly.
This small fixture does not prove thousand-prefix performance or device faults.

The thousand-prefix version remained incomplete. It first proved 149 of 2009
removals, then failed to read the next global baseline with transport-failed
and no deadline flag. Lifecycle and independent discard budgets remained.
Repeating the exact binary/budgets with pre-stop process diagnostics proved 167
removals before the same failure. Before harness cleanup, QEMU was already a
zombie with exit signal 9. Its observed soft/hard CPU limit was 360 seconds;
the pre-reap proc snapshot reported 31248 user + 2451 system ticks at 100 Hz.
Those facts establish an early killed emulator, but the 336.99-second proc
snapshot alone does not establish the cause of SIGKILL. The subsequent wait4 result and reviewed CPU-policy correction are recorded
above; this earlier proc-only observation is retained as incomplete evidence.

## Disabled-flag selection semantics

The original equality-only flag query failed native exact membership. A separate
existence-only overlay also failed: actual getters reported two false values,
but the negative-existence predicate returned only one of their IDs.
An independent union overlay then passed in 1.43 seconds: false is the union of
explicit false and absent flag, while true uses explicit true. It checked all
three default/explicit-false/explicit-true records against real getters, then
repeated both exact sets after disable/enable changes. These are fixed 7.24.6
predicate facts; they do not alone verify an optimized full observer.

The API now uses those verified predicates. Full-inventory normalization has a
distinct selection-evidence type: both queries must complete, every actual rule
ID must occur exactly once in the partition, and printed or getter values must
agree. It does not synthesize getter evidence or apply to single-object reads.
The separate `selection-scale` comparison then passed in 101.44 seconds with
2004 generation rules and 2008 inert additions. Before and after two original-ID
flag changes, actual getter/selection/full-Observe results matched all normalized
attributes, IDs, bodies, order and digests. Each single selected read used two
completed queries, exactly 2005 false IDs and one true ID, and zero disabled
getters; the legacy path used 2004 disabled getters. Timings were 9051/9426 ms
for getters versus 5739/5678 ms for selection. No connection, active root,
credential read or forbidden mutation was involved.

Only the fixed-7.24.6 `FilterRuleDisabledSelectionVerified` read-only capability
is now enabled. Failed or incomplete partitions still reject the observation;
individual object reads retain actual getter requirements. All production write
gates remain false and MaxHistoryActions remains zero.


## 2026-11-15: identity, address-family and authentication cancellation

`--case transition-budget` / `TestNativeCHRLiveIdentityAuthTransitionCancel`
passed in 51.38 seconds (root terminal session 81571, exit 0). A confirmed IPv4
peer with a nonempty synthetic MD5 A was updated under the same peer ID to IPv6,
MD5 B, a different local ASN and router ID. The baseline took 22379 ms and four
activation operations. The update took 24481 ms, prepared six new rules and
executed all nine compiled activation operations. Each operation read the actual
owner and history before and after; the observed maximum history and sum of
actual deltas were both nine. Zero deltas remain permitted by the test contract.

Cancellation took 3085 ms. The complete prepared checkpoint, original native
IDs and attributes, IPv4 peer, original ASN/router ID, nonempty MD5 A and its
authentication reference were restored; the new inactive generation remained.
The pending new state and restored old state both required full Strict and
opaque-credential verification. There were zero forbidden calls, no manual
cleanup, and no production gate change. This fixed combination does not verify
the full parameter-transition matrix, history capacity or device-fault recovery.

The first compiled fixture had an incorrect `ipv4` display-label assertion;
the actual typed RouterOS AF is `ip`. A fixture-only correction and compiled-plan
regression produced the new binary. The old binary is preserved. The result at
`.cache/native-transition-budget-v2-checks/native-result.json` is explicitly a
transcription of the root's terminal-output summary, not a fabricated raw log.
The frozen source, binary and exact diff remain in that directory's `snapshot`.

## Optimized thousand-prefix discard: bounded progress, not completion

With the verified read-only selector and corrected 600-second CPU allowance,
root session 68279 still did not complete the fixed single-run experiment.
Stage took 196537 ms; cancellation, checkpoint and fresh readback passed. The
shared 540-second lifecycle expired at 540004 ms, while Discard's own 420-second
allowance still had 136347 ms remaining. QEMU was still running before cleanup;
its sampled CPU usage was 322.66 seconds, below the 600-second CPU limit. This
failure is a lifecycle context deadline, unlike the earlier emulator CPU exit.

The formal cursor was 732/2009: one connection and 731 generation rules proven
by four actual complete batch tails. There were 797 Remove acknowledgements;
the remaining 65 rule ACKs had no committed full-tail proof and are **not**
counted as deleted. No per-object Absent evidence was manufactured and no
forbidden operation occurred. Final original-baseline/credentials/Strict/Review
completion was not reached. Saved filtered observations are in
`.cache/readonly-selection-global1000-checks/native-result.json`.

Approximately 256.35 seconds elapsed before Discard. Within Discard, 72.07 seconds
preceded the first rule batch; five batch execution intervals occupied 132.414
seconds, four full-tail intervals 38.556 seconds, and intervals between a tail
and the next batch 40.613 seconds. These are wrapper boundaries, not wire RTT or
exclusive CPU attribution. The phase recorded 28 Inventory attempts, 3196 exact
object reads, 1598 presence reads, 9137 property items in 4821 batches and 1597
owner reads. The selector was actually used; individual target/protected reads
still accounted for 3196 disabled getters.

The nine-minute bound belongs to this fixture. Product correctness requires
safe stopping, verifiable progress, no repeated uncertain intent, explicit
continuation and final proof; it does not require all cleanup to finish in one
request. Existing journal recovery can reconcile an interrupted batch read-only
on a fresh runtime, then await explicit continuation of its remaining suffix.
That is controller/model evidence, not a native continuation proof for this
failed guest. The separate small native continuation check below now verifies
normal stops after full tails and durable cursor commits, followed by explicit
continuation including fresh Open/Recover. Throughput optimization and further
large reruns remain secondary; budgets, owner checks, WAL and final proofs remain.


## 2026-11-15: bounded discard with explicit continuation

`TestNativeCHRLivePreparedDiscardBoundedContinue` (`--case discard-bounded`)
passed in 59.12 seconds, root terminal session 73339, exit 0. The first request
used MaxBatches=2 and returned verified progress 7/11 with Pending=true and
Complete=false: one connection and six rules whose actual global tail was
verified. The service/runtime was closed. A fresh Open and read-only Recover
still reported 7/11, with zero mutations; partial Review was rejected and no
background continuation occurred. Four explicit MaxBatches=1 requests then
returned 8/11, 9/11, 10/11 and 11/11; only the final request reported Complete.

Across the five requests, exactly the 11 original additions received one Remove
attempt and acknowledgement each, with zero forbidden calls. The six rule ACKs
had one actual full-tail proof and one private pending guard; no per-object
Absent callback was fabricated. The original baseline and credentials were
restored, fresh Review returned an empty namespace, and a new bootstrap both
confirmed and passed Strict verification. There was no manual cleanup. The
harness reported fixture completion; QEMU was running before cleanup with the
expected 360-second CPU limit, then was stopped and its state discarded.

The CLI/socket defaults to one verified work unit per request and accepts an
explicit 1..16 limit. It reports incomplete progress and a command argument
array for deliberate continuation; a Pending response does not schedule work.
Unknown effects, drift or failed WAL still require read-only reconciliation and
do not become successful bounded returns. This small native result verifies the
usable continuation lifecycle, not thousand-prefix throughput, all device-fault
paths or a production write capability. The RS role does not require data packets
to pass through the server. All production write gates remain closed.

Evidence is `.cache/discard-bounded-checks/native-result.json`, explicitly marked
as the root's terminal-output summary rather than raw logs. The independently
frozen source/binary/harness and validation records remain unchanged under
`.cache/discard-bounded-checks/snapshot` (manifest SHA-256
`50076229ad0e3a2597bf41af8340f9003c0a573edd606c980fb55c94fd69f074`).
