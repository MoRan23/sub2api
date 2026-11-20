# Codex 0.171 性能验证

本次新增 metadata 预算检查在 8096 字节以内为零分配；请求规范化和 WS 比较没有增加与正文大小成比例的分配。启用指纹观测时，新增字节数记录固定增加约 **56 B、2 次分配**。

默认链接的两份测试二进制在 8 MiB 场景出现可重复的耗时差异。CPU profile 将差异定位到未修改的 `gjson.parseSquash` 字符串扫描循环；统一函数对齐后，规范化差异为 **−0.11%**，指纹观测为 **+3.27%**。下面同时保留原始结果、控制实验和诊断结果，不将早期未固定 CPU 的大幅提速归为功能收益。

## 环境与方法

- 时间：2026-11-20。
- 基线：`3b3554c8b`，通过 `git archive` 导出到独立临时目录。
- 当前：本次 0.171 适配工作树；未修改基线源码。
- Windows/amd64，AMD Ryzen 7 9700X，Go 1.27.4，`GOEXPERIMENT=greenteagc,jsonv2`。
- 普通测试使用相同工具链；固定 CPU 的对照使用提前编译的 `-tags=unit` 测试二进制、逻辑 CPU mask `0x10`、`GOMAXPROCS=1`。baseline/current 交替运行，各三轮，表格取中位数。
- 模拟正文及 metadata，不访问真实模型。二进制、CPU profile 和临时 overlay benchmark 留在临时目录，仓库仅保存小型日志。

## 固定 CPU 的默认链接结果

`-test.benchtime=300ms`，双方各三轮。时间单位为 ns/op。

| 场景 | 基线 | 当前 | 耗时变化 | B/op 基线→当前 | allocs/op 基线→当前 |
|---|---:|---:|---:|---:|---:|
| normalize 64 KiB | 46,992 | 41,998 | −10.63% | 9,705→9,705 | 165→165 |
| fingerprint 64 KiB | 45,562 | 49,063 | +7.68% | 8,272→8,328 | 154→156 |
| normalize 1 MiB | 530,700 | 520,901 | −1.85% | 9,705→9,705 | 165→165 |
| fingerprint 1 MiB | 485,551 | 492,879 | +1.51% | 8,272→8,328 | 154→156 |
| normalize 8 MiB | 3,400,472 | 3,864,002 | +13.63% | 9,764→9,765 | 165→165 |
| fingerprint 8 MiB | 3,446,166 | 3,932,991 | +14.13% | 8,273→8,329 | 154→156 |
| WS 512 B，普通字符串 | 3,531 | 3,859 | +9.29% | 2,232→2,232 | 38→38 |
| WS 512 B，转义字符串 | 3,717 | 3,933 | +5.81% | 2,232→2,232 | 38→38 |
| WS 64 KiB，普通字符串 | 48,336 | 45,332 | −6.21% | 2,232→2,232 | 38→38 |
| WS 64 KiB，转义字符串 | 83,244 | 73,527 | −11.67% | 2,232→2,232 | 38→38 |
| WS 1 MiB，普通字符串 | 728,381 | 842,025 | +15.60% | 2,232→2,233 | 38→38 |
| WS 1 MiB，转义字符串 | 1,118,350 | 1,146,136 | +2.48% | 2,233→2,233 | 38→38 |
| WS 16 MiB，普通字符串 | 12,346,816 | 10,615,169 | −14.03% | 2,245→2,244 | 38→38 |
| WS 16 MiB，转义字符串 | 19,061,750 | 16,776,790 | −11.99% | 2,259→2,254 | 38→38 |

WS 使用 `BenchmarkForwardingWSComparable/.../legacy=false`。其中部分耗时差异随轮次变号；8 MiB 的规范化与指纹路径连续三轮变慢，因此继续进行了以下定位。

原始日志：[基线 1](affinity-baseline-1.log)、[2](affinity-baseline-2.log)、[3](affinity-baseline-3.log)；[当前 1](affinity-current-1.log)、[2](affinity-current-2.log)、[3](affinity-current-3.log)。

## 8 MiB 耗时差异定位

### 延长采样与 CPU profile

反转执行顺序，current/baseline 交替，各三轮、每项 1 秒，仍测得以下差异：

| 场景 | 基线 ns/op | 当前 ns/op | 变化 |
|---|---:|---:|---:|
| normalize 8 MiB | 4,066,523 | 4,575,769 | +12.52% |
| fingerprint 8 MiB | 3,990,651 | 4,663,516 | +16.86% |

随后对 normalize 8 MiB 各采集 3 秒 CPU profile。两端约 95% 的采样落在既有 `encoding/json.Valid` 与 `gjson.parseSquash`。按执行次数粗略折算：`json.Valid` 约 2.509→2.486 ms/op，`parseSquash` 约 0.718→1.098 ms/op。新增预算函数未成为可见热点。

`objdump` 对照发现 `parseSquash` 热循环的机器指令及相对跳转形状相同，函数起址由基线 `0x140664ec0`（64 字节对齐）变为当前 `0x14067fd60`（相对 64 字节边界偏移 32 字节）。新增实现没有增加业务正文扫描。

日志：[反序基线 1](confirmation-baseline-1.log)、[2](confirmation-baseline-2.log)、[3](confirmation-baseline-3.log)；[反序当前 1](confirmation-current-1.log)、[2](confirmation-current-2.log)、[3](confirmation-current-3.log)；[profile 摘要](profile-summary.log)、[基线采样](profile-baseline.log)、[当前采样](profile-current.log)。

### 同一二进制的预算包裹控制

