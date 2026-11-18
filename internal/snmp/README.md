# 交换机只读采集、发现审核与诊断

`ixp-snmp` 使用锁定版本 `github.com/gosnmp/gosnmp v1.47.0`，真实读取交换机的标准MIB。管理API只接收类型化快照，不存储SNMP用户名、认证口令、隐私口令或community，不提供任意OID查询或SNMP SET接口。开发和验证只使用模拟传输/本地测试服务器，没有访问真实交换机。

## 准备采集器

在能访问交换机管理网络的独立采集主机创建专用普通用户。UDP客户端不需要root或网络管理capability。交换机上配置仅可读取所需MIB的SNMPv3用户和来源ACL，然后建立0600本地文件：

```json
{
  "endpoint": "https://ixp.example/api/devices",
  "switchId": "switch-inventory-id",
  "target": "192.0.2.10",
  "port": 161,
  "version": "3",
  "username": "replace-with-local-snmp-user",
  "authProtocol": "SHA256",
  "authPassphrase": "REPLACE_WITH_AUTH_SECRET",
  "privacyProtocol": "AES",
  "privacyPassphrase": "REPLACE_WITH_PRIVACY_SECRET",
  "contextName": "",
  "timeoutSeconds": 3,
  "retries": 1,
  "mappings": [],
  "collectEntities": true,
  "collectSensors": true,
  "collectFdb": true
}
```

凭据只从私有普通文件读取，拒绝符号链接与组/其他用户可读权限。不要把真实配置提交到Git，也不要将口令放进命令参数。v3固定使用authPriv；默认SHA256/AES，可显式选其他支持的SHA/AES变体。不会在认证失败后自动降级。旧设备v2c必须同时设置 `version:"2c"`、`allowV2c:true` 和私有community；它不提供加密，应限制在受控管理网络。v2c不接受v3的contextName。不同community/VLAN上下文应建为独立逻辑交换机，避免复用同一计数器来源。

在账户安全页面签发 `devices:write` 采集Token，并明确绑定 `switchIds`。将Token保存为独立0600文件，或通过 `--token-stdin` 传入；它不进入SNMP配置快照。读取设备状态的机器Token可使用 `devices:read`，同样必须绑定交换机。两种Token仅工作人员可签发；浏览器成员账户只读自己当前端口的安全诊断。

## 先发现、再审核

```sh
ixp-snmp discover --config /etc/moedove/switch.json \
  --token-file /etc/moedove/device.token

# 或只保存私有离线快照，不上传
ixp-snmp discover --config /etc/moedove/switch.json --output discovery.json
```

发现数据形成不可变提案，**不会自动创建端口、修改成员归属或改变设备配置**。管理员在控制台预览真实ifIndex、名称、速率、MTU、状态、硬件身份、标准传感器和FDB，选择要新建或绑定的接口，并基于当前库存校验和提交审核。

现有端口只更新经审核的SNMP映射和身份锚点（ifName、ifType、物理MAC、稳定engineIdentity）；成员、库存名称、速率和期望状态都保留。新端口默认inactive且无成员。已有映射改到不同ifIndex或已批准的SNMP身份发生变化，都需要显式replaceMapping；整批映射交换/冲突检测采用一个事务。审核期间库存/连接/配线关联变化、发现超过24小时、或最新观测表明设备重启/身份变化，都会拒绝旧提案，要求重新发现或刷新。

只选择需要管理的物理接口。IF-MIB也包含VLAN、loopback和聚合逻辑接口；LAG流量若已按物理成员采集，不应再把逻辑聚合接口相加，避免重复统计。

## 明确映射后持续观测

审核后将实际端口ID/ifIndex写进私有配置的mappings，例如：

```json
"mappings": [
  {"portId":"port-inventory-id","ifIndex":7}
]
```

```sh
ixp-snmp observe --config /etc/moedove/switch.json \
  --token-file /etc/moedove/device.token --interval 30s

ixp-snmp observe --config /etc/moedove/switch.json \
  --token-file /etc/moedove/device.token --once
```

