# 渠道状态：分组概览 / Channel status: group overview

## 使用说明

在监控设置中选择 V2「真实请求统计」后，用户侧 `/monitor` 默认进入「分组概览」，可切换到原有「详细统计」。V1 主动探测和管理员监控页继续使用原有界面。

概览按平台展示当前用户有权使用的活跃分组。余额或订阅额度耗尽不会隐藏分组；订阅过期、分组停用或授权撤销后，下次刷新会移除卡片。未纳入监控的授权分组仍显示，但不会把缺失数据当作健康。Composite 分组内的真实平台请求汇总为一张卡片。

卡片上的用户倍率使用用户专属分组倍率，没有专属设置时使用分组默认倍率；模型、时段等计费规则仍按实际请求执行。

### 真实请求指标

- **缓存率**：成功样本的缓存读取 token /（普通输入 + 缓存写入 + 缓存读取 token）。30 分钟和 12 小时缓存写入是缓存写入总数的明细，不重复相加。
- **可用率**：成功请求 /（成功请求 + 服务侧失败）。明确的用户权限、鉴权、余额、参数、安全拒绝和主动取消不纳入分母；上游账号失效、上游额度、账号池不可用、限流、网络、超时和服务错误纳入。
- **首 Token P50**：来自可合并延迟直方图的分桶估算值，不是对各分组或各时段 P50 求平均。

免费成功请求参与统计。已经产生部分用量但最终失败的请求不再同时算作成功；内部重试后成功的请求保留为成功。这些变化只影响监控，不修改账单或扣费。

正常／波动／异常仅使用可用率，沿用管理员已有错误率阈值与最低样本设置。无有效样本显示暂无数据，样本不足显示灰色状态。统计仍在回填或原始日志覆盖不足时会明确显示，缺失历史不会显示为 100%。

四个窗口沿用已有粒度：90 分钟 18 格、24 小时 24 格、7 天 14 格、30 天 30 格。页面显示统计截止时间，最新一格可能仍在收集数据。

### 已有归因检测

仅统计当前组内未删除且**调度开启**的账号。OAuth 使用最近 15 分钟内全部有效检测；普通 API Key 和 Codex-Engine 使用各账号最新一次有效检测，不限时间，关闭自动检测后历史仍参与。有效结果为通过、不通过、执行失败和异常；取消、跳过及未完成任务不参与。

汇总优先级为：有通过则绿；没有通过但有不通过则黄；只有异常／执行失败则红；没有有效结果则灰。有结果优先显示结果；无结果时根据当前配置区分“未开启降智检测”和“近期无有效检测”。临时限流和排队不改变启用状态。

历史展示最近 **200 分钟、20 格、每格 10 分钟**，每格在结束时刻应用相同规则。最后一格与当前摘要使用同一服务端统计时间；API Key 在窗口之前的有效结果也可延续到窗口内，直至被后续有效结果替换。历史无结果统一显示灰色，不用当前开关推算历史是否开启。

该区域不触发模型请求，也不随上方真实请求时间窗口裁剪。账号换组后按当前归属展示；影子账号只使用自己的记录。关闭调度后，该账号退出摘要及历史统计；再次开启后恢复参与。悬停只公开时间范围、汇总状态和判定口径，不公开账号身份、测试回答或内部错误。

平台图标和标签使用统一品牌色：Anthropic 橙、OpenAI 绿、Gemini 蓝、Grok 中性、Antigravity 紫、Composite 青；检测状态颜色独立使用绿、黄、红、灰，兼容明暗主题。

### 刷新与权限

自动刷新使用监控配置的 60 秒或 300 秒，支持手动刷新。隐藏页面暂停轮询，返回页面后重新加载。快速切换窗口时，旧请求不能覆盖新窗口。详细统计接口仅在切换到详细统计时加载。

新增登录用户只读接口：`GET /api/v1/channel-monitor-v2/group-overview?range=90m`。接口受原有监控开关和 V2 模式控制，授权范围由服务端计算，不接受客户端指定其他用户。管理员访问用户侧概览时也按自身分组权限裁剪。

## 升级、回退与运行开销

升级执行 `285_channel_monitor_v2_availability.sql`、`286_channel_monitor_v2_attribution_history_notx.sql` 和 `287_channel_monitor_v2_request_identity_notx.sql`，为可用率统计和有界检测历史读取提供必要字段、版本和索引；后两个索引采用并发创建。后台沿用单实例领取、限时事务和分批回填机制；页面查询不触发历史重建。旧统计口径尚未更新的区间保持未就绪状态。

历史重建只覆盖仍有原始用量及错误记录的区间。不要为了显示完整历史而关闭日志保留策略或把缺失失败记录推算成成功。

