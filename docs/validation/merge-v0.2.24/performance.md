# v0.2.24 合并性能验证

基线 `f987b4d0e` 与合并 `328db26f4` 后的工作树使用同一批请求样本。共完成 **76 个双边子例与 8 个新增重复字段检查子例**，每个运行三轮。初轮超过 5% 的四项再交替运行五轮，确认复测未出现超过 5% 的耗时回退。

## 环境与方法

- Ubuntu 24.04 / WSL，Go 1.27.4，`GOEXPERIMENT=greenteagc,jsonv2`、`GOTOOLCHAIN=local`；两侧均以 `-tags=unit` 构建测试二进制。
- 固定 `GOMAXPROCS=2`，无并行构建、race 或集成测试；每轮按 baseline → merged 串行执行。
- 初轮 `-test.benchtime=300ms -test.count=1 -test.benchmem`，三轮取中位数；确认复测为 `1s`、五轮。
- 审核与重复字段检查覆盖 64 KiB / 1 / 4 / 8 MiB，解析覆盖 4 / 8 / 16 / 32 MiB，schema、namespace、时区及转换覆盖 1 / 8 MiB。时区重试为 1 / 5 / 8 次，Codex 转换另包含 1 / 5 / 15 次。
- Namespace 两侧使用同一请求 fixture；合并版本增加的 `current/` 名称层在汇总时移除。
- 网络仅使用测试进程内模拟数据和 miniredis，无真实模型请求。固定 Go/GC 参数只用于本地验证，未修改生产设置。

## 代表性结果

耗时单位 ms/op。完整 84 项结果及每轮样本见 `raw/performance-results.tsv` 和原始文本。

| 场景 | 基线 → 合并耗时 | 变化 | B/op | allocs/op |
|---|---:|---:|---:|---:|
| Anthropic 8 MiB 请求解析 | 11.243 → 4.879 | -56.61% | 440 → 440 | 4 → 4 |
| Gemini 8 MiB 请求解析 | 11.017 → 4.874 | -55.75% | 432 → 432 | 4 → 4 |
| 1 MiB 无图片意图 | 0.647 → 0.034 | -94.77% | 0 → 0 | 0 → 0 |
| 8 MiB 工具 schema，无修复 | 2.692 → 0.470 | -82.54% | 17 → 0 | 1 → 0 |
| 8 MiB 工具 schema，实际修复 | 11.848 → 9.663 | -18.44% | 16,794,118 → 16,794,123 | 13 → 12 |
| 8 MiB namespace，无变更 | 3.845 → 3.696 | -3.87% | 0 → 0 | 0 → 0 |
| 8 MiB namespace，删除首项字段 | 6.367 → 4.733 | -25.66% | 16,793,920 → 8,396,960 | 7 → 4 |
| 8 MiB 关键词审核 | 24.899 → 24.704 | -0.78% | 33,578,271 → 33,577,687 | 78 → 76 |
| 8 MiB 组合审核 | 24.709 → 24.730 | +0.09% | 33,598,774 → 33,597,991 | 97 → 94 |
| 8 MiB 审核快照（确认复测） | 924.690 → 915.759 | -0.97% | 42,055,176 → 42,055,240 | 106 → 107 |
| 8 MiB 时区处理，8 次尝试 | 88.324 → 75.159 | -14.91% | 134,469,122 → 134,468,326 | 1378 → 1371 |
| 8 MiB Codex 转换，5 次尝试 | 58.350 → 53.891 | -7.64% | 209,739,525 → 209,741,341 | 430 → 437 |
| 2,000 账号 / 100 组共享缓存读取 | 0.162 → 0.164 | +1.19% | 50,784 → 50,784 | 64 → 64 |

请求解析减少了重复根字段扫描；无候选图片/schema 请求提前退出；namespace 实际删除路径从两份正文分配减为一份，未修改路径继续零分配。审核与共享缓存保持原有分配规模。重试样本内仍按次派生可变出站内容，入口重复字段检查不在重试循环中。

## 初轮差异与确认复测

| 场景 | 初轮中位数变化 | 五轮确认变化 | 确认耗时：基线 → 合并 ms/op |
|---|---:|---:|---:|
| BenchmarkIsImageGenerationIntent/DuplicateKeysFirstWins | +5.01% | +1.92% | 0.670 → 0.683 |
| BenchmarkPromptAuditMemory/image/1048576/combination | +9.57% | +0.62% | 0.684 → 0.688 |
| BenchmarkPromptAuditMemory/image/1048576/snapshot | +5.21% | +0.32% | 0.680 → 0.682 |
| BenchmarkPromptAuditMemory/text/8388608/snapshot | +5.67% | -0.97% | 924.690 → 915.759 |

四项初轮差异均未在更长交替采样中稳定复现，未为计时噪声改写业务实现。完整审核快照的 8 MiB 文本场景仍是已有重路径（约 0.92 秒）；本次继续保留按需生成快照，未命中本地规则不会因此增加快照构建。

## 新增入口重复字段检查

该安全校验在基线中不存在，单列成本。下列为未发现重复字段的样本，每次只扫描一次，不复制正文；末尾重复字段用例同时完成验证，结果见原始数据。

| 正文规模 | 合并中位数 μs/op | B/op | allocs/op |
|---|---:|---:|---:|
| 64 KiB | 1.159 | 144 | 5 |
| 1 MiB | 18.026 | 144 | 5 |
| 4 MiB | 72.905 | 144 | 5 |
| 8 MiB | 145.350 | 144 | 5 |

HTTP 在鉴权和白名单之后执行；WebSocket 每个新帧独立检查。Engine 两回合测试验证回调恰好执行两次；同一帧在下游或重试时复用已验证事实。独立服务入口仍自行校验 JSON，重复字段扫描本身不替代 JSON 有效性检查。

## 内存观测

共享缓存的解码后存活堆在清理 fixture 引用并执行 GC 后采样：

| 共享组数 | 基线 → 合并 decoded-retained-B |
|---|---:|
| 1 | 7,109,488 → 7,109,928 |
| 10 | 11,312,632 → 11,312,424 |
| 100 | 47,903,064 → 47,902,584 |

每类进程峰值 RSS 取三轮中位数，包含测试 fixture、运行时与整类基准循环：

| 类别 | 基线 → 合并峰值 RSS（KiB） |
|---|---:|
| parser | 166,340 → 164,488 |
| image | 73,376 → 71,132 |
| schema | 189,224 → 195,240 |
| namespace | 198,608 → 203,468 |
| audit | 137,572 → 138,004 |
| timezone | 260,852 → 260,644 |
| transform | 158,664 → 157,668 |
| sharedcache | 403,472 → 410,228 |

## English summary

The same Go 1.27.4 toolchain and fixtures were used for 76 paired cases and eight new ingress-check cases. Three serial rounds were followed by five longer alternating rounds for the four initial timing differences above 5%; none remained above 5% in confirmation. Large request parsing improved by roughly 53–58%, no-image classification by 95%, and schema no-op scans by 83–85%. Namespace mutation now allocates one body instead of two. Audit snapshots, shared-cache ownership, and retry semantics remain intact. Raw measurements include timing, allocations, retained decoded heap, and process peak RSS.
