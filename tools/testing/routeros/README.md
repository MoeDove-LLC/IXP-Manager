# 可复现的 RouterOS 7.24.6 CHR 原生传输实验

该工具默认仅运行 `TestNativeCHRSafeModeRollback`，不会连接生产路由器。显式添加 `--properties` 可在取消验证通过后，独立运行 `TestNativeCHRAPIProperties`；`--ssh` 增加固定 CHR 的 SSH 取消与 Confirm 保留验证；`--live` 在SSH验证后运行经过审阅的live测试清单；`--bgp-cache` 增加固定单peer IPv4缓存实验。独立的`--ssh-disconnect`只运行长等待断开用例，不能与其他模式混用。没有自动下载、安装、生产设备连接、数据面转发测试或Safe Mode历史溢出测试。

已验证的范围是 **CHR 7.24.6**：TLS 证书校验与 API 登录、两个通道版本一致、串口拥有 Safe Mode 而 API 通道不是 owner、API 新增测试路由表及原生 `*ID` 读回、Ctrl-D 结束控制台会话，最后由原 API 连接读回表名和 ID 均消失。最后一步才是回滚证据；终端 EOF 或 Login 提示本身不是回滚证据。原可信实验中该 Go 用例已通过，耗时 2.50 秒；本仓库入口也已在上述固定镜像上独立复现，Go 用例耗时 2.49 秒、入口退出码 0。串口及下述 SSH 结果均限定已测路径和版本，完整 BGP 下发与生产 `NativeValidated` 仍保持原门槛。

## 准备

当前支持 Linux x86_64，需事先安装 Python 3（`/usr/bin/python3`）、bubblewrap、iproute2、util-linux 的 `setpriv`/`prlimit`、coreutils `timeout`，并允许创建 user/mount/PID/network 等 namespace。失败时工具不会退回非隔离运行。

QEMU 可来自系统安装或一个私有解包前缀，需要以下布局：

```text
<qemu-root>/usr/bin/qemu-system-x86_64
<qemu-root>/usr/lib/x86_64-linux-gnu/
<qemu-root>/usr/share/qemu/
<qemu-root>/usr/share/seabios/bios-256k.bin
```

镜像必须是自行取得并保留不变的官方 **raw** `chr-7.24.6.img`，大小 128 MiB，SHA-256 固定为：

```text
c6abf87622c5bdbd86f7b5730c8e83f86f2335ee2fbe97d008da176ffbf97878
```

只接受这个 raw 镜像；ZIP、qcow2 或其他版本不会被自动转换。镜像会在进入隔离环境前和启动来宾前分别校验。运行期间不要修改镜像、QEMU 工具树或测试二进制。

在仓库根目录先编译测试二进制（本工具不会安装 Go 或拉取依赖）：

```sh
CGO_ENABLED=0 go test -c -o /tmp/moedove-routeros-native.test ./internal/routeros
/usr/bin/python3 -I -S -B tools/testing/routeros/run.py \
  --image /absolute/path/chr-7.24.6.img \
  --qemu-root /absolute/path/unpacked-qemu \
  --test-binary /tmp/moedove-routeros-native.test
```

系统安装使用该布局时可传 `--qemu-root /`；只会只读挂载上述四个 `usr` 子树，不会挂载主机根目录。所有路径以独立 argv 传递，不经过 shell，也不会变为来宾命令。请使用普通测试二进制，不加 `-cover` 或 `-race`；这些模式不属于此受限原生运行入口，Go 单元测试可独立运行 race 检查。

## 独立 API 属性实验

```sh
/usr/bin/python3 -I -S -B tools/testing/routeros/run.py \
  --image /absolute/path/chr-7.24.6.img \
  --qemu-root /absolute/path/unpacked-qemu \
  --test-binary /tmp/moedove-routeros-native.test \
  --properties
```

只有第一项取消测试的实际回滚读回和 PASS 均已确认，才停止串口代理、重新登录，并创建临时 `mdlab_read` 组（仅 `read,api`）和 `mdlab-read` 用户。密码每次随机生成，仅写入私有 tmpfs 的 `/scratch/limited-password`（0600），不会输出。随后用独立进程、精确测试名运行属性用例；它检查固定版本的路由表、禁用 BGP 对象、过滤器和受限账号的属性/凭据可读性。

