# Codex turn-state 验证记录

基线：`dev@f56ed9393`。实现和验证日期：2026-09-19。

所有上游请求均使用模拟响应或本地测试服务。没有发送真实收费采集请求，没有部署、发布标签或等待 CI。

## 验证范围

- Go：全部后端包编译通过；配置、代理映射、状态形态与时间、版本 CAS、凭据代次、HTTP / WS 转发与交付边界、来源保护、完整性和观测相关回归通过。
- Linux race：功能相关服务测试通过，覆盖后台单飞、自然响应抢先发布、迟到结果、账号暂停、凭据更新、HTTP 转换和 WS 多轮。末次 WS 预热与 HTTP 请求长度修正另行运行对应竞态测试。
- PostgreSQL 18.1 / Redis 8.4：使用现有 integration harness 启动隔离容器，8 个运行态集成测试、2 个配置集成子测试及 2 个代理集成子测试通过。包含进程重建后的持久化读取、并发 CAS、跨实例锁所有者检查、取消通知、配置行锁、旧快照保护和代理删除保护。
- 前端：195 个相关测试通过，类型检查、完整 lint 和生产构建通过；构建保留现有依赖和分包提示。
- 浏览器：实际本地页面、模拟 API 的 7 个流程通过，覆盖创建、编辑、被动学习、独立代理、状态详情、未知套餐和 Spark 继承；无页面异常、控制台错误或外部请求。

可复跑的主要 Go 命令：

```sh
cd backend
go test ./... -run '^$'
go test -race ./internal/service -run 'TestCodexState|TestCodexTurnState|TestCodexWSState' -count=1
go test ./internal/service -run 'OpenAICodexTurnState|FingerprintObservation|RequestIntegrity|CodexTelemetryHTTP' -count=1
go test -tags integration ./internal/repository -run '^TestCodexState' -count=1
```

数据库测试需要 Docker。没有实际启动容器而被 harness 跳过的执行不应视为集成验证通过。

## 已确认的既有失败

`TestOpenAIWSHTTPBridgeAcceptsFirstFrameAboveLegacy16MiB` 的长度断言失败：实际 `17826622`，预期小于 `17826304`。在独立、干净的 `f56ed9393` 工作树上运行同一单测，得到完全相同的失败。本功能没有修改该断言。

其余所运行的相关 WS 回归通过。该既有失败不计入新功能通过项。

使用说明见 [CODEX_TURN_STATE.md](CODEX_TURN_STATE.md)。
