# 三系统遥测验证记录

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