每次概览请求重新验证权限；统计读取已有聚合表。检测汇总按唯一账号批量读取时间窗口及 API Key 窗前锚点，再生成每组固定 20 格，不逐卡或逐格查询，也不加载完整任务快照。兼容的 `latest/recent` 通过另一条有界批量查询返回；页面只使用新增的 `enabled/as_of/summary_state/history`。

本次 200 分钟汇总无需新迁移，复用索引 286。清理保留原有最近 100 条任务，并额外保护最近 215 分钟的有效结果及每个 API Key 在窗口前的最后一条有效结果。旧清理策略已经删除的历史不会补造。回退旧应用后，其清理任务可能删除新历史条所需记录；重新升级时只能展示实际保留的数据。

回退应用时可保留新增字段和索引。统计口径升级期间应统一运行新版本，避免旧聚合进程重新写入旧口径；若曾回退并运行旧聚合进程，再升级后应重新执行对应统计版本回填。

## English

The user-facing `/monitor` page defaults to **Group overview** in V2 real-request mode, with the previous detailed statistics available through a view switch. V1 active monitoring and the admin monitor remain unchanged.

Cards show all active groups the authenticated user may use. Exhausted balance or quota does not remove a card; revoked access, inactive groups, and expired subscriptions do. Authorized groups outside the configured monitoring scope remain visible with an unmonitored state. Composite traffic is merged into one group card using counters and latency histograms, never by averaging rates or percentiles.

- **Cache rate:** cached input tokens divided by ordinary input, cache writes, and cache reads. Cache-write TTL details are already included in the write total.
- **Availability:** successful requests divided by successes plus service-side failures. Explicit user-side access, balance, parameter, safety, and cancellation failures are excluded; upstream authentication, upstream quota, capacity, transport, timeout, and server failures remain included.
- **First-token P50:** a histogram-based estimate. The tooltip identifies its approximation.

Zero-cost successes are included. Final failed requests with partial usage are not also counted as successes, while recovered retry attempts remain successful. Billing is unchanged. Health colors use availability only, with existing error thresholds and minimum samples. Missing or rebuilding data is not presented as 100% availability.

The traffic windows contain 18, 24, 14, and 30 buckets for 90 minutes, 24 hours, 7 days, and 30 days. Attribution uses only current, nondeleted group members with scheduling enabled. OAuth contributes all effective results from the previous 15 minutes; API Key and Engine accounts contribute their latest effective result regardless of age or the automatic-test switch. Cancelled, skipped, and unfinished jobs are ignored. Any pass wins; otherwise a mismatch wins; failures/errors alone are red; no effective result is gray. Only the current empty summary distinguishes disabled testing from no recent effective tests.

Attribution history independently covers 200 minutes in 20 ten-minute buckets. Each bucket evaluates the same rules at its end; the last bucket equals the current server-time summary. API Key results before the window remain effective until superseded. Historical empty buckets do not infer past configuration. Membership and scheduling are evaluated using current accounts, and shadows do not inherit their credential owner's history. Tooltips expose aggregate time, state, and rules only. No additional model probes are started.

Platform icons and labels reuse shared colors in both themes: orange Anthropic, green OpenAI, blue Gemini, neutral Grok, purple Antigravity, and cyan Composite. Result colors remain independent.

Polling follows the configured 60/300-second interval, pauses while hidden, and supports manual refresh. Stale requests cannot overwrite newer selections. The new authenticated `GET /api/v1/channel-monitor-v2/group-overview` endpoint recomputes authorization for every request and returns only small public summaries.

Forward migrations extend aggregation and bounded history lookup. Versioned backfill runs in bounded background batches within available source-log coverage, never within a page request. Deploy aggregators consistently during the statistics transition. Additive database fields and indexes may remain on application rollback; running an old aggregator requires a statistics rebuild when upgrading again.

The attribution-summary update requires no migration and reuses index 286. One batch query reads distinct accounts and generates twenty states per group; a second bounded batch preserves legacy `latest/recent` fields. New clients use `enabled/as_of/summary_state/history`. Retention keeps the latest 100 jobs plus effective results from the last 215 minutes and each API Key's last effective result before that window. Already-pruned history is not reconstructed; rolling back to an older cleaner can reduce available history on a later upgrade.

## 初版验证记录 / Initial implementation validation (2026-11-14)

本轮使用本地模拟 API 与 WSL 中的隔离数据库；未连接真实模型、未修改生产配置、未部署。Go 为 1.27.3，实验参数为 `greenteagc,jsonv2`。

