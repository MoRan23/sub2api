# v0.2.24 隔离存储验证 / Isolated storage validation

验证日期：2026-11-20。验证对象为 `f987b4d0e` 合入上游 `328db26f4` 后的工作区及本次兼容修复。

三个定向测试批次全部通过：计费、订阅与缓存 12 个顶层测试；OAuth 账号套件 16 个主要子测试；目录与影子账号 PostgreSQL 投影 1 个测试。测试使用隔离容器及测试数据，没有调用真实模型或连接生产数据库。

All three targeted batches passed: 12 billing/subscription/cache tests, 16 selected OAuth suite cases, and one PostgreSQL catalog/shadow projection test. These checks used isolated containers and test fixtures, with no production database or live model calls.

## 环境 / Environment

| 项目 | 实际环境 |
| --- | --- |
| 执行环境 | Windows → WSL `Ubuntu-24.04`，Ubuntu 24.04.3 LTS |
| Docker | 29.4.0 |
| Go | 1.27.4；`GOTOOLCHAIN=local` |
| Go 实验开关 | `GOEXPERIMENT=greenteagc,jsonv2` |
| 并发限制 | `GOMAXPROCS=2`、`go test -p 1` |
| PostgreSQL | `postgres:18.1-alpine3.23` |
| Redis | `redis:8.4-alpine` |
| Testcontainers | Go v0.40.0、Ryuk 0.13.0 |

前两个批次使用 `internal/repository/integration_harness_test.go`：每个测试进程新建 PostgreSQL/Redis，执行完整迁移后运行选中的测试。`CI=1` 使 Docker 不可用成为失败，而非跳过。Redis 使用测试命名空间。第三个测试使用独立临时 PostgreSQL 和连接级临时表，测试结束后显式停止其容器。

The first two batches applied the complete migration set to fresh PostgreSQL containers before running. Redis was isolated by the test harness. The projection check used a separate disposable PostgreSQL instance and temporary tables. No new schema migration was introduced by this merge.

## 实际命令 / Executed commands

以下命令从 Windows Git Bash 执行。`MSYS_NO_PATHCONV=1` 保留传给 WSL 的 Linux 路径；直接使用 `wsl.exe --exec env` 避免 Windows PATH 中的空格被嵌套 shell 解释。

### 计费、订阅、缓存与统计

```bash
MSYS_NO_PATHCONV=1 wsl.exe -d Ubuntu-24.04 --cd /mnt/c/Users/Admin/.codex/worktrees/merge-upstream-v0-2-22/sub2api/backend --exec env PATH=/mnt/d/DevCache/go-mod/golang.org/toolchain@v0.0.1-go1.27.4.linux-amd64/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin GOTOOLCHAIN=local GOEXPERIMENT=greenteagc,jsonv2 GOMAXPROCS=2 CI=1 go test -p 1 -tags=integration ./internal/repository -count=1 -timeout=15m -run '^Test(SchedulerCacheSnapshotUsesSlimMetadataButKeepsFullAccount|CatalogAndShadowProjectionsPostgres|GrokSafetyBillingAtomicity|GrokSafetyAccountCostAndSuccessStatistics|GrokSafetySubscriptionBilling|UsageBillingRepositoryApply_UsesBothWalletsAndReportsCombinedBalance|UsageBillingRepositoryApplyBatch_DualWalletMatchesSequential|TotalQuotaPermanentRepositoryLifecycle|TotalQuotaConcurrentGrantsAndRedeemAtomicity|TotalQuotaMigrationFreshInstallAndRestart|OpsRepositoryGetOpenAITokenStats_WeightsRateByDuration|ReferralRegistrationPendingOAuth_QuotaBootstrapAfterCommitOnly|OpenAIOAuthAccountState_PublishesCommittedCanonicalSnapshot)$' -v
```

结果：12 个顶层测试全部通过，包耗时 **6.971s**。该正则中的 `TestCatalogAndShadowProjectionsPostgres` 带 `unit` 构建标签，本批次未运行，已在下方单独执行。

