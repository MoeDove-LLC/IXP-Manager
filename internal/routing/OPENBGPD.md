# OpenBGPD 受约束适配

支持 OpenBGPD portable 8.8 及以上的双栈 route-server 和 route-collector。已用 Debian 官方 OpenBGPD 8.8 二进制执行真实离线 `bgpd -n -f` 正例；本轮没有启动原生 daemon、建立 BGP 会话或更改内核路由。

## 策略与审阅

`RenderOpenBGPD` 只接受现有 typed `Router`/`Peer`。邻居地址、本地地址、ASN、exact prefix allowlist、prefix/origin 配对、最大前缀数和 TCP-MD5 都沿用共同验证。配置没有任意 include、宏、命令、路径、network 或重分发入口。服务端 `require-valid` RPKI 在本适配器明确拒绝：原生 OpenBGPD 本身支持 RTR，并不代表本项目已完成运行时有效性与故障语义适配。

路由服务器使用独立 `MOEDOVE` RIB（没有 rtable 关联），并设置 `fib-update no`。`transparent-as yes` 保留 AS_PATH、MED 和社区透明性；`set nexthop no-modify` 保留 NEXT_HOP。`rfc1997Passthrough` 省略或为 `true` 时，没有隐式 NO_EXPORT/NO_ADVERTISE 过滤或社区改写，符合 [RFC 7947 §2.2](https://www.rfc-editor.org/rfc/rfc7947.html#section-2.2) 的代理语义。只有明确设置 `rfc1997Passthrough=false` 时，路由服务器在一般出口 allow 之后追加 NO_EXPORT、NO_ADVERTISE、NO_EXPORT_SUBCONFED deny，执行所选择的 RFC 1997 本地出口策略；AS_PATH、MED、NEXT_HOP 透明性不变。该选项纳入 typed 模型与严格完整字节重建。Collector 始终拒绝所有出口，三态选项都不能放宽它。客户端须按自身实现正确配置 route-server 邻居的首 AS 检查。`rde evaluate all` 允许在某条路径被出口过滤时继续评估其他路径。所有输入默认拒绝，只有对应邻居的 exact prefix 和可选 origin-set 配对才允许进入；首 AS 检查开启、AS_SET 拒绝、路径最长 64 个 AS、接收前缀数硬限制。

Collector 两种策略都使用 `MOEDOVE no evaluate`、`fib-update no`、每邻居 `export none`，且没有任何出口 allow。`authorized-only` 的受管 RIB 仅保留通过明确授权的路线；`observe` 还开启 `Loc-RIB include filtered`，允许区分授权与过滤的前缀。OpenBGPD 的 Adj-RIB-In 本身保留输入过滤前的原始路线，因此 authorized-only 不承诺完全不缓存被拒输入。协议层首 AS/AS_SET/无效 UPDATE 拒绝的内容不会伪装成可观察路线。

原生 `max-prefix` 对 Adj-RIB-In 计数，先于输入策略；因此被过滤的前缀也受到限制。此顺序已核对 [官方 8.8 源码归档](https://cdn.openbsd.org/pub/OpenBSD/OpenBGPD/openbgpd-8.8.tar.gz) 中 `src/bgpd/rde.c:rde_update_update`。无自动 restart 限制参数，超过限额会停止会话；不会默默开始新的洪泛周期。RIB、filter、透明性、MD5 语义见 [OpenBSD bgpd.conf](https://man.openbsd.org/bgpd.conf)。

生成文件的 `moedove-model` 头是无密码 typed 模型的 base64url 编码，不是认证凭据。`ValidateManagedOpenBGPD` 从有限语法提取密码后重新渲染并比较完整字节；任何添加、删除或弱化原生指令都会被拒绝。`RedactOpenBGPD` 只处理严格受管文件，未知语法不返回任何配置；输出密码占位符不能用于校验、下载应用或 native parser。实际 checksum 始终由原始发布字节计算。

`ValidateOpenBGPD` 核对 native `-V`、只运行 `-n -f`，要求明确 `configuration OK`；缺工具和错误文本失败，诊断不回显原始配置或密码。此 parser 会只读枚举本机接口；受限测试环境需允许对应系统调用，本轮验证在 user/network namespace 内完成。

## 本机事务范围

`ixp-openbgpd` 提供 `check`、`serve`、`stage`、`status`、`confirm`、`cancel`。发布使用专用 Linux 原生实例和独立私密 Unix watchdog socket；没有 HTTP 远端监听、任意 command 参数或用户选取的运行时 socket。

原生 administrative socket 固定为 `/run/moedove-openbgpd/bgpd.sock`，写在受管配置中，父目录必须为 0700。看护从 `SO_PEERCRED` 取得 root parent PID，再用 `/proc` 核对真实 daemon binary、完整 `-f` 目标路径和进程 starttime。独占容器中的 PID 1 也可通过上述全部证据验证；不会仅因 PID 为 1 接受其他 init 进程。可用 `--daemon-pid` 额外限定预期 PID；默认自动发现通过相同验证，便于监督器重启后恢复新实例。只允许 native daemon 的可选 `-d` 与明确 `-f target` 启动参数；不能使用 `-D` 覆盖、不同配置文件或 wrapper 进程。

必须明确传入 `--exclusive-instance`。该承诺表示此实例的配置文件、control socket 和动态 `network`/`fib`/neighbor 操作都由此看护独占。OpenBGPD 没有本适配器可用的 running-config hash 查询；身份绑定和本地 disk checksum **不等于**检测所有带外 runtime drift。其他 root 管理员的动态命令、重启参数变更或手工配置修改不在自动事务保证内；遇到磁盘未知第三版本不会覆盖它，而是保留 recovery-failed 状态等待处置。

Stage 顺序是：校验候选及回滚文件 → 核对原始 SHA 和实例身份 → 将 previous/candidate 以 0600 fsync 保存 → 持久化 staging journal → 原子替换目标 → 请求 native reload → 待确认。目标也要求 0600 常规文件，链接/FIFO 拒绝；同目标独占锁防止第二个看护抢占，绑定文件防止绕过旧 recovery journal。

`bgpctl` 即使收到 daemon 错误也可能退出 0，因此必须同时收到准确的 `request processed`。成功 reload 回应首先表示已开始分发配置；第二次相同文件的 reload 在原生 `previous reload still running` 消失后被接受，证明前一次已完成，作为密钥重置屏障。该机制不是配置 hash 查询，也不证明所有会话已经 Established。官方控制语义见 [bgpctl](https://man.openbsd.org/bgpctl)；实际回应与屏障顺序已核对同版本 `bgpd.c` 和 `bgpctl/output.c`。

OpenBGPD 文档要求 TCP-MD5 变化后重置会话。看护仅对密码实际改变且仍存在的邻居发送固定 `neighbor ADDRESS clear`，回滚也做同样处理；新增邻居由 reload 创建。单次最多改变 64 个既有认证会话，避免无限 reset 操作。生成文件不会把“语法成功”冒充认证已建立，确认前应检查真实会话。

这是**监督器支持的本机恢复，不是设备原生 confirmed-commit**。运行中的看护在 60–3600 秒确认期限到达、取消、阶段失败或正常退出时恢复旧字节并重载；journal 未确认时，重新启动先恢复，再接受请求。看护被 SIGKILL、断电或所在主机停止时无法执行定时器；必须由外部监督器重新启动。恢复失败保留 journal 并退出，监督器重试并重新验证当前原生 parent 身份。删除 state 文件、改绑定目录或绕过独占文件会破坏恢复契约。

## 操作例子

先通过受控部署下载取得审批版本和真实文件 SHA，在停止的专用实例上完成初始文件部署；配置必须保存到 0600 的 `bgpd.conf`。不要以这个完整文件覆盖现有共享路由实例。准备专用 local IP、私密控制目录、系统 `_bgpd` 用户及发行版要求的运行环境，并按站点程序启动独立 daemon：

```sh
ixp-openbgpd check --file /etc/moedove-openbgpd/bgpd.conf \
  --checksum ACTUAL_SHA256 --bgpd /usr/sbin/bgpd
# 在目标主机经运维安排启动专用实例；不是校验步骤：
/usr/sbin/bgpd -d -f /etc/moedove-openbgpd/bgpd.conf
```

看护应交给监督器启动，而非一个易消失的终端会话：

```sh
ixp-openbgpd serve --exclusive-instance \
  --target /etc/moedove-openbgpd/bgpd.conf \
  --state-dir /var/lib/moedove-openbgpd \
  --socket /run/moedove-openbgpd-agent/agent.sock \
  --bgpd /usr/sbin/bgpd --bgpctl /usr/sbin/bgpctl
ixp-openbgpd stage --file /private/approved.conf \
  --checksum NEW_ACTUAL_SHA256 --base-checksum CURRENT_ACTUAL_SHA256 --timeout 120
ixp-openbgpd status
ixp-openbgpd confirm --transaction TRANSACTION_ID --checksum NEW_ACTUAL_SHA256
# 或在确认前取消：
ixp-openbgpd cancel --transaction TRANSACTION_ID --checksum NEW_ACTUAL_SHA256
```

Stage 返回 `pending` 后检查外部真实路由与会话状态，再确认精确的 transaction ID 和新 SHA。Web 审阅标识不能替代真实文件 SHA。任务中没有执行上述 daemon/service 激活命令。

可审阅的 systemd 样例在 `cmd/ixp-openbgpd/deploy/`；不会被自动安装/enable。它们使用专用原生 unit，而不是复用可能管理共享 `/etc/bgpd.conf` 的发行版 unit。根据实际安装路径、daemon 账号和地址就绪方式审阅后使用；必须保留 Restart 监督、私密目录和关闭顺序。

## 验证

`OPENBGPD_BIN=/path/to/bgpd go test -race ./internal/routing -run '^TestOpenBGPD'` 启用真实 parser 正例；namespace 内运行可确保没有外部网络。包含 server/两种 collector、双栈、origin 配对、MD5、严格模型和脱敏防回写。watchdog 测试使用假的本地 host adapter，覆盖重载失败、确认/取消/超时、并发、真实测试子进程被 kill 后恢复、第三方磁盘变更不覆盖、固定 native 回应与重载屏障、socket 身份和私密 IPC。`cmd/ixp-openbgpd` 专项验证显式动作和文件边界。

测试只运行离线 parser、假控制回应和本地测试进程，没有连接真实 daemon，也没有写入任何真实内核路由表。
