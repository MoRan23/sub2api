# Grok 拒绝计费与归因目录验证 / Validation record

## 环境与范围

- 基线：`f8989e58f`；归因目录修复独立提交 `6964a5a3a`。
- Go 1.27.3，`GOEXPERIMENT=greenteagc,jsonv2`；Windows amd64 与 WSL Ubuntu 24.04。
- 前端：Node 24、pnpm 10；数据库专项：隔离 PostgreSQL 18 / Redis 8 测试容器。
- 仅本地 fixture、HTTP / WS 模拟上游及隔离数据库；未调用生产模型、修改生产配置或部署。

## 已验证内容

| 范围 | 结果 |
| --- | --- |
| 归因配置页面自动查询 | 90 项定向前端测试通过；覆盖真实下拉、共享候选、禁用自动检测仍查询、重载、失败保留、地址变化、迟到结果和卸载取消 |
| Grok 分组配置 | 默认关闭、八位金额、非有限值、负数清空、仅 Grok、部分更新和复制通过 |
| 配置持久化 | PostgreSQL / Redis 验证省略保留、旧快照保护、并发更新、鉴权加载、迁移及重复应用通过 |
| 拒绝计费 | token-only、固定费隔离、零倍率、缺价、成功单位清零、Simple 模式及小型快照测试通过 |
| 队列 | mandatory 溢出同步兜底、客户端取消、独立结算 context、快照隔离及普通请求原丢弃策略通过 |
| 原子结算 | 8 个并发重复请求只结算一次，账单失败完整回滚，零费用拒绝、双钱包、普通／有限／永久订阅、Key / 账号额度及统计通过 |
| DTO | 用户隐藏内部及上游请求 ID，管理员保留诊断；旧记录兼容通过 |
| 协议 | 精确路径及转义键、HTTP 403 / 200、任意分块 / 多行 SSE、usage 可信性、重复终态、CLI 回退隔离、WS HTTP 桥接、媒体 / 搜索 / 音频拒绝通过 |
| Realtime | 真实 Handler + 本地 TLS 握手拒绝，原始错误扩展字段、最终 token、一次 Dial / 一次记录 / 一次释放、零健康处罚通过；会话内首次拒绝终止通过 |
| 前端展示 | 用量相关 71 项测试、分组相关 30 项测试通过；定向 ESLint、类型检查、生产构建通过 |
| 竞态 | 配置／仓储集成、DTO、计费／快照、队列、媒体／语音及拒绝错误响应的定向 race 通过 |
| 最终组合回归与编译 | service / handler / DTO 扩大回归通过（仅显式跳过下述已单独复现的 quota 既有失败）；`go build ./...` 通过 |

原始错误 JSON 保留扩展字段；Messages 使用对应的错误 envelope。HTTP 200 中的精确拒绝以失败返回；拒绝不进入普通成功单位计费。拒绝观察和账变只采用服务端物理调用 ID。

## 复现命令

在 `backend` 设置 `GOEXPERIMENT=greenteagc,jsonv2`。集成测试还需要本地 Docker；现有 harness 自动创建隔离的 `postgres:18.1-alpine3.23` 和 `redis:8.4-alpine`，动态端口、自动迁移及清理，不传入生产 DSN。

```bash
export GOEXPERIMENT=greenteagc,jsonv2
go test -tags unit ./internal/repository ./internal/handler/dto \
  -run 'Test.*UsageLog|TestPrepareUsageLog|TestGrokSafetyUsageDTO|Test.*ChannelMonitorV2|Test.*UsageStats' -count=1
go test ./internal/handler \
  -run 'TestGrokSafetyFrozenUsage|TestGrokSafetyOrdinaryUsage' -count=1
go test -race ./internal/handler \
  -run 'TestGrokSafetyFrozenUsage|TestGrokSafetyOrdinaryUsage' -count=1
go test -race -tags unit ./internal/service ./internal/repository ./internal/pkg/groksafetyobserver \
  -run 'TestGrokSafety|TestGrokAccessDenied' -count=1
go test -race -tags unit ./internal/handler \
  -run '^TestGrokRealtimeSafetyHandshakePreservesBodyBillsOnceAndReleasesSlot$' -count=1
CI=true go test -tags integration ./internal/repository \
  -run 'TestGrokSafety' -count=1 -v
CI=true go test -race -tags integration ./internal/repository \
  -run 'TestGrokSafety' -count=1
CI=true go test -tags integration ./internal/repository \
  -run 'TestGroupRepoSuite/TestGrokSafetyFee|TestAPIKeyRepoSuite/TestGrokSafetyFee|TestGroupGrokSafetyFeeConcurrentPartialUpdates|TestMigration283GrokSafetyFeeDefaultsAndReapply' -count=1 -v
CI=true go test -race -tags integration ./internal/repository \
  -run 'TestGroupRepoSuite/TestGrokSafetyFee|TestAPIKeyRepoSuite/TestGrokSafetyFee|TestGroupGrokSafetyFeeConcurrentPartialUpdates|TestMigration283GrokSafetyFeeDefaultsAndReapply' -count=1
go test -tags unit ./internal/service ./internal/handler ./internal/handler/dto \
  -run 'Grok|Attribution|UsageRecordSnapshot|UsageBilling|TestProxyOpenAIWSHTTPBridge|TestHandleStreamingResponse|TestHandleChat|TestHandleMessages|TestOpenAICompat' \
  -skip '^TestGrokQuotaBillingStillRejectsUnavailableCredentialsDuringCooldown$' -count=1
go build ./...
```

