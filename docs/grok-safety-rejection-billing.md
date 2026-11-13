# Grok 安全拒绝计费 / Grok safety-rejection billing

## 管理配置

Grok 分组新增 `grok_safety_fee_enabled`（默认关闭）和 `grok_safety_fee`
（可空、USD、最多八位小数）。关闭、空金额或零金额均不收固定费。
其他平台及 Composite 不允许开启。创建、编辑、复制和管理 API 使用同一校验；
更新省略字段保留原值，负数金额清空设置。

固定费在请求开始时冻结为小型标量，配置调整影响后续请求。不会复制价格目录。
正常 token 价格、倍率及余额预留沿用原流程。

## 收费证据与范围

仅完整 JSON 的 `error.code` 或 `response.error.code` 精确等于
`safety_review_rejected` 才产生拒绝账单。状态码、相似文案、嵌套工具内容和
估算 token 不作为证据。HTTP、SSE 和实时语音使用同一证据判定。

- 覆盖文本、压缩、独立搜索、图片／视频提交、TTS、STT 和 Realtime。
- 轮询、下载、模型目录、配额、诊断及管理请求不收固定费。
- 每次物理上游调用生成独立服务端 CallID。CLI 兼容回退也使用新 CallID。
- 精确拒绝不重试、不切账号、不触发账号健康处罚；首次 Realtime 拒绝终止会话。
- 主动读取已观察到的拒绝可以结算，即使随后客户端断开。未观察到拒绝的断流、
  超时不收固定费；不会为此新增后台排空或跨重启追取用量。
- 客户端收到失败及拒绝码，不在错误正文中追加扣费金额。

## 金额与事务

同一被采纳尝试的可信最终 token 按 token-only 价格计算；拒绝的图片、视频、搜索
和音频成功单位归零。

```
用户费用 = token 用户费用 + 固定费
账号费用 = token 原始成本 × 账号倍率 + 固定费
```

固定费不参与任何倍率、折扣或峰时计算。倍率为零仍保留固定费。
token 缺价时保留固定费，并记录 `token_pricing_unavailable`；不猜算 token 费用。
Simple 模式保留现有不扣钱包语义。

专用原子入口使用 `grok-safety:<CallID>` 幂等键，同一数据库事务写入去重记录、
钱包／订阅、Key／账号额度和一条失败用量账单。重复处理只生效一次，账单失败全部
回滚。已确认拒绝使用 mandatory 队列，停池或溢出使用现有同步兜底和独立超时；
不新增跨重启补账任务。

用量页面和导出展示拒绝、token 费用、固定费与合计。费用汇总包含拒绝费用，成功
次数排除拒绝。用户 DTO 不返回内部 CallID 或上游请求 ID；管理员保留诊断信息。

## 升级

- `283_group_grok_safety_fee.sql` 增加默认关闭的分组配置。
- `284_usage_log_grok_safety_rejection.sql` 增加 nullable JSONB 证据，旧记录为 NULL。
- 鉴权缓存版本更新为 30；旧缓存自动回源。
- 升级不启用任何分组、不重算历史、不修改生产设置。部署前按现有流程备份。
- 回退应用可保留新增列；旧程序不提供该收费功能。保留新程序产生的账单和去重记录，
  不删除已结算的证据。

## English

Grok groups expose an opt-in fixed USD safety-rejection fee with eight-decimal precision.
Only an exact decoded `safety_review_rejected` at `error.code` or `response.error.code`
qualifies. Each physical upstream call has a server-generated ID. The observer reuses
complete response payloads; it does not retain request bodies or entire price catalogs.

The feature covers inference, compaction, search, media submissions, HTTP audio and
Realtime, but excludes polls, downloads and auxiliary/management requests. A confirmed
refusal is not retried and does not penalize account health. Realtime ends on its first
refusal. A refusal observed during active reading remains billable after disconnect;
there is no new drain, late-usage recovery or durable billing retry mechanism.

Trusted final tokens are priced using token-only rules. Successful media/search/audio
units are cleared. Customer cost is token cost after existing multipliers plus the fixed
fee; account cost is token base cost times the account multiplier plus the fixed fee.
The fixed fee survives zero multipliers and missing token pricing. Simple mode retains
its no-wallet-debit behavior.

Deduplication, wallet/subscription/key/account updates and the failed usage row commit
atomically under `grok-safety:<CallID>`. Failed rows contribute to monetary totals, not
successful-request counts. Public usage metadata excludes internal/upstream request IDs.
Migrations are additive and existing records remain unchanged. Rollback may retain the
new columns and must preserve already-settled usage and deduplication records.
