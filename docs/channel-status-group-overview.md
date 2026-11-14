# 渠道状态：分组概览 / Channel status: group overview

## 使用说明

在监控设置中选择 V2「真实请求统计」后，用户侧 `/monitor` 默认进入「分组概览」，可切换到原有「详细统计」。V1 主动探测和管理员监控页继续使用原有界面。

概览按平台展示当前用户有权使用且已纳入监控的活跃分组。余额或订阅额度耗尽不会隐藏分组；订阅过期、分组停用、授权撤销或移出监控范围后，下次刷新会移除卡片。已纳入监控但没有记录的分组继续显示“暂无数据”。Composite 分组内的真实平台请求汇总为一张卡片。

卡片上的用户倍率使用用户专属分组倍率，没有专属设置时使用分组默认倍率；模型、时段等计费规则仍按实际请求执行。

### 真实请求指标

- **缓存率**：成功样本的缓存读取 token /（普通输入 + 缓存写入 + 缓存读取 token）。30 分钟和 12 小时缓存写入是缓存写入总数的明细，不重复相加。
- **可用率**：成功请求 /（成功请求 + 服务侧失败）。明确的用户权限、鉴权、余额、参数、安全拒绝和主动取消不纳入分母；上游账号失效、上游额度、账号池不可用、限流、网络、超时和服务错误纳入。
- **首 Token P50**：合并延迟直方图的样本计数后，取中位样本所在桶的上界。新聚合数据在 1 秒以内按 50ms、1–30 秒按 100ms、30–60 秒按 250ms、60–120 秒按 1 秒、120–600 秒按 5 秒分桶；零耗时单独保留。例如 3.21 秒由原来的 5.00 秒改善为 3.30 秒。不对各分组或各时段的 P50 求平均，也不对桶内分布做插值。

免费成功请求参与统计。已经产生部分用量但最终失败的请求不再同时算作成功；内部重试后成功的请求保留为成功。这些变化只影响监控，不修改账单或扣费。

正常／波动／异常仅使用可用率，沿用管理员已有错误率阈值与最低样本设置。最低样本数分别用于整张卡片和每个时间格：整段数据充足时，部分时间格仍可能样本不足。无有效样本显示灰色短线，已有请求但样本不足显示灰色短柱，并保留悬停中的实际可用率；统计更新中使用灰色虚线柱。图例区分这三种状态，缺失历史不会显示为 100%。

四个窗口沿用已有粒度：90 分钟 18 格、24 小时 24 格、7 天 14 格、30 天 30 格。页面显示统计截止时间，最新一格可能仍在收集数据。

### 已有归因检测

仅统计当前组内未删除且**调度开启**的账号。OAuth 使用最近 15 分钟内全部有效检测；普通 API Key 和 Codex-Engine 使用各账号最新一次有效检测，不限时间，关闭自动检测后历史仍参与。有效结果为通过、不通过、执行失败和异常；取消、跳过及未完成任务不参与。

汇总优先级为：有通过则绿；没有通过但有不通过则黄；只有异常／执行失败则红；没有有效结果则灰。有结果优先显示结果；无结果时根据当前配置区分“未开启降智检测”和“近期无有效检测”。临时限流和排队不改变启用状态。

历史展示最近 **200 分钟、20 格、每格 10 分钟**，每格在结束时刻应用相同规则。最后一格与当前摘要使用同一服务端统计时间；API Key 在窗口之前的有效结果也可延续到窗口内，直至被后续有效结果替换。历史无结果统一显示灰色，不用当前开关推算历史是否开启。

该区域不触发模型请求，也不随上方真实请求时间窗口裁剪。账号换组后按当前归属展示；影子账号只使用自己的记录。关闭调度后，该账号退出摘要及历史统计；再次开启后恢复参与。用户页面保留状态、最近 200 分钟历史及更新时间，悬停只显示时间范围和汇总状态；账号类型、调度和判定口径留在本文档，不在卡片重复解释。

OpenAI／Composite 分组卡片展示当前生效的探针模型：启用分组独立策略时取组内模型，否则取全局模型。该字段来自当前配置，不根据历史检测结果推算；其他平台或尚未配置模型时不显示。配置变化将在页面下次刷新时生效。

平台图标和标签使用统一品牌色：Anthropic 橙、OpenAI 绿、Gemini 蓝、Grok 中性、Antigravity 紫、Composite 青；检测状态颜色独立使用绿、黄、红、灰，兼容明暗主题。

### 刷新与权限

自动刷新使用监控配置的 60 秒或 300 秒，支持手动刷新。隐藏页面暂停轮询，返回页面后重新加载。快速切换窗口时，旧请求不能覆盖新窗口。详细统计接口仅在切换到详细统计时加载。

