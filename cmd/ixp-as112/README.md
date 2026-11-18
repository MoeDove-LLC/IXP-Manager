# AS112 专用实例 CLI

`ixp-as112` 把已审阅的 BGP 配置和 DNS 包绑定到一个专用目录、当前主机开机标识、network namespace，以及实际 native ELF 的路径和 SHA256。当前执行器支持 BIRD 2、BIRD 3、FRR；OpenBGPD 的正常 privsep 和远端 Junos 不属于此执行器，不能通过更换 `--platform` 绕过。

运行时与真实隔离验证的边界见 [as112ops](../../internal/as112ops/README.md)。原来的生成、下载和 DNS probe 仍见 [AS112 配置说明](../../internal/as112/README.md)。本工具不安装软件包，不配置接口地址，也不修改共享路由实例。

## 准备与安装

先取得当前已批准的 BGP 文件和 `as112-dns` 包，以及各自下载响应中的真实 SHA256。界面的 review identifier 不能代替它们。输入与计划都必须是当前用户拥有的 `0600` 常规文件；符号链接、多硬链接或可由其他用户替换的父目录会被拒绝。

使用持久、私密的布局，例如 `/var/lib/moedove-as112/approved/` 保存下载文件，`/var/lib/moedove-as112/plan.json` 保存计划，`/var/lib/moedove-as112/node1/` 作为专用实例目录。父目录需由管理员预先建立，并由运行本工具的同一账号拥有；不要指向现有共享 BIRD/FRR/PowerDNS 配置目录。

```sh
ixp-as112 prepare --platform bird2 --root /var/lib/moedove-as112/node1 \
  --active-file /var/lib/moedove-as112/approved/router.conf \
  --active-checksum ACTIVE_FILE_SHA256 \
  --dns-file /var/lib/moedove-as112/approved/as112.json \
  --dns-checksum DNS_FILE_SHA256 \
  --router-binary /usr/sbin/bird --pdns-server /usr/sbin/pdns_server \
  --named-checkzone /usr/bin/named-checkzone \
  --plan /var/lib/moedove-as112/plan.json
```

prepare 只生成新计划，不发布文件、不检查运行健康，也不启动进程。standby 严格从同一 active 模型派生为零 BGP 邻居；原批准 active 字节不变。工具 SHA 和实际 boot/netns 标识自动捕获，不能从命令行冒充目标。stdout 只含摘要和四个校验和；完整计划含原始 BGP 凭据，应按秘密文件保管。

审阅私密计划后明确提交全部校验和：

```sh
ixp-as112 install --plan /var/lib/moedove-as112/plan.json \
  --plan-checksum PLAN_SHA256 --active-checksum ACTIVE_FILE_SHA256 \
  --standby-checksum DERIVED_STANDBY_SHA256 --dns-checksum DNS_FILE_SHA256 \
  --exclusive-instance-acknowledged
```

install 检查目标 pin、实例占用与 native 配置，原子发布版本和 current 指针，保持 withdrawn。它不会启动 DNS 或 BGP。相同计划可再次核对；其他计划必须走下方的离线 replace，不能靠普通 install 覆盖旧绑定。不要删除 journal 或 current 来绕过未知结果。

## 前台运行与停止

在 DNS 节点实际配置全部 `requiredLocalAddresses`，并独立确认主机返回路径。现有 native 工具及它们的库/模块、FRR 发行版要求的运行目录也必须准备好。只有计划指定的专用实例可使用这些地址和端口。

```sh
# 默认只运行 DNS/standby，始终不授予 active 发布。
ixp-as112 run --plan /var/lib/moedove-as112/plan.json

# 明确允许同一批准 artifact 在连续健康检查后发布及恢复。
ixp-as112 run --plan /var/lib/moedove-as112/plan.json --recover-ack

ixp-as112 status --root /var/lib/moedove-as112/node1
```

默认检查间隔 5 秒，连续 2 次健康才可恢复；prepare 可设置间隔 1–30 秒、阈值 2–10。实际 DNS/地址健康失效会先停止唯一受管 BGP 进程；恢复也只使用计划绑定的原 active。新模型显式关闭 GR/LLGR，避免把断开会话当作可保留的重启路由。

