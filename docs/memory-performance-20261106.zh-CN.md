# 2026-11-06 内存优化性能记录

共享调度缓存的收益随账户在多个分组中的复用程度增加。2000 账户的单组 retained heap 变化 +9.35%；100 组变化 -98.34%。SSE 减少逐帧分配和重复解析，普通计费积压不再保留整份请求/响应正文。下表保留所有正负变化。

## 测量环境与口径

最初 cache/SSE/billing 基线测试二进制保存自 `8972274c2871fd961dd5a01d0a4c63b76e358add`，候选测试二进制保存自其上的内存优化工作树。后续分项可能使用重新构建的二进制，分别保留各自 manifest。原 runner 的 `source_head` 记录测量时 checkout HEAD；新 runner 将 `binary_source` 与 `measurement_checkout_head` 分开记录。来源按分项构建记录解释，不把测量时 HEAD 当成保存二进制的源码身份，也不把这些分项测量标成最终发布提交。Linux amd64（WSL），AMD Ryzen 7 9700X，Go 1.27.3，`GOEXPERIMENT=greenteagc,jsonv2`，`GOMAXPROCS=4`。

每侧独立运行三轮；cache 每个 fixture 100 次，SSE 20 次，billing 1 次。表格为基线 → 候选的三轮中位数及变化百分比。波动定义为 `(最大值−最小值)/|中位数|×100%`；三轮是描述性样本。每轮内测得的 p95 再取中位数，不合并为一个总体 p95。

全部三轮原值、中位数、波动和资源记录见[完整汇总](../backend/benchmarks/results/memory-20261106/performance-summary.md)与[机器可读 JSON](../backend/benchmarks/results/memory-20261106/performance-summary.json)。

## 调度缓存

相同 2000 账户由 1/10/100 组复用。Redis fixture 建立完成后，以显式 GC 前后 HeapAlloc 差值测解码对象 retained heap；计时区间是循环读取快照，含两侧相同反射桥和本地 miniredis 访问。ops/s 表示每秒完整候选集读取。

| Fixture | Retained MiB | KiB/op | ms/read | p95 ms | reads/s |
|---|---:|---:|---:|---:|---:|
| accounts_2000_groups_1 | 6.21 → 6.791 (+9.35%) | 1,700 → 427.8 (-74.83%) | 3.126 → 2.168 (-30.66%) | 3.888 → 2.459 (-36.74%) | 319.9 → 461.3 (+44.23%) |
| accounts_2000_groups_10 | 85.47 → 10.63 (-87.56%) | 1,700 → 427.8 (-74.83%) | 3.283 → 2.066 (-37.07%) | 4.944 → 2.307 (-53.33%) | 304.6 → 484.1 (+58.91%) |
| accounts_2000_groups_100 | 2,661 → 44.13 (-98.34%) | 1,700 → 427.8 (-74.83%) | 2.859 → 2.098 (-26.61%) | 3.457 → 2.436 (-29.52%) | 349.8 → 476.6 (+36.26%) |

单组 retained heap 增长体现母本索引与轻量视图的固定成本；每次读取的分配字节仍减少，分配次数未下降。单组延迟与波动应与多组结果一起评估。

### 共享比例与冷热态

此组固定 2000 个不同账户与 100 个 bucket。共享比例为 0%/50%/100% 时，每个 bucket 分别有 20/1010/2000 个成员；剩余非共享账户均匀分配。一次 op 完整读取 100 个 bucket，因此跨共享比例比较时必须保留不同成员数量。warm 先预热一次；cold 每次 sweep 使用空进程缓存，Redis 保持预填，sweep 后显式 GC 位于计时区间外。每轮只有 3 次 sweep，其 p95 为本轮最大样本。

| Fixture | ms/sweep | MiB/sweep | allocs/sweep | p95 sweep ms | bucket reads/s |
|---|---:|---:|---:|---:|---:|
| accounts_2000_groups_100_shared_0_warm | 22.32 → 22.35 (+0.12%) | 1.86 → 0.6123 (-67.09%) | 18,304 → 18,305 (+0.01%) | 22.9 → 25.39 (+10.86%) | 4,480 → 4,474 (-0.12%) |
| accounts_2000_groups_100_shared_0_cold | 67.34 → 80.11 (+18.97%) | 13.8 → 16.06 (+16.32%) | 143,549 → 161,334 (+12.39%) | 74.06 → 85.51 (+15.46%) | 1,485 → 1,248 (-15.94%) |
| accounts_2000_groups_100_shared_50_warm | 134 → 147.8 (+10.29%) | 83.56 → 21.06 (-74.80%) | 526,804 → 526,810 (+0.00%) | 156.9 → 152.6 (-2.75%) | 746 → 676.4 (-9.33%) |
| accounts_2000_groups_100_shared_50_cold | 7,025 → 2,868 (-59.18%) | 4,804 → 2,449 (-49.03%) | 8,006,086 → 2,183,582 (-72.73%) | 8,269 → 2,942 (-64.42%) | 14.24 → 34.87 (+144.97%) |
| accounts_2000_groups_100_shared_100_warm | 291.5 → 253.7 (-12.96%) | 166 → 41.78 (-74.83%) | 1,037,004 → 1,037,008 (+0.00%) | 323.7 → 267.3 (-17.41%) | 343 → 394.1 (+14.89%) |
| accounts_2000_groups_100_shared_100_cold | 19,939 → 5,814 (-70.84%) | 9,596 → 4,882 (-49.12%) | 15,871,464 → 4,207,103 (-73.49%) | 24,845 → 6,021 (-75.77%) | 5.015 → 17.2 (+242.95%) |

### sharing-zero100x

单独保留的复测组；每个 fixture 每轮迭代数：[100]。
Concurrent host workloads were observed; diagnostic only. 宿主机存在并发负载，仅诊断。

| Fixture | ms/sweep | MiB/sweep | allocs/sweep | p95 sweep ms | bucket reads/s |
|---|---:|---:|---:|---:|---:|
| accounts_2000_groups_100_shared_0_warm | 20.84 → 20.53 (-1.52%) | 1.86 → 0.5968 (-67.92%) | 18,304 → 18,204 (-0.55%) | 22.27 → 22.13 (-0.61%) | 4,798 → 4,872 (+1.55%) |
| accounts_2000_groups_100_shared_0_cold | 59.06 → 62.67 (+6.11%) | 13.8 → 13.61 (-1.36%) | 143,544 → 151,478 (+5.53%) | 62.43 → 65.21 (+4.44%) | 1,693 → 1,596 (-5.76%) |