后台每轮优先更新最近请求，历史回填失败只延后下一次历史尝试，不延后实时统计。刷新间隔从本轮开始计算，扣除已执行时间；聚合仍串行运行并保留原有单例锁及 55 秒总超时。页面刷新时间与统计截止时间分开，最新请求需等后台完成聚合后出现。

新增登录用户只读接口：`GET /api/v1/channel-monitor-v2/group-overview?range=90m`。接口受原有监控开关和 V2 模式控制，授权范围由服务端计算，不接受客户端指定其他用户。管理员访问用户侧概览时也按自身分组权限裁剪。

## 升级、回退与运行开销

升级执行 `285_channel_monitor_v2_availability.sql`、`286_channel_monitor_v2_attribution_history_notx.sql` 和 `287_channel_monitor_v2_request_identity_notx.sql`，为可用率统计和有界检测历史读取提供必要字段、版本和索引；后两个索引采用并发创建。后台沿用单实例领取、限时事务和分批回填机制；页面查询不触发历史重建。旧统计口径尚未更新的区间保持未就绪状态。

历史重建只覆盖仍有原始用量及错误记录的区间。不要为了显示完整历史而关闭日志保留策略或把缺失失败记录推算成成功。

每次概览请求重新验证权限；统计读取已有聚合表。检测汇总按唯一账号批量读取时间窗口及 API Key 窗前锚点，再生成每组固定 20 格，不逐卡或逐格查询，也不加载完整任务快照。兼容的 `latest/recent` 通过另一条有界批量查询返回；页面使用 `enabled/probe_model/as_of/summary_state/history`。`probe_model` 在现有批量查询中解析配置，只返回模型名，不增加逐组查询。

本次 200 分钟汇总无需新迁移，复用索引 286。清理保留原有最近 100 条任务，并额外保护最近 215 分钟的有效结果及每个 API Key 在窗口前的最后一条有效结果。旧清理策略已经删除的历史不会补造。回退旧应用后，其清理任务可能删除新历史条所需记录；重新升级时只能展示实际保留的数据。

回退应用时可保留新增字段和索引。统计口径升级期间应统一运行新版本，避免旧聚合进程重新写入旧口径；若曾回退并运行旧聚合进程，再升级后应重新执行对应统计版本回填。

首 Token 分桶细化无需迁移或重置可用率统计版本。正常实时聚合会重算最近 10 分钟，新请求及已重算区间使用细桶；未重算的旧历史仍保留原桶精度，混合窗口继续按样本上界统计。更长历史按现有回填及保留规则逐步更新，不为这次精度调整额外启动全量回填。回退旧程序可读取细桶，后续重算区间会恢复旧精度。

此次仅细化 TTFT，总耗时分桶不变。每个维度最多 588 个 TTFT 上界，数据库只存非空桶；页面仍读取已有汇总表，查询次数不变，非空桶行数和汇总读取量会随样本分布增加。超过 600 秒仍沿用原溢出桶 `2147483647ms`，不赋予该桶细精度。

## English

The user-facing `/monitor` page defaults to **Group overview** in V2 real-request mode, with the previous detailed statistics available through a view switch. V1 active monitoring and the admin monitor remain unchanged.

Cards show active monitored groups the authenticated user may use. Exhausted balance or quota does not remove a card; revoked access, inactive groups, expired subscriptions, or removal from monitoring scope do. Monitored groups without records remain visible with a no-data state. Composite traffic is merged into one group card using counters and latency histograms, never by averaging rates or percentiles.

- **Cache rate:** cached input tokens divided by ordinary input, cache writes, and cache reads. Cache-write TTL details are already included in the write total.
- **Availability:** successful requests divided by successes plus service-side failures. Explicit user-side access, balance, parameter, safety, and cancellation failures are excluded; upstream authentication, upstream quota, capacity, transport, timeout, and server failures remain included.
- **First-token P50:** the upper bound containing the median sample after merging histogram counts. New aggregates use 50ms buckets up to 1s, 100ms up to 30s, 250ms up to 60s, 1s up to 120s, and 5s up to 600s, with zero kept separately. For example, 3.21s is reported as 3.30s instead of the previous 5.00s. Percentiles are neither averaged across groups/time windows nor interpolated within buckets.

Zero-cost successes are included. Final failed requests with partial usage are not also counted as successes, while recovered retry attempts remain successful. Billing is unchanged. Health colors use availability only, with existing error thresholds and minimum samples applied separately to the whole card and each interval. Low-sample intervals retain their measured availability and use a short gray column; empty intervals use a thin gray line, while rebuilding uses a dashed gray column. The legend distinguishes all three states. Missing or rebuilding data is not presented as 100% availability.

