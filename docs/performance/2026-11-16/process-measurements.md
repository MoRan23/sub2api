# 转发与独立进程测量 / Forwarding and process measurements

原始记录：[process-measurements.log](raw/process-measurements.log)、[forwarding-bench.log](raw/forwarding-bench.log)、[forwarding-race.log](raw/forwarding-race.log)。运行器：[run-process-measurements.sh](run-process-measurements.sh)。

Linux / WSL、Go 1.27.3、`GOEXPERIMENT=greenteagc,jsonv2`、`GOMAXPROCS=4`。每个场景、每种实现使用独立进程，先预热一次并 GC，再执行 32 次；三轮串行运行。基线算法保留于差分测试辅助函数，来自 `e6de1c8eb` 的累加与 WS 比较行为；两种算法编译在同一测试二进制中。没有调用模型或生产数据库。

Each scenario/implementation runs in a separate Linux process with fixed Go settings, one warmup and 32 timed operations, repeated three times. The legacy algorithms reproduce the baseline accumulation/comparison behavior in the same binary. CPU includes user and system time across runtime threads. p95 is the empirical operation latency, not an HTTP endpoint percentile. RSS is `/proc/self/status` VmHWM, including the test binary and fixtures. Stable heap is measured after dropping results and forcing GC in this test only; production GC policy is unchanged.

## 三轮耗时与分配 / Three-run time and allocations

| 场景 / Scenario | 实现 | 每次耗时 ms，三轮 | p95 ms，三轮 | CPU ms/op 中位 | B/op 约值 | allocs/op |
|---|---|---|---|---:|---:|---:|
| 1 MiB text, 256 B fragments | baseline | 332.388 / 265.584 / 232.236 | 481.068 / 307.293 / 249.163 | 605.109 | 2,163,854,900 | 4,121–4,123 |
| 同上 | optimized | 0.921 / 0.554 / 0.607 | 2.118 / 1.256 / 1.385 | 1.198 | 5,241,850 | 33 |
| 1 MiB tool JSON, 256 B fragments | baseline | 1744.579 / 2174.690 / 1533.167 | 2095.995 / 3250.016 / 1632.556 | 2930.375 | 4,332,150,000 | 54,315–54,342 |
| 同上 | optimized | 0.693 / 0.571 / 0.605 | 1.745 / 1.436 / 1.292 | 1.278 | 5,241,990 | 30 |
| WS comparison, 16 MiB input | baseline | 21.451 / 19.142 / 19.515 | 27.957 / 29.406 / 29.778 | 23.819 | 83,892,000 | 66–68 |
| 同上 | optimized | 10.478 / 9.153 / 9.771 | 12.589 / 9.590 / 13.326 | 9.838 | 2,281–2,461 | 38 |
| XLSX, 1,000 rows × 37 columns | optimized | 6.338 / 4.761 / 5.117 | 7.949 / 5.384 / 6.948 | 5.955 | 2,355,330 | 40,057 |
| XLSX, 200,000 rows × 37 columns | optimized | 935.325 / 960.400 / 950.039 | 961.093 / 996.079 / 960.512 | 1102.285 | 453,484,850 | 8,000,066–8,000,068 |

旧工具累加的耗时波动较大，与高分配和 GC 工作相符；三轮优化结果均显著下降。WS 的完整 JSON 有效性检查仍需遍历输入，分配已不再随被跳过的 input 大小线性增长。普通标准 SSE delta 的微基准为 **0 B/op、0 allocs/op**，旧路径为 **864 B/op、18 allocs/op**。

The tool baseline is noisy under its heavy allocation/GC load, but every optimized run is substantially faster. WS still validates the JSON bytes; skipping input removes its allocation cost rather than all byte-scanning CPU. Standard SSE deltas use zero allocations; extensions, duplicate keys, usage and terminal/error shapes retain the existing parser fallback.

## 存活堆与峰值 / Retained heap and peak RSS

| 场景 | 基线峰值 RSS MiB，三轮 | 优化峰值 RSS MiB，三轮 | GC 后存活堆 |
|---|---|---|---|
| 文本累加 | 119.55 / 109.00 / 107.68 | 97.49 / 94.00 / 95.64 | 两种均约 5.6 MiB |
| 工具 JSON 累加 | 112.11 / 142.34 / 95.05 | 100.79 / 90.19 / 90.01 | 两种均约 5.6 MiB |
| WS 16 MiB | 236.22 / 229.66 / 228.44 | 109.73 / 100.66 / 100.44 | 两种均约 5.6 MiB |
| XLSX 1,000 行 | 旧版在浏览器，不作 Go 对照 | 85.86 / 77.87 / 77.54 | 约 6.6 MiB |
| XLSX 200,000 行 | 同上 | 106.04 / 104.53 / 104.55 | 约 6.6 MiB |

稳定堆接近说明这几条路径的主要收益是降低短命分配和峰值，而不是消除已有永久对象。Excel 旧版使用浏览器 XLSX 库，表中仅提供新流式生成器的绝对测量。数据库读取仍固定每批 1,000 行，工作簿使用内联字符串和流式 ZIP/XML，不随总行数保留单元格对象。

Retained heaps converge after results are released. These changes primarily reduce transient allocation and peak memory. The old Excel implementation ran in the browser, so its Go comparison is marked unavailable. The new writer streams inline strings/ZIP data and does not retain a workbook-sized cell graph.

20 万行单独三轮记录见 [export-fullsize-process.log](raw/export-fullsize-process.log)。每轮预热一次、测量三次，表中 p95 在这组三个样本上即最大值；这是编码器上限规模检查。输出写入 `io.Discard`，不计数据库和磁盘速度。总行数扩大 200 倍后，GC 后存活堆仍约 6.6 MiB，峰值 RSS 约 105 MiB；累计分配随写出行数增长，没有工作簿全量驻留。

The 200,000-row check uses three separate processes, with one warmup and three measured operations each. Its three-sample p95 is simply the maximum. Output goes to `io.Discard`, so this measures encoding rather than database/disk throughput. Retained heap remains approximately 6.6 MiB while rows increase 200-fold; peak RSS stays around 105 MiB. Cumulative allocations grow with emitted rows without retaining the complete workbook.

## 复现 / Reproduce

```bash
# Repository root, Linux with Go 1.27.3 installed
bash docs/performance/2026-11-16/run-process-measurements.sh
```

首次编译可能占用较多内存，运行器以 `-p 1` 构建；避免并行构建多个 service/repository race 二进制。只有已按相同源码和工具链编译过时，才设置 `SUB2_PERF_SKIP_BUILD=true`。其他微基准、协议差分和 race 命令见 [forwarding-commands.log](raw/forwarding-commands.log)。