`SIGINT`/`SIGTERM` 使前台 owner 退出并调用 Close，停止顺序为 BGP、DNS。若退出遇到未决结果，返回非零并保留 unknown。Linux 的 parent-death 信号保护 owner 死亡时的子进程。仍活着而挂死的 supervisor 由下方 systemd watchdog 集成监督；普通前台运行没有独立的挂起保护。

run 只在启动前和停止后输出状态，活动期间不向 stdout 写周期记录，避免日志消费者卡住健康循环。使用 status 读取已持久化的最近样本。其 `source="persisted-journal"`、`liveHealthConfirmed=false` 是明确边界：`phase=active` 仅描述最近记录的本机进程模式，不能证明当前进程存活、对端会话建立或全网路由已收敛。

## 未知结果与恢复

未知写入、文件漂移、外来进程、工具 pin 变化等会拒绝后续动作。重新 run 不会重放未知操作。先停止 owner/外部重启循环，检查实际实例，再明确要求核对：

```sh
ixp-as112 reconcile --plan /var/lib/moedove-as112/plan.json \
  --plan-checksum PLAN_SHA256 --acknowledge
```

reconcile 只核对实际零进程和精确已发布字节，再更新私密 journal；不会杀陌生 PID、启动服务或重放旧命令。核对完成后重新运行 `run --recover-ack`，仍需重新积累健康。跨宿主重启或 netns 改变的旧计划不会自动获准；使用下方独立的目标重新绑定审阅。

## 宿主重启后的显式重新绑定

新 install 在私密 status 中记录随机 instance ID、当前执行 boot/netns，并在可读取可信 `/etc/machine-id` 时记录按实例隔离的摘要，不保存或输出原始 machine ID。原 Plan 的字节和 checksum 不变。该本机身份不是硬件证明；刻意复制 machine ID、特权管理员带外并发修改不在持续独占契约内。

宿主重启后，停止服务的自动重启循环并保持专用 UID 和实例目录的独占。原 journal 可以是上次已确认的 active，但必须没有 pending/unknown；实际同 UID 可观察实例进程必须全部缺失。未完成的 replacement/retention、未知 staging、改变的 artifact、外来进程均会拒绝。先生成新的私密审阅文件：

```sh
ixp-as112 rebind-plan --plan /var/lib/moedove-as112/plan.json \
  --mode same-host-reboot \
  --output /var/lib/moedove-as112/rebind-review.json
```

这个动作只核对文件和进程缺失，不运行旧 checker、不停止 PID。审阅绑定原计划、active/standby/DNS/tool hashes、完整版本目录身份、current、status 和已完成的 replacement/retention marker。`same-host-reboot` 只在原已记录的稳定本机身份与现在相符、boot 确实已变化时成立；netns 的新身份也写入审阅。

核对具体 review 后明确应用：

```sh
ixp-as112 rebind-apply --plan /var/lib/moedove-as112/plan.json \
  --review /var/lib/moedove-as112/rebind-review.json \
  --checksum REBIND_REVIEW_SHA256 \
  --acknowledge --exclusive-instance-acknowledged
```

apply 再次核对全部前提，只原子替换 status 的执行绑定，保留原 instance ID、计划和所有 artifact/tool pins，清空旧 PID/健康计数并保持 withdrawn，不启动服务。然后同一计划可重新 `run --recover-ack`，仍需重新通过地址和 UDP/TCP DNS 健康。原 service 文件可继续引用原计划。

若重启时同时升级了 native 软件包，rebind 仍可审阅并保留原 pins，但旧工具 SHA 不匹配时 run 继续拒绝执行。此时先按下一节 prepare 新计划并显式 replace，使用新的真实 native 工具完成验证。不得直接修改旧计划或把 rebind 当作新工具的批准。

如果 apply 返回持久化结果不确定，用同一参数把命令换成 `rebind-confirm`。它只读取、报告精确 `unapplied` 或 `applied`，不会推进任何事务。明确重试 apply 时，精确新状态只确认不重写；精确旧状态必须重新通过全部核对才可提交。第三种状态、变化的材料或遗留临时文件会拒绝。这是单个状态文件的条件替换，不会重放未决服务、删除或 replacement 操作。