每次采集**之前**，采集器向 `/api/devices/bindings?switchId=...` 获取当前ownershipRevision，只采纳修订标识，不自动改变本地端口ID/ifIndex。随后真实SNMP读取带着该修订上传。服务器会再次验证映射、稳定接口/引擎身份和所有权；SNMP引擎ID按原始二进制字节编码后哈希，不经过有损UTF-8转换。普通engineBoots变化只重建基线，不冒充稳定硬件身份变化。ifIndex被复用于不同接口时会拒绝观测并要求重新发现审核，避免把另一端口流量记给旧成员。如果采集期间发生重分配，整批旧修订观测被拒绝，即使设备时钟偏差让时间戳看起来较新也不能绕过。

服务器从控制图派生成员归属，不信任采集端的归属。核心/未分配端口也可采集，历史标为无成员，仅工作人员可见。成员看不到其他端口、全局设备身份、SNMP别名、累计错误/八位组、序列号或FDB。成员诊断还校验服务器缓存的所有权/ifIndex/代次，不会把完整IF表中未明确映射的行分享给成员。

八位组以十进制字符串保存，64位Counter64不经过浮点转换。时间差只用于最终bps计算。优先IF-X高容量计数器；只有真实Counter32才回退，缺失值不补零。ifCounterDiscontinuityTime、引擎启动代次、uptime回退、ifIndex及所有权代次都会重建基线。32位计数器可能多次回绕的区间不会生成猜测速率。失败轮询只记录通用错误状态，不注入零值样本。

包数与事件计数使用可选的 `unicast`、`multicast`、`broadcast`、`errors`、`discards` 对象。`in` / `out` 分别是十进制字符串；某方向缺失时省略该方向，两向均缺失时省略整个对象。真实计数零写为 `"0"`。每方向独立优先64位高容量计数，按需回退32位；混合方向通过 `inBits` / `outBits` 表示，不缩窄另一侧或补零。共享 `bits` 仍用于同位宽方向。旧 `inErrors` / `outErrors` / `inDiscards` / `outDiscards` 保持兼容，存在新旧字段时必须一致。

| 指标 | 优先 IF-X 列（入/出，Counter64） | 回退列（Counter32） |
| --- | --- | --- |
| 单播 | 7 / 11 | IF-MIB 11 / 17 |
| 组播 | 8 / 12 | IF-X 2 / 4 |
| 广播 | 9 / 13 | IF-X 3 / 5 |
| 错误 | 无 | IF-MIB 14 / 20 |
| 丢弃 | 无 | IF-MIB 13 / 19 |

