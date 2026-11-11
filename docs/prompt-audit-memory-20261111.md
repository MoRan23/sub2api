# 提示词审计内存优化 / Prompt audit memory optimization

本次在 `f823a9cec` 后仅改变本地审核数据的持有与生成方式。关键词顺序、组合规则、Guard、分组范围、精确 Key 白名单、证据、摘要脱敏和事件保留规则保持原样。

## 实现

- 内置审核器共享请求内的只读提取结果。JSON 解析后仅持有所需文本段，不保留原请求正文或图片数据；第三方 `PromptEngine` 仍使用隔离副本。
- 关键词与组合未命中时不生成事件 hash、全量审计文本和脱敏摘要；进入 Guard 或保存拦截事件时才生成。
- 异步入队在返回前取得文本所有权，后台任务只持有提取文本和身份字段。未包含当前分组或没有启用端点时，保持跳过行为且不解析正文。
- 截取短文本按 UTF-8 边界定位，不将全文展开为 `[]rune`；保留非法 UTF-8 的既有转换行为。
- 脱敏仍对全文按原正则顺序检查；未匹配时复用原文，匹配时从首个已确认位置执行原替换规则，避免每条规则都复制全文。

组合组间共享解析在此前提交已实现；本次扩大到关键词、组合及 Guard 之间。HTTP 审核原本位于重试循环前，WS 也已有同轮正文去重。本次不改变 429 重试窗口、调度、正文改写、计费或 Go 内存参数。

## 本地验证

固定工具链 Go 1.27.3、`GOEXPERIMENT=greenteagc,jsonv2`，Windows amd64，Ryzen 7 9700X，默认 GOMAXPROCS 16。64 KiB、1/4/8 MiB 文本与图片请求，关闭/关键词/组合/完整快照共 32 个场景各三轮，`benchtime=100ms`。

8 MiB 场景分配中位数：

| 场景 | 优化前 B/op | 优化后 B/op | 降幅 |
|---|---:|---:|---:|
| 文本，审核关闭 | 8,396,992 | 896 | 99.99% |
| 文本，关键词未命中 | 210,032,576 | 33,579,776 | 84.0% |
| 文本，8 个组合组未命中 | 218,474,360 | 33,606,040 | 84.6% |
| 文本，完整事件快照 | 193,238,608 | 42,062,488 | 78.2% |
| 图片，组合未命中 | 25,202,433 | 8,402,404 | 66.7% |

顺序运行的耗时受同时段宿主负载影响；补充同进程旧→新→新→旧的配对脱敏基准，三轮新/旧分别为 837/884、956/944、862/941 ms，每轮包含两次各实现的调用。配对中位耗时下降 8.4%，最大单轮变化为 +1.2%。原始数据和全部场景见 `performance/prompt-audit-20261111/` 的 `baseline.txt`、`optimized.txt`、`paired-redaction.txt` 与 `comparison.md`。

最终 `optimized.txt` 对全部 32 个场景重新运行三轮，快照路径的 8 MiB 耗时中位数为 953.9 ms，原基线为 1,127.7 ms。正文提取和摘要属于一次审核成本；不把这些数字乘以网关 429 重试次数。

顺序基准中 1/4 MiB 快照曾出现约 12–13% 的耗时增长，因此又单独比较完整快照：同一进程、同一冻结正文，旧→新→新→旧交错，每轮各实现调用六次，共三轮；输入构造、结果等价验证、主动 GC 和堆采样均不在计时区间内。原快照参照包含原版全文脱敏和 `[]rune` 截断。各轮均未出现新实现变慢：

| 正文 | 第 1 轮旧→新 | 第 2 轮旧→新 | 第 3 轮旧→新 | 中位耗时变化 |
|---|---:|---:|---:|---:|
| 1 MiB | 131.95→125.00 ms | 168.44→159.56 ms | 121.83→121.42 ms | -5.3% |
| 4 MiB | 463.00→451.99 ms | 468.49→440.38 ms | 472.24→461.41 ms | -3.5% |