旧版安装若尚未重启，可在离线且无未决操作时，按原四个 checksum 再次明确 install，以记录稳定本机身份；原计划 checksum 不变。若已经重启且没有原身份，或稳定身份不可用、需要迁移/克隆/替换 netns，则不能声称已证明同宿主：使用 `rebind-plan --mode move`，审阅后在 apply/confirm 额外传 `--source-isolated-acknowledged`。该声明要求运维已在独占控制下隔离原实例及全部克隆，防止同时运行；工具不联系远端，也不证明原宿主已死亡。迁移仍使用同一绝对 root 路径，已有 instance ID 保留。

## 同主机离线更新

先停止旧 owner 并确认旧计划 withdrawn、无 pending、DNS/BGP 进程全无。用 prepare 为**同一 root、platform、router 实体及当前已批准执行目标 pin（经过 rebind 后，新计划用当前真实 boot/netns，旧计划保留原 pin）**生成新的私密计划，不覆盖原计划文件。审阅新 active/standby/DNS 字节和四个 checksum 后提交：

```sh
ixp-as112 replace --previous-plan /var/lib/moedove-as112/plan.json \
  --plan /var/lib/moedove-as112/plan-next.json \
  --previous-plan-checksum PREVIOUS_PLAN_SHA256 \
  --current-target versions/PREVIOUS_PLAN_SHA256 \
  --plan-checksum NEXT_PLAN_SHA256 --active-checksum NEXT_ACTIVE_SHA256 \
  --standby-checksum NEXT_STANDBY_SHA256 --dns-checksum NEXT_DNS_SHA256 \
  --exclusive-instance-acknowledged
```

实际 current 指针必须与提供的旧值相同。执行器重新做 native 校验并写入独立 WAL，再发布新目录、切换指针、保留旧版本；结果仍为 withdrawn，不启动任何服务。读取 status 或重新打开目录不会自动完成中断的 replace。

若文件事务中断，使用同一组参数将动作换成 `replace-reconcile`。它可在明确授权和实际文件证据下完成该精确事务的目录/指针发布，**不等同于普通 reconcile 的只读核对**；两者都不会重放未知进程操作。状态输出中的 replacement 字段显示独立事务阶段。

最多保留 8 个版本、总计 128 MiB；容量不足会拒绝，不会擅自删旧。可用下方的明确离线清理释放已审阅旧版本。完成更新后改用新计划运行 `run --recover-ack`，并重新审阅引用旧计划的 service 文件。跨 boot/netns 必须先完成上方明确 rebind，replace 本身不放宽目标绑定。

## 离线审阅和清理历史版本

先停止 owner，确认当前实例 withdrawn、无未决服务或 replacement 事务，并保持专用 UID 的持续独占。清理保护当前版本和最新 replacement 的前一版本，不启动、停止或重新加载 DNS/BGP 服务。

提供候选版本的原始私密计划文件；只有目录名不能证明归属。未提供计划的版本会保留并报告原因。旧 checker 已升级或卸载不影响原文件摘要的核对；历史读取仅用于清理，不能拿来部署旧配置。

```sh
ixp-as112 retention-plan --plan /var/lib/moedove-as112/plan-current.json \
  --history-plan /var/lib/moedove-as112/plan-old-1.json \
  --history-plan /var/lib/moedove-as112/plan-old-2.json \
  --output /var/lib/moedove-as112/retention-review.json
```

输出列出候选、保留原因、确切剩余路径及 review checksum；不删除任何内容。新的 `0600` review 文件只能写在 instance root 之外，不含配置正文或 MD5 密钥。核对后明确执行：

```sh
ixp-as112 retention-apply --plan /var/lib/moedove-as112/plan-current.json \
  --review /var/lib/moedove-as112/retention-review.json \
  --checksum RETENTION_REVIEW_SHA256 \
  --acknowledge --exclusive-instance-acknowledged
```

