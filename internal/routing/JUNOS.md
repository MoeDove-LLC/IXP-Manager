# Junos 配置与事务下发

Junos 适配器生成真实的 XML 配置候选：每台路由器拥有 `moedove_<稳定标识>` 配置组，组使用 NETCONF `operation="replace"` 更新。删除对等体后再次生成时，旧的托管对等体会随配置组替换而消失；其他配置组和设备管理配置不在替换范围内。生成的 `routing-options` 明确设定整个默认路由实例的 router-id 与 ASN，因此这个配置面向专用交换中心路由服务器，不能未经检查套用到承担其他业务的默认实例。审批候选时必须检查这两个全局值；`stage` 默认在编辑前比对设备 running 中明确配置的全局 router-id/ASN，如与候选不同则拒绝。确实需要迁移这些全局值时，操作员必须显式传入 `--allow-global-routing-change`，授权会记录到私密回执。这个保护只检查明确的全局配置，无法推导其他配置组、逻辑系统或继承后的有效值，因此仍须检查目标设备的实际业务范围。

BGP 会话采用 Junos `route-server-client`，保留路由服务器需要的路径及下一跳透明性。每个会话的导入策略要求精确前缀白名单、第一跳 ASN，并限制路径至 64 跳；接受前缀数超过限制时 teardown，保持关闭直到管理员处理。导出策略重新匹配已授权的 ASN/精确前缀组合，仅接受 BGP 路由，其余拒绝。Junos 在 BGP 导出策略中忽略 `neighbor` 条件，所以本实现没有依赖它。导出策略保证前缀和 ASN 授权边界，不声称能区分从其他现存 BGP 会话学到的相同授权路径。托管组以外的设备级覆盖配置仍可能影响最终有效策略，必须检查设备候选与策略继承。

控制台的 `ValidateJunosXML` 仅进行 XML 结构检查，不等于目标 Junos 原生验证。硬件、系统版本必须支持 `route-server-client`；设备 NETCONF `validate` 是安装前的必要步骤。没有使用真实 Junos 设备完成本次测试。

## 执行

先在路由主机启用 NETCONF SSH 服务，并通过独立可信渠道把设备主机密钥加入专用 `known_hosts`。操作用户需要 candidate lock、edit、validate、commit 权限。凭据采用本机 SSH 密钥，不存入 Web 控制台。

```sh
ixp-netconf stage \
  --file approved.xml --checksum <授权下载返回的文件SHA-256> \
  --host rs.example.net --port 830 --user ixp-automation \
  --known-hosts /etc/moedove/known_hosts \
  --identity /etc/moedove/id_ed25519 \
  --receipt /var/lib/moedove/rs-change.json --timeout 300

# 检查管理连通性、实际 BGP 会话和路由策略后，再明确确认：
ixp-netconf confirm --receipt /var/lib/moedove/rs-change.json

# 或主动撤销尚未确认的提交：
ixp-netconf cancel --receipt /var/lib/moedove/rs-change.json
```

`stage` 只接受文件校验和匹配的候选；它校验设备能力、锁定 candidate、对比 candidate 与 running，若有其他人的未提交更改便直接拒绝。然后检查全局路由标识保护条件，通过后才 edit、原生 validate、带 persist token 的 confirmed-commit。编辑或验证失败时，只撤销当前持锁会话自己的更改。提交结果不确定时保留回执，由设备计时器回滚或操作员取消。

文件 SHA-256 来自授权下载响应的 `X-Config-SHA256` 或下载成功后的显示；控制台脱敏预览的审查标识不能用作文件 SHA。配置中的 `authentication-key` 为 TCP-MD5 密钥，控制台仅展示占位符和公共 revision；占位符 XML 会被执行器拒绝，不能直接复制预览安装。下载文件及设备回执需要私密保存。凭据字符限制与轮换边界见 [README.md](README.md)。

操作分为 stage 和显式 confirm 两步，绝不自动确认。确认时再次锁定并检查 candidate，防止把他人的新改动一起提交。设备原生计时器在未确认时负责回滚，不依赖客户端进程继续运行；因此不要在计时期间进行其他配置更改。连接中断、命令超时或 RPC 错误不会伪装为成功。

回执在远程操作前以 `0600` 写入并 fsync，绑定目标、用户、端口、身份文件路径、主机密钥文件 SHA-256、候选 SHA-256 与 persist token。后续状态用原子替换持久化；token 不打印到终端。回执不可发布，也不要在确认或撤销前删除。确认/撤销不会接受命令行覆盖回执中的目标。identity 和 known_hosts 必须是绝对路径的普通文件，文件名不得含空白或 SSH 配置展开字符。

SSH 固定调用 `netconf` subsystem，禁用 agent 转发、代理命令、本地命令及用户 SSH 配置，要求严格的已固定主机密钥。没有拼接远程 shell。

## 测试与边界

`go test -race ./internal/netconf ./cmd/ixp-netconf ./internal/routing` 覆盖真实协议 URI、消息编号、候选锁、脏 candidate 拒绝、原生校验失败清理、持久确认/取消、XML 命名空间、主机密钥约束及私密回执持久化。协议测试采用模拟传输；没有实机安装或 BGP 可达性验证。NETCONF 当前只协商 base:1.0 分隔符 framing，设备同时支持 base:1.1 时仍使用共同的 base:1.0。

参考：

- [Junos BGP 路由服务器](https://www.juniper.net/documentation/us/en/software/junos/bgp/topics/concept/bgp-route-server-overview.html)
- [Junos 路由策略匹配条件](https://www.juniper.net/documentation/us/en/software/junos/routing-policy/topics/concept/policy-configuring-match-conditions-in-routing-policy-terms.html)
- [Junos AS path 正则表达式](https://www.juniper.net/documentation/us/en/software/junos/routing-policy/topics/concept/policy-configuring-as-path-regular-expressions-to-use-as-routing-policy-match-conditions.html)
- [Junos confirmed commit](https://www.juniper.net/documentation/us/en/software/junos/netconf/topics/task/netconf-configuration-committing-with-confirmation.html)
- [RFC 6241 NETCONF：candidate、validate 与 persistent confirmed commit](https://www.rfc-editor.org/rfc/rfc6241.html)