原始记录为 `paired-snapshot.txt`。完整快照字段在计时前与旧实现比较一致。该补测解决顺序测量中的宿主负载差异，没有据此更改生产实现。

### 存活堆、峰值与释放

使用相同 8 MiB 输入构造和固定 GOMAXPROCS，在同一测试进程内执行旧实现参照与最终实现各三轮；分别保留 1、5、8 个相同结果引用，覆盖重试期间重复引用同一不可变结果。旧参照仅在测试中执行。每毫秒采样 Go `HeapAlloc`；基线、持有和释放测量点各执行两次测试 GC，以清除 `sync.Pool` 的上一代缓存。生产路径不调用 GC。

| 场景 | 引用数 | 旧实现峰值 / 新实现峰值 | 旧实现存活 / 新实现存活 |
|---|---:|---:|---:|
| 文本完整快照 | 1 | 92,445,776 / 33,669,152 B | 8,468,192 / 8,462,888 B |
| 文本完整快照 | 5 | 92,442,296 / 33,670,896 B | 8,464,456 / 8,464,504 B |
| 文本完整快照 | 8 | 92,442,584 / 33,668,544 B | 8,465,224 / 8,465,112 B |
| 图片异步交接 | 1 | 33,600,528 / 33,612,768 B | 8,397,768 / 1,376 B |
| 图片异步交接 | 5 | 41,997,744 / 33,613,696 B | 8,398,472 / 2,192 B |
| 图片异步交接 | 8 | 41,998,240 / 33,614,336 B | 8,398,968 / 2,720 B |

各值为三轮中位数，包含双方相同的合成输入构造。文本快照仍需持有约 8 MiB 的扫描文本；收益在峰值及临时分配。图片任务只有短 `role` 和文本需要继续使用，后台任务不再持有 8 MiB 原正文。全部 36 个测量点释放后相对基线残留最多 6,584 B，1/5/8 个引用不会将正文驻留倍增。

独立所有权实验核对了短 `role`/文本的行为：JSON 解码后已具有独立存储，三轮持有量 880–1,072 B；额外 `strings.Clone` 为 880–896 B。因此没有在实现中新增大文本 Clone。必要的解码分配仍保留，原始字节可在解析后覆盖的回归测试也通过。单次 GC 会把 JSON 库缓存误计入活引用，测量记录明确采用两次 GC，并区分缓存与请求所有权。

原始 36 场景和六次所有权记录见 `heap-retention.txt`，中位数见 `heap-comparison.md`。这是受控的 Go 堆测量；进程峰值 RSS、完整网关 p95 和生产并发压测不在这次补充记录内。

### 行为与集成验证

完整 securityaudit 单测和 race 通过。隔离 PostgreSQL 18.1 / Redis 8.4 的完整包测试也通过，原始逐测试结果保存在 `integration.jsonl`；最终实现的隔离数据库全包 race 再次通过，用时 25.441s，记录为 `integration-race.txt`。新增对照覆盖 Unicode/非法 UTF-8、各协议全文/最新轮快照、组合证据偏移、并发共享、原缓冲覆盖、异步队列和配置跳过。额外 race 验证不适用分组与无端点输入不进行解析。

现有行为夹具直接复用，避免重复构造相同测试：

- `TestPreparedPromptServiceReusesKeywordInputForGuard`：关键词/组合未命中后同步 Guard 使用同一提取结果，覆盖原正文覆盖后继续处理。
- `TestPreparedPromptAsyncEnqueueOwnsTextBeforeReturning`：阻塞异步交接，返回后覆盖原正文、修改分组，解阻后的事件与 payload 仍准确。
- `TestPreparedPromptTailMatchAndRetryReferences`：大文本末尾命中，清空原正文后 1/5/8 次读取同一结果仍一致。
- `TestEnqueuerStagingPayloadPublishProtocolAndFailureCleanup`：队列满、准入忙、数据库失败及发布失败的清理行为。
- `TestGuardEvaluatorFlagSharedDeadlineFailClosedAndContextCancel`：共享截止时间和父 context 取消；`TestWorkerPanicLeaseLossAndLifecycleAreContained` 验证停止、panic 和租约丢失。
- `TestPromptAuditSyntheticAsyncBaseline`：既有 100 个合成异步 Guard 请求流程。

