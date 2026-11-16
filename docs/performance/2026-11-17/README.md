# OpenAI 重试与观测 CPU 优化 / OpenAI retry and observation CPU optimization

基线 / Baseline: `4dcd21440`。本次修复针对已观察到的 429 重试下的大正文重复处理。只修改 Sub2API；不调整重试窗口、次数、计费、账号健康、授权隔离或生产配置。

This change removes repeated large-body work during OpenAI retries. It preserves retry policy, billing, account health and authorization isolation. No production configuration or Engine changes are included.

## 修改 / Changes

1. **遥测借用最终正文**：使用 finalizer 已持有的不可变私有快照，取消每次物理发送的 `GetBody` + `ReadAll` 副本。兼容 reader 保留独立读取、关闭和错误处理；有效空正文与读取失败分别处理。finalizer 的所有权副本仍保留。每次真实发送仍单独生成遥测 attempt，429 仍分别记录。
2. **指纹小字段投影**：验证 JSON 后只借用需要的字段视图，跳过大 `input`、`tools` 和扩展值的解码与复制。同次指纹观测共享载体解析和 profile；发布到观察缓冲或异步任务的结果持有独立小字段，不保留整个正文。
3. **重试预处理复用**：第一次选中非 Engine 账号时才执行 compact、页面、automation、delegation 兼容转换及工具结果校验；随后非 Engine 重试复用结果并恢复路径。Engine 仍使用原始正文和路径；身份、模型、时区以及透传边界派生继续按每次账号尝试执行。
4. **延迟哈希**：只有实际 Cyber 记录或用量结算需要时计算原始入站正文哈希，每请求最多一次；计费唯一性输入保持原样。

Telemetry borrows the existing immutable wire snapshot for synchronous extraction. The finalizer still owns its private copy, and fallback readers retain their close/error semantics. Every physical attempt remains observable. Fingerprint observation now projects only relevant metadata and shares that projection within an entry. Account-independent normalization is lazy and request-local, while Engine bytes and account-dependent processing remain unchanged. Billing hashes are computed lazily once from the original ingress body.

重复键最后值、转义键、carrier 大小写匹配、null、无效 UTF-8 和来源优先级有差分覆盖。一处诊断行为改善：无关字段中的合法超大 JSON 数字（如 `1e1000`）不再导致身份小字段观测丢失；`client_metadata` 自身的非法数值仍按原规则处理。请求身份与路由不受此调整影响。

Differential tests cover duplicate/escaped keys, case-insensitive carriers, null, invalid UTF-8 and precedence. A diagnostic improvement allows unrelated large JSON numbers to coexist with valid identity metadata; invalid numbers inside `client_metadata` retain their previous handling.

## 三轮基准 / Three-run benchmarks

环境 / Environment: Windows amd64, AMD Ryzen 7 9700X, Go 1.27.3, `GOEXPERIMENT=greenteagc,jsonv2`, `GOMAXPROCS=4`, `-p 2`, `-benchtime=100ms`, `-count=3`。

相同测试程序内保留旧算法作对照，输入覆盖 64 KiB、1 MiB、8 MiB；重试覆盖 1、5、15 次。下表为三轮中位数；完整结果见 [原始记录](raw/cpu-bench.log)。

The previous algorithms remain in test-only helpers for same-binary comparisons. The table reports three-run medians for the 8 MiB fixtures. These are individual processing stages, not end-to-end model response times or production CPU reductions.

| 场景 / Stage | 旧耗时 / Before | 新耗时 / After | 旧 B/op | 新 B/op | 旧 / 新 allocs/op |
|---|---:|---:|---:|---:|---:|
| 遥测取得正文，15 次发送 / Telemetry snapshot, 15 sends | 27.392 ms | 0.000413 ms | 255,956,428 | 1,200 | 465 / 30 |
| 单次指纹 profile / Fingerprint profile | 10.404 ms | 3.295 ms | 8,405,189 | 7,362 | 145 / 136 |
| 兼容预处理，1 次尝试 / Preparation, 1 attempt | 134.579 ms | 131.273 ms | 302,130,520 | 302,130,096 | 647 / 640 |
| 兼容预处理，5 次尝试 / Preparation, 5 attempts | 519.185 ms | 121.781 ms | 1,443,412,560 | 302,128,696 | 2,789 / 635 |
| 兼容预处理，15 次尝试 / Preparation, 15 attempts | 1,583.857 ms | 122.666 ms | 4,296,619,200 | 302,130,272 | 8,177 / 644 |