这些列来自 [RFC2863 IF-MIB](https://www.rfc-editor.org/rfc/rfc2863.html)。只有正确的Counter32/Counter64 PDU才可作为计数器，Gauge或错误值不会转成零。不用废弃的NUcast总和拆分组播/广播，不用系统总收发包数冒充单播。出方向包计数包含上层请求但被丢弃或未发送的包，不能直接称为成功线上发包；错误/丢弃是事件计数，不是字节流量。入库与速率推导仍须独立验证归属、重启/不连续标记和每方向位宽变化。

`Snapshot.Clone()` 会复制新增计数器指针、传感器数值指针和FDB VLAN列表，避免缓存/消费者间共享可变数据。快照继续受8MiB总上限约束，不包含SNMP认证材料。

## 固定MIB与边界

- SNMPv2-MIB：sysDescr/sysObjectID/sysUpTime/sysName/sysLocation/sysContact。
- IF-MIB、IF-X：名称/别名、MTU、速率、admin/oper状态、物理MAC、八位组、单播/组播/广播、错误/丢弃及不连续标记。
- ENTITY-MIB：机箱/模块身份、型号、序列号和标准ifIndex别名关联。
- ENTITY-SENSOR-MIB：标准类型/scale/precision原始值与单位；仅关联到已知接口的传感器可用于对应端口。Watts等标准光学功率可显示，不猜测私有厂家OID或RX/TX方向，不伪造dBm。
- Q-BRIDGE/BRIDGE：FDB ID、MAC、桥端口、VLAN与ifIndex。缺失标准关联时保留未知，而非猜测。FDB/登记VLAN或MAC不一致仅作为工作人员诊断，不自动改配置。

单轮总时限30秒、单请求最多5秒/2次重试；最多2048接口、4096实体/传感器、16384FDB记录、25万PDU值、32MiB读取预算、8MiB上传快照。缺失标准MIB会明确记录缺失/不可用；达到边界不会静默伪造完整数据。观测间隔5–60秒，默认30秒。所有上传使用HTTPS且拒绝重定向，防止Token跟随到其他主机。

## 持久化与恢复

`devices/discovery-<id>.json`是0600不可变发现快照，由state中的SHA-256引用；`devices/observation-<switch-hash>.json`是原子替换的最新观测与服务器所有权标记。热state只保存发现元数据，实际流量历史写入独立telemetry日志。备份包含两类文件；停止/关闭的旧实例不能继续读写新实例缓存。

若快照文件损坏，API拒绝将其用于审核。最新观测缓存损坏时应停止服务，保留故障副本，修复/恢复对应缓存或移除可重建的观测缓存后重新采集；不要篡改不可变发现校验和。新的首次计数器会重建基线，而不是继承未知速率。

验证覆盖真实计数器精度、32位回退、固定OID/解析界限、私密配置、HTTP重定向、采集前绑定顺序、提案冲突/原子映射、同ifIndex租户重分配、未映射行隔离、计数器中断、设备重启与关闭实例边界。硬件型号/厂商私有光模块MIB需在对应设备实验环境另行验证。

参考：[GoSNMP](https://github.com/gosnmp/gosnmp)、[RFC2863 IF-MIB](https://www.rfc-editor.org/rfc/rfc2863.html)、[RFC6933 ENTITY-MIB](https://www.rfc-editor.org/rfc/rfc6933.html)、[RFC3433传感器](https://www.rfc-editor.org/rfc/rfc3433.html)、[RFC4363 Q-BRIDGE](https://www.rfc-editor.org/rfc/rfc4363.html)。

## 配额、提案处置与缓存清理

发现原始快照和最新观测缓存各有独立128MiB配额；发现元数据最多1024条，最新观测最多4096台设备。待审核证据不会因达到配额而自动删除。

工作人员浏览器可查看 `GET /api/devices/usage`：发现区显示usedBytes/quotaBytes/fileCount/recordCount/recordLimit，观测区另列出缓存对应switchId、checksum、size和是否已删除/过期。仅浏览器管理员可以执行以下显式操作，机器Token没有处置或清理权限：

- `POST /api/devices/discoveries/{id}/dismiss {reason}`：放弃待审提案，不改库存。
- `POST /api/devices/discoveries/{id}/prune {reason}`：只删除已应用或已放弃的原始快照，保留元数据、校验和、审核基线以及映射变更摘要；之后不能预览或应用已删除原文。
- `DELETE /api/devices/discoveries/{id} {checksum,reason,acknowledge:true}`：仅对已经prune的终态记录清理元数据槽位。先备份/导出需要保留的详细历史；不可修改的审计记录继续保留提案ID、交换机、校验和、审核基线和映射变化摘要。
- `POST /api/devices/observations/clear {switchId,checksum,reason,acknowledge:true}`：显式清理指定设备的最新诊断缓存，不删除流量历史。checksum来自usage列表或状态中的observationChecksum；采集器恰好更新缓存时会返回冲突，须刷新后决定是否再次清理。

活跃采集器会重新填充已清空缓存，因此通常应先停止对应采集器，或优先清理已删除/停用设备。没有新观测的首次数据会重新建立速率基线。清理操作先留下审计意图，文件系统失败时可修复后重试；启动时会回收已经标记pruned但因崩溃未删除的原始文件。
