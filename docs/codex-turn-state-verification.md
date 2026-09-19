# Codex turn-state 采集与状态展示验证记录

基线：`dev@26d980d46`。本次验证使用仓库声明的 Go 1.27.0、`GOEXPERIMENT=jsonv2`、WSL Ubuntu 24.04，以及隔离的 PostgreSQL 18.1 和 Redis 8.4 测试容器。没有连接收费模型上游或部署应用。

## 验证范围

- 编译全部后端包；运行 service、repository、admin handler、DTO、migrations 中相关 turn-state 回归与竞态测试。
- 验证 292/312、332/356 封装和时间、自然响应优先、无状态不采集、异常历史凭据隔离及成功交付边界。
- 验证 10 秒退避、账号级 429/401/403 约束、提前 5 分钟续采、同值不延寿、旧有效缓存保留、真实业务活动窗口。
- 验证慢采集 CAS、业务抢占、取消通知丢失、状态行锁等待后新租约可见，以及快速 WS 响应在成功写回调前完成的两种交错。
- 验证缓存开启但指纹关闭，以及维护初始化失败时的 HTTP/Chat/Messages/WS 观测；独立采集观测标明来源且不延长业务活跃时间。
- PostgreSQL/Redis 集成验证配置行锁、凭据代次、历史消费水位、单飞、代理删除和导入映射；真实 Redis 验证通知载荷、断线重连和订阅取消清理。集成测试设置 `CI=true`，不将跳过算作通过。
- 前端相关 132 项测试、类型检查、lint、生产构建；浏览器模拟验收 8 项，包括最多五行、无占位、额外观测动态补入、红绿灰、宽屏左右列和窄屏堆叠、五秒刷新保持内容、无重叠请求及关闭取消。

## 可复现命令

后端在 `backend` 目录执行：

```bash
env -u OPENAI_API_KEY GOEXPERIMENT=jsonv2 go test ./... -run '^$'
env -u OPENAI_API_KEY GOEXPERIMENT=jsonv2 go test -race \
  ./internal/service ./internal/repository ./internal/handler/admin ./internal/handler/dto ./migrations \
  -run 'Codex(State|TurnState)|AccountConfigurationCommit' -count=1
env -u OPENAI_API_KEY GOEXPERIMENT=jsonv2 go test -tags unit \
  ./internal/service ./internal/repository ./internal/handler/admin ./internal/handler/dto ./migrations \
  -run 'Codex(State|TurnState)|AccountConfigurationCommit' -count=1
```

真实存储测试使用 `go test -tags integration ./internal/repository` 的相关测试；TestMain 创建隔离容器并应用迁移 245/246。完整命令、输出及浏览器截图保存在本地 `.git/task-artifacts/codex-turn-state-demand/` 和 `.git/task-artifacts/codex-turn-state-status-refresh/`。

## 额外发现与基线对照

第一次集成选择式意外扩大到整个账号仓储套件，额外出现两项失败，未将其计为本次相关测试通过：

- `TestAccountRepoSuite/TestEnsureOpenAIInstallationIDSupportsSetupTokenOwnerOnly`：当前树和固定基线均因同一 `chk_accounts_parent_dimension` 约束失败，已确认是原有 fixture 问题。
- `TestAccountRepoSuite/TestUpdateExtra_SchedulerNeutralSkipsOutboxAndSyncsFreshSnapshot`：组合运行出现 outbox 计数失败；当前树和基线分别单独运行均通过。组合运行问题未归因，未删除或放宽断言。

本记录不宣称后端所有无关测试或全量后端 lint 已通过。模型长度分类只说明可观察封装形态，不验证解密内容或模型质量。

## 采集正文与 SSE 限流调整（基线 `45ebf0935`）

采集正文对齐 `ccodex-sleep-state@b18fabf9`；保留现有账号身份与独立请求隔离。结构化 SSE 限流沿用 HTTP 429 的账号级采集退避，真实 HTTP 状态保持不变。按后续要求，普通失败及无目标状态的最短重试间隔由 10 秒改为 30 秒，更长 Retry-After 和账号冷却优先。

新增测试使用模拟 HTTP transport，覆盖完整正文、18 组限流码/事件/位置组合、字段优先级、SSE event 回退、多行/单字节分块、畸形错误与文本误判防护、失败立即停止及 token 清空、Retry-After，以及 HTTP/SSE 限流后旧缓存保留和跨模型冷却。没有调用真实模型接口。

本轮在 `backend` 目录执行以下相关检查；测试输出保存在 `.git/task-artifacts/codex-collector-sse-limits/`：

普通回归和带 `unit` 标签的竞态测试各通过 484 项测试结果（含子测试），其中新增 56 项；两轮均无失败、无跳过。使用仓库声明的 Go 1.27.0 和 `GOEXPERIMENT=jsonv2`。本轮未改数据库或前端，未重复运行真实存储集成和前端套件。

```bash
env -u OPENAI_API_KEY GOEXPERIMENT=jsonv2 go test ./internal/service \
  -run 'Codex(State|TurnState)' -count=1 -json
env -u OPENAI_API_KEY GOEXPERIMENT=jsonv2 go test -race -tags unit ./internal/service \
  -run 'Codex(State|TurnState)' -count=1 -json
```

## 采集与业务并行（基线 `cbaa6cf97`）

移除采集启动、运行、历史需求创建、到期扫描及仓储发布中的业务租约拦截。业务开始不再取消采集；业务取得满足需求的新目标后仍取消多余采集，同一临期 token 不取消续期。保留账号级采集单飞、30 秒最短退避、配置代次、模型策略和发布 CAS。

并发回归覆盖业务在采集前后开始、续期与业务并行、调度版本变化不破坏冻结快照、业务异常失效 CAS 冲突重试、重复异常业务期间采集合格结果仍可发布，以及取消通知丢失时新业务缓存仍受保护。真实 PostgreSQL 测试覆盖业务租约存在时扫描和发布、状态行锁等待后业务租约提交、新版本提交后旧 CAS 拒绝，以及历史需求消费幂等性。

本轮使用 Go 1.27.0、`GOEXPERIMENT=jsonv2` 和隔离 PostgreSQL / Redis 测试容器。以下命令在 `backend` 执行，输出保存在 `.git/task-artifacts/codex-collector-parallel/`：

```bash
env -u OPENAI_API_KEY GOEXPERIMENT=jsonv2 go test ./internal/service ./internal/repository \
  -run 'Codex(State|TurnState)' -count=1 -json
env -u OPENAI_API_KEY GOEXPERIMENT=jsonv2 go test -race -tags unit ./internal/service ./internal/repository \
  -run 'Codex(State|TurnState)' -count=1 -json
env -u OPENAI_API_KEY CI=true GOEXPERIMENT=jsonv2 go test -tags integration ./internal/repository \
  -run 'TestCodex(CollectorPublicationPostgres|HistoryDemandPostgres|StatePostgres(ScanSelectsOnlyDueCollectors|ConcurrentCASAndGenerationFence|DisableSerializesWithPublication))' \
  -count=1 -json
```

普通回归和带 `unit` 标签的竞态测试各通过 513 项测试结果（含子测试）；真实存储集成通过 14 项。三轮均无失败、无跳过。额外覆盖 PostgreSQL 微秒精度不会误保留本轮防崩溃预留，以及并发更长账号冷却在采集普通失败、自然业务成功后仍有效。

本轮没有前端代码或数据库结构变更，未重复运行前端套件；没有发送真实收费模型请求或部署应用。
