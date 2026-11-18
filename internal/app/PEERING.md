# 双边互联请求与对等矩阵

双边互联是成员之间的管理约定，与控制台中配置的 route-server peer、监控采集到的 BGP 会话状态分别展示。`accepted` 不代表 Established，不创建对等体、修改网络分配或下发路由配置。

当前选中成员的普通成员可发起请求；只有接收方成员管理员可以同意/拒绝。双方成员管理员可撤销，运营管理员也可做明确的管理撤销；普通发起人只能撤回自己尚未决定的请求。员工账号不能冒充成员同意互联。请求同时固定双方 ASN；ASN 变化或旧记录未固定 ASN 时，只保留历史，需撤销并重新申请，不沿用旧同意。每次决定都重新检查当前权限、成员状态、固定 ASN 和实际 VLAN ID，不把不同交换网络上相同的 VLAN tag 当成共享 LAN。

只有 active、具有 ASN 的互联成员参与发现。成员的 `peeringManagerEnabled` 与 `peeringMatrixEnabled` 分别控制互联发现/请求和矩阵参与；省略时保持旧数据的默认启用行为。VLAN 的 `peeringManager` 与 `peeringMatrix` 同样独立，省略时启用。`private: true` 的 VLAN 始终不向成员发现、请求或矩阵开放，即使显式打开上述功能标志。

IX-F 的 `excludeIxf` 只控制公开导出，不等于禁止已经共享 LAN 的登录成员互联；它不替代上述专用隐私开关。员工可以查看完整配置清单，包括私有 VLAN，但成员仅看到当前会员和允许发现的共享 LAN 伙伴，不看到无关公司的双边约定。成员矩阵里的 route-server 行只包含本成员的实际配置。

双方有效共享条件存在时，请求详情提供本 VLAN 的当前接入地址，供人工配置。组织关闭、连接禁用或隐私选项变更后，历史请求继续保留，`topologyValid` 变为 false、当前对端地址被隐藏，不能继续接受旧请求；不阻止成员离网或网络资源维护。

状态只有 pending → accepted/rejected/revoked，以及 accepted → revoked。每次变化追加带时间、行为、操作者标识和备注的事件，不替换原事件。成员响应不公开对方人员姓名、账号 ID 或邮箱；管理员审计可追踪账号 ID。历史最多 10000 条，每个成员最多 100 个待处理请求。

只有动作显式选择 notify 且 SMTP 已启用时，才向对方当前成员管理员的邮箱入队。不存在合适收件人或邮件服务关闭时，操作会明确报错，可取消通知选项后提交。邮件使用加密发件箱，发送前复核接收方管理员权限并跳过已被后续事件替代的通知；运营管理员撤销时可通知双方。接收地址不向其他成员返回，不在本次开发测试中实际投递外部邮件。

接口：`GET /api/peering/partners`、`GET/POST /api/peering/requests`、`POST /api/peering/requests/{id}/decision`、`POST /api/peering/requests/{id}/revoke`、`GET /api/peering/matrix`。服务端提供 canDecide/canRevoke，前端不应从缺失行推断“无关系”或自行拼造权限。
