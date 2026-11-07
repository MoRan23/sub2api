# 总额度按次发放验证记录 / Verification record

基线：`d418e1356`。验证日期：2026-11-07。使用本地模拟服务与独立测试数据库，未连接生产用户、订阅或支付网关。

## 后端

Go 1.27.3，`GOEXPERIMENT=greenteagc,jsonv2`。

```bash
cd backend
GOEXPERIMENT=greenteagc,jsonv2 go build ./...
GOEXPERIMENT=greenteagc,jsonv2 go test -race -tags=unit -p=1 \
  ./internal/service ./internal/handler/admin ./internal/handler \
  ./internal/repository ./internal/managementmcp \
  -run 'Test(.*Subscription.*|.*TotalQuota.*|.*QuotaGrant.*|.*Redeem.*|AdminGroupTotalQuota.*|DefaultSubscriptionGroupsRejectTotalQuota.*|CatalogTotalQuota.*|.*BillingCache.*|DuplicateGroup.*|APIKeyRepository_GetByKeyForAuth_PreservesPermanentQuota)' \
  -count=1 -timeout=8m
```

覆盖每次额度与精度校验、旧接口及普通订阅、永久模式不可变和复制继承、真 NULL 期限、零额度拒绝、兑换批量改组／启用、CSV、幂等追加、缓存失效、真实鉴权投影及默认赠送限制。可空期限改变后，旧测试夹具与断言同步改为指针；未放宽额度或幂等断言。

## 隔离 PostgreSQL / Redis

Linux / WSL 测试，Docker 使用 `postgres:18.1-alpine3.23`、`redis:8.4-alpine`。测试 harness 创建独立 `sub2api_test` 数据库、随机宿主机端口和 Redis 命名空间；测试结束自动清理容器。

```bash
cd backend
GOEXPERIMENT=greenteagc,jsonv2 go test -tags=integration ./internal/repository \
  -run '^(TestTotalQuota|TestUserSubscriptionRepoSuite|TestRedeemCodeRepoSuite|TestRedeemReduction|TestRedeemConcurrentReductions|TestBillingCacheSuite)' \
  -count=1 -timeout=5m
GOEXPERIMENT=greenteagc,jsonv2 go test -race -tags=integration ./internal/repository \
  -run '^(TestTotalQuota|TestRedeemConcurrentReductionsBothApply|TestBillingCacheSuite)' \
  -count=1 -timeout=5m
```

已通过：空库迁移、迁移重复执行、旧码停用、自动赠送配置定点清理、已有权益保留；八路并发首次发放追加；兑换失败整笔回滚与重复兑换拒绝；有限期额度先消费；永久事件保持 NULL（耗尽后追加也不篡改旧期限）；永久订阅不参与到期清理；Redis 有限／永久往返与损坏格式拒绝。旧并发扣费测试改用已提交夹具及连接池，避免多个 goroutine 共用同一个 SQL 事务连接。

## 前端

```bash
cd frontend
pnpm test:run
pnpm typecheck
pnpm build
```

全量 Vitest：439 个文件、3788 项测试全部通过。类型检查、生产构建和 30 个变更 TypeScript / Vue 文件的定向 ESLint 通过。覆盖永久模式创建／只读编辑／复制、每码与每人追加金额、期限选择、单个及批量发放幂等键、历史兼容及永久订阅剩余额度展示。构建仍有现有 Browserslist 数据陈旧与 chunk 体积提示。

缓存迟到回填测试最初的内存预算小于 Ristretto 条目自身开销，导致夹具没有缓存数据；已修正夹具预算，保留精确读取次数及旧值不能覆盖新值的断言，定向 race 重复 20 次通过。

## English

Verification uses Go 1.27.3 with `greenteagc,jsonv2`, local fixtures and isolated PostgreSQL / Redis containers. The commands above cover grant amounts, nullable expiry, finite-first consumption, zero-credit admission, code snapshots, transaction rollback, duplicate requests, cache recovery and migration replay. No production balances, subscriptions, payment gateway or model requests were used.

All 3,788 frontend tests in 439 files passed, along with type checking, the production build and targeted lint. Existing build advisories concern Browserslist data age and bundle chunk sizes.