The traffic windows contain 18, 24, 14, and 30 buckets for 90 minutes, 24 hours, 7 days, and 30 days. Attribution uses only current, nondeleted group members with scheduling enabled. OAuth contributes all effective results from the previous 15 minutes; API Key and Engine accounts contribute their latest effective result regardless of age or the automatic-test switch. Cancelled, skipped, and unfinished jobs are ignored. Any pass wins; otherwise a mismatch wins; failures/errors alone are red; no effective result is gray. Only the current empty summary distinguishes disabled testing from no recent effective tests.

Attribution history independently covers 200 minutes in 20 ten-minute buckets. Each bucket evaluates the same rules at its end; the last bucket equals the current server-time summary. API Key results before the window remain effective until superseded. Historical empty buckets do not infer past configuration. Membership and scheduling are evaluated using current accounts, and shadows do not inherit their credential owner's history. Cards show status, the history window and update time; tooltips show only the interval and aggregate state. Account-type and aggregation details stay in this documentation. No additional model probes are started.

OpenAI and Composite cards also show the currently configured probe model: the group's enabled independent policy takes precedence, otherwise the global default applies. This comes from current configuration, never from historical checks. Other platforms or unconfigured models omit the label. The next page refresh picks up configuration changes.

Platform icons and labels reuse shared colors in both themes: orange Anthropic, green OpenAI, blue Gemini, neutral Grok, purple Antigravity, and cyan Composite. Result colors remain independent.

Polling follows the configured 60/300-second interval, pauses while hidden, and supports manual refresh. Stale requests cannot overwrite newer selections. The new authenticated `GET /api/v1/channel-monitor-v2/group-overview` endpoint recomputes authorization for every request and returns only small public summaries.

Each aggregation cycle updates recent requests first. Historical backfill failures delay only historical retries, leaving live updates on their configured cadence. Cycle execution time is deducted from the wait; execution remains serial with the existing distributed lock and 55-second total timeout. Page refresh time and aggregate coverage time are distinct, and recent requests appear once aggregation completes.

Forward migrations extend aggregation and bounded history lookup. Versioned backfill runs in bounded background batches within available source-log coverage, never within a page request. Deploy aggregators consistently during the statistics transition. Additive database fields and indexes may remain on application rollback; running an old aggregator requires a statistics rebuild when upgrading again.

The finer TTFT buckets require no migration or availability-version reset. Normal live aggregation rewrites the latest ten minutes; new and recomputed intervals gain finer precision while older intervals keep their previous bounds. Mixed windows remain upper-bound estimates, with existing backfill and retention rules handling older data. No additional full-history rebuild is started. An older application can read the fine buckets and returns to coarse precision where it recomputes data. Duration buckets remain unchanged. TTFT has at most 588 bounds per dimension, storing only occupied buckets; query count stays unchanged, although histogram rows and rollup reads may increase. Values above 600s retain the previous overflow bound of 2147483647ms without a finer-precision guarantee.

The attribution-summary update requires no migration and reuses index 286. One batch query reads distinct accounts and generates twenty states per group; a second bounded batch preserves legacy `latest/recent` fields. New clients use `enabled/probe_model/as_of/summary_state/history`. The configured model is projected as a scalar in the existing batch query, with no per-group query. Retention keeps the latest 100 jobs plus effective results from the last 215 minutes and each API Key's last effective result before that window. Already-pruned history is not reconstructed; rolling back to an older cleaner can reduce available history on a later upgrade.

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

## 文案与配置探针展示验证 / Simplified cards and configured probe (2026-11-14)

本次移除用户页权限及聚合实现说明，新增当前配置的探针模型。Go 定向单测、后端编译、22 项前端相关测试、类型检查、生产构建及定向 lint 均通过。隔离 PostgreSQL 18.1／Redis 8.4 的 8 项归因概览集成测试全部通过，覆盖独立策略、全局继承、配置更新、空组、全局关闭、历史隔离及非 OpenAI 平台；50 组批量查询仍返回固定 1,000 行，执行计划采样为 5.347 ms。构建保留既有 Browserslist、模块分包和大 chunk 提示。本次无数据库迁移、生产修改或部署，未执行远端 CI。

Targeted Go tests, backend compilation, 22 related frontend tests, type checking, the production build and targeted lint passed. Eight isolated PostgreSQL/Redis attribution overview tests passed, including configured probe inheritance and updates independent of historical results. The 50-group query still returned 1,000 rows (5.347 ms in the local plan sample). Existing build warnings remain. No migration, production changes, deployment or remote CI were performed.

## 实时刷新与灰格修复验证 / Live refresh and low-sample display (2026-11-14)