属性用例通过 fresh ID 与本次随机名称/备注重新核对身份后显式清理其测试对象。**这种删除不构成取消回滚证明**，也不替代前一项测试。属性日志只允许已审阅的固定 `probe_*` 标签、字段/阶段枚举、布尔值和 `api_code` 分类，不输出实际属性值、原生 ID 或密码。两项测试分别需要实际执行及 PASS；空测试、Skip、未知日志或输出超限均失败。

`--properties` 已在上述固定 CHR 7.24.6 镜像上实际复现，入口退出码 0：取消回滚用例耗时 2.52 秒，独立属性用例耗时 2.92 秒。日志确认 connection 默认路由表为 `main`，显式 set/add 后 print/get 均匹配指定表；`fib` 与两个 AF 限制的 unset、MD5 设置/清除及受限账号不可得知凭据也通过。输出仍受原过滤器和 64 KiB 界限约束，退出时销毁来宾状态。它不证明 SSH、Confirm、数据面转发、真实邻居会话或其他 RouterOS 版本能力，也不会修改生产审批门槛。

## 可选 SSH 生命周期实验

`--ssh` 需要主机已安装 `/usr/bin/ssh`、`/usr/bin/ssh-keygen`、`/usr/bin/ssh-keyscan`；仅选择此模式时才把这三个固定程序只读挂入隔离环境：

```sh
/usr/bin/python3 -I -S -B tools/testing/routeros/run.py \
  --image /absolute/path/chr-7.24.6.img \
  --qemu-root /absolute/path/unpacked-qemu \
  --test-binary /tmp/moedove-routeros-native.test \
  --ssh
```

可同时添加 `--properties`。执行顺序固定为串口取消证明、可选属性测试、SSH 取消、SSH Confirm；每项使用独立 Go 进程和精确测试名，前一项不通过就不继续。SSH 的固定目标为私有网络 namespace 中的 `admin@127.0.0.1:12222`，不会接受外部 hostname、密钥路径、代理或现有 agent。

SSH bootstrap 生成一次性随机 Ed25519 用户密钥，通过可信串口安装公钥。7.24.6 没有可依赖的 `host-key-fingerprint` getter，因此在来宾内导出 host key 后，只读取实际观察到的 `mdlab_host_rsa_pub.pem` 中的 `BEGIN PUBLIC KEY` 内容，并限定转换后的类型为 `ssh-rsa`。private half 始终留在将被销毁的来宾中。`ssh-keygen` 转换公共 PEM 后，必须同时匹配 `ssh-keyscan` 的算法和解码后公钥字节，才写入 0600 的 `/scratch/known_hosts`。网络扫描本身不是信任来源。这一host文件名/类型白名单仅针对固定7.24.6实验镜像；生产SSH的其他预置算法支持不变。SSH身份密钥也仅存在于私有tmpfs，生产终端使用严格host key校验和无密码/无agent的固定参数。

两个 Go 用例通过版本与 System ID 指纹核对 SSH/API 为同一设备。取消用例要求 Ctrl-D 后原 TLS API 连接读回表名和原生 ID 均消失。Confirm 用例要求精确 release 回复、普通提示符和无 owner 读回，再通过原 TLS API 确认相同表在 release 后及 SSH 关闭后仍然存在；只有取得保留证据后才显式清理。终端本身的 `CommitVerified`/`RollbackVerified` 仍为 false。

该版本实际System ID曾包含`/`，因此按有界opaque字母表`[A-Za-z0-9+/]{1,128}`读取，首字符可为`+`或`/`。值不解码、不正规化，不拼接进路径或命令；空白、控制、引号、分号、padding与多条数据行均拒绝。跨通道只核对相同来源字段的SHA256。Software ID和RouterBOARD serial仍采用原有较严格规则。

本仓库入口已用 `--properties --ssh` 在上述固定 CHR 7.24.6 镜像上实际复现，退出码 0：串口取消 2.51 秒、属性测试 2.90 秒、SSH 取消 2.70 秒、Confirm 保留 1.29 秒。四项独立证据均通过，退出时来宾进程和临时状态已销毁。公钥转换器的本地行尾注释及扫描器标准注释不参与 pin；算法、解码公钥字节和唯一匹配条件仍严格校验。以上范围不含中途断线、history 溢出、其他设备/版本或数据面能力。