| Fixture / metric (ms) | Baseline round 1 / 2 / 3; median; spread | Candidate round 1 / 2 / 3; median; spread |
|---|---|---|
| warm / ns/op | 20.842889 / 21.456081 / 8.741167; **20.84**; 61.00% | 20.525141 / 20.579614 / 20.134945; **20.53**; 2.17% |
| warm / p95-sweep-ns | 22.26547 / 22.55259 / 13.189276; **22.27**; 42.05% | 21.684164 / 22.130417 / 23.569886; **22.13**; 8.52% |
| cold / ns/op | 59.062927 / 58.866292 / 63.766596; **59.06**; 8.30% | 62.671973 / 61.528073 / 74.020949; **62.67**; 19.93% |
| cold / p95-sweep-ns | 61.709132 / 62.434407 / 71.859755; **62.43**; 16.26% | 65.209339 / 63.860169 / 83.087727; **65.21**; 29.49% |

### sharing-zero100x-exclusive

单独保留的复测组；每个 fixture 每轮迭代数：[100]。
Exclusive measurement window; 100 sweeps per fixture per round; retain round spread. 独占窗口，每轮100次，保留波动。

| Fixture | ms/sweep | MiB/sweep | allocs/sweep | p95 sweep ms | bucket reads/s |
|---|---:|---:|---:|---:|---:|
| accounts_2000_groups_100_shared_0_warm | 20.58 → 20.92 (+1.64%) | 1.86 → 0.5968 (-67.92%) | 18,304 → 18,204 (-0.55%) | 22.55 → 22.54 (-0.04%) | 4,858 → 4,780 (-1.62%) |
| accounts_2000_groups_100_shared_0_cold | 67.48 → 61.94 (-8.22%) | 13.8 → 13.61 (-1.35%) | 143,540 → 151,478 (+5.53%) | 77 → 69.31 (-9.98%) | 1,482 → 1,615 (+8.95%) |

| Fixture / metric (ms) | Baseline round 1 / 2 / 3; median; spread | Candidate round 1 / 2 / 3; median; spread |
|---|---|---|
| warm / ns/op | 21.262343 / 7.986307 / 20.582708; **20.58**; 64.50% | 21.062394 / 20.921251 / 18.218049; **20.92**; 13.60% |
| warm / p95-sweep-ns | 22.547052 / 11.71055 / 23.798268; **22.55**; 53.61% | 22.53797 / 22.018067 / 23.259194; **22.54**; 5.51% |
| cold / ns/op | 58.622385 / 70.561763 / 67.480509; **67.48**; 17.69% | 61.93693 / 60.103859 / 68.444126; **61.94**; 13.47% |
| cold / p95-sweep-ns | 61.406852 / 91.673395 / 76.999253; **77**; 39.31% | 64.476283 / 69.311305 / 74.265909; **69.31**; 14.12% |

### 争抢宿主 canary（仅诊断）

Concurrent host workload observed; diagnostic canary only.
该批存在并发宿主负载，仅作诊断；不与独占100x复测合并。

| Fixture | Time | p95 | B/op | allocs/op |
|---|---:|---:|---:|---:|
| 50_warm | -25.633% | -24.322% | -75.732% | -0.019% |
| 50_cold | -66.549% | -71.703% | -49.316% | -72.849% |
| 100_warm | +12.572% | +15.441% | -75.772% | -0.009% |
| 100_cold | -75.655% | -76.904% | -49.386% | -73.555% |

## SSE 转发

全部采用合成内容和丢弃输出的 writer；一次 op 为完整 fixture 流。WS 指 Engine 的 HTTP/SSE→客户端 WebSocket 适配器，fixture 不建立真实 WebSocket 连接。慢写组每次 Write 睡眠 100 µs，实际调度延迟包含在结果中。

| Fixture | ms/stream | MiB/op | allocs/op | p95 ms | streams/s |
|---|---:|---:|---:|---:|---:|
| HTTP/small_256 | 1.116 → 0.3288 (-70.55%) | 0.2055 → 0.01562 (-92.40%) | 3,893 → 304 (-92.19%) | 1.468 → 0.415 (-71.73%) | 895.8 → 3,042 (+239.54%) |
| WS/small_256 | 0.9074 → 0.3666 (-59.60%) | 0.2766 → 0.0239 (-91.36%) | 5,181 → 565 (-89.09%) | 1.044 → 0.4948 (-52.60%) | 1,102 → 2,728 (+147.54%) |
| HTTP/multiline_128 | 0.4449 → 0.1864 (-58.11%) | 0.14 → 0.01211 (-91.35%) | 2,356 → 181 (-92.32%) | 0.6398 → 0.2673 (-58.22%) | 2,248 → 5,365 (+138.70%) |
| WS/multiline_128 | 0.5169 → 0.2025 (-60.82%) | 0.1895 → 0.01643 (-91.33%) | 3,133 → 314 (-89.98%) | 0.6686 → 0.2579 (-61.43%) | 1,934 → 4,938 (+155.24%) |
| HTTP/slow_write_32 | 37.24 → 36.54 (-1.90%) | 0.03268 → 0.008892 (-72.79%) | 532 → 80 (-84.96%) | 37.67 → 36.77 (-2.40%) | 26.85 → 27.37 (+1.94%) |
| WS/slow_write_32 | 37.25 → 36.57 (-1.83%) | 0.04232 → 0.01024 (-75.81%) | 701 → 117 (-83.31%) | 37.82 → 36.73 (-2.88%) | 26.85 → 27.35 (+1.86%) |
| HTTP/terminal_1MiB | 13.63 → 6.624 (-51.40%) | 15.06 → 6.452 (-57.15%) | 144 → 86 (-40.28%) | 17.56 → 8.366 (-52.35%) | 73.37 → 151 (+105.77%) |
| WS/terminal_1MiB | 12.82 → 6.675 (-47.95%) | 17.07 → 6.453 (-62.21%) | 173 → 95 (-45.09%) | 16.58 → 7.873 (-52.50%) | 77.97 → 149.8 (+92.12%) |
| HTTP/terminal_8MiB | 92.38 → 49.44 (-46.49%) | 119.8 → 50.88 (-57.54%) | 153 → 95 (-37.91%) | 100.4 → 53.24 (-46.95%) | 10.82 → 20.23 (+86.87%) |
| WS/terminal_8MiB | 96.45 → 50.78 (-47.35%) | 135.8 → 50.88 (-62.54%) | 182 → 104 (-42.86%) | 105 → 53.07 (-49.47%) | 10.37 → 19.69 (+89.94%) |
| HTTP/terminal_32MiB | 390 → 199.1 (-48.95%) | 511.8 → 197 (-61.52%) | 160 → 101 (-36.88%) | 427.1 → 221.3 (-48.18%) | 2.564 → 5.023 (+95.88%) |
| WS/terminal_32MiB | 415.9 → 206.1 (-50.45%) | 575.9 → 197 (-65.80%) | 189 → 110 (-41.80%) | 450 → 220.6 (-50.97%) | 2.405 → 4.853 (+101.81%) |

