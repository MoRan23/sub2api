# 大正文 CPU 复查与第二轮优化 / Large-body CPU follow-up

基线 / Baseline: `50e234d9f`。

## 线上只读证据 / Read-only observations

2026-11-17 北京时间约 15:24 起复查。运行进程中确认包含上一轮三个新函数：正文快照读取、指纹投影、请求重试预处理。没有重启服务、改生产配置或发送模型探针。

The running binary contains all three previous optimization paths. Diagnosis used existing counters, bounded log summaries, read-only SQL and two valid 20-second user-space CPU samples. No restart, production configuration change or model probe was performed.

- 一次即时采样 Sub2 为 158.91% CPU（约 1.59 核），PostgreSQL 为 46.01%；机器共 8 核。
- 最近一小时六段采样分配速度为 223.93、221.52、270.62、280.06、213.41、274.64 MiB/s。计费队列为空，统计日汇总已追平。
- 三分钟请求样本：293 个请求、697.55 MiB 正文；P95 约 12.02 MiB，最大 89.79 MiB。Luna 的 121 个请求合计仅 12.30 MiB，Blue 的 57 个请求合计 298.52 MiB，Astra 的 22 个请求合计 191.34 MiB。
- 邻近三分钟日志中同账号重试约 790 次，主要是 Luna 的 429；大正文正常请求和重试共同产生处理成本。

The allocation rate remained 213–280 MiB/s despite the previous fixes. Recent traffic included payloads up to approximately 90 MiB. The most retried model had comparatively small payloads; the cost is not confined to retries. Billing queues were empty and daily rollups were current.

## CPU 采样 / CPU sampling

通过 Linux `perf_event_open` 对现有 20 个线程进行 100 Hz CPU-clock 采样，排除内核态，读取 Go 函数表解析地址。两轮分别取得 3,987 和 3,315 个样本，丢失均为 0。采样不读取请求正文或凭据，也没有开启 HTTP 调试端口。

| 样本类别 / Leaf samples | 第一轮 / Run 1 | 第二轮 / Run 2 |
|---|---:|---:|
| 主要 GJSON / Go JSON 扫描与解析函数 / Main JSON scanning and parsing | 54.1% | 54.6% |
| `runtime.memmove` + `memclrNoHeapPointers` | 11.9% | 14.8% |

比例是本次用户态采样份额。调用栈中的 inclusive 数会重叠：Forward、finalizer、身份处理等不能相加当作独立 CPU 百分比。两次采样共同定位到内容审核提取、时区、身份处理和遥测提取中的大正文扫描。前一轮遥测快照借用正常命中；下游仍存在 `ParseBytes` 的全文 string 副本，以及 `GetBytes("input")` 的完整 input 副本。

Percentages describe this sampling window. Inclusive stack counts overlap and must not be summed. The previous immutable snapshot fast path is active; downstream field extraction still copied whole bodies or large input arrays.

原始结果 / Raw data: [sample 1](raw/cpu-sampling-1.json), [sample 2](raw/cpu-sampling-2.json)。复现脚本 / Reproducer: [sample-go-cpu.py](sample-go-cpu.py)，Linux amd64、具有目标进程采样权限时运行 `python3 sample-go-cpu.py PID`。脚本使用 ELF `.text` 兼容 Go 1.27 已清零的旧 textStart 字段。

## 修复内容 / Fixes

1. **遥测**：同一物理发送共享只读正文视图；reviewer、Guardian、shell 和 tool-call IDs 不再复制完整正文/input。保留原 `Get` 与 `Parse` 的不同容错语义、8 MiB 限制和最终小字段独立所有权。Messages guard 改写正文后继续使用专用快照。
2. **内容审核**：语义审核和关键词审核在同一次提取中生成各自文本，保留 reminder 差异及最新用户边界；借用 input，按对象一次提取相关字段，避免重复扫描大 Base64。重复图片父键通过兼容分支保持原来的嵌套路径搜索。返回文本和图片数据独立持有，异步任务不借用请求。UTF-8 截断不再展开整份 `[]rune`；保留无效 UTF-8 和截断行为。
3. **身份处理**：仅检查对象形状的两处操作不再全文转 string；context-window 遍历借用 input，通过迭代避免数组展开，减少对非 developer 图片条目的重复查找。改写仍生成独立输出，未改写复用原文。

Telemetry shares a borrowed read-only view but copies final retained scalars. Moderation extracts semantic and keyword views together without changing their distinct rules, preserving image extraction and duplicate-key compatibility. Identity shape checks and context-window traversal avoid full-body copies and unnecessary array materialization. No retry, authorization, billing or moderation policy is changed.

