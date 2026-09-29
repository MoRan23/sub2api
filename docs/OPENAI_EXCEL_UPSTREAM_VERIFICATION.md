# Excel 上游验证记录

验证日期：2026-09-28 至 2026-09-29。修改前固定基线：`214165ed8`；同工具链复现基线失败。参考适配逻辑来自 `excel-codex-bridge` 固定提交 `9254d3f25b0323bbf71db4f25a242fe32027a544`（Unlicense）。

已通过：

- Excel wire 协议转换、工具声明和 function/custom/namespace 回传；未知工具和跨会话 file ID 被拒绝。
- SSE 多行 data、真实完成终态、失败/截断处理、取消和 64 MiB 读取上限。
- Windows、macOS、Linux 入站身份各自冻结 installation ID 与会话；Excel 出站统一 Windows WebView2/Chromium UA 和 Windows TLS，Responses、透传、Chat、Messages、图片与 WS→HTTP bridge 共用该发送规则。
- 图片生成/编辑真实返回计数、空结果失败、实际 endpoint、代理、附件上传缓存和输入数量/64 MiB 工作量限制。
- Redis 加密状态跨实例读取、TTL、原子容量限制、代次/租户/会话隔离。
- 糖果测试的 Excel 目录、原始模型 ID、默认 medium、单次发送、配置代次变化、取消和账号状态隔离。
- Go race 定向测试、前端 369 个文件/2,809 项测试、前端构建、lint、真实 Chromium mock 验收。浏览器验收未发送外部请求。

后端全包编译 `go test ./... -run '^$'` 和构建 `go build ./...` 通过。核心协议、图片、账号开关、糖果题、Redis 状态及 DTO 的定向回归和 race 通过；WS bridge 和相关原生 WS 回归通过。这里的“全包编译”不代表全量测试运行通过。

最终 Windows WebView2 UA 模板修改后，重新执行 Excel 协议、所有入口、图片上传、账号开关、糖果、状态存储和配置守卫的定向 `go test -race`，全部通过。工具回传中的附件归属校验、DTO 私有字段隐藏另行执行竞态回归通过。最终 `golangci-lint run --new-from-rev=214165ed8` 为 `0 issues`；没有使用新增豁免。

前端命令为 `pnpm test:run`、`pnpm build`（包含类型检查）、`pnpm lint:check`，另有 Excel 完整性文案相关 42 项回归。Chromium 全 API mock 验收包含新建默认关闭、编辑保存、批量不修改/开启、列表徽标、内置目录/default medium 和历史上游显示；没有页面异常或外部请求，没有点击真实推理。临时验收文件位于本机 `tmp/excel-upstream-browser`，未加入发布产物。

PostgreSQL 集成测试已编写，并用 `go test -tags=integration ./internal/repository -run 'TestAccountRepoSuite/TestExcelRoute|TestAccountCandy' -count=1 -v` 尝试执行。本机没有 Docker/PostgreSQL，harness 明确跳过，不能称为通过。miniredis 的 Lua 原子容量限制、实际 AES 加密和跨实例恢复测试已执行；它不能替代真实 PostgreSQL/Redis 集成验收。

扩大相关回归范围时，以下失败均在修改前的 `214165ed8` 基线复现；未删除测试、放宽断言或增加豁免：

| 测试 | 基线失败内容 |
| --- | --- |
| `TestAccountConfigurationExplicitIntentIsScopedAndCopied` | 旧环境 UA 不包含 Ubuntu |
| `TestOpenAIGatewayService_APIKeyPassthrough_ImageIntentPreservesGateAndBilling` | 请求体预期未包含现有默认指令 |
| `TestOpenAIGatewayService_APIKeyPassthrough_Transient5xxTriggersFailover` | 多个状态码用例的相同请求体预期差异 |
| `TestOpenAIRequestBodyLimitFailover_HTTP413SwitchesAccountsBeforeWrite` | API Key 透传请求体预期差异 |
| `TestOpenAIWSHTTPBridgeAcceptsFirstFrameAboveLegacy16MiB` | 已有默认提示词影响请求体大小预期 |
| `TestForwardAsAnthropic_AutoDerivesPromptCacheKeyWhenMessagesDispatchHasNoSessionID` | prompt cache key 预期差异 |
| `TestForwardAsAnthropic_TrimsFullReplayOnlyForCodexCompatModels` | 历史回放数量预期差异 |
| `TestForwardAsAnthropic_APIKeyMetadataSessionSurvivesChangingCacheControlAnchorAfterContinuationDisabled` | 会话/prompt cache key 预期差异 |
| `TestForwardAsAnthropic_InjectsPromptCacheKeyForAPIKeyMessagesDispatch` | prompt cache key 预期差异 |

未执行真实 Excel 服务连通性、真实 OAuth 刷新、实际糖果推理、生图和遥测。没有部署、发布标签或连接未知用途数据库。