| 测试 | 实际覆盖 |
| --- | --- |
| `TestGrokSafetyBillingAtomicity` | 并发重复结算只提交一次；账单写入失败回滚扣款及收据；零费用拒绝仍保存账单 |
| `TestGrokSafetyAccountCostAndSuccessStatistics` | 拒绝费用及账号统计、成功统计口径 |
| `TestGrokSafetySubscriptionBilling` | 普通周期、有限总额度、永久总额度订阅扣费 |
| `TestUsageBillingRepositoryApply_UsesBothWalletsAndReportsCombinedBalance` | 双钱包扣减与合计余额 |
| `TestUsageBillingRepositoryApplyBatch_DualWalletMatchesSequential` | 批量与逐笔双钱包结算一致，覆盖两个金额分支 |
| `TestTotalQuotaPermanentRepositoryLifecycle` | 永久额度存储及生命周期 |
| `TestTotalQuotaConcurrentGrantsAndRedeemAtomicity` | 并发发放与兑换事务原子性 |
| `TestTotalQuotaMigrationFreshInstallAndRestart` | 空库迁移及重复启动 |
| `TestSchedulerCacheSnapshotUsesSlimMetadataButKeepsFullAccount` | 精简调度元数据与完整账号缓存的字段边界 |
| `TestOpsRepositoryGetOpenAITokenStats_WeightsRateByDuration` | 同一正 token/正耗时样本集按总耗时加权；无有效样本时的空值 |
| `TestReferralRegistrationPendingOAuth_QuotaBootstrapAfterCommitOnly` | OAuth 注册事务提交后才初始化额度，回滚不初始化 |
| `TestOpenAIOAuthAccountState_PublishesCommittedCanonicalSnapshot` | 发布已提交的权威账号缓存快照 |

This batch covered atomic safety-fee settlement and rollback, dual wallets, finite/permanent subscriptions, fresh-install/restart migrations, slim scheduler serialization, weighted token throughput, and post-commit cache/quota publication.

### OAuth CAS、共享授权与撤销

```bash
MSYS_NO_PATHCONV=1 wsl.exe -d Ubuntu-24.04 --cd /mnt/c/Users/Admin/.codex/worktrees/merge-upstream-v0-2-22/sub2api/backend --exec env PATH=/mnt/d/DevCache/go-mod/golang.org/toolchain@v0.0.1-go1.27.4.linux-amd64/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin GOTOOLCHAIN=local GOEXPERIMENT=greenteagc,jsonv2 GOMAXPROCS=2 CI=1 go test -p 1 -tags=integration ./internal/repository -count=1 -timeout=15m -run '^TestAccountRepoSuite$/(TestPatchOpenAIOAuthCredentialsIfUnchanged|TestOAuthSharedCredentialsAllOSReadOneGrantAndRefreshFencesEveryIdentity|TestOAuthOSCredentialsCASRejectsRevokeAndConcurrentRefresh|TestOAuthOSCredentialsSharedGrantAndNoResurrection|TestOAuthOSCredentialsErrorsAndRevokeApplyToSharedAuthorization|TestOAuthMetadataRevokeAndCASAreAtomic|TestOpenAIOAuthAccountState_)' -v
```

结果：16 个主要子测试全部通过，包耗时 **5.970s**。

- CAS 保留并发修改的模型限制；认证字段改变后拒绝旧刷新结果。覆盖 access/refresh/id token、认证模式、用户/账号/组织/client ID 和 token version。
- 不同 OS 身份读取同一共享授权，刷新更新所有身份的隔离标记；撤销与并发刷新互斥，旧凭据不能恢复已撤销授权。
- generation/revision 过期结果没有副作用；授权失败、手动暂停、重新授权和普通错误分别保留正确的暂停归属。
- 影子账号使用当前母账号状态，影子授权失败不取得母账号暂停归属；恢复保留私有授权和凭据。

All selected OAuth cases passed: field-level CAS, shared authorization across OS identities, revoke/refresh fencing, stale generation/revision rejection, pause ownership, and owner/shadow isolation.