后续版本事实与live验证范围见[原生记录](../../../internal/routeros/NATIVE-7.24.6.md)。准备/激活的未验门槛仍关闭。临时System ID长度/字符诊断已从原生测试移除，严格日志过滤器继续只接受固定布尔、版本和错误分类；无需扩大过滤范围。此次文档与白名单收窄仅做离线契约检查，没有重复启动虚拟机。

## 可选 live 实验

`--live` 默认关闭，必须同时提供 `--ssh` 和独立预编译的 `--live-test-binary`。在仓库根目录构建live包的测试二进制；原`--test-binary`仍使用前述routeros包二进制：

```sh
CGO_ENABLED=0 go test -c -o /tmp/moedove-routeros-live.test ./internal/routeros/live
/usr/bin/python3 -I -S -B tools/testing/routeros/run.py \
  --image /absolute/path/chr-7.24.6.img \
  --qemu-root /absolute/path/unpacked-qemu \
  --test-binary /tmp/moedove-routeros-native.test \
  --ssh --live --live-test-binary /tmp/moedove-routeros-live.test
```

live二进制只读挂载为`/lab/routeros-live.test`，启动前后核对输入文件身份，没有向namespace增加Go工具链、源码目录、凭据或网络端口。可同时选择`--properties`，固定顺序是串口取消、可选属性、SSH取消、SSH Confirm，然后依次执行以下六项独立进程；前一步不通过则停止：

| 精确测试名 | 必需证据 | 原生验证状态 |
| --- | --- | --- |
| `TestNativeCHRLiveObserve` | 严格空namespace、双通道身份、关闭owner后读回 | 独立实验已通过1.51秒 |
| `TestNativeCHRLiveGeneratedPrepare` | 固定双AF bootstrap的规范Prepare命令及精确属性/凭据读回，未激活、未证明undo预算 | 独立实验已通过12.46秒 |
| `TestNativeCHRLiveResetIdle` | 新未持有owner会话、身份一致、未恢复旧owner | 独立实验已通过2.38秒 |
| `TestNativeCHRLiveSupervisorCancel` | prepare/activate实际历史计数及prepared checkpoint回滚，证明前无手工清理 | 独立实验和仓库全套均通过，后者71.49秒 |
| `TestNativeCHRLiveSupervisorConfirm` | prepare/activate实际历史计数、严格desired读回和关闭owner后保留，证明前无手工清理 | 独立实验和仓库全套均通过，后者70.84秒 |
| `TestNativeCHRLiveSupervisorRestartReadOnly` | 新Runtime与HMAC journal只读恢复、精确checkpoint、fresh身份、拒绝旧确认、禁止调用计数为0，证明前无手工清理 | 独立实验58.75秒、仓库全套80.99秒均通过 |

GeneratedPrepare确认两种AF的connect/listen明确写入且print/get均为true，连接仍disabled、表非FIB。三个Supervisor用例仅通过测试代码中的能力注入，才在一次性CHR中执行固定计划；生产`PreparationVerified`/`ActivationVerified`仍为false。0..80是这些测试的拒绝上限，不是任意计划、并发写入或历史溢出的安全预算。

测试许可由`live_schema.py`中的完整名字与必需证据决定，不使用开放的`^TestNativeCHRLive`前缀自动授权未来测试。新增测试必须显式审阅名称、字段与证据。live输出只接受固定布尔、枚举、封闭错误分类、规范`operation_index`（0..100000）及`history_before/after/delta`（0..80）；校验delta和所属cancel/confirm/restart模式，不输出ID、设备值或凭据。RestartReadOnly必须同时出现只读恢复、精确checkpoint、fresh身份、旧确认被拒绝、`forbidden_native_calls=0`与`manual_cleanup_before_proof=false`，缺一项、出现非零禁止调用或混入别的阶段模式都失败。相同标签在不同已审阅字段的观察可以变化，最终证据不可自相矛盾。

六项固定清单及前置串口/SSH测试已由仓库`--ssh --live`入口整体原生复现，退出码0且`guest_state_discarded=true`；Observe、GeneratedPrepare、ResetIdle分别1.62、15.62、2.68秒，后三项见上表。27项离线契约检查也通过。RestartReadOnly限定为关闭原owner与Runtime后，以全新Runtime和同一HMAC journal只读恢复；禁止的mutation/owner/rebind/reset调用计数为0。它不代表网络半开、RouterOS设备重启或任意故障恢复已验证，这些fixture不提升完整发布门槛。

## 可选固定 BGP 缓存实验

