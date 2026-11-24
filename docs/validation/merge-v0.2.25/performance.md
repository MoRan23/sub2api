# v0.2.25 合并性能验证 / Merge performance validation

## 方法 / Method

- 合并前基线：`b3d991119c0f1cf119a077a578709eef7ac4010b`，通过 `git archive` 导出到工作区外的 `/tmp/sub2-merge025-baseline-20261124`。
- 相同工具链：Go **1.27.4 linux/amd64**，`GOEXPERIMENT=greenteagc,jsonv2`、`GOTOOLCHAIN=local`、`GOMAXPROCS=2`、`-p 1`。
- 硬件：AMD Ryzen 7 9700X；WSL Ubuntu-24.04。计时期间暂停其他代理的密集编译及前端检查。
- 每项运行三轮：`-benchmem -benchtime=300ms -count=3`。不调用真实模型，不连接生产服务。
- 原始结果保留 `ns/op`、`B/op`、`allocs/op`。三轮用于定向回归比较；显著性不足时不宣称性能提升。

Both revisions use the same Go toolchain, experiments, CPU limit and benchmark parameters. Baseline sources are exported outside the shared checkout. Timed runs do not overlap other agents' heavy checks.

## 执行范围 / Scope

```sh
export PATH=/mnt/d/DevCache/go-mod/golang.org/toolchain@v0.0.1-go1.27.4.linux-amd64/bin:$PATH
export GOEXPERIMENT=greenteagc,jsonv2 GOTOOLCHAIN=local GOMAXPROCS=2

go test -tags=unit -p 1 -run '^$' \
  -bench '^(BenchmarkOpenAIWSPoolAcquire|BenchmarkOpenAIWSForwarderHotPath|BenchmarkOpenAIWSErrorEventFieldReuse)$' \
  -benchmem -benchtime=300ms -count=3 ./internal/service

go test -tags=unit -p 1 -run '^$' \
  -bench '^BenchmarkOpenAIResponsesRetryPreparation$' \
  -benchmem -benchtime=300ms -count=3 ./internal/handler

# 新功能路径仅在合并版本运行 / New feature paths, merged version only.
go test -tags=unit -p 1 -run '^$' \
  -bench '^(BenchmarkHeaderOverrideRequestIdentity|BenchmarkHeaderOverrideWSCompatibility|BenchmarkOpenAIPassthroughErrorSanitizer)$' \
  -benchmem -benchtime=300ms -count=3 ./internal/service
```

`BenchmarkOpenAIResponsesRetryPreparation` 的 `before` / `after` 子项指其内部的重复解析实现与现有缓存实现；两个版本按完全相同的子项比较。正文为 64 KiB、1 MiB、8 MiB，分别模拟 1、5、15 次账号尝试。

The retry benchmark's `before` / `after` labels describe its uncached and cached preparation variants, not the merge revisions. Matching variants are compared between revisions.

新增基准分别测量请求级身份作用域、静态及模板请求头（含 64 条上限）、固定摘要的 WS 兼容键，以及一次错误内复用的四字段脱敏。WS 额外包含 4 KiB 固定模板前缀；错误文本包含 4 KiB 与 512 KiB 的未命中及脱敏案例。

New benchmarks cover request identity scopes, static/template headers including the 64-entry limit, fixed-digest WS compatibility and one sanitizer shared by four error fields. Large inputs include a 4 KiB header prefix and 4/512 KiB error text.

## 首轮结果与定向修复 / Initial findings and focused fixes

首轮三次采样发现两处可修复的新增分配：

1. WS 成功获取连接时，新增错误报告代码无条件声明 `errors.As` 的输出指针，导致每次多分配 **8 B / 1 alloc**。已把该变量和错误检查移到真正的错误分支。
2. 错误脱敏使用的正则替换在未命中时仍复制字符串。512 KiB 未匹配文本分配约 **3.16 MB/op**。已限定在新增 sanitizer 内，先进行只读匹配，命中时才替换，保留原有脱敏顺序和 Unicode 行为。

The initial runs exposed an unnecessary successful-acquire allocation and full-text copies on unmatched error sanitization. Both were fixed within the affected paths; final confirmation uses the same benchmark fixtures.

重试准备的三轮原始结果见 `baseline-handler.txt`、`merged-handler.txt` 和 `comparison-handler.txt`。现有缓存路径在 1、5、15 次尝试下仍保持近似恒定的正文准备分配，未恢复逐尝试复制。8 MiB 单次尝试的首轮耗时有超过 5% 的波动，单独纳入最终复测。

Cached retry preparation retains approximately constant allocation across 1, 5 and 15 attempts. The noisy 8 MiB single-attempt timing is included in the final targeted rerun.

## 最终复测 / Final targeted comparison