### 首次写出

首次写出为本地合成流从 relay 入口到 writer 首次返回的时间，包含组帧和观察；它独立于完整流吞吐。

| Fixture | First write µs | Total write ms |
|---|---:|---:|
| HTTP/small_256 | 16.9 → 17.13 (+1.37%) | 0.006897 → 0.005107 (-25.95%) |
| WS/small_256 | 11.63 → 20.15 (+73.35%) | 0.006678 → 0.005228 (-21.71%) |
| HTTP/multiline_128 | 10.94 → 13.88 (+26.82%) | 0.00321 → 0.00262 (-18.38%) |
| WS/multiline_128 | 9.68 → 13.13 (+35.64%) | 0.003406 → 0.002712 (-20.38%) |
| HTTP/slow_write_32 | 1,183 → 1,124 (-4.92%) | 36.62 → 36.23 (-1.08%) |
| WS/slow_write_32 | 1,156 → 1,163 (+0.60%) | 36.41 → 36.24 (-0.46%) |
| HTTP/terminal_1MiB | 75.34 → 46.45 (-38.35%) | 0.001097 → 0.0006211 (-43.38%) |
| WS/terminal_1MiB | 66.92 → 35.86 (-46.42%) | 0.001198 → 0.0005176 (-56.79%) |
| HTTP/terminal_8MiB | 71.58 → 57.76 (-19.31%) | 0.001021 → 0.001021 (+0.00%) |
| WS/terminal_8MiB | 78.07 → 65.48 (-16.12%) | 0.00109 → 0.000976 (-10.46%) |
| HTTP/terminal_32MiB | 78.82 → 65.68 (-16.67%) | 0.001024 → 0.0009384 (-8.36%) |
| WS/terminal_32MiB | 78.77 → 63.59 (-19.28%) | 0.001023 → 0.001071 (+4.69%) |

代价同样保留：WS 小帧首次写出中位数为 11.63 → 20.15 µs（+73.35%），HTTP/WS 多行分别变化 +26.82%/+35.64%。这些本地微秒级启动成本与完整流耗时下降同时存在。

## 普通计费队列积压

阻塞一个 worker，排队 32/128 个任务，每个任务使用独立 1 MiB 正文；显式 GC 后测每任务保留内存。`ns/op` 包含 fixture 创建、GC、队列排空与关闭，是实验耗时；`experiments/s` 只是其倒数。两者不作为业务延迟或计费 TPS。累计 B/op 仍包含正文的初始创建，因此不会随 retained heap 同比例下降。

本节选择 `baseline-billing` → `candidate-typed-final-billing`。

Final typed ownership implementation; ordinary request/response body retention. 最终typed所有权实现。

| Fixture | Retained B/task | Allocated MiB/experiment | Experiment ms (includes GC) |
|---|---:|---:|---:|
| ws_replay/queued=32 | 1,049,519 → 3,770 (-99.64%) | 32.06 → 32.17 (+0.33%) | 11.63 → 14.32 (+23.13%) |
| ws_replay/queued=128 | 1,049,515 → 3,763 (-99.64%) | 128.2 → 128.6 (+0.33%) | 9.123 → 21.14 (+131.77%) |
| systemone_response/queued=32 | 1,049,267 → 3,516 (-99.66%) | 32.05 → 32.15 (+0.31%) | 4.534 → 7.908 (+74.43%) |
| systemone_response/queued=128 | 1,049,266 → 3,516 (-99.66%) | 128.1 → 128.5 (+0.31%) | 7.404 → 20.51 (+176.98%) |
| grok_request/queued=32 | 1,048,867 → 3,823 (-99.64%) | 32.04 → 32.15 (+0.34%) | 21.52 → 24.79 (+15.20%) |
| grok_request/queued=128 | 1,048,866 → 3,819 (-99.64%) | 128.1 → 128.5 (+0.34%) | 75.24 → 72.51 (-3.63%) |

### 早期积压实验（单独保留）

`baseline-billing` → `candidate-billing`；早期测量的诊断结果，未与上表合并。

| Fixture | Retained B/task | Allocated MiB/experiment | Experiment ms (includes GC) |
|---|---:|---:|---:|
| ws_replay/queued=32 | 1,049,519 → 1,240 (-99.88%) | 32.06 → 32.09 (+0.09%) | 11.63 → 16.28 (+39.99%) |
| ws_replay/queued=128 | 1,049,515 → 1,235 (-99.88%) | 128.2 → 128.3 (+0.09%) | 9.123 → 19.7 (+115.94%) |
| systemone_response/queued=32 | 1,049,267 → 958.8 (-99.91%) | 32.05 → 32.07 (+0.07%) | 4.534 → 8.746 (+92.91%) |
| systemone_response/queued=128 | 1,049,266 → 955.6 (-99.91%) | 128.1 → 128.2 (+0.07%) | 7.404 → 20.78 (+180.68%) |
| grok_request/queued=32 | 1,048,867 → 654.8 (-99.94%) | 32.04 → 32.06 (+0.04%) | 21.52 → 25.63 (+19.10%) |
| grok_request/queued=128 | 1,048,866 → 651.6 (-99.94%) | 128.1 → 128.2 (+0.03%) | 75.24 → 75.74 (+0.66%) |