```sh
/usr/bin/python3 -I -S -B tools/testing/routeros/run.py \
  --image /absolute/path/chr-7.24.6.img \
  --qemu-root /absolute/path/unpacked-qemu \
  --test-binary /tmp/moedove-routeros-native.test \
  --ssh --bgp-cache
```

`--bgp-cache`默认关闭且必须同时选择`--ssh`，在前面的所有已选测试通过后才执行。它复用已准备的随机用户密钥、可信串口导出的host key pin和随机API密码。仅此模式才在私有namespace创建`127.0.0.1:11790`单连接listener，并向QEMU添加固定`guestfwd=tcp:10.0.2.100:179-tcp:127.0.0.1:11790`；无外网、任意目标参数或shell转发命令。

可信speaker只发送一次包含`203.0.113.0/24`与`198.51.100.0/24`的IPv4通告。固定手写过滤器起初允许203前缀、拒绝198前缀；SSH owner进入Safe Mode后由串口切换root，要求既有缓存转为允许198、拒绝203；Ctrl-D之后由独立串口确认恢复原结果及Safe Mode关闭；禁用connection三秒后要求该表路由为空。整个证明同时要求只有一次BGP OPEN、一次前缀通告和零route-refresh；收到意外refresh就失败，不重新通告后冒称缓存成功。

每个BGP帧上限4096字节、缓冲16 KiB、总输入1 MiB、256条消息，speaker自身最长450秒并受相同480秒namespace总限。bootstrap和SSH读写继续受既有字节/行/时间限制。输出只允许`bgp_cache_fixture=fixed-single-peer-ipv4`及固定布尔证据，不输出raw路由transcript、AS path、密钥或目标值。

独立可信CHR脚本和仓库`--ssh --bgp-cache`入口均已实际复现上述固定case，仓库入口退出码0：baseline、无refresh切换、rollback、禁用后空缓存、单次OPEN/UPDATE及零refresh证据全部为true，generated-policy与dual-stack标志保持false。前置串口用例2.51秒、SSH取消2.70秒、SSH Confirm1.18秒也通过，结束时`guest_state_discarded=true`。这个手写单peer IPv4结果不证明生成策略、双栈、全部缓存情形或数据面转发，相关生产门槛不自动开启。

## 独立断开用例

```sh
/usr/bin/python3 -I -S -B tools/testing/routeros/run.py \
  --image /absolute/path/chr-7.24.6.img \
  --qemu-root /absolute/path/unpacked-qemu \
  --test-binary /tmp/moedove-routeros-native.test \
  --ssh-disconnect
```

此模式自行准备固定SSH/API bootstrap，只运行精确selector `^TestNativeCHRSSHDisconnectRollback$`，不执行串口/普通SSH/live/BGP测试，也不启动串口代理。它与`--ssh`、`--properties`、`--live`及`--bgp-cache`均互斥，仅给该测试进程增加`MOEDOVE_CHR_FAILURE_TEST=1`，同时保留原固定fixture开关。基础镜像、工具、凭据、端点、namespace与资源限制均不变。

该独立用例已在可信实验中通过4.10秒：主动终止本地SSH进程后，原API连续两次确认表名/ID消失且owner关闭，实际等待3014毫秒，无Ctrl-D、Confirm、Remove或重连。用例保留10分钟等待上限与11分钟context；本次约3秒不能替代网络半开/黑洞的超时证据。只有本模式总wall为900秒（另5秒强杀宽限），Go timeout为12分钟，父进程730秒；其他模式继续使用480秒总wall。

正常证据只输出固定布尔和`ssh_disconnect_wait_ms`（规范整数0..600000），失败保留固定安全错误分类。解析器必须看到实际用例执行、PASS、原API撤回及owner关闭证据，不能由进程结束或超时推断成功。测试二进制使用`ssh_disconnect_version_verified=true`的布尔日志合同。仓库`--ssh-disconnect`入口已独立原生复现，退出码0：用例4.08秒、wait_ms=3014，精确表名/原生ID消失、owner关闭及没有手工清理的证据均通过，`guest_state_discarded=true`。最坏等待预算未缩短，验证范围仍仅为主动结束本地SSH进程。

## 隔离与生命周期

新增执行路径采用独立入口，不能与普通`--live`、`--properties`、`--bgp-cache`或断开模式混跑：