前端在 `frontend` 运行定向 Vitest、`pnpm run typecheck`、`pnpm run build`；ESLint 仅覆盖本次涉及的 Vue / TS 文件。分组配置的定向测试命令为：

```bash
pnpm exec vitest run \
  src/views/admin/__tests__/GroupsView.codexManifest.spec.ts \
  src/views/admin/__tests__/GroupsView.duplicate.spec.ts \
  src/i18n/__tests__/localeKeyCompleteness.spec.ts
```

## 分配量：三轮快照基准

命令（`backend` 目录）：

```bash
GOEXPERIMENT=greenteagc,jsonv2 go test ./internal/service \
  -run '^$' -bench '^BenchmarkGrokSafetyUsageSnapshot$' -benchmem -count=3
```

CPU：AMD Ryzen 7 9700X；以下为本地三轮原始数值，耗时包含同机并行验证的波动。

| replay 大小 | 拒绝证据 | ns/op（3 轮） | B/op（每轮） | allocs/op（每轮） |
| --- | --- | --- | --- | --- |
| 0 | 无 | 4094 / 3748 / 4009 | 5864 | 15 |
| 0 | 有 | 4039 / 4079 / 3927 | 5992 | 16 |
| 8 MiB | 无 | 3111 / 3438 / 4958 | 5864 | 15 |
| 8 MiB | 有 | 4259 / 3587 / 3351 | 5992 | 16 |

拒绝证据固定增加 128 字节、1 次分配；源结果的 replay 正文从 0 增至 8 MiB，计费快照分配量不变。

正常 SSE 帧观察使用相同工具链、`BenchmarkGrokSafetyNormalFrame` 三轮测试：

| 内容 | ns/op（3 轮） | B/op | allocs/op |
| --- | --- | --- | --- |
| 短 delta | 94.36 / 85.25 / 99.10 | 0 | 0 |
| 32 MiB `choices[0].delta.content` | 7295334 / 8745857 / 7341745 | 0 | 0 |

观察器扫描现有帧，没有为大 `choices` 数组另建字符串或数组。需要读取完整拒绝 / usage 结构时才进行对应 JSON 校验；无拒绝的正常请求不新增数据库计数查询。

## 已知基线失败与未执行项

仓储全量 unit 存在以下三组失败，已从 `git archive f8989e58f` 建立独立目录，使用相同 Go 工具链复现：

- `TestUpdateExtraCodexDisplaySnapshotsAvoidSchedulerOutbox`
- `TestUpdateCredentialsPlainCNAPIKeyAccountCleanupStaysSemanticallyEquivalent`
- `TestGrokCatalogCASRefreshesSingleAccountOutsideTransaction`

它们涉及既有账号快照 SQL mock 预期。扩大 service 回归还复现了既有 `TestGrokQuotaBillingStillRejectsUnavailableCredentialsDuringCooldown/missing_configured_proxy`：实际 `ACCOUNT_PROXY_UNAVAILABLE`，断言期待 `GROK_QUOTA_TOKEN_UNAVAILABLE`。此用例在当前版本和原基线均单独执行确认；最终扩大回归显式跳过该测试并保留记录。

本轮扩大回归发现的 `TestOpenAICompatBufferedModelEvidenceIncludesNonTerminalFrames` 空账号、`TestOpenAIWSHTTPBridgeGrok429PersistsRateLimit` 空 Gin Context 问题已补充 nil 保护，原测试及最终组合回归重新通过。Realtime 握手 fixture 使用 Simple 模式，其账单记录费用但不扣钱包，测试分别校验账单金额与钱包零变动。未通过删保护逻辑或弱化断言处理失败。本功能的隔离数据库事务和定向用例已单独执行。

未执行真实模型测试、生产压测或部署。远端 CI 未执行；按项目当前约定记录本地验证结果。

## English

Tests use Go 1.27.3 with `greenteagc,jsonv2`, local HTTP/WebSocket fixtures and isolated PostgreSQL 18 / Redis 8 containers. The checks cover exact refusal evidence, immutable fee snapshots, mandatory queue fallback, transactional deduplication, rollback, both wallets, finite/permanent subscriptions, quota and statistics, and public/admin DTO separation.

The attribution page, usage displays and group settings passed their focused frontend suites, type checks, lint and production build. Three snapshot benchmark rounds show a fixed 128-byte / one-allocation evidence overhead, independent of an 8 MiB replay body.

Three pre-existing repository unit failures and one quota error-code assertion were reproduced from the original baseline in a separate checkout. A newly introduced nil-account compatibility regression was fixed and its original tests passed again. Normal short and 32 MiB SSE observations allocate zero bytes in all three benchmark rounds. Remote CI and production model/load tests were not run; no deployment or production configuration change was made. See the feature guides for migration and rollback details.