| 检查 / Check | 结果 / Result |
| --- | --- |
| Go 概览、统计及权限定向单元测试 | 通过 |
| service / repository / handler 定向 race | 三包通过 |
| 后端 `cmd/server` 编译 | 通过 |
| PostgreSQL 18.1 / Redis 8.4 隔离集成 | 9 个顶层测试通过，实际运行，未跳过 |
| 迁移升级、重复应用、空源和旧粗粒度数据保护 | 通过；局部回填保留旧口径记录，未伪造新可用率 |
| 前端相关测试 | 48 项通过 |
| 前端类型检查、生产构建、定向 ESLint | 通过 |
| 浏览器模拟接口检查 | 1440px 桌面及 390px 手机，深浅主题、四个时间窗通过 |

数据库用例覆盖免费成功、失败但已有用量、重试恢复、跨 Key/组同 ID、带 `client:`/`local:` 的原始客户端 ID、用户拒绝与上游失败、Composite 无账号池故障、有限/永久额度耗尽后的权限、过期/撤销、影子账号不继承及按组最近 20 次检测。多分组样本有 1,028 条历史，50 个分组通过一次归因 SQL 返回 994 条小型摘要，共 113,842 字节，`EXPLAIN ANALYZE` 执行时间为 1.240 ms；该数值来自本地合成数据。

复现命令（从 `backend` 执行）：

```sh
GOEXPERIMENT=greenteagc,jsonv2 go test -tags unit -race ./internal/service ./internal/repository ./internal/handler -run 'ChannelMonitorV2|APIKeyService_GetAvailableGroups' -count=1
GOEXPERIMENT=greenteagc,jsonv2 go test -tags integration ./internal/repository -run 'TestChannelMonitorV2Overview' -count=1 -timeout=12m -v
GOEXPERIMENT=greenteagc,jsonv2 CGO_ENABLED=0 go build -trimpath ./cmd/server
```

前端从 `frontend` 执行 `pnpm typecheck`、`pnpm build`；相关测试位于 `src/features/channel-monitor-v2/__tests__` 和 `src/views/user/__tests__/ChannelStatusV2View.overview.spec.ts`。保留仓库既有构建警告（Browserslist、模块分包等），不借此次功能升级依赖。

Validation used only local fixtures and isolated PostgreSQL/Redis. Targeted Go tests, race checks, the backend build, 48 frontend tests, type checking, the production build, targeted lint, and desktop/mobile theme checks passed. The database suite actually ran; no integration checks were skipped. The 50-group fixture returned 994 public summaries in one attribution query (113,842 bytes; 1.240 ms local execution). No production models or configuration were accessed. Remote CI is reported separately from these local checks.

## 汇总规则更新验证 / Attribution summary update validation (2026-11-14)

本次补充 OAuth 15 分钟、API Key／Engine 不限时有效历史、20 格状态汇总、调度筛选及平台配色；使用 Go 1.27.3 与 `GOEXPERIMENT=greenteagc,jsonv2`。

- Service／Repository／Handler 定向单元及 race 测试、后端 `cmd/server` 编译通过。
- 隔离 PostgreSQL 18.1／Redis 8.4 的 `TestChannelMonitorV2Overview` 全套通过，新增 6 个顶层测试覆盖时间边界、21／101 条历史、旧 API Key 锚点、成员与影子隔离、暂停／恢复调度，以及清理前后 20 格一致。
- 前端相关测试共 55 项、构建中的 i18n 3 项、类型检查、生产构建和定向 ESLint 通过。
- 独立区间公式对照覆盖 40,000 个状态，包含微秒边界、相同完成时间按任务 ID 排序及无效结果。
- 50 个共享分组、2,002 条历史的合成场景中，汇总 SQL 返回固定 1,000 行，`EXPLAIN ANALYZE` 为 10.227 ms。兼容历史查询为 2.791 ms；包含兼容字段的公开结果 JSON 共 236,401 字节。上述数值为本地测试测量，并非生产基准。

复现核心检查：

```sh
GOEXPERIMENT=greenteagc,jsonv2 go test ./internal/service ./internal/repository ./internal/handler -run 'ChannelMonitorV2.*(Overview|GroupAttribution)' -count=1
GOEXPERIMENT=greenteagc,jsonv2 go test -tags unit -race ./internal/service ./internal/repository ./internal/handler -run 'ChannelMonitorV2.*(Overview|GroupAttribution)' -count=1
GOEXPERIMENT=greenteagc,jsonv2 go test -tags integration ./internal/repository -run TestChannelMonitorV2Overview -count=1 -timeout=12m -v
```

The update passed targeted Go unit/race tests, the complete isolated overview integration suite (including six new top-level tests), 55 frontend tests, three i18n checks, type checking, the production build, and targeted lint. Scheduling-disabled accounts are excluded from current, historical, legacy, and enabled-state projections without deleting their history or changing attribution execution. The 50-group fixture used two batch queries, with 1,000 summary rows in 10.227 ms and the legacy-history query in 2.791 ms. No production models, configuration, or deployment were involved. Remote CI was not run for this update.