```sh
# 两个实际BGP对端验证生成策略、透明属性与缓存撤回。
/usr/bin/python3 -I -S -B tools/testing/routeros/run.py \
  --image /absolute/path/chr-7.24.6.img --qemu-root /absolute/path/unpacked-qemu \
  --test-binary /tmp/moedove-routeros-native.test --ssh \
  --bgp-generated-ipv4 --live-test-binary /tmp/moedove-routeros-live.test

# 同一个owner SSH流中的八类受限命令。
/usr/bin/python3 -I -S -B tools/testing/routeros/run.py \
  --image /absolute/path/chr-7.24.6.img --qemu-root /absolute/path/unpacked-qemu \
  --test-binary /tmp/moedove-routeros-native.test --ssh --case owned

# 延迟加密命令并保持data→EOF顺序；terminal包须单独go test -c编译。
/usr/bin/python3 -I -S -B tools/testing/routeros/run.py \
  --image /absolute/path/chr-7.24.6.img --qemu-root /absolute/path/unpacked-qemu \
  --test-binary /tmp/moedove-routeros-native.test --ssh --case owned-delay \
  --terminal-test-binary /tmp/moedove-routeros-terminal.test
```

`--case batch1000`和`--case cleanup`还要求`--live-test-binary`。它们分别只运行`TestNativeCHRLiveRuleBatch1000Cancel`和`TestNativeCHRLiveAutomaticCleanup`。较长实验的固定清单为`batch1000`、`cleanup`、`discard`、`discard-global-tail`、`review-reuse`、`discard1000`、`discard1000-global-tail`，使用900秒总wall、600秒Go/610秒父进程期限；已核实旧360秒CPU额度会提前终止QEMU，这些明确较长案例现用600秒CPU，内存、进程和文件限制不变。固定案例名之外的selector不能执行。批量场景的两层读取版本已通过366.42秒：2004条规则全部ACK，Stage265637ms、完整Inventory43次、对象/身份读取各16次，最终Strict与取消后的完整checkpoint/凭据相等；自动清理实验已通过127.64秒，10个原批准目标按计划删除，Confirmed与新Strict状态保留。

以下入口也要求`--ssh --live-test-binary`，每次只运行一个固定案例：

| `--case` | 检查范围 | 已有原生结果 |
| --- | --- | --- |
| `discard` | 取消后关闭旧服务，新连接精确删除原新增对象，恢复原基线，再完成新bootstrap | 67.08秒通过，11次Remove/11次ACK |
| `presence` | 一个精确规则ID的存在、删除及两次已完成的缺失查询 | 1.96秒通过，使用240/250秒Go/父进程及480秒总wall |
| `review-reuse` | 已管理更新取消后的只读审阅，保留对象ID/认证引用，零Prepare复用并确认新事务 | 53.89秒通过，零AddRule |
| `discard1000` | 1000前缀准备取消后的逐对象缺失证明模式 | 首次原生未通过；原证据保留，不算完整清理 |
| `discard-global-tail` | 规则ACK后由真实全量尾读证明整个前缀消失 | 58.97秒通过，6规则/1批，11对象精确删除并恢复原基线 |
| `discard1000-global-tail` | 千前缀准备、取消、新runtime批量删除及最终原基线 | 优化后在共享540秒截止处未完成：正式732/2009，797次ACK；65个ACK没有已提交尾证明 |
| `selection-scale` | 2004 generation规则的逐项getter与完整布尔分区对照 | 101.44秒通过；两种状态全部属性、ID、链序、摘要与Observe一致 |
| `discard-bounded` | 小规模global-tail分次返回7/11后关闭服务，fresh只读Recover，再四次显式单项继续至11/11及新bootstrap | 59.12秒通过；5次显式请求，fresh恢复0写，11个原ID各一次删除，最终新bootstrap确认/Strict通过；不宣称大规模吞吐完成 |
| `transition-budget` | 同peer ID的IPv4→IPv6、MD5 A→B及ASN/router ID变更后取消 | 51.38秒通过；4次基线/9次更新激活，实际history最大值及增量和均为9，原ID/属性/凭据恢复 |
| `device-reset` | 确认基线后在 Safe Mode 更新待确认时硬重启 CHR，重新连接并只读核对事务 | 61.44 秒通过，未确认候选仍在设备上，旧确认及新规划审阅均拒绝 |
| `device-restore` | 同样触发硬重启，另以当前候选和原日志审阅、暂存、确认一笔恢复交易 | 74.37 秒通过，原策略、认证及非 FIB 表严格读回匹配，恢复日志重新打开成功 |
| `cleanup1000-confirm` | 确认千前缀 A，再确认单前缀 B，分次清理 A 的 2004 条过期规则，在已验证批次边界重开并只读恢复 | Go 原生 1130.07 秒通过，15 批共 2004 次 ACK 和全量尾读，两次重开零写入，最终严格审阅通过。外层资源报告随后报错，完整记录见 `.cache/native-confirm-cleanup1000/results.json` |

