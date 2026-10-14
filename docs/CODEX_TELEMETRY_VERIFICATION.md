# 三系统遥测验证记录

## 失效池退出与维护热点修复（2026-10-14）

基线为 `dev@3fb0fcd7c`。删除账号在同一事务内清理其遥测池；历史遗留的
已删除或认证类型已改变的所有者先从到期扫描、批次领取中排除，再分批清理。
迁移 271 一次性回填池级到期时间，并增加全量批次关联索引。日常扫描不再
聚合历史活动；业务唤醒仍可立即派发批次，到期扫描限为每实例每秒一次。

隔离 PostgreSQL／Redis 用例覆盖：101 个失效池不阻塞健康池、关闭遥测时
分批清理、账号删除与级联回滚、并发写入不复活、认证资格变化、活动与到期
汇总原子提交、重复迁移、终态批次索引，以及原地修改历史 JSON 仍须校验。
查询计划断言确认日常到期查询不再访问活动表。

服务用例覆盖高频通知合并、超时立即停止后续维护、并发删除、错误日志脱敏
与限频、租约到期后的空闲截止时间前移，以及历史保留与容量保护。三系统
重启测试补齐当前授权校验要求的 UUIDv7 `SyncSessionID`，保持精确批次数
及重启断言；指标测试辅助函数显式推进维护，不依赖限频窗口内再次扫描。

同一台开发机上的局部基准前后对比：

| 步骤与样本 | 原耗时 | 修复后 | 每次分配字节（原 → 后） |
| --- | --- | --- | --- |
| JSON 校验，约 27 KB 嵌套指标 | 3.413 ms | 0.224 ms | 9,587,778 → 80,727 |
| 未达容量的 3000 条已完成记录维护 | 194.6 µs | 44.3 µs | 368,289 → 0 |

JSON 键校验复用只读替换器；未变更历史不重复运行存储边界校验；未超容量时
不构造、排序淘汰列表。凭据字段、重复键和原地修改检查均保留。仍存在整池
加载与快照复制，这些局部数据不能推算为整机 CPU／内存降幅，也不解决宿主机
CPU steal。测试仅使用模拟发送端，没有真实模型、官方遥测或生产部署。

以下检查通过（Go 1.27.2，`GOEXPERIMENT=jsonv2`）：

```sh
go test ./internal/service ./internal/handler/admin -run 'Test.*CodexTelemetry' -count=1 -timeout=180s
go test -race ./internal/service -run 'Test.*CodexTelemetry' -count=1 -timeout=180s
CI=1 go test -tags=integration ./internal/repository -run 'TestCodexTelemetry|TestMigration241CodexTelemetry|TestAccountDelete.*Telemetry' -count=1 -timeout=240s
go build ./...
golangci-lint run --new-from-rev=HEAD ./internal/service/... ./internal/repository/...
```

数据库与竞态检查在 WSL Linux 执行；数据库使用测试临时容器，未连接生产。
本轮没有前端改动。

## Codex 0.162 观测缺口修复（2026-10-07）

基线为合并后的 `dev@7d758e332`。本轮补齐窗口元数据解析、最终出站
Guardian 来源线程、脱敏额外字段和 WS 冻结快照，并兼容四项 auth-storage
指标定义。技能日志和凭据存储操作没有客户端原始数据入口；接口和页面明确
显示“未采集”，不推定或生成这些事件，不改变已有遥测开关及模拟策略。

以下检查通过：

```sh
# backend
go test -tags=unit ./internal/service ./internal/handler/admin -run 'Test.*(CodexTelemetry|CodexWire|Fingerprint|OpenAICodexWindow)' -count=1
go test -race ./internal/service -run 'Test(CodexTelemetryCoverage|CodexTelemetryClientOnly|CodexTelemetryAuthStorage|CodexWireProfileParsesWindow|CodexWireWindowMetadataMerge|FingerprintObservationWireMetadata|FingerprintObservationExtraMetadata|FingerprintObservationSafe)' -count=1
go build ./cmd/server
go vet ./internal/service ./internal/handler/admin

# frontend
pnpm test:run src/views/admin/__tests__/CodexTelemetryObservations.spec.ts src/api/__tests__/admin.codexTelemetry.spec.ts src/views/admin/__tests__/FingerprintObservationRequestDetails.spec.ts src/views/admin/__tests__/FingerprintObservationView.spec.ts src/i18n/__tests__/localeKeyCompleteness.spec.ts
pnpm typecheck
pnpm build
```

