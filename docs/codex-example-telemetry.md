# Codex 示例关闭上报 / Disable reporting in Codex examples

所有生成的 Codex 配置统一包含：

```toml
[analytics]
enabled = false

[otel]
exporter = "none"
metrics_exporter = "none"
trace_exporter = "none"
```

覆盖 API Key 使用说明中的 HTTP、WS、Grok、路由配置、PAT 配置，以及 CC Switch 一键导入；中英文 PAT 示例同步更新。`analytics` 与 OTEL 的日志、指标、追踪导出均明确关闭。

此变更作用于新生成的示例和导入配置。已有客户端的 `config.toml` 需要合并上述设置并重启客户端。Sub2API 服务端遥测设置保持现状。

All generated Codex examples now explicitly disable analytics and OTEL log, metric and trace exporters. Coverage includes HTTP, WebSocket, Grok, routed providers, PAT examples and CC Switch imports, with matching Chinese and English instructions.

Existing client configuration is not rewritten. Merge these settings into existing `config.toml` and restart the client. Sub2API server-side telemetry settings are unchanged.