`cleanup1000-confirm` 的整次实验预算为 1200 秒。Go 进程 1260 秒，父进程 1270 秒，外层 1440 秒，CPU 960 秒。每次 API 命令仍为 15 秒，监督器单次操作 30 秒，每次暂存的确认窗口 90 秒，准备预算 7 分钟，SSH 会话 600 秒。该固定案例从一批已读回的进度重新连接，恢复阶段检查同一事务、确认标志与零写入，再由每次最多两批的请求继续。验收要求所有 2004 个原规则得到真实移除 ACK，已完成计数与全量尾读一致，B 的活动策略和原认证密钥经严格读回保留。首次运行的资源报告未识别 960 秒的预算，Go 原生通过后外层退出码为 1。报告路径已修复并通过离线回归，原生来宾未重跑。

两种硬重启用例的私有 QMP 都只允许一次固定 `system_reset`，收到 QEMU 的回执和本次 host RESET 事件后才给 Go 用例确认。原 QMP 路径在 Go 用例启动前移除，唯一触发入口使用私有 Unix socket 并绑定 Go 子进程的 PID 和 UID。Go 预算 300 秒，父进程 310 秒，外层 480 秒。镜像、临时覆盖层和原网络隔离与其他短用例一致。`device-restore` 还要求独立的恢复审阅、暂存、确认和日志重开证据，不能用只读结果替代。

`discard1000`和`discard1000-global-tail`保持9分钟测试context、15秒单命令、180秒完整观察及90秒确认窗口，分阶段记录有界I/O计数和耗时。RS只参与BGP控制平面，非FIB验证是不装转发表/不引流到RS，不要求数据包经过RS转发。固定合同要求所有原新增ID单次删除、最终Strict原基线/凭据/Review一致；缺阶段、计数矛盾、未知字段或错误均不能当作成功。仅大表对照已经证明的固定版本只读选择器gate已开启；生产写门仍关闭。9分钟是单次夹具预算，不是要求产品必须一次清完的用户约束。732/2009表示已提交全量证明的进度，797次ACK不能替代最终原基线验收；后续优先验证安全分次、只读恢复与显式继续。

`transition-budget`只运行`TestNativeCHRLiveIdentityAuthTransitionCancel`，Go300秒/父310秒/外层480秒；原context240秒、API15秒、Observe120秒、Prepare180秒及确认90秒不变。输出只接收固定文件、操作枚举、0..80的真实history和有界耗时。必须有连续的4次基线和9次更新操作、逐步delta与最终汇总一致，以及原ID/属性/MD5/auth引用和prepared checkpoint恢复；不接受ACK替代读回，也不将单场景推广为完整转换矩阵或history容量证明。三项race守卫、105项Python合同检查及live vet通过；已冻结fixture的IPv4断言从显示标签`ipv4`修正为实际类型`ip`，旧binary和差异记录保留。

IPv4 生成策略实验仅在显式模式下增加两个固定 guestfwd 和独立命名的 socket chardev。来宾 `10.0.2.100/101:179` 分别连接 namespace 内 `127.0.0.1:11790/11791`。真实接收端解析有界 BGP UPDATE 和撤回，维护 Adj-RIB-In，核对来源及 origin、透明 AS_PATH 和下一跳，以及策略切换与取消后的已缓存路由。双方各一次 OPEN，来源一次通告，零 refresh。

IPv4 和 IPv6 生成策略用例都要求在初始通告、切换和取消后读取实际 `/routing/route`，核对两个测试前缀均只出现在专用表中，并通过 API-SSL 读回原生 `fib` 开关未设置。当前获准前缀必须可读，`main` 或其他表中的测试前缀、缺少查询完成标记或附加输出均判失败。两套隔离 CHR 用例分别在 64.69 秒和 65.68 秒通过。