执行前会重新核对 current、状态/marker、同 UID 可观察实例进程、权限、实际 inode 和文件内容。逐个文件及空目录写入 intent/result，不递归删除整个目录。当前版本、保留 baseline、符号链接、hardlink、异常类型/权限及无法确认的恢复材料不会被清理。成功释放容量后可以继续离线 `replace`。

如果删除结果未知，`status` 会显示 retention 的独立 cursor/pending。重新打开或读取状态不会继续删除。用相同参数将动作换成 `retention-reconcile`，只可在实际确认未决条目已缺失时推进日志；它不删除内容。随后可再次明确 `retention-apply`，继续尚未尝试的条目。

若未决条目仍在，旧 intent 不会重发。必须先重新审阅剩余对象，取得新 checksum，再明确批准：

```sh
ixp-as112 retention-plan --plan /var/lib/moedove-as112/plan-current.json \
  --resume --output /var/lib/moedove-as112/retention-review-next.json
ixp-as112 retention-apply --plan /var/lib/moedove-as112/plan-current.json \
  --review /var/lib/moedove-as112/retention-review-next.json \
  --checksum NEW_RETENTION_REVIEW_SHA256 \
  --acknowledge --exclusive-instance-acknowledged
```

新审阅绑定原 journal、cursor 和未决 intent，且只能涵盖身份与字节仍完全相同的剩余条目。若内容、inode、链接或 marker 已变化，会拒绝继续，不能靠删除 journal 或修改 review 绕过核对。特权管理员的刻意带外操作不在专用 UID 持续独占的部署契约内。

## 可审阅的 systemd 文件

```sh
ixp-as112 unit --plan /var/lib/moedove-as112/plan.json \
  --binary /usr/local/bin/ixp-as112 \
  --output /var/lib/moedove-as112/moedove-as112.service
```

unit 只写一个新的私密审阅文件，不调用 systemctl、不安装、不 enable、不 start。默认不包含 `--recover-ack`；如要把持续恢复授权写入 ExecStart，生成时也必须明确传该参数。

生成文本里的 supervisor SHA 只是生成时注释，不是 ExecStart 的强制 hash 检查；保护已安装的 supervisor 文件仍由运维负责。`ProtectHome=yes` 与 `PrivateTmp=yes` 会隐藏 `/root`、`/home`、`/run/user`、`/tmp`、`/var/tmp` 中的计划、root 或工具，因此生成器拒绝这些位置。使用上述 `/var/lib/...` 布局并审阅实际账号、native 路径、端口权限、目录和地址准备。不要加 `PrivateNetwork=yes`，它会改变计划绑定的 netns。