只读核对用量、分钟聚合及后台日志后，确认已有请求进入统计；旧页面把低样本区间和空区间画成相同灰线，同时历史回填超时会触发整个聚合循环退避。现改为区分低样本柱、空数据线和更新中虚线柱，并将历史退避限制在历史阶段，按本轮开始时间计算刷新周期。

- 聚合器及概览定向 Go 单元与 race 测试通过；假时钟覆盖连续历史失败时的 60／300 秒实时节拍、退避截止及恢复、长期失败、首次回填、锁、停止和唤醒。
- 概览测试验证低样本区间仍保留实际可用率，并与真正空区间区分；前端相关 20 项测试、类型检查、定向 lint、生产构建及构建内 3 项 i18n 检查通过。
- `GOEXPERIMENT=greenteagc,jsonv2 go build ./...` 通过。无 SQL、表结构或存储格式变更，本次未重复运行数据库集成；生产仅只读排查，未部署。既有构建提示保留，远端 CI 未执行。

```sh
# backend
GOEXPERIMENT=greenteagc,jsonv2 go test -tags unit ./internal/service -run 'TestChannelMonitorV2(Aggregator|MaxChunkForDepth|Overview)' -count=1
GOEXPERIMENT=greenteagc,jsonv2 go test -race -tags unit ./internal/service -run 'TestChannelMonitorV2(Aggregator|MaxChunkForDepth|Overview)' -count=1
GOEXPERIMENT=greenteagc,jsonv2 go build ./...
```

Read-only production checks confirmed that requests were present in the aggregates. Low-sample and empty intervals previously looked identical, while historical timeouts delayed the whole aggregation loop. The fix separates those visual states and confines historical backoff to history, with cycle duration deducted from the configured refresh interval. Targeted Go unit/race checks, backend compilation, 20 related frontend tests, type checking, targeted lint, the production build and its three i18n checks passed. No SQL or storage format changed, so database integration was not rerun. No production changes, deployment or remote CI were performed.

## 首 Token 精度验证 / First-token precision validation (2026-11-14)

使用 Go 1.27.3、`GOEXPERIMENT=greenteagc,jsonv2`，Repository／Service／Handler 的 ChannelMonitorV2 定向单测、Repository 精度及概览 race、后端 `go build ./...` 均通过。SQL 单测遍历 0–600001ms，验证新桶始终向上取整且不粗于旧桶，并覆盖 588 桶上限、零值和溢出边界。隔离 PostgreSQL 18.1 中 4 项新增集成和 4 项既有聚合回归全部通过，覆盖四档 rollup、重复重算、用户／平台隔离、混合历史及失败关联。前端格式化已支持小数，本次未修改前端。

同一合成数据包含 20,000 条记录、60 个分钟、7 个用户和 3 个模型；统计全局／用户两个范围及 TTFT／duration 两项指标。以下为 PostgreSQL `EXPLAIN ANALYZE` 的聚合查询时间，不含索引写入：

| 轮次 / Run | 原分桶 / Legacy | 细 TTFT / Fine TTFT |
| --- | ---: | ---: |
| 1 | 42.077ms | 57.031ms |
| 2 | 34.643ms | 47.483ms |
| 3 | 35.681ms | 55.777ms |

该样本的非空桶从 5,797 增至 37,894，两种方案均保留 80,000 个范围×指标样本。精度提升会增加非空桶存储和后台聚合成本；页面仍走原有汇总读取，无新增原始日志扫描或逐卡查询。没有触发全量历史回填、生产修改或部署；远端 CI 未执行。

```sh
GOEXPERIMENT=greenteagc,jsonv2 go test -tags unit ./internal/repository ./internal/service ./internal/handler -run TestChannelMonitorV2 -count=1
GOEXPERIMENT=greenteagc,jsonv2 go test -race -tags unit ./internal/repository -run 'TestChannelMonitorV2.*(Histogram|TTFT|Precision|Overview)' -count=1
GOEXPERIMENT=greenteagc,jsonv2 go test -tags integration ./internal/repository -run 'TestChannelMonitorV2(LatencyPrecision|Overview(AggregationOutcomes|AvailabilityErrorClassification|CompositeHistogramAndScope|RecoveredAcrossChunks))' -count=1 -timeout=12m -v
GOEXPERIMENT=greenteagc,jsonv2 go build ./...
```

Targeted unit tests, race checks, backend compilation, and all eight selected isolated PostgreSQL integration tests passed. The fixed 20,000-row fixture above measured aggregate query execution, excluding index writes. Occupied histogram rows increased from 5,797 to 37,894 while both versions retained all 80,000 audience/metric samples. Finer resolution trades additional bounded aggregate storage/work for better precision; page requests still read rollups without raw-log scans or per-card queries. No frontend code, production state, deployment, full-history rebuild or remote CI was involved.