预处理基准故意使用需要兼容转换的大型工具结果；普通请求的收益取决于实际正文形态和重试次数。首次必要的协议转换、每次最终正文完整性检查及账号专属转换仍执行。此修复减少 429 带来的本地计算放大；上游返回 429 的原因和现有重试策略未变。

The preparation fixture deliberately exercises large compatibility tool outputs. Real savings depend on payload shape and retry count. Necessary first-pass conversion, final integrity checks and per-account adaptation still run. Upstream 429 responses and retry semantics are unchanged.

## 验证与复现 / Verification and reproduction

在 `backend` 目录执行 / Run from `backend`:

```bash
export GOEXPERIMENT=greenteagc,jsonv2 GOMAXPROCS=4
filter='Test(.*Fingerprint|.*CodexWireProfile|.*Guardian|CodexTelemetryHTTP|OpenAIRequestIntegrity|OpenAIResponsesRetryPreparation|OpenAIUsagePayloadHash|CodexEngineMixedPool|NormalizeOpenAIResponsesCompactRequest|OpenAIResponsesCompactionRoutingFlags|DeriveOpenAIForwardAttemptBody|NormalizeCodexPageContext|NormalizeCodexAutomationBootstrap|NormalizeCodexDelegationBootstrap|ValidateStandaloneFunctionOutput|OpenAIResponses_APIKeyPassthroughSSERateLimitUsesConfiguredPoolRetry)'
go test -p 2 -tags unit ./internal/service ./internal/handler -run "$filter" -count=1
CGO_ENABLED=1 go test -p 2 -race -tags unit ./internal/service ./internal/handler -run "$filter" -count=1
go test -p 2 -tags unit ./internal/service ./internal/handler -run '^$' \
  -bench '^(BenchmarkCodexTelemetryHTTPRequestSnapshot|BenchmarkFingerprintObservationBodyProjection|BenchmarkOpenAIResponsesRetryPreparation)$' \
  -benchmem -benchtime=100ms -count=3
go build -p 2 ./cmd/server
```

- 单元 / Unit: **通过 / Passed** — [cpu-unit.log](raw/cpu-unit.log)
- 竞态 / Race: **通过 / Passed** — [cpu-race.log](raw/cpu-race.log)
- 构建 / Build: **通过 / Passed** — [cpu-build.log](raw/cpu-build.log)（成功时无标准输出 / silent on success）
- 三轮基准 / Three-run benchmarks: **通过 / Passed** — [cpu-bench.log](raw/cpu-bench.log)

测试使用本地合成正文及测试桩，不调用真实模型。没有数据库 schema 或前端改动，因此本次不重跑数据库迁移或前端套件。执行前发现一份本地已跟踪测试文件全为 NUL 字节，保留损坏副本后从本次基线恢复，再完成验证；恢复不产生提交差异。检测器目录的既有修改及排查文件均保留。

Fixtures use synthetic payloads and local stubs. There are no schema or frontend changes. A pre-existing all-NUL local test file was preserved separately and restored from the baseline to unblock compilation; this restoration adds no Git diff. Unrelated detector edits and investigation files remain untouched.

本次不部署、不调整生产开关。上线后的 CPU、GC 与分配速度应按相同流量窗口重新采样；本记录中的收益来自上述可复现阶段基准。

No deployment or production switch changes are performed. Production CPU, GC and allocation rates can be compared after deployment under comparable traffic; the measurements here are reproducible stage benchmarks.

实现提交 / Implementation commits: `59f8404cf`（遥测 / telemetry）、`82e5c196f`（指纹 / fingerprint）、`bd938c9ce`（重试 / retries）。远端 CI 不作为上述本地验证的来源；仓库 Actions 先前关闭，本次未触发工作流。

Validation above was executed locally; no remote workflow was manually triggered. Repository Actions were previously disabled.