### 早期积压实验（单独保留）

`baseline-billing` → `candidate-packed-billing`；早期测量的诊断结果，未与上表合并。

| Fixture | Retained B/task | Allocated MiB/experiment | Experiment ms (includes GC) |
|---|---:|---:|---:|
| ws_replay/queued=32 | 1,049,519 → 4,408 (-99.58%) | 32.06 → 32.19 (+0.40%) | 11.63 → 13.54 (+16.46%) |
| ws_replay/queued=128 | 1,049,515 → 4,406 (-99.58%) | 128.2 → 128.7 (+0.40%) | 9.123 → 17.04 (+86.79%) |
| systemone_response/queued=32 | 1,049,267 → 4,411 (-99.58%) | 32.05 → 32.18 (+0.40%) | 4.534 → 6.905 (+52.30%) |
| systemone_response/queued=128 | 1,049,266 → 4,412 (-99.58%) | 128.1 → 128.7 (+0.40%) | 7.404 → 18.62 (+151.44%) |
| grok_request/queued=32 | 1,048,867 → 4,459 (-99.57%) | 32.04 → 32.17 (+0.40%) | 21.52 → 22.62 (+5.10%) |
| grok_request/queued=128 | 1,048,866 → 4,460 (-99.57%) | 128.1 → 128.6 (+0.40%) | 75.24 → 70.61 (-6.16%) |

### 早期积压实验（单独保留）

`baseline-billing` → `candidate-packed-final-billing`；早期测量的诊断结果，未与上表合并。

| Fixture | Retained B/task | Allocated MiB/experiment | Experiment ms (includes GC) |
|---|---:|---:|---:|
| ws_replay/queued=32 | 1,049,519 → 4,407 (-99.58%) | 32.06 → 32.19 (+0.40%) | 11.63 → 15.04 (+29.33%) |
| ws_replay/queued=128 | 1,049,515 → 4,404 (-99.58%) | 128.2 → 128.7 (+0.40%) | 9.123 → 17.29 (+89.57%) |
| systemone_response/queued=32 | 1,049,267 → 4,415 (-99.58%) | 32.05 → 32.18 (+0.39%) | 4.534 → 7.256 (+60.04%) |
| systemone_response/queued=128 | 1,049,266 → 4,412 (-99.58%) | 128.1 → 128.7 (+0.40%) | 7.404 → 18.06 (+143.87%) |
| grok_request/queued=32 | 1,048,867 → 4,459 (-99.57%) | 32.04 → 32.17 (+0.40%) | 21.52 → 22.78 (+5.86%) |
| grok_request/queued=128 | 1,048,866 → 4,460 (-99.57%) | 128.1 → 128.6 (+0.40%) | 75.24 → 72.84 (-3.19%) |

### 同一候选版本内的完整本地生命周期

此组在同一个候选测试二进制内对比 legacy_capture 与 owned_snapshot；不把两种模式标成两个提交。16 个 producer、4 个 worker、128 队列容量；每次复制并解析 8 KiB/1 MiB 正文，经实际 worker pool，执行相同的本地价格计算和扣费命令构造，再等待结果。计时区间无显式 GC、sleep、模拟网络或数据库时延；fixture 不访问真实数据库。吞吐来自并行全程耗时，p95-e2e 包含正文处理、入队、worker 工作与唤醒，不能直接用并行 ns/op 替代单请求延迟。

| Fixture | requests/s | p95 E2E ms | KiB/op | Runtime CPU µs/op |
|---|---:|---:|---:|---:|
| ws_replay/bytes=8192 | 177,080 → 138,437 (-21.82%) | 0.25 → 0.4413 (+76.53%) | 12.79 → 18.53 (+44.84%) | 22.42 → 28.77 (+28.35%) |
| ws_replay/bytes=1048576 | 3,634 → 3,471 (-4.49%) | 19.42 → 20.38 (+4.91%) | 1,029 → 1,035 (+0.56%) | 1,087 → 1,140 (+4.85%) |
| systemone_response/bytes=8192 | 181,147 → 152,664 (-15.72%) | 0.2312 → 0.3582 (+54.96%) | 12.52 → 18.03 (+44.07%) | 21.49 → 25.77 (+19.89%) |
| systemone_response/bytes=1048576 | 3,697 → 3,721 (+0.65%) | 17.66 → 18.79 (+6.43%) | 1,029 → 1,034 (+0.54%) | 1,060 → 1,063 (+0.32%) |
| grok_request/bytes=8192 | 151,419 → 119,003 (-21.41%) | 0.2972 → 0.4369 (+46.97%) | 13.13 → 18.94 (+44.29%) | 25.73 → 32.89 (+27.84%) |
| grok_request/bytes=1048576 | 2,533 → 2,661 (+5.05%) | 22.61 → 25.74 (+13.80%) | 1,029 → 1,035 (+0.56%) | 1,569 → 1,486 (-5.28%) |

原始文件前缀：`candidate-typed-final-lifecycle`。

### 早期生命周期诊断（单独保留）

`candidate-lifecycle` 结果保留如下；它是各自二进制内两种模式的对比，不与上面的最终组混合统计。

Earlier deep-copy projection; diagnostic history. 旧深复制投影，仅保留诊断。

| Fixture | requests/s change | p95 E2E change | B/op change |
|---|---:|---:|---:|
| ws_replay/bytes=8192 | -23.80% | +58.26% | +63.96% |
| ws_replay/bytes=1048576 | +8.89% | -3.97% | +0.80% |
| systemone_response/bytes=8192 | -38.72% | +111.59% | +63.36% |
| systemone_response/bytes=1048576 | +2.72% | -5.98% | +0.77% |
| grok_request/bytes=8192 | -31.34% | +56.10% | +62.80% |
| grok_request/bytes=1048576 | -4.73% | +17.34% | +0.80% |

### 早期生命周期诊断（单独保留）

`candidate-packed-lifecycle` 结果保留如下；它是各自二进制内两种模式的对比，不与上面的最终组混合统计。

Packed projection with overlapping host test work; diagnostic only. packed首批有宿主测试竞争，仅诊断。

