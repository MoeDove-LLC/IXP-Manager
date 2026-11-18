# 配线授权书与交叉连接流程

LOA由已经登录的管理员或操作员针对**当前成员预留**签发。它固定保存签发时的成员名称/ASN、机房、机柜、配线架、端口、交换机接口、线路编号和有效期；下载时不重新生成文件，因此历史内容不会随当前库存编辑而变化。

PDF使用 `codeberg.org/go-pdf/fpdf v0.12.0`，在文件内嵌入 Noto Sans SC 字体子集与 ToUnicode 映射，并使用用户提供的 MoeDove logo。字体、图片及许可证保存在本目录 `assets/`，生成过程不请求远程资源。中文名称已通过实际PDF字体/文本流测试；当前字体编码支持Unicode基本多文种平面，补充平面字符会明确拒绝，避免静默输出缺失字形。另保存可直接打印的、转义后的完整HTML。

两个文件使用已有不可变blob存储，state.json只保存文档快照、blobID、大小和SHA-256。读取先核对大小与校验和；文件缺失或损坏时拒绝下载。备份包含这些blob；配线附件和LOA共用文档配额。

## 生命周期

`available / prewired → reserved → awaiting-connect → connected → awaiting-cease → ceased → release`

- `reserve`必须指定成员，可同时选择一个同配线架的空闲端口配成双工。新双工配对要求两端各自当前revision，已成对动作使用包含两端的统一revision。
- `request-connect`、`request-cease`是可选的工作单阶段；`connect`必须有物理交换机端口及线路编号，`cease`可以从connected或awaiting-cease进入。
- 未接通预留可以release。已接通线路须先cease。release清除租户、预留ID、日期、线路编号和附件关联；`keepPrewired=true`保留物理绑定及双工关系，以prewired状态等待新的预留。
- 物理prewired也可release以清除接线记录；空闲端口可pair/unpair，broken可以repair。所有动作是库存状态变更，不会自动操作交换机或实际布线。
- 双工两端同步改变状态、成员、预留标识和绑定；任何引用、归属、冲突或日期校验失败，整个事务回滚。
- 一个交换机物理端口只能对应一个单工/双工配线组，且不能承载另一个成员的连接。

每次动作需要理由，完成日期不能倒退或使用尚未发生的未来日期。历史记录只追加，包含原始before/after副本。成员不能读取历史；工作人员可按端口查询。通用CRUD只能调整库存元数据，不能绕过流程改变状态或分配。完整释放的库存记录可删除，但删除事件和此前历史保持不变。

旧版库存加载时为原有预留/连接分配稳定的legacy allocation ID，不编造原始日期或交换机绑定。没有成员的旧预留只能先释放；不能签发LOA或直接接通。

## LOA与租户隔离

只有reserved或awaiting-connect且归属明确的预留可以签发LOA，有效期必须在未来且不超过30天。同一预留只能保留一份当前有效授权，重签前需要撤销上一份，或等待它因信息变化/过期失效。

每次列表与下载都检查当前预留ID、端口组、成员、物理位置与接口等授权事实。接通、释放、重分配、重要元数据变化、成员停用、过期或撤销后，旧LOA不能继续作为当前授权下载。撤销不改写原文；原始blob仍保留供备份和历史核查。下载需要登录，不提供可转发的匿名令牌，也不发送邮件。文件本身不含数字签名，已离线保存的副本无法远程撤回，正文要求核对平台当前授权状态。

配线附件只链接现有staff/member范围文档。成员仅看到其当前预留和当下可见的附件。release总是清除链接，新租户不能继承上一个租户的文档或LOA。文档改所属成员、改权限或删除前必须先解除不再合法的配线关联。

## API

- `GET /api/patching/ports`：当前可见端口、安全位置标签、revision、availableActions和可见附件。
- `POST /api/patching/ports/{id}/transition`：action、revision、reason，以及所需memberId/switchPortId/circuitId等字段；新配对另带duplexPeerId和duplexPeerRevision。
- `GET /api/patching/history?portId=...`：工作人员的只读历史。
- `POST /api/patching/ports/{id}/loas`：revision、expiresAt、reason。
- `GET /api/patching/loas`：元数据与issued/expired/revoked/stale状态。
- `POST /api/patching/loas/{id}/revoke`：reason。
- `GET /api/patching/loas/{id}/download?format=pdf|html`：仅当前有效、已授权文档。

测试覆盖双工原子回滚、并发版本、物理绑定冲突、通用CRUD绕过、重分配、不可变历史、租户附件隔离、真实中文PDF、HTML转义、过期/撤销和blob损坏。未执行实际布线、设备变更或邮件投递。
