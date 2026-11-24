# Upstream v0.2.25 merge / 上游 v0.2.25 合并

## 基线与范围

- 本地基线：`b3d991119c0f1cf119a077a578709eef7ac4010b`。
- 上游基线：`328db26f450edbdf9f8d4344a3ea903ac6396842`；本次合入 `0f6671457132425d1b84f3d80536b53e02942424`。
- 上游范围为 15 个提交、91 个文件，普通 merge 保留双方历史。20 个文本冲突按现有接口契约融合。
- 普通合并提交：`ec802473d`；WS 上下文并发修复：`4e2f6e0af`。
- 仅升级上游指定的 `gorilla/websocket` 提交；保留 Go 1.27.4、实验开关及现有 req fork。不增加数据库迁移，不重新生成未变更的 Ent/Wire。
- 保留 17 份未跟踪排查文件。验证使用模拟上游及隔离数据库；不修改 Engine、生产配置或部署状态。

## 功能与兼容

### 请求头身份变量

API Key 账号的管理员请求头模板支持 `{{user_id}}`、`{{api_key_id}}`、`{{group_id}}` 和 `{{request_id}}`。用户、Key 和分组身份来自已认证请求；缺少上下文或无法解析的变量会省略整条请求头。OAuth 及共享授权不发送这些网关身份变量。管理探针和共享目录查询不借用业务请求身份。

身份上报证据以最终发出的请求头为准，只记录变量对应的脱敏内容。WS 连接兼容性同时保留现有授权、路由、Guardian 和身份隔离条件；模板身份使用固定长度摘要，连接不保留完整请求正文或请求上下文。Engine 继续使用现有独立透传路径。

### 上游分组名称与备注

账号可选择从既有 billing 探针同步上游分组名称和描述。启用后，上游结果会在后续探针中更新本地名称、备注；不新增轮询请求。停用探针同时关闭倍率和分组文字同步。

`GET /v1/sub2api/billing?include=group` 可返回分组显示文字；省略参数时保持原响应。名称预览支持已保存账号和创建中的配置，不保存账号。新预览路由适用于管理员和全权管理 Key，受限 Key 不继承相邻接口权限。

同步继续通过锁定后的最新账号、身份校验及原有局部更新提交。保留授权撤销、Daybreak、共享凭据、Engine 容量和模型同步保护，避免后台旧快照覆盖管理员配置。编辑表单未更改的名称与备注不作为显式更新提交。

### 错误透传与 Compact

保留结构化上游错误中的安全标量字段，按实际物理调用的凭据和目标地址脱敏。错误正文沿用既有有界读取，四个错误字段共用一次脱敏准备；正常成功请求不增加此处理。

已确认的 `invalid_encrypted_content` 400 不进入账号池 failover 或账号处罚；现有 HTTP、WS 定向恢复继续生效。Compact 接入结构化错误码及容量错误码转换，同时保留短写检查、心跳停止和实际交付结果。Grok 精确安全拒绝及原子计费、Engine 完整交付与断连不补扣规则不变。

运维详情同时显示原有身份诊断和新的脱敏上报头，复用同一次错误 JSON 解析。

### 验证中发现的兼容问题

- 批量创建账号补齐新同步开关的显式字段转发，与单个创建执行相同校验。
- Sub2 的 Engine WS 桥接把连接上下文与每轮上下文分开，修复原有读取协程与回合更新之间的数据竞争，同时避免跨回合累积上下文链。
- WS 连接成功获取路径不执行仅错误时需要的反射检查；大错误消息未命中敏感内容时复用原串，避免多次全文分配。
- 代理探针集成测试按本测试账号限定缓存事件计数，避免 Engine 测试留下的事件干扰断言；生产缓存行为不变。

## 验证与升级

验证结果见 [验证报告](validation/merge-v0.2.25/README.md)。此版本没有数据库迁移；回退时使用先前匹配的应用版本。分组名称同步默认关闭，管理员主动启用后生效。

相关 Go 单元、race、隔离 PostgreSQL/Redis 集成检查和后端构建通过；前端 427 项测试、类型检查、构建及定向 lint 通过。完成三轮基线对比、五轮定向复测及 64 条模板上限测量；WS 复用分配恢复到原基线，大错误未命中脱敏分配由约 3.16 MB 降至约 8 KB。Forwarder 耗时有波动，具体中位值和范围完整记录于性能报告。Actions 查询返回关闭，未执行远端 CI。

## English release notes

This normal merge integrates upstream v0.2.25 while retaining local authorization isolation, Engine delivery rules, Daybreak, billing, audit penalties, telemetry, monitoring, and recent memory optimizations. The only dependency update is the upstream-selected gorilla/websocket revision; there is no database migration or code-generation change.

API Key header templates can report authenticated request identity. Final outbound headers determine diagnostic evidence, and WS compatibility uses a bounded identity digest. OAuth, background catalog/probe requests, and the independent Engine path retain their existing identity boundaries.

Optional upstream group-name/description synchronization reuses the existing billing probe and locked account update. Preview does not persist changes. Restricted administrator keys do not implicitly gain access to the new preview endpoint.

Structured error forwarding redacts actual transport credentials once per response. Confirmed encrypted-content failures do not trigger account-pool failover. Compact delivery checks and existing Grok/Engine settlement boundaries remain intact. Validation and deployment are separate; no production setting or real model request is involved.

Validation also identified an existing race in Sub2's Engine WS bridge: the reader captured a context variable that later turns reassigned. An immutable connection context and separate turn contexts remove that race without changing cancellation or settlement. Batch account creation now forwards the new synchronization settings consistently. No-match error sanitization reuses text, and successful WS acquisition avoids an unnecessary escape allocation.