### 目录与影子账号 PostgreSQL 投影

启动隔离 PostgreSQL，并读取随机映射端口：

```bash
MSYS_NO_PATHCONV=1 wsl.exe -d Ubuntu-24.04 --exec docker run --detach --rm --name sub2api-v024-catalog-billing-validation --publish 127.0.0.1::5432 --env POSTGRES_PASSWORD=sub2api_validation --env POSTGRES_DB=sub2api_validation postgres:18.1-alpine3.23
MSYS_NO_PATHCONV=1 wsl.exe -d Ubuntu-24.04 --exec docker port sub2api-v024-catalog-billing-validation 5432/tcp
MSYS_NO_PATHCONV=1 wsl.exe -d Ubuntu-24.04 --exec docker exec sub2api-v024-catalog-billing-validation pg_isready -U postgres -d sub2api_validation
```

本次映射端口为 `127.0.0.1:32769`，就绪检查通过。重跑时应使用当次 `docker port` 返回的端口。以下 DSN 仅用于该次临时容器。

```bash
MSYS_NO_PATHCONV=1 wsl.exe -d Ubuntu-24.04 --cd /mnt/c/Users/Admin/.codex/worktrees/merge-upstream-v0-2-22/sub2api/backend --exec env PATH=/mnt/d/DevCache/go-mod/golang.org/toolchain@v0.0.1-go1.27.4.linux-amd64/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin GOTOOLCHAIN=local GOEXPERIMENT=greenteagc,jsonv2 GOMAXPROCS=2 TEST_SCHEDULER_PG_DSN=postgres://postgres:sub2api_validation@127.0.0.1:32769/sub2api_validation?sslmode=disable go test -p 1 -tags=unit ./internal/repository -count=1 -timeout=5m -run '^TestCatalogAndShadowProjectionsPostgres$' -v
```

结果：通过，包耗时 **0.046s**。实际验证目录投影过滤停用/未调度账号，保留模型映射和协议；不携带 access token、测试回答、完整分组或代理；批量影子健康投影保留账号状态及自动暂停字段。

The PostgreSQL projection test passed. It checked active/schedulable filtering, model/protocol preservation, omission of credentials and heavy fields, and batched shadow-parent health projection.

测试结束后执行以下命令，容器成功停止并由 `--rm` 删除：

```bash
MSYS_NO_PATHCONV=1 wsl.exe -d Ubuntu-24.04 --exec docker stop --time 10 c699c0656f6bd7fdca24a07e5a6ffd3989de163f038b8522acc4a42695fd5763
```

## 结果记录 / Result records

| 批次 | 本机日志 | 结果 |
| --- | --- | --- |
| 计费、订阅、缓存与统计 | `C:/Users/Admin/.fastctx/jobs/j-c6v55z/output.log` | PASS，6.971s |
| OAuth CAS、共享授权与撤销 | `C:/Users/Admin/.fastctx/jobs/j-d5gmwh/output.log` | PASS，5.970s |
| 目录及影子投影 | `C:/Users/Admin/.fastctx/jobs/j-3xiwf5/output.log` | PASS，0.046s |

```text
PASS
ok github.com/Wei-Shaw/sub2api/internal/repository 6.971s
PASS
ok github.com/Wei-Shaw/sub2api/internal/repository 5.970s
=== RUN   TestCatalogAndShadowProjectionsPostgres
--- PASS: TestCatalogAndShadowProjectionsPostgres (0.02s)
PASS
ok github.com/Wei-Shaw/sub2api/internal/repository 0.046s
```

最初一次启动命令因嵌套 shell 解释 Windows PATH 中的空格而退出，尚未执行 Go 测试；改为上述直接 `--exec env` 后三个批次均成功。此记录只覆盖列出的存储验证；单元测试、race、编译、前端及性能测量由本次合并的其他验证记录分别列出。

The initial launcher failed before tests started because a nested shell parsed spaces in the Windows PATH. Direct `--exec env` resolved it. This report records the selected storage checks; unit/race/build/frontend/benchmark results are documented separately.