修复后先编译基线和合并版本的 service、handler 测试二进制，再按照 `baseline → merged` 的顺序交错运行五轮。每项使用 `-test.benchtime=1s -test.count=1 -test.benchmem`，继承相同工具链及 CPU 限制。仅复测三项 WS 基准和 `BenchmarkOpenAIResponsesRetryPreparation/8192KiB/1attempts/after`；随后对 sanitizer 重新运行三轮 300 ms 基准。

After compiling both revisions, the final comparison alternates baseline and merged test binaries for five rounds at one second per case. The sanitizer rerun retains the original three-round, 300 ms parameters.

| 基准 / Benchmark | 基线中位耗时 / Baseline median | 合并中位耗时 / Merged median | 基线 → 合并分配 / Allocation |
|---|---:|---:|---|
| WS pool acquire | 1.442 µs | 1.432 µs | 584 → 584 B/op；10 → 10 allocs/op |
| WS error field reuse | 752.8 ns | 756.3 ns | 488 → 488 B/op；13 → 13 allocs/op |
| WS forwarder | 93.904 µs | 101.730 µs | 33,182 → 33,183 B/op；847 → 847 allocs/op |
| 8 MiB cached retry preparation，单次 / one attempt | 80.51 ms | 83.84 ms | 95.46 → 93.79 MiB/op；291 → 291 allocs/op |

WS acquire 新增的 8 B / 1 alloc 已消除。Forwarder 的五轮中位耗时为 **+8.3%**，基线范围为 **81.577–114.663 µs**，合并版本为 **95.302–105.698 µs**；`benchstat p=0.548`，首轮三次中位差异为 +1.4%。耗时差异随轮次变化，没有形成稳定回退证据；分配数量保持一致。8 MiB 单次准备复测中位差异为 +4.1%，分配数量相同。

The acquire allocation regression is resolved. Forwarder median time is **8.3% higher** in the five-round run, with overlapping ranges and `p=0.548`; the initial run differed by 1.4%. This is recorded as variable timing rather than a demonstrated stable regression. Allocation counts are unchanged. The large retry case differs by 4.1% with identical allocation counts.

完整配对结果保留在 `final-baseline-service.txt`、`final-merged-service.txt`、`final-comparison-service.txt`，以及对应的 `final-*-retry-large.txt`。

## 新增路径 / New feature paths

下表为三轮中位值。请求身份基准包含创建请求作用域和两个既有请求头；WS 兼容键基准复用已经准备好的不可变报告。

| 路径 / Path | 中位耗时 / Median | B/op | allocs/op |
|---|---:|---:|---:|
| 请求头未启用 / Disabled headers | 90.90 ns | 128 | 4 |
| 两个静态头 / Two static headers | 346.2 ns | 160 | 6 |
| 两个模板头 / Two template headers | 1.755 µs | 952 | 22 |
| 64 个模板头，上限 / 64 template headers, limit | 129.026 µs | 40,456 | 599 |
| WS 兼容键，无模板 / No template | 711.9 ns | 320 | 8 |
| WS 兼容键，模板 / Template | 744.2 ns | 320 | 8 |
| WS 兼容键，4 KiB 前缀 / 4 KiB prefix | 740.1 ns | 320 | 8 |
| 错误脱敏 4 KiB，无匹配 / No match | 107.832 µs | 7,715 | 45 |
| 错误脱敏 4 KiB，有匹配 / Redactions | 182.738 µs | 40,631 | 58 |
| 错误脱敏 512 KiB，无匹配 / No match | 14.352 ms | 7,976 | 45 |
| 错误脱敏 512 KiB，有匹配 / Redactions | 25.791 ms | 4,210,374 | 61 |

4 KiB 静态模板前缀不会扩大 WS 兼容键的分配。512 KiB 未命中脱敏从首轮约 **3.16 MB/op** 降为约 **8 KB/op**，未命中正文直接复用原字符串；实际匹配时保留按原顺序生成脱敏输出。表中包含每次最终错误的 sanitizer 构造及四字段处理，成功响应和中间重试不执行此新增客户端脱敏路径。

The large template prefix does not increase compatibility-key allocation. Unmatched 512 KiB sanitization drops from approximately **3.16 MB/op to 8 KB/op** by retaining the original string. Matching inputs still produce redacted output in the original order. These figures include sanitizer construction and four fields for one final error; the new client sanitization does not run on successful responses or intermediate retries.

原始新路径结果见 `merged-new-paths.txt`；sanitizer 修复后的结果以 `final-sanitizer.txt` 为准。64 条模板上限单独按相同工具链和 300 ms × 3 补测，结果见 `final-headers-64.txt`。原始结果均保留，便于复核发现及修复的过程。