| Fixture | requests/s change | p95 E2E change | B/op change |
|---|---:|---:|---:|
| ws_replay/bytes=8192 | -26.06% | +59.07% | +48.57% |
| ws_replay/bytes=1048576 | +6.55% | -7.59% | +0.60% |
| systemone_response/bytes=8192 | +22.69% | -34.59% | +49.88% |
| systemone_response/bytes=1048576 | -2.75% | +14.02% | +0.61% |
| grok_request/bytes=8192 | -21.51% | +38.73% | +47.92% |
| grok_request/bytes=1048576 | -0.30% | +19.33% | +0.61% |

### 早期生命周期诊断（单独保留）

`candidate-packed-final-lifecycle` 结果保留如下；它是各自二进制内两种模式的对比，不与上面的最终组混合统计。

Exclusive packed projection before typed Account and published auth Group optimization. typed和auth Group优化前的独占packed批。

| Fixture | requests/s change | p95 E2E change | B/op change |
|---|---:|---:|---:|
| ws_replay/bytes=8192 | -18.36% | +60.19% | +48.57% |
| ws_replay/bytes=1048576 | +2.12% | +3.20% | +0.60% |
| systemone_response/bytes=8192 | -16.17% | +58.53% | +49.88% |
| systemone_response/bytes=1048576 | +1.05% | -1.49% | +0.61% |
| grok_request/bytes=8192 | -20.58% | +31.10% | +47.92% |
| grok_request/bytes=1048576 | +1.13% | +18.86% | +0.61% |

### 真实认证 L1 路径的生命周期微基准

此组单独测量真实 `APIKeyService.GetByKey` 的 L1 命中与后续 worker 生命周期；每个计时请求都鉴权，冷发布与独立价格投影构建在计时前经真实缓存 miss 完成，仓库替身仅位于存储边界。仍为同一二进制的 legacy_capture/owned_snapshot 对比，16 producer、4 worker；不经过 HTTP handler。

| Fixture | requests/s | p95 E2E ms | KiB/op | Runtime CPU µs/op |
|---|---:|---:|---:|---:|
| ws_replay/bytes=8192 | 161,276 → 138,682 (-14.01%) | 0.2936 → 0.4246 (+44.61%) | 15.44 → 19.02 (+23.19%) | 24.49 → 28.34 (+15.69%) |
| ws_replay/bytes=1048576 | 3,355 → 3,625 (+8.05%) | 20.14 → 18.9 (-6.14%) | 1,032 → 1,035 (+0.35%) | 1,161 → 1,093 (-5.83%) |
| systemone_response/bytes=8192 | 159,790 → 143,091 (-10.45%) | 0.34 → 0.4273 (+25.67%) | 15.16 → 18.52 (+22.16%) | 24.9 → 27.47 (+10.33%) |
| systemone_response/bytes=1048576 | 3,782 → 3,446 (-8.88%) | 17.91 → 19.76 (+10.35%) | 1,031 → 1,035 (+0.33%) | 1,048 → 1,140 (+8.84%) |
| grok_request/bytes=8192 | 118,057 → 110,140 (-6.71%) | 0.4872 → 0.5195 (+6.64%) | 15.77 → 19.42 (+23.09%) | 33.72 → 36.12 (+7.13%) |
| grok_request/bytes=1048576 | 2,440 → 2,531 (+3.73%) | 23 → 27.62 (+20.10%) | 1,032 → 1,036 (+0.35%) | 1,615 → 1,561 (-3.33%) |

Raw-file prefix: `candidate-typed-final-authenticated`.

### HTTP 请求与计费结算闭环

16 个客户端构成同一闭环：每次等待 HTTP EOF 和 UsageLog.Create 结算完成后才发下一请求；该组与服务层 fallback/authenticated microbench 分开。它使用本地 transport 和存储边界替身，不测 PostgreSQL 性能。GNU time RSS/CPU 覆盖两个正文尺寸、校准与预热。

| Fixture | HTTP req/s | Settled req/s | HTTP p95 ms | Settled p95 ms | Runtime CPU ms/request | Allocated MiB/op |
|---|---:|---:|---:|---:|---:|---:|
| 8192 bytes | 2,403 → 2,426 (+0.96%) | 2,403 → 2,426 (+0.96%) | 11.4 → 11.25 (-1.37%) | 10.78 → 10.59 (-1.73%) | 1.66 → 1.637 (-1.42%) | 0.7735 → 0.7464 (-3.49%) |
| 1048576 bytes | 72.34 → 74.97 (+3.64%) | 72.34 → 74.97 (+3.64%) | 326.6 → 318 (-2.62%) | 321.9 → 312.9 (-2.81%) | 55.02 → 53.11 (-3.47%) | 39.84 → 39.82 (-0.06%) |

所选HTTP批次：confirm10s。8 KiB 组 HTTP p95 -1.371289%，结算 p95 -1.732854%，CPU/request -1.419328%；1 MiB p95/CPU 最差变化 -2.621112%。本批两个尺寸的 p95/CPU 中位变化均在5%以内。

| HTTP fixture / metric | Baseline round 1 / 2 / 3; median; spread | Candidate round 1 / 2 / 3; median; spread |
|---|---|---|
| request_bytes=8192 / p95-http-ns (ms) | 11.402706 / 10.990988 / 11.653247; **11.4**; 5.81% | 11.4803 / 11.246342 / 10.826938; **11.25**; 5.81% |
| request_bytes=8192 / p95-settled-ns (ms) | 10.780941 / 10.32294 / 10.896356; **10.78**; 5.32% | 10.679963 / 10.594123 / 10.145628; **10.59**; 5.04% |
| request_bytes=8192 / runtime-cpu-ns/request (ms) | 1.660434 / 1.599438 / 1.690322; **1.66**; 5.47% | 1.641683 / 1.636867 / 1.592631; **1.637**; 3.00% |
| request_bytes=1048576 / p95-http-ns (ms) | 326.565664 / 332.303579 / 313.960807; **326.6**; 5.62% | 408.710322 / 318.006013 / 307.768302; **318**; 31.74% |
| request_bytes=1048576 / p95-settled-ns (ms) | 321.947293 / 328.298032 / 305.605031; **321.9**; 7.05% | 394.012322 / 312.90534 / 301.79565; **312.9**; 29.47% |
| request_bytes=1048576 / runtime-cpu-ns/request (ms) | 55.022514 / 55.596185 / 52.171344; **55.02**; 6.22% | 57.100847 / 53.113331 / 52.012129; **53.11**; 9.58% |