`owned`仓库入口通过7.97秒，覆盖字面量`$`、实例/连接字段、认证设置/清空、AF unset和启停；进入Safe Mode后API只读。`owned-delay`通过6.23秒：代理在固定loopback端口12223转发到12222，仅缓冲加密流并保留EOF顺序；释放前owner仍持有，此时的baseline不被当作回滚，释放后恢复且有界观察未发现晚写。它不解密、不输出命令/密码或路由原文，不代表所有网络故障均已验证。

- bubblewrap 使用全部独立 namespaces、空环境和新 PID 会话；仅短暂授权可信 bootstrap 升起私有 `lo`，随后归零能力并启用 `no-new-privs`，才启动 QEMU 和 Go 测试。
- 新网络 namespace 只有 loopback。QEMU 使用 TCG、`restrict=on` 的私有用户网络，默认只转发该 namespace 内 `127.0.0.1:18729` 到来宾 API-SSL；仅 `--ssh` 增加 `127.0.0.1:12222` 到来宾22，仅`--bgp-cache`增加上述固定guestfwd及namespace内11790 listener。不会使用主机网络、KVM或外部网络。
- 镜像、测试二进制（包括可选live二进制）、可信 Python 脚本、必要 Python/动态库、QEMU `usr` 子树均只读挂载。`/etc` 只有仓库内的合成账号/hosts 配置，不映射主机 home、凭据或已有 socket。
- 根目录和 `/dev` 只读；唯一文件存储写层是 768 MiB 的 `/scratch` tmpfs。来宾磁盘使用 `snapshot=on`，overlay、API 临时证书、随机测试密码、可选 SSH 临时身份/公钥 pin、串口 socket、context 文件都只在该 namespace 中存在，不导出产物。
- 限制：普通模式480秒外层wall，`--ssh-disconnect`及上述较长案例固定清单为900秒，均再给5秒强杀宽限；每进程CPU为普通360秒、上述明确较长案例600秒；4 GiB地址空间、128进程、256文件描述符、256 MiB单文件、禁core dump不变。QEMU使用1个vCPU和768 MiB RAM；Go设定`GOMAXPROCS=2`、`GOMEMLIMIT=256MiB`。普通SSH用例有独立120秒context及10秒回滚读回上限，断开用例另按上述长等待预算。
- bootstrap串口输出和代理字节分别受1 MiB上限约束；每个Go测试进程输出上限64 KiB，只转发已知布尔/版本/固定错误分类、属性枚举及上述有界数字。Go/父进程期限：普通用例150/160秒、属性180/190秒、前三个live用例240/250秒、三个Supervisor及只读规模/transition-budget用例300/310秒，独立断开用例720/730秒，较长案例600/610秒。SSH密钥转换输出上限8 KiB、扫描上限16 KiB，工具也有独立时限。总时间到达即终止并销毁来宾；断开及较长案例900秒，其他组合仍共同受480秒外层wall限制。不会输出原始终端尾部、密码、密钥值或设备inventory。
- 任何异常均终止子进程；PID namespace 和 `die-with-parent` 覆盖外层超时/中断。退出后销毁全部来宾状态。没有失败后自动重试写入、手动删表伪造回滚或自动保留故障镜像。属性模式重新登录仅用于已完成取消证明后的独立实验。

有效运行必须同时出现该测试确实执行、PASS 和明确的 `rollback_readback_verified=true`。仅退出码 0、默认 Skip、空测试或未经验证的“取消成功”均不构成通过。

## 离线契约检查

以下只检验参数构造、隔离挂载、镜像拒绝和输出证据规则，不启动 QEMU，也不是原生证据：

```sh
/usr/bin/python3 -I -S -B tools/testing/routeros/test_harness.py
```

目录中的脚本是可信测试基础设施。更新脚本、QEMU 参数或隔离配置后，应重新审阅，并重新运行这条实际 CHR 用例；工具不会修改生产 Go/Web 功能或自动开放审批/应用权限。

## Isolated IPv6 transport and generated-policy validation

The separate `--ssh --bgp-generated-ipv6 --live-test-binary ... --passt-binary ...`
mode uses a second guest NIC and a fixed private UNIX stream to passt. Management
API/SSH retains the existing restricted IPv4 SLIRP network. The two BGP clients
use real IPv6 sockets with distinct fixed documentation source addresses; an
IPv4 proxy is not accepted as IPv6 transport evidence. The bootstrap configures
only `lo` inside a fresh network namespace, adds no default route, and drops all
capability sets before runtime code starts. No host TAP device is created.

