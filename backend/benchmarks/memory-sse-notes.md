# Engine SSE memory and latency / Engine SSE 内存与延迟

## Scope / 范围

The Engine HTTP relay keeps original SSE bytes for Responses, Chat Completions,
Messages, and image streams. The WebSocket bridge receives the same parsed data
view and preserves its existing JSON event adaptation. This change does not alter
Engine confirmed-events settlement, ordinary billing, or upstream retries.

Engine 的 Responses、Chat Completions、Messages 和图片流继续转发原始 SSE
字节。WebSocket 桥复用相同的 data 视图，保持现有 JSON 事件适配。此改动不调整
Engine confirmed-events 结算、普通计费或上游重试。

## Buffer ownership / 缓冲所有权

- Each stream has exactly two frame leases. A lease contains one raw frame and
  an optional multiline-data scratch buffer. The producer must obtain a lease
  before reading the next frame.
- Single `data:` fields borrow a range of the raw frame. A second `data:` field
  starts scratch assembly. Integer offsets survive raw buffer growth.
- The consumer observes the frame, writes it, and flushes before returning the
  lease. The WebSocket writer finishes its synchronous write before returning.
- Individual raw/scratch capacities above 64 KiB are released on return. Smaller
  buffers are reused within that stream. No cross-request frame pool is added.
- Shutdown closes the upstream body before joining the producer, drains queued
  leases, and releases both buffer gauges. Terminal events finish without waiting
  for an upstream EOF. Cancellation stops inference immediately.
- Complete upstream lines refresh idle activity even before their event closes.
  Semantic first-output timing still depends on the observed event.

- 每条流固定两个帧租约。每个租约包含一份原始帧缓冲和可选的多行 data 拼接缓冲；
  读取下一帧前必须取得租约。
- 单个 `data:` 字段直接借用原始帧中的区间，第二个 `data:` 字段才开始拼接。
  使用整数偏移，因此原始缓冲扩容不会留下失效切片。
- 观察、写出、flush 完成后才归还租约。WS 写函数同步返回后才可复用。
- raw/scratch 各自容量超过 64 KiB 时在归还时释放；较小容量只在当前流内复用。
  不新增跨请求帧池。
- 关闭时先关闭上游 Body 解阻塞，再等待读取协程退出、排空待消费租约并释放两个
  gauge。终帧不等待 EOF；取消仍立即停止执行。
- 每个完整上游行都会更新 idle 活跃时间，包括尚未闭合的事件；首个语义输出计时
  继续由事件判断。

## Parsing and protocol checks / 解析与协议检查

Image candidates share one validated JSON root across image accounting and
receipt-finality observation. Text deltas skip image materialization. Retained
image IDs and sizes own their strings, so they do not retain a large image JSON
string. Image result hashing and accounting rules are unchanged. Non-Engine
image accounting retains its original path.

图片候选帧在图片观察与 receipt finality 之间共享一个已验证 JSON root；普通文本
delta 跳过图片物化。需要跨帧保存的图片 ID 和尺寸拥有独立字符串，避免保留整个
大图 JSON。图片结果哈希与计数规则保持一致，非 Engine 图片观察沿用原路径。

Regression coverage includes CRLF, comments, unknown fields, empty and multiline
data, arbitrary input fragmentation, EOF tails, exact/overflow limits, borrowed
WebSocket views, two-lease immutability, large-buffer release, slow multiline
activity, authoritative zero usage, and image-accounting equivalence. Existing
Engine suites cover terminal/error events, partial usage, disconnects, write and
flush failures, and async-image task context.

回归覆盖 CRLF、注释、未知字段、空/多行 data、任意网络分块、EOF 尾帧、上限边界、
WS 借用视图、两个租约的不可变性、大缓冲释放、慢多行活跃、权威零 usage 和图片
观察等价性。既有 Engine 测试继续覆盖终帧、错误、部分 usage、断连、写/flush
失败与异步图片 task context。

```sh
# Run from backend. / 在 backend 目录运行。
go test ./internal/service -run '^TestCodexEngine' -count=1
go test -race ./internal/service -run '^TestCodexEngine' -count=1
```

## Reproducible measurements / 可复现测量

`BenchmarkCodexEngineRelay` uses synthetic fixtures and a discard writer. The
same fixture runs against the baseline and candidate: HTTP/WS small 256-frame
streams, 128 multiline frames, 32 slow writes, and 1/8/32 MiB image terminal
frames. It reports `ns/op`, `B/op`, `allocs/op`, throughput, first-write latency,
write duration, and a bounded-sample latency percentile. Fixture creation and
sample sorting occur outside the timed region.

`BenchmarkCodexEngineRelay` 使用合成数据与丢弃输出的 writer。基线和候选使用完全
相同的 fixture：HTTP/WS 的 256 个小帧、128 个多行帧、32 次慢写，以及 1/8/32 MiB
图片终帧。输出 `ns/op`、`B/op`、`allocs/op`、吞吐、首次写延迟、写耗时和固定
样本数量的延迟分位。fixture 构造和样本排序位于计时区间外。

Use the same Go toolchain, GOEXPERIMENT, GOMAXPROCS, benchmark duration, CPU
environment, and fixtures for both trees. The recorded baseline uses Go 1.27.3
with `GOEXPERIMENT=greenteagc,jsonv2`. Run performance rounds without concurrent
builds/tests and retain raw output from three rounds before comparing medians.

两侧必须使用相同 Go 工具链、GOEXPERIMENT、GOMAXPROCS、基准时长、CPU 环境和
fixture。已保存基线使用 Go 1.27.3 与 `GOEXPERIMENT=greenteagc,jsonv2`。性能轮次
期间停止并行编译/测试，保存三轮原始输出后比较中位数。

```sh
GOEXPERIMENT=greenteagc,jsonv2 go test ./internal/service \
  -run '^$' -bench '^BenchmarkCodexEngineRelay$' \
  -benchmem -benchtime=1x -count=3
```

The final comparison and machine/runtime resource measurements are recorded by
the coordinated benchmark run. Active SSE buffer count/bytes represent the raw
and scratch capacities currently owned by stream leases. Frame-size histograms
record those capacities once per consumed frame. RSS, heap profiles, and GC
measurements additionally capture parser temporaries and unreclaimed allocations.

最终前后对照及机器/运行时资源数据由统一基准轮次补充。active SSE buffer 数量和
字节表示当前流租约持有的 raw/scratch 容量；每消费一帧记录一次容量直方图。
RSS、堆 profile 和 GC 数据同时覆盖解析临时值及尚未回收的分配。