#### HTTP history: original (2s)

早期批次保留为独立诊断，不与所选批次混合。

| Fixture | CPU change | HTTP p95 change | Settled p95 change |
|---|---:|---:|---:|
| request_bytes=8192 | +5.36% | +5.00% | +6.61% |
| request_bytes=1048576 | -0.00% | +3.79% | +2.61% |

CPU 定位使用另一次带采样的 8 KiB 请求运行，不混入性能三轮。候选 profile 中 submitFrozenOpenAIUsageRecord 整个同步构造与入队路径累计 0.42 秒、占样本 0.47%；其中 SnapshotOpenAIRecordUsageInput 为 0.25 秒、0.28%。0.47% 不是纯构造占比。完整路径主要成本仍在 HTTP/JSON 解析与系统调用；这些数值描述该次 profile。
[Candidate CPU top](../backend/benchmarks/results/memory-20261106/http-cpu-final-top.txt), [baseline CPU top](../backend/benchmarks/results/memory-20261106/http-cpu-baseline-top.txt), [construction and enqueue detail](../backend/benchmarks/results/memory-20261106/http-cpu-final-enqueue.txt).
原始 .prof 保存在本地任务 artifacts，交付只包含其文本输出。

## 整个阶段进程的资源

GNU time 统计整个阶段进程，涵盖全部子基准、fixture 准备、预热、GC 与清理。峰值 RSS 与上文 retained Go heap 口径不同；CPU 每轮先对 user+system 秒数求和，再取三轮中位数。

| Phase | Peak RSS MiB | User CPU s | System CPU s | Total CPU s | Wall s |
|---|---:|---:|---:|---:|---:|
| cache | 4,407 → 368.9 (-91.63%) | 41.14 → 7.72 (-81.23%) | 15.58 → 8.47 (-45.64%) | 56.72 → 16.46 (-70.98%) | 42.9 → 11.47 (-73.26%) |
| sse | 556.6 → 369.8 (-33.56%) | 21.07 → 11.02 (-47.70%) | 2.46 → 0.87 (-64.63%) | 23.53 → 11.91 (-49.38%) | 23.58 → 12.88 (-45.38%) |
| billing | 56.09 → 56.69 (+1.07%) | 0.2 → 0.25 (+25.00%) | 0.12 → 0.14 (+16.67%) | 0.31 → 0.39 (+25.81%) | 0.5 → 0.45 (-10.00%) |
| sharing | 4,291 → 370.9 (-91.36%) | 144 → 44.35 (-69.21%) | 57.19 → 44.3 (-22.54%) | 205.3 → 89.35 (-56.48%) | 159.1 → 63.54 (-60.07%) |
| sharing-zero100x | 118.7 → 116.4 (-1.92%) | 4.89 → 4.94 (+1.02%) | 6.34 → 6.73 (+6.15%) | 11.34 → 11.7 (+3.17%) | 9.74 → 10 (+2.67%) |
| sharing-zero100x-exclusive | 118.1 → 117.4 (-0.58%) | 5.1 → 5.16 (+1.18%) | 6.81 → 6.75 (-0.88%) | 11.38 → 11.69 (+2.72%) | 9.68 → 9.97 (+3.00%) |
| http-lifecycle | 173.6 → 178.2 (+2.69%) | 101.7 → 108.7 (+6.89%) | 16.15 → 18.52 (+14.67%) | 117.8 → 127.2 (+7.95%) | 34.1 → 36.83 (+8.01%) |

计费实验进程的峰值 RSS 中位数变化 +1.07%，CPU 总秒数变化 +25.81%；这与阻塞队列的 retained heap 降低同时出现。该进程仍构造全部 1 MiB 正文并执行显式 GC，资源表覆盖这些成本。

生命周期进程每轮都运行两种模式，其整进程资源只报告合计，不归因给任一模式。

| Lifecycle resource | Round 1 / 2 / 3; median; spread |
|---|---|
| max_rss_KiB | 115380 / 121128 / 110136; **115,380**; 9.53% |
| user_CPU_s | 120.28 / 110.81 / 136.59; **120.3**; 21.43% |
| system_CPU_s | 6.42 / 5.27 / 7.11; **6.42**; 28.66% |
| user_plus_system_CPU_s | 126.7 / 116.08 / 143.7; **126.7**; 21.80% |
| wall_s | 33.96 / 31.11 / 38.49; **33.96**; 21.73% |

认证微基准的进程资源同样包含两种模式，单独列出。

| Authenticated resource | Round 1 / 2 / 3; median; spread |
|---|---|
| max_rss_KiB | 112816 / 109132 / 138408; **112,816**; 25.95% |
| user_CPU_s | 129.12 / 148.78 / 115.3; **129.1**; 25.93% |
| system_CPU_s | 5.49 / 6.06 / 5.44; **5.49**; 11.29% |
| user_plus_system_CPU_s | 134.61 / 154.84 / 120.74; **134.6**; 25.33% |
| wall_s | 35.75 / 41.44 / 32.11; **35.75**; 26.10% |

## 已执行验证

验证记录与性能二进制分开保存。集成覆盖共享缓存跨实例权威与 Redis 重连、分组退休/重开 epoch、快照/outbox、调度投影、Engine 原子结算恢复与并发、普通计费去重及脱离请求生命周期后的结算。日志中的 Redis 重连故障和 cache-not-ready 是对应测试主动注入的场景。