The fixed transport has one `[::1]:13790` to guest TCP/179 mapping. The guest's
second interface receives `2001:db8:ffff::15/64` as fixture topology, before any
Go-generated BGP policy. UDP, DHCP, DHCPv6, router advertisements and DNS discovery
are disabled. ICMPv6 remains enabled because this passt version's `--no-icmp`
also disables the NDP needed for IPv6 replies. The namespace contains only the
private loopback interface and fixed test addresses.

The initial Debian passt binary was SHA-256 checked against its package index,
but its new user namespace left a nonzero capability bounding set after dropping
effective/permitted capabilities. The fixture therefore uses the one-line
[capability patch](passt-capability.patch) against upstream tag
`2026_07_28.f8df3f1`, commit `f8df3f1b228fe19a74a269334fdfe6cc7d0605ce`.
The [official source archive](https://passt.top/passt/snapshot/passt-2026_07_28.f8df3f1.tar.gz)
has SHA-256 `2b3effb3dcd1f6b1b46da3a2419c037508d24bc39f90513bbbb660e8eb6520e5`.
The patch retains `CAP_SETPCAP` only until the existing bounding-set clamp and
final capability drop. This does not assert that initialization itself has no
capabilities: creating a new user namespace temporarily grants capabilities in
that new namespace. The launcher requires all five capability sets to be zero,
`NoNewPrivs=1` and seccomp active after initialization and before starting QEMU.
Do not mount an adjacent unpatched `passt.avx2`; passt can select that executable.
The locally built fixture binary has SHA-256
`e76f2977308a76ad2c19795400bd44f7c323ec03e8a62546a6234a67e2abd464`.
It is a private test dependency, not part of the application container.

[QEMU documents the passt stream backend](https://www.qemu.org/docs/master/system/devices/net.html),
and [passt documents its address and forwarding controls](https://passt.top/builds/latest/web/passt.1.html).
Runtime source proof must still come from two bounded reads of the CHR's actual
BGP session `remote.address` and `remote.as`, while both independent wire peers
remain established. Intended connection configuration and host socket addresses
cannot replace that proof. The fixed native IPv6 experiment passed on 2026-11-14 (59.84 seconds in the Go
test, excluding guest startup). Both actual source pairs were read twice from
the CHR session menu using `print as-value`; terminal records allow CR, LF and
CRLF without swallowing the next record. The wire evidence proved source/origin
binding, transparent AS_PATH and IPv6 next-hop, no reflection to the source,
policy-only cached withdrawal/switch, and exact cancellation back to the original
prepared checkpoint and credentials. Each peer opened once; the sender announced
once and no refresh occurred. The guest was destroyed after completion. This is
a fixed BGP control-plane fixture, not packet-forwarding or full production
capability validation; production gates remain closed.


`--case discard-bounded`只选择
`TestNativeCHRLivePreparedDiscardBoundedContinue`，使用300秒Go、310秒父进程、
480秒外层wall，保留小场景240秒context/15秒API/120秒Observe/90秒确认窗口。
第一次MaxBatches=2须在一个连接和一个真实全尾核对的六规则批次后正常返回7/11，
关闭旧服务并fresh只读Recover不得增加写次数；后续四次MaxBatches=1分别返回
8/9/10/11，只有最后一次允许Complete。最终原基线、凭据、Strict、11个原ID
各一次Remove，以及后续新bootstrap确认仍全部必需。它验证显式继续的可用闭环，
不是要求RS转发数据包，也不以此声称1000前缀单次清理或生产能力已获授权。


上述分次闭环已在root终端73339原生通过，Go用例59.12秒、入口exit0：
第一次7/11正常Pending后，fresh Recover仍7/11且0写；第2..5次明确请求分别
推进到8/9/10/11，最后才Complete。6个规则ACK由1次真实完整尾读证明，
Absent回调0；11个原对象恰好11次Remove/11次ACK、禁止调用0，原基线与凭据
恢复、fresh Review空基线及新bootstrap Confirm/Strict均通过。没有自动后台继续
或手工清扫。停止前QEMU仍运行、CPU360秒限额匹配，随后guest stopped/discarded、
fixture_completed=true。此结果不增加任何期限或生产写能力，也不证明千前缀吞吐。
`.cache/discard-bounded-checks/native-result.json`为根代理终端摘要转录，明确不是raw log。