通过临时 Go overlay 在同一当前二进制中比较 `marshalNestedJSON(true)` 与 `MarshalNestedJSON(true)`，不改仓库生产源码。metadata 为几十字节，分别测试纯序列化和包含 8 MiB 正文提取的路径；各三轮、1 秒。

| 场景 | 无预算包裹 ns/op | 有预算包裹 ns/op | 分配 |
|---|---:|---:|---|
| 仅 metadata 序列化 | 1,953 | 2,028 | 双方 2,480 B、31 allocs |
| 提取 8 MiB 正文后序列化 | 3,936,209 | 4,237,473 | 双方中位 9,704 B、165 allocs |

纯包裹的中位差异为 75 ns（+3.84%）。完整路径仍存在测量波动，有预算包裹的三次结果为 4.237／4.035／5.016 ms；新增处理的范围仍仅为提取后的 metadata。[原始结果](wrapper-comparison.log)

### 统一函数对齐的诊断

最后仅对临时测试二进制增加 `-ldflags=-funcalign=64`，双方重新链接，继续固定同一 CPU，8 MiB 各三轮、每项 1 秒：

| 场景 | 基线 ns/op | 当前 ns/op | 变化 | allocs/op 基线→当前 |
|---|---:|---:|---:|---:|
| normalize 8 MiB | 4,566,486 | 4,561,366 | −0.11% | 165→165 |
| fingerprint 8 MiB | 5,139,547 | 5,307,690 | +3.27% | 154→156 |

原先超过 10% 的差异在统一对齐后消失，支持默认测试二进制链接布局影响扫描循环的定位。生产构建参数保持原样；没有加入 padding，也没有修改 JSON 库。跨二进制耗时具有本机布局和运行状态的限制，因此报告同时保留默认构建结果，并以稳定的分配变化和控制实验说明本次实现开销。

日志：[对齐基线 1](aligned-baseline-1.log)、[2](aligned-baseline-2.log)、[3](aligned-baseline-3.log)；[对齐当前 1](aligned-current-1.log)、[2](aligned-current-2.log)、[3](aligned-current-3.log)。

## 新增预算与压缩的独立开销

以下为新增功能自身的三轮中位数，使用默认测试进程设置，未做跨版本耗时比较。

| metadata fixture 的 remote 字符数 | ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| 200，完整 metadata 在预算内 | 1.450 | 0 | 0 |
| 8,000，封装后超过预算 | 62,405 | 85,539 | 66 |
| 32,000，超过预算 | 127,918 | 172,489 | 67 |

这是 metadata 字符串的处理成本；没有复制业务正文。只有超预算且包含可裁剪 remote 时才进入解析、排序和重建。[原始结果](current-unpinned.log)

| 遥测 fixture | 原始 JSON 大小 | 压缩后占比 | ns/op | B/op | allocs/op |
|---|---:|---:|---:|---:|---:|
| 1 个 metrics batch | 35,660 B | 8.071% | 77,670 | 9,880 | 6 |
| 8 个 metrics batch | 285,133 B | 1.719% | 78,696 | 12,568 | 5 |

zstd 编码器由串行 worker 复用，压缩输出仅存活于本次发送；已封批 JSON 保持原字节。仅 metrics 压缩，analytics 继续发送 JSON。[压缩原始结果](telemetry-unit-compression-benchmark.log)

## 复现命令与日志范围

在基线导出目录及当前目录分别执行：

```sh
GOEXPERIMENT=greenteagc,jsonv2 go test -tags=unit -c -o /absolute/path/service.test.exe ./internal/service
```

由启动器对自身调用 Windows `SetProcessAffinityMask(..., 0x10)`，随后启动的子进程继承该亲和设置；对子进程显式设置 `GOMAXPROCS=1`。分别运行以下两个过滤器，并按 baseline/current 顺序交替三轮：

```sh
service.test.exe -test.run='^$' -test.bench='^BenchmarkCodex0169Compatibility$' -test.benchmem -test.count=1 -test.benchtime=300ms
service.test.exe -test.run='^$' -test.bench='^BenchmarkForwardingWSComparable$/././legacy=false$' -test.benchmem -test.count=1 -test.benchtime=300ms
```

8 MiB 确认使用 `-test.bench='^BenchmarkCodex0169Compatibility$/./8388608$' -test.benchtime=1s`。profile 仅选择 `normalize/8388608`，使用 `-test.benchtime=3s -test.cpuprofile=/absolute/path/cpu.pprof`。对齐诊断只在编译命令临时增加 `-ldflags=-funcalign=64`，其余条件相同。

同二进制控制的临时 benchmark 在同一 fixture 中调用以下两种入口，各三轮 `-test.benchtime=1s`：

```go
profile.marshalNestedJSON(true) // existing serializer
profile.MarshalNestedJSON(true) // serializer + metadata budget
```

最初未固定 CPU 的三轮日志也予以保留：[基线规范化与观测](baseline-normalization-fingerprint.log)、[基线 WS](baseline-ws-comparison.log)、[当前](current-unpinned.log)、[紧邻基线复测](baseline-unpinned-recheck.log)。这部分耗时变化不用于宣称提速。

English summary: the below-budget metadata path allocates nothing. Normalization and WS comparison retain their allocation counts; enabled fingerprint observation adds about 56 bytes and two allocations, independent of request-body size. The default Windows test binaries showed an 8 MiB timing regression concentrated in an unchanged scanner. Equalizing function alignment reduced it to −0.11% for normalization and +3.27% for observation. Alignment was a temporary diagnostic only; production build flags were not changed.
