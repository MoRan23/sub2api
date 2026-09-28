# v0.2.9 网关交叉验证

本记录对应固定上游提交 `9a62841fd124d026cf3694fcf9b79e98addcdbdc` 与本地糖果题测试实现。所有请求均为本地合成响应，无真实上游、账号授权或遥测请求。

## 通过项

- Responses 与透传路径在 Windows、macOS、Linux 配置下保留账号对应 UA、安装身份、账号代理和 opaque turn-state；多代理 beta 保留，旧 `responses=experimental` 仅对 OAuth 清理。
- 糖果题测试在 Responses、透传、Chat 三条路径直接使用实时目录返回的原始模型 ID；业务模型映射不影响测试。模型目录错误、401/403/429、取消和传输错误不会修改账号健康状态，也不会重发推理。
- 糖果题流式响应要求成功终态。部分响应、仅 `[DONE]`、流内失败后完成、截断后完成均返回测试失败，不会被后续完成事件改写为成功。
- native HTTP 的 UA、TLS/代理隔离、请求生命周期及 opaque 状态保护专项测试通过；原生 WS 不使用已退役的票据缓存。
- Anthropic、Antigravity、Google API、Claude 和 apicompat 相关包测试通过。

## 执行命令

```text
GOMAXPROCS=2 GOEXPERIMENT=jsonv2 TYPESAFE_LIVE_TEST=0 go test -p 1 ./internal/service -run 'TestOpenAIBuildUpstreamRequestOAuthResponsesPreservesCallerBeta|TestOpenAIHTTPPassthroughStripsOnlyOAuthLegacyResponsesBeta|TestUpstreamV029|TestCandy' -count=1
GOMAXPROCS=2 GOEXPERIMENT=jsonv2 TYPESAFE_LIVE_TEST=0 go test -race -p 1 ./internal/service -run 'TestUpstreamV029|TestCandy|TestFetchCandy|TestGuardOpenAICodexTurnState|TestOpenAIHTTPIdentityBuildersGuardCompositeTurnStateAfterProjection|TestOpenAINativeHTTP|TestWithOpenAINativeHTTPScope|TestCodexTelemetryNativeHTTP|TestNativeAux|TestOpenAIPluginHTTP|TestStructuredOutputsBeta|TestBuildUpstreamRequestStructuredOutputsBeta|TestAntigravityCompat|TestHandleClaudeStreamingResponse_EmptyStream' -count=1
GOMAXPROCS=2 GOEXPERIMENT=jsonv2 TYPESAFE_LIVE_TEST=0 go test -p 1 ./internal/repository -run 'TestNativeUpstream|TestOpenAINativeReq|TestTLSFingerprint' -count=1
GOMAXPROCS=2 GOEXPERIMENT=jsonv2 TYPESAFE_LIVE_TEST=0 go test -p 1 ./internal/pkg/apicompat ./internal/pkg/antigravity ./internal/pkg/googleapi ./internal/pkg/claude -count=1
```

结果：上述四组均通过；race 专项无报告。合并工作树中的其他全包测试和历史失败由主合并验证记录单独列出。