| 范围 | 实际命令 | 环境与记录 |
|---|---|---|
| Repository integration / real PostgreSQL and Redis | <code>CI=true GOMAXPROCS=4 integration-repository.test '-test.run=Test(Scheduler&#124;UsageRecordSnapshotDetached&#124;UsageBillingRepositoryApply&#124;CodexEngineBillingStore&#124;ListSchedulableAccountLoads)' -test.v -test.timeout=15m</code> | Earlier Linux/amd64 integration binary compiled with -tags=integration; testcontainers PostgreSQL 18.1-alpine3.23 and Redis 8.4-alpine; final rerun is listed separately; [PASS](../backend/benchmarks/results/memory-20261106/verification-integration.txt) |
| Frontend locale completeness, TypeScript and production build | <code>pnpm run check:i18n && vue-tsc -b && vite build</code> | frontend directory; invoked by the package build script; [PASS](../backend/benchmarks/results/memory-20261106/verification-frontend-build.txt) |
| Memory dashboard component tests | <code>pnpm exec vitest run src/views/admin/ops/components/__tests__/OpsMemory.spec.ts</code> | frontend directory; FastCtx job j-puxjvd started 2026-11-06T15:32:12Z; Vitest 2.1.9, 1 file and 7 tests passed, tests 439 ms, total 4.00 s; original job output copied after checking for sensitive content; [PASS](../backend/benchmarks/results/memory-20261106/verification-frontend-components.txt) |
| Targeted frontend ESLint | <code>pnpm exec eslint src/views/admin/ops/OpsDashboard.vue src/views/admin/ops/components/OpsMemory.vue src/views/admin/ops/components/__tests__/OpsMemory.spec.ts src/api/admin/ops.ts src/i18n/locales/en/admin/ops.ts src/i18n/locales/zh/admin/ops.ts</code> | frontend directory; FastCtx job j-hhz78g exit 0, no diagnostics; result recorded by the coordinating agent; [PASS](../backend/benchmarks/results/memory-20261106/verification-frontend-lint.json) |
| Handler billing error request-ID logging race regression | <code>GOEXPERIMENT=greenteagc,jsonv2 GOMAXPROCS=4 go test -tags=unit -race ./internal/handler -run 'Test(FrozenUsage&#124;WrapUsage&#124;UsageRecord)' -count=1 -timeout=5m</code> | backend directory; after restoring request_id on the WebSocket billing error branch; FastCtx job j-nnqqjs exit 0 confirmed by the coordinating agent; [PASS](../backend/benchmarks/results/memory-20261106/verification-billing-log-trace-race.txt) |
| Final unit | <code>python backend/benchmarks/verify-memory.py --phase unit</code> | GOEXPERIMENT=greenteagc,jsonv2; GOMAXPROCS=4; Go 1.27.3; exact subprocess arguments are retained in the log; [PASS](../backend/benchmarks/results/memory-20261106/verification-final-unit.txt) |
| Final race | <code>python backend/benchmarks/verify-memory.py --phase race</code> | GOEXPERIMENT=greenteagc,jsonv2; GOMAXPROCS=4; Go 1.27.3; exact subprocess arguments are retained in the log; [PASS](../backend/benchmarks/results/memory-20261106/verification-final-race.txt) |
| Final build | <code>python backend/benchmarks/verify-memory.py --phase build</code> | GOEXPERIMENT=greenteagc,jsonv2; GOMAXPROCS=4; Go 1.27.3; exact subprocess arguments are retained in the log; [PASS](../backend/benchmarks/results/memory-20261106/verification-final-build.txt) |
| Final integration | <code>python backend/benchmarks/verify-memory.py --phase integration</code> | GOEXPERIMENT=greenteagc,jsonv2; GOMAXPROCS=4; Go 1.27.3; exact subprocess arguments are retained in the log; [PASS](../backend/benchmarks/results/memory-20261106/verification-final-integration.txt) |
| Final handler billing error tracing race regression | <code>GOEXPERIMENT=greenteagc,jsonv2 GOMAXPROCS=4 go test -tags=unit -race ./internal/handler -run 'Test(FrozenUsage&#124;WrapUsage&#124;UsageRecord)' -count=1 -timeout=5m</code> | backend directory; WebSocket error logging restores request_id/model from the owned Result and gateway_request_id/client_request_id from the independent worker context; FastCtx job j-momlwb exit 0 confirmed by the coordinating agent; [PASS](../backend/benchmarks/results/memory-20261106/verification-billing-final-log-trace-race.txt) |

最终集成另包含真实 PostgreSQL 用户行锁等待：确认 worker 阻塞后取消父请求、清空原输入，再解锁并验证一次扣费、缓存同步、预留释放与重复幂等。

完整 unit/race/build/integration 验证后，最后补齐了 WebSocket 计费错误日志字段：从已独立持有的 Result 读取 request_id/model，从独立 worker context 读取 gateway_request_id/client_request_id。成功请求路径保持原样；随后用上表同一 handler race 命令通过定向回归（6.514 秒）。两份日志追踪回归记录均保留。

既有失败单列：保存自 `8972274c2` 的 baseline 二进制复现 `TestGrokQuotaBillingStillRejectsUnavailableCredentialsDuringCooldown/missing_configured_proxy`；断言期待 `GROK_QUOTA_TOKEN_UNAVAILABLE`，实际为 `ACCOUNT_PROXY_UNAVAILABLE`。本次未弱化该断言，保留[失败日志](../backend/benchmarks/results/memory-20261106/verification-baseline-known-grok-failure.txt)。

前端 build 运行 i18n 完整性检查、TypeScript 检查与 Vite 生产构建；日志保留 Browserslist 数据过期、动态/静态混合导入和 bundle 大小提示。该命令没有包含 lint。

## 升级、观测与回退