复现：在 `backend` 中运行：

```bash
GOEXPERIMENT=greenteagc,jsonv2 go test ./internal/securityaudit -run '^$' -bench '^BenchmarkPromptAuditMemory$' -benchtime=100ms -count=3 -benchmem
GOEXPERIMENT=greenteagc,jsonv2 go test ./internal/securityaudit -run '^$' -bench '^BenchmarkPromptRedactionPaired$' -benchtime=1x -count=3 -benchmem
GOEXPERIMENT=greenteagc,jsonv2 go test ./internal/securityaudit -run '^$' -bench '^BenchmarkPromptSnapshotPaired$' -benchtime=3x -count=3 -benchmem
GOEXPERIMENT=greenteagc,jsonv2 PROMPT_AUDIT_HEAP_MEASURE=1 go test ./internal/securityaudit -run '^TestPrompt(AuditHeapRetention|DecodedTextOwnershipHeap)$' -count=1 -v
GOEXPERIMENT=greenteagc,jsonv2 go test -race ./internal/securityaudit
```

无需数据库迁移，回退应用版本即可。

## English

The built-in auditor shares request-local extracted text across keyword, combination and Guard stages. It retains neither the raw request nor decoded image fields after extraction. Event metadata is lazy; asynchronous work owns extracted text before returning to the handler. Out-of-scope groups and missing endpoints skip decoding. External PromptEngine implementations keep their defensive-copy contract.

UTF-8 truncation preserves the previous invalid-byte behavior. Whole-input redaction keeps the same expressions and order, while avoiding whole-string copies on misses. Audit matching, response evidence, retention, queue bounds and retry semantics are unchanged.

The final 32-scenario benchmark was repeated three times after implementation was settled. Fixed 8 MiB text fixtures reduce keyword/combination allocations by 84.0%/84.6%, and full-event snapshot allocations by 78.2%. Raw three-run records and medians are retained alongside this document. Paired redaction timings account for host-load variation.

The sequential 1/4 MiB snapshot results showed apparent 12–13% timing regressions, so complete old/new snapshots were additionally measured in alternating order on the same input. Three rounds of six calls per implementation exclude fixture construction, equality checks, explicit GC and heap sampling from the timed interval. Every round was faster for the optimized implementation; median time improves 5.3% at 1 MiB and 3.5% at 4 MiB. `paired-snapshot.txt` retains the raw measurements. The old reference includes the original whole-string redaction and rune-array truncation, and complete snapshot equality is checked before timing.

The controlled heap fixture samples HeapAlloc every millisecond and performs two test-only GCs at each ownership boundary to expire sync.Pool victim caches. It includes identical input construction and holds 1/5/8 shared references. Text-snapshot sampled peak falls from about 92.44 MB to 33.67 MB; necessary live scan text remains about 8.46 MB. An 8 MiB image async handoff holds only 1.3–2.7 KB in the optimized median cases, versus 8.40 MB previously. All 36 measurements release the payload after request completion; the largest residual over baseline is 6,584 B. Short role/text ownership checks show that decoded strings already have independent storage, so no redundant Clone is added. The explicit GC calls exist only in the opt-in measurement fixture.

These are audit-local HeapAlloc measurements, not OS RSS or gateway retry benchmarks. Existing guarded/cancelled sync and async flows, queue failures and 1/5/8 immutable-reference fixtures verify lifecycle behavior. Full package unit/race, ownership/differential regressions, and isolated PostgreSQL/Redis integration pass; the final isolated full-package race run completed in 25.441s (`integration-race.txt`). No production benchmark, migration, deployment or runtime GC change was performed.