本轮没有跨账号复用可变转换 map，也没有跳过最终完整性检查。必要的协议转换和时区投影仍执行；后续是否继续优化，以新版本上线后的同类采样为依据。

Mutable protocol maps are not shared across account attempts, and final integrity checks remain in place. Further work can be guided by the same sampling method after deployment.

## 验证 / Validation

使用 Go 1.27.3、`GOEXPERIMENT=greenteagc,jsonv2`、`GOMAXPROCS=4`、包编译并发 2。本地合成文本、图片和工具结束请求覆盖 1、8、32 MiB；保留旧算法用于同一测试进程的三轮比较。无生产模型或数据库写入。

Commands from `backend`:

```bash
export GOEXPERIMENT=greenteagc,jsonv2 GOMAXPROCS=4
filter='Test(.*ContentModeration|CodexTelemetry|OpenAIMessagesGuard|GuardOpenAICodexTurnState|OpenAIIdentityBodyView|OpenAICodexContextWindowBody|CodexWireProfile|OpenAIOAuthIdentity)'
go test -p 2 -tags unit ./internal/service -run "$filter" -count=1
CGO_ENABLED=1 go test -p 2 -race -tags unit ./internal/service -run "$filter" -count=1
go test -p 2 -tags unit ./internal/service -run '^$' \
  -bench '^(BenchmarkCodexTelemetryBodyView|BenchmarkContentModerationInputCPU|BenchmarkOpenAIIdentityBodyViews)$' \
  -benchmem -benchtime=100ms -count=3
go build -p 2 ./cmd/server
```

### 三轮结果 / Three-run results

下表为三轮中位数；原始记录保留各轮波动：[cpu-followup-bench.log](raw/cpu-followup-bench.log)。各行是独立处理阶段，不能相加解释为整个请求或线上 CPU 降幅。形状检查基准仅度量对象判定；context 基准包括原有校验和窗口处理。遥测 8 MiB 文本耗时中位数为 60.69 → 61.73 ms（约 +1.7%），分配量仍下降；本轮没有隐去这项波动。

Medians from three runs are shown below. Stages are measured independently. The 8 MiB telemetry text case has a small timing increase (+1.7%) while eliminating large allocations; all raw runs are retained.

| 阶段 / Stage | 正文 / Body | 旧耗时 / Before | 新耗时 / After | 旧 B/op | 新 B/op |
|---|---|---:|---:|---:|---:|
| 审核提取 / Moderation extraction | 8 MiB image | 236.91 ms | 20.01 ms | 83,971,216 | 8,400,736 |
| 审核提取 / Moderation extraction | 32 MiB image | 969.97 ms | 76.33 ms | 335,629,456 | 33,566,560 |
| 审核提取 / Moderation extraction | 32 MiB text | 895.11 ms | 260.30 ms | 857,207,008 | 295,920,608 |
| 审核提取 / Moderation extraction | 32 MiB tool-ended history | 95.62 ms | 36.19 ms | 67,125,728 | 944 |
| 遥测字段 / Telemetry fields | 8 MiB image | 39.53 ms | 31.38 ms | 33,554,592 | 120 |
| 遥测字段 / Telemetry fields | 32 MiB image | 58.90 ms | 54.38 ms | 67,108,936 | 40 |
| 身份窗口 / Identity context window | 32 MiB image | 57.06 ms | 35.78 ms | 33,563,096 | 394 |

单元、race、三轮基准及后端构建均通过 / Unit tests, race checks, three-run benchmarks and backend build passed.

单元日志 / Unit log: [cpu-followup-unit.log](raw/cpu-followup-unit.log)。竞态与构建日志 / Race and build logs: [cpu-followup-race.log](raw/cpu-followup-race.log), [cpu-followup-build.log](raw/cpu-followup-build.log)（构建成功无输出 / successful build is silent）。

差分测试期间发现的畸形 JSON 前缀、重复图片父字段兼容问题均已修复；旧身份测试完整保留。新增参考测试的初次编译命名冲突也已修正，以上日志记录最终成功结果。

Differential tests caught malformed-prefix and duplicate-parent image compatibility differences; both were fixed. Existing identity tests remain intact. A naming conflict in the test-only reference helper was also resolved before the final passing checks.

代码提交 / Code commits: `7cc5bc550`（telemetry）、`7b83b99aa`（moderation）、`94bc17594`（identity）。

不涉及前端、数据库 schema 或部署；保留检测器目录的既有改动和未跟踪排查文件。远端 CI 未作为本次本地验证来源。

Frontend, schema and deployment are outside this change. Existing detector edits and investigation files are preserved. Verification is local; remote CI is not used as evidence.