1. 保存当前后端/前端发布产物与配置，先在一个实例更新并按正常关闭流程排空普通计费队列。本次内存优化没有新增数据库迁移或修改共享 Redis 快照格式。进程启动后本地解码缓存为空。
2. `gateway.scheduling.snapshot_cache_max_bytes` 是每进程母本与 bucket 索引的软预算，默认 64 MiB；值为 0 时使用默认值。沿用当前限流、账单队列容量及溢出策略；按相同流量比较命中率、估算字节、淘汰与延迟。缓存复用最长 10 秒，空闲淘汰为 100 秒，清理周期 10 秒。
3. 管理员在运维页面打开内存观测，并保持已有运维监控开关启用。选择当前实例 ID，观察 RSS、HeapAlloc/Inuse/Idle/Released、GC、goroutine、缓存、active body/SSE 与队列指标。每 10 秒采样，单实例保留 360 点（约 1 小时），30 秒未见采样标为 stale。用相同实例、负载和观察窗口比较；进程重启会生成新 ID 并重置累计计数。
4. 流结束或请求退出后，`active_sse_buffers`/`active_sse_buffer_bytes` 与 active body 指标应随实际活跃请求回落；队列 waiting/running 应随负载排空。关注 dropped、sync_fallback、等待/执行直方图与结算失败增量。完成观察后逐实例扩大部署。
5. 需要 heap profile 时，在所选实例对应的后端连接上启用下载，再下载分析，完成后关闭。管理 API 为 `/api/v1/admin/ops/runtime/memory` 和 `/api/v1/admin/ops/runtime/heap-profile/{enable,disable,download}`。启用/禁用 JSON 与下载 query 都需 `instance_id`；仅当前响应进程可操作，跨进程请求返回 409。下载默认关闭，启用 30 分钟后过期，每进程只允许一个下载；它直接写出已有采样，不强制 GC、不修改 MemProfileRate、不落临时文件。
6. 回退时先停止向目标实例分配新流量，按正常关闭流程等待活动流和计费任务完成，再恢复保存的后端/前端产物与配置。本地缓存随进程重启清空；共享 Redis 快照继续复用，观测数据按 TTL 自然过期，无需清空 Redis。

队列语义：`completed`、`failed` 统计 worker 闭包执行，`submitted`、`dropped`、`sync_fallback` 反映调度行为。普通 `RecordUsage` 调用的返回结果单独计入 `settlement_succeeded`/`settlement_failed`，幂等去重返回成功也归成功；Engine 普通计费 no-op 被排除，其 durable ledger 结算另行观察。闭包正常返回可能仍有结算错误，两组计数需要一起看。队列等待直方图覆盖排队任务，执行直方图还包含同步回退。

认证 L1 的每个发布条目另持有一份独立的 billing-only Group 价格投影，队列任务复用该冻结版本；普通非认证 Group 仍逐次深复制。价格投影不引用认证 Group 的地图或价格指针；L2 JSON 格式保持原样，解码后重建投影。改价沿现有 L1/L2 失效、PubSub 与 TTL 发布新版本，已排队任务保留原版本。这部分常驻内存按 `api_key_auth_cache.l1_size` 条目数约束，不包含在 `scheduler_accounts`/`scheduler_cache_total` 的估算或调度缓存 64 MiB 预算内。

## 适用范围与尚未覆盖的场景

性能数据来自上述固定合成 fixture。缓存 benchmark 使用 miniredis；SSE writer 丢弃输出，首写是本地 writer 返回时间；生命周期组只执行本地价格计算和扣费命令构造。真实 PostgreSQL/Redis 用于独立正确性集成测试；真实模型、外部网络、TLS、数据库吞吐和长时间生产流量未参与这些计时。

缓存软预算以对象估算字节计数，与实际 HeapAlloc/RSS 的口径不同，正在使用的调用方视图可继续持有已淘汰母本。10 秒快照复用上限保留了未推进 revision 的写入可见窗口。最初共享比例组每轮仅 3 次 sweep，复测必须保留独立样本数与二进制来源。

运行观测仅保存数值快照；Redis 保存期 70 分钟，最多登记 128 个进程，达到上限显示 truncated。Redis 不可用时当前实例仍返回进程内历史。heap profile 属于单个进程的采样堆视图，应按现有管理员访问权限管理下载文件。

## 从原始文件重新汇总

汇总脚本只读取已保存结果，不执行基准。交付目录只含小型 txt/json/md 原始数据与汇总，不含测试二进制。

```sh
python backend/benchmarks/summarize-memory.py --source backend/benchmarks/results/memory-20261106 --output-dir .git/task-artifacts/memory-20261106 --phases cache sse billing sharing sharing-zero100x sharing-zero100x-exclusive --include-lifecycle --lifecycle-prefix candidate-typed-final-lifecycle --authenticated-prefix candidate-typed-final-authenticated --http-prefix candidate --http-tag confirm10s --http-history-tag original --lifecycle-history-prefix candidate-lifecycle --lifecycle-history-prefix candidate-packed-lifecycle --lifecycle-history-prefix candidate-packed-final-lifecycle --lifecycle-note 'candidate-lifecycle=Earlier deep-copy projection; diagnostic history. 旧深复制投影，仅保留诊断。' --lifecycle-note 'candidate-packed-lifecycle=Packed projection with overlapping host test work; diagnostic only. packed首批有宿主测试竞争，仅诊断。' --lifecycle-note 'candidate-packed-final-lifecycle=Exclusive packed projection before typed Account and published auth Group optimization. typed和auth Group优化前的独占packed批。' --candidate-label billing=candidate-typed-final --history-phase billing=candidate --history-phase billing=candidate-packed --history-phase billing=candidate-packed-final --phase-note 'sharing-zero100x=Concurrent host workloads were observed; diagnostic only. 宿主机存在并发负载，仅诊断。' --phase-note 'sharing-zero100x-exclusive=Exclusive measurement window; 100 sweeps per fixture per round; retain round spread. 独占窗口，每轮100次，保留波动。' --phase-note 'billing=Final typed ownership implementation; ordinary request/response body retention. 最终typed所有权实现。'
```

共享比例与生命周期属于独立实验组，分别保留自己的操作单位与测量范围。此文报告已保存的原始测量，是否满足验收需结合协议、正确性与性能评审。

### 复现性能与回归验证

性能 runner 固定 Go/GOEXPERIMENT/GOMAXPROCS，并通过 GNU time 保存每轮资源；在独占 CPU 的窗口执行。使用新的 label 保存复测，避免覆盖历史。只有已有匹配的测试二进制时才使用 `--skip-build`，并传入准确的 `--binary-source`。baseline 必须从基线源码与相同 benchmark fixture 构建，不能把当前候选构建标成 baseline。

```sh
python backend/benchmarks/memory-performance.py --label rerun --phase all
python backend/benchmarks/memory-performance.py --label rerun --phase sharing
python backend/benchmarks/memory-performance.py --label rerun --phase lifecycle
python backend/benchmarks/verify-memory.py --phase unit
python backend/benchmarks/verify-memory.py --phase race
python backend/benchmarks/verify-memory.py --phase build
python backend/benchmarks/verify-memory.py --phase integration
```
