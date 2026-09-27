# 糖果测试队列与存储验证

本记录覆盖持久化队列、判分结果保存、领取与取消，以及同次运行的 turn-state 退役迁移测试。HTTP 执行器、管理接口和页面验证另见总验证记录。

## 环境与隔离

- 实现基线：`dev@35ccf3aa7`。
- 单元测试：Windows / Go 1.27.0；竞态与数据库：WSL Ubuntu-24.04 / Go 1.27.0 工具链。
- 设置 `GOEXPERIMENT=jsonv2`、`TYPESAFE_LIVE_TEST=0`；WSL 限制 `GOMAXPROCS=4`、构建并发 `-p 2`。
- 数据库测试使用现有 `integration_harness_test.go`，通过 Docker 29.4.0 为每次运行新建 PostgreSQL `18.1-alpine3.23` 和 Redis `8.4-alpine` 容器。没有连接既有数据库。
- 使用合成账号、凭据和回答；没有执行真实推理、授权刷新或遥测请求。

## 已执行检查

以下命令从 `backend` 目录执行，均设置上述环境变量。

| 检查 | 结果 |
|---|---|
| 队列单元测试 | 通过，6 个顶层测试；执行分类另含 6 个子用例 |
| 队列竞态 | 通过，实际匹配 6 个 Queue 顶层测试，1.127 秒；此表达式不包含独立 grader 测试 |
| 初始数据库回归 | 修正首次发现的 SQL 参数类型问题后通过，5.271 秒 |
| 并发与迁移竞态 | 通过，8 个顶层测试，6.316 秒；包含下列前 6 项 Candy 测试及两项退役测试 |
| 指定项取消竞态 | 新增第 7 项 Candy 测试单独通过，5.619 秒 |

```sh
go test ./internal/service -run '^TestCandyQueue' -count=1
go test -race -p 2 ./internal/service -run '^TestCandy(Queue|Answer)' -count=1 -timeout=3m
go test -p 2 -tags integration ./internal/repository -run '^(TestCandyRepository|TestRetireCodexStateMigrationPreservesAuthorizationAndIdentity|TestCodexImportRealPostgresRetiresCollectorConfiguration)' -count=1 -timeout=5m
go test -race -v -p 2 -tags integration ./internal/repository -run '^(TestCandyRepository|TestRetireCodexStateMigrationPreservesAuthorizationAndIdentity|TestCodexImportRealPostgresRetiresCollectorConfiguration)' -count=1 -timeout=5m
go test -race -v -p 2 -tags integration ./internal/repository -run '^TestCandyRepositorySubsetCancellationIsBoundToBatch$' -count=1 -timeout=3m
```

队列单元测试覆盖：配置归一及明确跳过、无效账号与参数、完整正确／错误／缺项回答、缺少终态、传输失败、1 MiB 上限、截止后迟到成功、错误脱敏和执行器 panic 不重试。

数据库测试清单：

1. `TestCandyRepositoryGlobalCapacityAndAccountSerialization`：12 个独立 repository 实例对应 12 个 goroutine，经屏障同时领取，验证全局仅 3 项运行、账号不重复、错误领取 UUID 不能续租。
2. `TestCandyRepositoryQueuedSameAccountWaitsAcrossInstances`：6 个实例同时领取两个相同账号任务及一个不同账号任务；即使全局还有空位，同账号仍等待，前项终结后才可领取后项。
3. `TestCandyRepositoryCancellationLeaseExpiryAndLateCompletion`：排队／运行中取消、取消胜过迟到成功、过期租约不能续期或提交、运行中断不重放。
4. `TestCandyRepositoryIdempotencyAndFiveResultRetention`：同键同配置不重复创建、同键不同配置冲突、最近五条终态保留、列表摘要不加载原始回答。
5. `TestCandyRepositoryTimeoutWinsOverSuccess`：数据库截止时间优先于完成回调，清除成功判分。
6. `TestCandyRepositoryKeepsResultsUntilWholeBatchFinishes`：未完成批次不裁剪其已完成项；批次完成后裁剪，原始进度计数保留，`retained_total` 用于剩余记录分页。
7. `TestCandyRepositorySubsetCancellationIsBoundToBatch`：仅取消指定批次中的指定项，不影响同批其他项或其他批次。

退役测试：`TestRetireCodexStateMigrationPreservesAuthorizationAndIdentity`、`TestCodexImportRealPostgresRetiresCollectorConfiguration` 均在同一隔离套件中通过。

## 发现的问题及验证范围

- 首次真实 PostgreSQL 运行发现插入语句中复用的 `$7` 被推导为不一致的 `varchar`／`text` 类型。已增加明确类型转换，随后完整数据库回归与竞态通过；没有放宽断言。
- 开发并行期间出现的旧 turn-state 符号、导入项和文件移动编译错误发生在容器启动前，后续源码收敛后已通过。它们不计为执行过数据库测试。
- 空库验证由 harness 执行完整迁移序列，包含 260 和 261。260 增量语义由独立临时 schema 的旧格式夹具验证，并重复执行检查幂等；**没有使用完整生产 259 数据库备份做升级演练**。
- 队列不依赖 Redis 通知，因此 Redis 丢通知不会触发重放。测试容器中的 Redis 由通用 harness 启动，本组测试的状态协调由 PostgreSQL 完成。
- 本组不是全仓测试结果；不得据此宣称其他既有回归、HTTP 路径或前端检查已通过。

原始日志：`C:/Users/Admin/.fastctx/jobs/j-juara8/output.log`（初始数据库通过）、`j-4ot62d/output.log`（队列竞态）、`j-ozowih/output.log`（8 项隔离竞态）、`j-932n71/output.log`（指定项取消竞态）。

## Agent Identity 首次 task 持久化专项

新增 `account_candy_agent_task_cas_integration_test.go`，直接调用真实 PostgreSQL 仓储的 `PatchOpenAIOAuthCredentialsIfUnchanged`，验证糖果测试执行器使用的签名字段、身份字段及 `task_id` 快照条件。没有改变生产仓储实现。

```sh
go test -race -v -p 2 -tags integration ./internal/repository -run '^TestCandyAgentTaskPostgresCAS' -count=1 -timeout=5m
```

共 5 个顶层测试、11 个子用例：task_id-only 更新保留其余全部凭据与账号状态；签名密钥、runtime、账号、用户、组织、access token 和 token version 替换拒绝；缺失／空 task 的旧回调不能覆盖先到结果；母账号／代理绑定变化拒绝；两个 repository 同时注册回调只允许一个 CAS 成功。所有数据为合成夹具，不注册真实 Agent Identity task。

结果：隔离 PostgreSQL／Redis 竞态全部通过，5.701 秒。原始日志：`C:/Users/Admin/.fastctx/jobs/j-w6kjt3/output.log`。