前端 5 个测试文件、93 项测试通过；9 个改动文件的定向 ESLint 通过。
构建只有现有大 chunk 和 Browserslist 数据陈旧提示，无编译失败。

覆盖窗口序号零值、null／负数／溢出、嵌套与兼容载体优先级、服务器窗口
不被客户端值覆盖、HTTP／WS 一致性、快照深拷贝、原始请求不变、未知额外
字段值脱敏、旧记录缺失及页面安全文本。覆盖四项指标的标签、单位、聚合与
序列化恢复，以及全部模式不生成客户端专属事件，coverage 不计为发送记录。

使用本地 Vite、模拟管理员接口和独立无头 Chrome 验证中文 1280×720、英文
390×844：采集范围两项状态可见，20 条列表和详情正常，桌面内部滚动与移动
页面滚动有效，无横向溢出及 JavaScript 异常。未访问生产服务。

本轮无数据库 schema 或迁移改动，未运行数据库集成测试或无关全仓库套件。
全部上游和发送端均使用模拟；没有真实模型、官方遥测调用或部署。

## 四项兼容修正（2026-09-22）

本轮从 `dev@a5411ee97` 修改，仍以本地 Codex 0.155.1 源码快照为依据。
仅修正权限字段、多环境 shell、指标桶与标签、子代理 hook；其余客户端活动
继续使用原模拟规则，不新增公共 API、页面配置或数据库迁移。

在 `backend` 执行并通过：

```sh
go test ./internal/service -run '^TestCodexTelemetry' -count=1
go test -race ./internal/service -run 'Test.*CodexTelemetry' -count=1
go test -race ./internal/handler/admin ./internal/repository -run 'Test.*CodexTelemetry' -count=1
go test ./cmd/server -run '^$' -count=1
```

另在 WSL 隔离 Docker 环境执行 PostgreSQL 18.1／Redis 8.4 集成测试，普通
回归和最终竞态检查均通过，最终 15 项无失败、无竞态：

```sh
CI=true go test -race -tags integration ./internal/repository -run 'CodexTelemetry' -count=1
```

新增存储用例贯穿真实数据库 v1 加载、两实例并发追加、重启和封口，确认计数
及周期保留、旧封口批次不变，内部兼容原因不进入 OTLP。发送使用模拟 sender。

新增及调整的用例覆盖：

- 主回合完全访问网络为 true，未知／受限仍为模拟 false；标题独立保持受限。
- 旧版和多环境 XML、三系统远程 shell、唯一主环境、主次冲突、不可用状态、
  重复 ID、引用／技能／历史排除，以及正文、节点、深度和环境数量上限。
- 主线程 Stop／显式 Interrupt，spawned 子代理 SubagentStop，以及其他、未知、
  矛盾和 Guardian 来源的 hook 抑制；事件数量与指标样本一致。
- 三项字节指标原生桶边界、两个目录标签、600 个以上流事件完整聚合、逐事件
  kind／success、HTTP 不虚构计时、WS 缓冲补解析不重复计数及深拷贝。
- 固定事件名名单、任意 UUID／JWT 样式 type 归 unknown、不写入报文或快照；
  每次尝试及跨请求窗口的类别上限，overflow 样本仍累加。
- v1／v2 快照、旧类别归 unknown 后碰撞合并、三个不可重桶的旧聚合清除及
  内部兼容原因、其他样本和启动标记保留、已封口批次不可变。

