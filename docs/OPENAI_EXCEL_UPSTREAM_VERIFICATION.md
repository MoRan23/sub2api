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

## 2026-09-29：修正只返回准备说明的回归

本次基线为 `97c979d7c`。该版本跳过未知/损坏工具及超出并行限制的调用后，仍可能发送成功终态；若只剩 commentary 或 reasoning，客户端便没有工具可以继续执行。过滤还会改变终态的输出索引。现恢复整次响应失败的严格约束，并撤回虚构缺失 `call_id` 的处理；对象/字符串参数兼容保留。

通过合成夹具覆盖：准备说明/空 reasoning/部分回答加坏工具、有效工具混排索引、并行关闭、真实 ID 回传、仅 commentary 失败及普通短回答成功、空/非最终消息不掩盖 commentary、真实终态省略/空输出的 item 恢复、已宣布工具在终态缺失/冲突、非法/重复索引、只有参数事件的工具关联、尾随 `[DONE]` 时读取协程退出、15 秒保活和取消。保活测试使用 Go 虚拟时钟，没有实际等待或外部推理。

参考项目的行动提示与稳定前缀 reminder 已补齐；测试验证 tool_choice none、无工具的纯回答、原指令不变、真实命名空间和历史增长时前缀稳定。没有引入自动补发请求或自动执行工具。

本次执行 `go test -race ./internal/service -run 'Excel' -count=1` 和 `go vet ./internal/service` 通过；补齐新测试的 lint 检查后，`go test ./internal/service -run '^TestExcelCompletion' -count=1` 通过，`golangci-lint run --new-from-rev=97c979d7c ./internal/service/...` 为 `0 issues`。新回归夹具覆盖错误不泄漏工具名称/参数、原始模型和 usage 保留，以及终态与工具事件的索引一致性。

服务器只读检查在对应时段发现账号的 HTTP 200 完成记录，未保存原始成功响应流，因此不能从日志确认截图请求具体丢失了哪个调用。上述本地回归已独立复现，不将所有简短回答都归为该原因。本次不部署、不发送真实上游请求。前端和数据库结构未修改，未重跑前端全量与外部存储集成测试。

## 2026-09-29：细分 invalid_tool_call 与命名空间兼容

本次基线为 `20770241d`。只读服务器日志确认连续六次 `invalid_tool_call`，但该版未记录具体工具转换分支，不能据此认定这六次请求的确切失败形态。未采集原始业务内容、未发送真实推理、未部署。

源码与合成夹具确认两项遗漏：独立 `namespace` 的 function/custom 调用只按叶名称查找，导致已声明工具被拒绝；原生 `update_plan` 的 `planned/queued/blocked/active/started/current/finished` 等参考状态别名未完整归一。修复按完整声明匹配工具，并保留原始调用历史；持久化字段白名单同步保留 `namespace`，回传结果根据完整原生调用识别 Excel 内置工具，避免误改其他命名空间的同名工具。流内已观察到的命名空间同样参与终态一致性检查。未知工具、命名空间冲突、缺失真实调用 ID 和不合法参数仍拒绝，不恢复吞工具、伪造调用 ID 或自动重试。

新增固定的工具转换细分原因，覆盖原生参数、转接 envelope、嵌套上限、工具查找、调用 ID、custom 输入和 function schema；接口错误和日志均不输出原始工具名、参数键值、schema 路径或业务正文。

验证通过：`go test -race ./internal/service -run 'Excel' -count=1`、`go vet ./internal/service`，以及新增往返/诊断测试的 `go test -race ./internal/service -run '^TestExcelToolCompat' -count=1`。夹具覆盖直接与转接的 function/custom、独立/全限定命名空间、真实状态存储的字段过滤与重新实例化恢复、同名内置工具隔离、十类细分错误及日志脱敏、SSE 一致性、18 种计划状态表达。状态存储验证使用内存后端与测试加密器，不代表外部 Redis 集成验证；本次未运行外部数据库、前端全量或真实上游测试。

新测试的类型断言/返回值检查与格式化修正后，`golangci-lint run --new-from-rev=20770241d ./internal/service/...` 为 `0 issues`，未新增豁免。

## 2026-09-29：默认 functions 命名空间与详细服务端诊断

本次基线为 `10c694681`。此前只读日志确认 `native_tool_undeclared`，未记录原生工具名；不能从旧日志倒推出名称。对照本地 Codex `protocol/src/tool_name.rs` 与 Lite 工具序列化，确认缺失/空命名空间与 `functions` 等价。适配器此前只按完全相同的命名空间匹配，能够稳定复现声明 `functions.update_plan` 而原生调用省略命名空间时的误拒。此次兼容默认空间，仍拒绝其他命名空间错配和默认空间重名歧义；没有放行 `list_skills` 等未声明工具。

按用户要求增加详细服务端诊断，记录有界标识符、阶段、转接状态、参数形状、匹配结果和声明工具清单；仅在失败时生成，客户端错误格式仍为静态原因码。非法/过长标识符整段隐藏，工具描述、参数及 schema 键值、令牌、URL、图片原文和业务正文不入日志。兼容修复和诊断代码已通过本地合成验证，但尚未部署，因此这些日志无法补回旧请求的原始信息。

新增夹具覆盖 function/custom 的直接与转接调用、默认空间的省略/显式表达、重名歧义拒绝、legacy/全限定计划名称、字面同名工具隔离、参数归一及原生 history 精确回放。诊断覆盖 JSON/SSE、所有解析阶段、嵌套层数、错误候选名、恶意标识符、32 项清单上限及不可变快照；凭据、业务正文、参数键值与 schema 内容均不得进入日志或下游错误。既有并行限制、未知工具拒绝和真实终态要求保持。

验证通过：`go test -race ./internal/service -run 'Excel' -count=1`、`go vet ./internal/service`。修正新增代码的两项 staticcheck 风格问题后，`go test -race ./internal/service -run 'Excel.*Diagnostic' -count=1` 再次通过，`golangci-lint run --new-from-rev=10c694681 ./internal/service/...` 为 `0 issues`。没有新增 lint 豁免，也没有放宽原有失败断言。

本次只改后端工具兼容及本地日志；没有数据库迁移或前端改动。未重跑前端全量、全后端非 Excel 测试或外部 PostgreSQL/Redis 集成；状态回放使用内存后端和测试加密器。本次未发送真实上游、授权或遥测请求，未部署或重启服务。