单元使用 `Type=notify`、`NotifyAccess=main`、`WatchdogSec=180` 和 `TimeoutStartSec=180`。首次完整、安全地完成 controller Tick 后才发送 READY；健康失败已撤回也可表示监督循环就绪，不能解释成 DNS/BGP 已健康。之后每次 Tick 完成才发心跳，没有能掩盖监督循环卡死的后台续约 goroutine。180 秒包含现有最长 60 秒 DNS probe、最多 30 秒间隔及有限的子进程停止时间；不要未经验证就缩短正式部署的期限。协议及超时语义见 [systemd watchdog](https://www.freedesktop.org/software/systemd/man/latest/systemd.service.html)。

当 main 被 SIGSTOP 或监督循环卡住，实际运行的 systemd 会独立到期处理。模板使用 `WatchdogSignal=SIGKILL`、`KillMode=control-group`、`SendSIGKILL=yes`、`FinalKillSignal=SIGKILL`，按单元归属终止 main 和剩余子进程；平常停止仍先 SIGTERM，`TimeoutStopSec=30` 后强制结束，并禁用 core dump。CLI 不会从 notify 环境变量取任意 PID 发信号。见 [systemd 终止策略](https://www.freedesktop.org/software/systemd/man/latest/systemd.kill.html)。

CLI 检查 `WATCHDOG_PID`（若设置，必须是自身）和 `WATCHDOG_USEC`；错误 PID、无通知 socket 的 watchdog 或格式错误会在打开 controller 前拒绝。仅发送固定 READY/WATCHDOG/STOPPING 字段，内核提供实际发送者凭据，不发送配置、密钥或环境变量内容；native 子进程不继承 notify 设置。发送有时限，失败会退出并清理自己拥有的进程。成功入队不等于 manager 已接收/接管，生成 unit 也不等于保护已安装。没有 notify/watchdog 配置的前台 run 继续工作，但不具有独立挂起撤回保证。见 [sd_notify 协议](https://www.freedesktop.org/software/systemd/man/latest/sd_notify.html)。

单元保留 `Restart=on-failure`，60 秒内最多启动 3 次。unknown 或目标 pin 不匹配不会因重启变安全；达到启动上限后应先显式处理状态，再由管理员恢复服务。watchdog kill 后也不会重放 unknown WAL。服务级 watchdog 不保证 systemd 自身挂死、内核失效或不可中断内核等待时的终止；尚未进行宿主机 systemd 安装或启动验收。

## 验证范围

CLI 专项使用真实私密文件、目标/ELF pin 与真实 controller，运行适配器由受限模型提供；覆盖显式审批、原子文件发布、健康恢复/撤回、退出顺序、unknown 不重放、只读核对及 unit 文本边界。实际 DNS/BGP 子进程和双栈 peer 行为由独立隔离 native fixture 验证，不与模型结果混称。

离线清理的独立证据在 `.cache/as112-retention-final/results.json`：9 个后端测试、2 个 CLI 测试、21 个子例通过 race，两包 vet 和 CGO0 CLI 构建通过。包括满 8 版本后的确证清理与再次升级、旧 checker 不可用、current/baseline 保护、未知结果缺失确认、仍存在条目的新 checksum 再审批，以及异常权限、链接、FIFO、挂载别名和审阅输出范围。没有启动真实服务或 VM，也不属于之前的全项目冻结结果。

显式重启/迁移与完整 AS112 ops/CLI 回归见 `.cache/as112-rebind-final/results.json`（源码聚合 `979a465a2e5ce22f7d7449b48b02bc4a1ee904bc53379b7167c8ccb6714ccfec`）：35 个后端顶层测试、9 个 CLI 顶层测试及 80 个子例通过 race，1 个真实 native 集成按开关跳过，两包 vet 与 CGO0 CLI build 通过。新增 rebind 为后端 7、CLI 2、子例 24，涵盖完整 same-host-reboot、明确迁移/legacy 声明、原子提交前后只读确认、旧 active 记录清空、未知状态/进程/改动拒绝、旧 native 不在时重新绑定及后续新计划替换与跨 boot 清理。稳定身份使用私有 user namespace 中的只读合成 machine ID，宿主普通用户只映射为 namespace UID0 且移除全部 capabilities；没有复制真实 machine ID、重启宿主、启动 DNS/BGP 服务或 VM，也不扩充此前真实 peer 行为证据。

watchdog 的独立增量证据为 `.cache/as112-watchdog-final/results.json` 与 `.cache/as112-watchdog-final-native/results.json`，源码聚合 `369b2ed18024fcca4194aa459485ebdfd676b37994fd8251e854550c63296695`。4 个定向 CLI 测试、15 子例通过 race，两包 vet 和 CGO0 CLI build 通过；unit 通过 stock systemd 257 的 `systemd-analyze verify`。私有环境中的 stock manager 因未由 systemd 启动而拒绝运行，未修改宿主服务或 cgroups，不能把语法验证当成完整 systemd 运行验收。

另外的单实例验证实际运行 CLI、PowerDNS 和 BIRD 2，记录 7 次正确 sender 心跳并拒绝 1 次其他 sender；真实 DNS 失效正常撤回/恢复后，SIGSTOP CLI，使独立测试 watcher 到期终止自己启动的 owner 句柄，再观察 owned 子进程退出。这证明真实 notify 与 parent-death 链，未验证 stock manager 的 cgroup kill 路径；无远端 peer、无 router 矩阵重跑、无 VM。已有旧 service 不会因 CLI 升级自动获得 watchdog，仍须由运维审阅并安装新 unit 配置。