本轮没有前端改动，未重复前端测试、构建和浏览器验收；未运行无关全仓库
套件。上述相关检查没有未解决失败。所有发送均由测试模拟，不发送真实业务
或官方遥测，不部署、不发布标签、不等待 CI。

## 原三系统实现记录

验证日期：2026-09-22。实现基线：`dev@b051d90ca`。行为依据为本地 Codex
0.155.1 源码快照；该源码目录没有 Git 元数据，不宣称对应某个上游提交。

## 后端

在 `backend` 执行，以下检查通过：

```sh
go test ./internal/service ./internal/handler/admin ./internal/repository -run 'Test.*CodexTelemetry' -count=1
go test -race ./internal/service ./internal/handler/admin ./internal/repository -run 'Test.*CodexTelemetry' -count=1
go test -race -tags integration ./internal/repository -run '^TestCodexTelemetryPostgres' -count=1
go test ./cmd/server -run '^$' -count=1
```

新增和调整的测试覆盖：

- 三系统、实际安装身份及缺失分区，Spark 凭据归属、冻结 UA/代理和身份异常跳过。
- 同回合多次 sampling、物理重试、工具待回传、明确下一回合封存，以及静默未完成。
- HTTP/SSE/WS 统一事件分类，首帧与有效输出、AgentMessage 分离，失败锁存和实际交付结果。
- 场景种子、只读写入限制、平台专属能力、hook/Guardian/标题关联，官方指标排除名单及 Delta 聚合。
- 四种模式、真实优先、配置代次隔离、未封存活动失效，节点强关与全局关闭的差异。
- PostgreSQL 行锁、跨实例领取、版本保护、发送租约、未知结果不重发、保留期和漏通知补偿。
- 公开设置已提交但运行态回调失败时的策略修复；旧回调不能逆转最新配置。
- 策略变化时 `sending` 保存为 `unknown`，`queued/claimed` 取消；迟到回调不能改写终态。

PostgreSQL/Redis 测试使用仓库集成测试启动的隔离 Docker 容器并实际执行迁移
250。额外的运行态联调贯穿业务采样、数据库、进程重建、批次恢复和模拟发送，
确认三个系统的种子与初始化去重不因重启改变，发送读取最新凭据，批次不保存
token。全部发送由本地模拟 sender 接管。

收尾修复后，运行态 13 项测试、HTTP/WS/Stream/NativeHTTP、内存存储及数据库
策略相关竞态测试分别重新通过。综合回归中曾出现旧池键预期、旧回合完成预期
及异步等待辅助计数不匹配，均已修正；本次相关检查没有未解决失败。

## 前端

在 `frontend` 执行：

```sh
pnpm test:run src/views/admin/__tests__/CodexTelemetryObservations.spec.ts src/api/__tests__/admin.codexTelemetry.spec.ts src/views/admin/__tests__/SettingsView.spec.ts src/views/admin/__tests__/FingerprintObservationView.spec.ts src/i18n/__tests__/localeKeyCompleteness.spec.ts
pnpm typecheck
pnpm lint:check
pnpm build
```

5 个测试文件、111 项测试通过；类型检查、lint 和构建通过。最终布局与原因文案
调整后重跑相应组件、语言完整性检查及构建。

模拟接口浏览器验收通过：

- 三个设置开关的加载、保存、互相约束，以及仅模拟、仅实测模式显示。
- Windows/macOS/Linux 与 `observed/simulated/mixed` 筛选，池、批次和字段来源详情。
- 未知发送结果、存储故障和推定原因的显示。
- 约 1266×710 的短屏下列表可见并可滚动；浏览器控制台没有错误。

浏览器只连接本地 Vite 和合成接口，验收结束后关闭测试标签页与服务。

## 验证边界

没有发送真实业务或官方遥测请求，没有部署、发布标签或等待 CI。没有运行与
此改动无关的全仓库测试；不将本地模拟端点接受成功解释为官方端点或模型路由
效果。生产升级前须按 [使用说明](CODEX_TELEMETRY.md) 排空旧进程后执行迁移，
避免新旧遥测规则同时运行。
