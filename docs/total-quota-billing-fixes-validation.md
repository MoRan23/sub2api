# 总额度计费修复验证 / Total-quota billing fixes

日期：2026-11-08。起始版本：`129d16ba7`。工具链：Go 1.27.3，`GOEXPERIMENT=greenteagc,jsonv2`。

## 修复与证据

| 问题 | 修复 | 回归证据 |
| --- | --- | --- |
| 永久历史尾数持续准入 | 十位 Decimal 精确消费，只退休真实耗尽的有限批次 | 预存 `0.0000000005` 尾数，经真实 `Apply` 的八位费用量化及扣费后剩额为零，准入拒绝；重复请求不再消费 |
| 单笔追加提交后断连丢失幂等结果 | 与批量共用最长两分钟独立执行及结果持久化 | 缓存失效阶段断开 HTTP 后仍保存成功结果，同 key 重放只发放一次；失败和超时可重试 |
| 发放金额校验超过账本容量 | `0 < quota_amount < 10000000000`，保留八位小数规则 | 金额边界、非有限值和非法精度回归；合法上限与 `DECIMAL(20,10)` 的整数容量一致 |
| 总额度扣费后缓存剩额未变化 | 总额度走失效及 L1 广播，周期模式保持原增量 | 两个独立 SubscriptionService 的一小时 L1 均收到广播并回源零额度；周期增量保持 |
| 充值后的旧异步回填恢复陈旧余额 | Redis generation 与 Lua CAS；DB 读取前取版本、读后复查 | 延迟队列、跨实例、DB 读取期间充值、generation 丢失、数据 TTL 到期、Redis 故障及重叠失效 |
| Engine 恢复丢失准入额度批次 | pricing snapshot v2 持久化批次身份，结算时恢复上下文 | 真实 Capture→数据库 pending→新 worker Settle；原批次到期／之后追加永久额度，迟到费用和超额仍只记原批次；重复结算只入账一次 |

## 已执行检查

- service、admin handler、repository 定向单元及 race：通过。
- PostgreSQL 18.1 / Redis 8.4 隔离集成：11 个顶层测试、13 个叶用例通过。包括空库和重复迁移、旧额度保留、并发发放／兑换回滚、永久生命周期、FIFO、超额、到期快照、历史尾数和 Engine 恢复。
- Redis Lua / 双实例 L1：7 个 miniredis 顶层测试通过；服务层 generation 回归 6 个顶层测试通过。
- `go build ./...`：通过。
- `git diff --check`：通过。

最初集成编译中发现测试将事件值传给指针参数，已修复。race 首轮发现旧金额测试仍把超出账本上限的数值作为合法样本，已调整为正确边界并复测通过。没有未解决的本次定向检查失败。未改前端，未运行前端检查；未操作生产订阅、调用真实模型或部署。

## 复现命令

从 `backend` 目录执行；集成测试需要本地 Docker，自动创建隔离数据库。开发验证的完整输出位于本地 `.git/task-artifacts/quota-review/`，不随源代码发布。

```bash
export GOEXPERIMENT=greenteagc,jsonv2

go test -race -tags=unit ./internal/service ./internal/handler/admin ./internal/repository \
  -run 'Test(CodexEngine(FrozenPricing|QuotaSnapshot|BillingCache)|SubscriptionGeneration|BillingCache|CheckBillingEligibility|FinalizePostUsageBilling|PostUsageBilling|ApplyUsageBilling|ValidateSubscription|IdempotencyCoordinator_Detached|SubscriptionAssignQuota|ConsumeTotalQuotaSubscriptionUsage|AssignTotalQuota|PermanentQuota|SubscriptionCache)' -count=1

go test -tags=integration ./internal/repository \
  -run 'TestUsageBillingRepositoryApply_TotalQuota|TestTotalQuota|TestCodexEngineBillingRecoveryPreservesTotalQuotaAdmissionIntegration' -count=1 -v

go build ./...
```

## 上线与兼容

没有新增数据库迁移，不自动改写任何历史余额。建议排空旧请求和计费队列，统一升级所有 Sub2API 实例，避免旧程序继续运行旧的缓存／快照协议。订阅数据缓存改用 `billing:sub:v3:`；旧数据自然过期。generation 键仅保存随机版本，不含正文或凭据。

Engine 新任务使用 pricing snapshot v2。旧 v1 待结算任务继续兼容读取并按原逻辑结算，因为其原始准入批次未被记录；本补丁不按当前余额猜测该历史信息。回退到不支持 v2 快照的程序前，必须完成新任务的结算；不能直接回退并遗留新版本待处理任务。

## English summary

The six fixes cover historical ledger dust, detached single-grant idempotency, grant amount bounds, authoritative total-quota cache invalidation, generation-fenced Redis writes, and persisted Engine admission batches. Targeted unit/race checks, isolated PostgreSQL/Redis integration and the backend build passed. Production data and model endpoints were not accessed.

No new database migration is required. Drain old work and upgrade all Sub2API instances together. New Engine jobs carry pricing snapshot v2; legacy v1 jobs retain their existing settlement semantics. Complete v2 pending jobs before rolling back to a binary that cannot read them. The Engine project itself is unchanged.
