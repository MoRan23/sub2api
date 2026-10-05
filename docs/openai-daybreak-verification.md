# Daybreak 验证记录

本次验证仅使用本地模拟目录、模拟 HTTP／WebSocket 上游及 Docker 隔离数据库，未调用真实模型或部署。

## 本次功能

- Go 定向测试覆盖：档位与请求值映射、客户端显式值保留、真实能力来源、授权隔离、缓存过期／撤回／304／淘汰恢复、辅助查询和过期令牌不改变账号健康。
- 转发模拟覆盖：HTTP 模型映射与换号、Chat／Messages 原始 JSON、ctx_pool／WS passthrough／HTTP bridge 多轮、HTTP→WS 预热、独立压缩和计数排除，以及重试来源不被自动字段污染。
- 管理测试覆盖：开启前能力校验、Blue／Red 依赖、并发设置保护、缓存往返、新导入关闭与警告。
- WSL Ubuntu-24.04 的 Docker 实际运行四项数据库集成测试：授权 CAS 与后台旧快照、三类写入口并发关闭 Blue、批量退出 OAuth 清理开关、仓储新建默认关闭。
- 前端验证覆盖创建／编辑、异步请求不串号、查询失败仍可关闭、未改开关不回写、API 类型和各导入警告展示；执行类型检查及定向 ESLint。
- 执行后端编译及改动范围 Go lint。

数据库验证命令：

```sh
go test -tags=integration ./internal/repository -run 'TestAccountRepoSuite/TestDaybreak' -count=1 -v
```

## 已在修改前代码复现的既有失败

扩展回归中的下列失败通过 `git show 8e7e85c6a:<文件>` 配合 Go overlay 复现。本次未跳过或修改旧断言；这些结果不计作通过。

- `TestFetchCodexModelsManifestOAuth401OnlyCoolsSelectedAuthorization`
- `TestFetchCodexModelsManifestOAuth401TokenRevokedOnlyDisablesSelectedAuthorization`
  - 原测试的账号状态仓储未配置，所期待的旧槽位状态写入没有发生。目录测试的基线 overlay 仅补充新调用方编译所需的类型字段和委托函数，原公开目录实现保持不变。
- `TestFinalizeOpenAIOAuthResponsesRequestAppliesDefaultsAndWireSnapshotForAPIKey`
  - 原 API Key beta header 断言不符。
- `TestAccountConfigurationExplicitIntentIsScopedAndCopied`
  - 原 UA 断言要求包含 `Ubuntu`，与当前 OS profile 行为不符；使用原配置意图文件的 overlay 仍同样失败。
- Chat／Messages 扩展回归中的以下五项（以下列出测试名的辨识部分）：
  - `AstraContinuationRestoresHistoryAndDisablesUnsupportedSession`
  - `AutoDerivesPromptCacheKeyWhenMessagesDispatchHasNoSessionID`
  - `InjectsPromptCacheKeyForAPIKeyMessagesDispatch`
  - `TrimsFullReplayOnlyForCodexCompatModels`
  - `APIKeyMetadataSessionSurvivesChangingCacheControlAnchorAfterContinuationDisabled`
  - 原有身份或历史兼容断言不符；替换回本次修改前的七个转发文件后仍以相同方式失败。

相关本地诊断日志保存在 `.git/daybreak-*-baseline*.log`，不纳入交付代码。
