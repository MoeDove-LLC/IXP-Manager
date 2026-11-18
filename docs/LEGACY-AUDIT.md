# 旧操作审计摘要迁移

从旧数据库导出 `log` 表，将选中的行写入表形式的 JSON。保留原始归档作为历史取证材料。本工具生成供新版控制台浏览的摘要。

```sh
ixp-migrate --audit-archive --audit-source previous-exchange \
  --source-timezone Asia/Shanghai \
  --input legacy-audit.json --output audit-summary.json
```

输入为 `{"tables":{"log":[...]}}`。行结构来自旧 [log 表迁移](../database/migrations/2021_03_12_150418_create_log_table.php)。旧 [Observable 写入逻辑](../app/Traits/Observable.php) 会将对象属性写入 `models`，对象属性和 `message` 自由文本可能含敏感数据。新摘要只提取旧事件 ID、用户 ID、已知模型和模型 ID、操作类型以及来源时间。未知模型或操作以固定的「未知」标签显示。ID 作为十进制字符串保留，包括超出 JavaScript 精确整数范围的 ID。未填写 `--source-timezone` 时显示「未确认」，时间按来源文本展示。

`--audit-source` 是来源数据库的标签，默认 `legacy-ixp-manager`。同一旧数据库的多个批次请使用同一个标签。转换器写入权限为 `0600` 的新文件，拒绝覆盖已有文件。每批最多 10,000 行。控制台最多保留 50 批和合计 50,000 行。需要拆分时，请按不同的旧 `log.id` 范围导出。

新版管理员在「操作审计 → 旧操作审计归档」选择摘要文件，并点击「导入历史摘要」。控制台校验格式、摘要和来源行冲突后保存归档，按批次分页浏览。来源用户 ID 始终属于旧数据库。当前操作审计记录实际的导入动作，旧事件保留在独立历史视图。归档随完整备份保存。

批次 SHA-256 用于检查摘要完整性和识别重复批次。管理员负责选择可信的源导出。普通资源 JSON 导出与 `/api/audit` 继续使用各自的数据范围，历史摘要通过独立的管理员会话接口访问。
