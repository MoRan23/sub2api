# Engine TTL 契约与并行归因验证 / Engine TTL and parallel attribution

## 实现范围

本次 Sub2API 改动基于 `9c88bba22`。Engine 由独立的 `codex engine 1` 会话实现；本仓库未修改 Engine。两边仅提交推送，不部署，不调用生产模型或修改线上配置。

- `confirmed-events` 支持真实的完整 30m/12h 缓存写入分桶。旧事件和缺少可靠明细的事件继续聚合计价；缺省与零保持区别。事件分桶之和必须等于聚合值，混合来源摘要的已知分桶之和可小于聚合值。
- Sub2 使用现有 TTL 定价、账号成本与使用记录列；不新增数据库列，不新增价格目录副本，不改账本及完整交付门禁。
- 每轮三个归因探针并行，固定三个结果槽及有界通道。协调者独占任务状态，成功回答保留原索引；取消、终态失败和 panic 先取消并收拢同轮请求，再结束任务。
- 私有检查点 v2 支持乱序成功和逐探针 503 到期时间；v1 前缀结果平滑转换。普通重试仍每探针最多两次，共享非 503 十分钟累计预算。明确 503 的耗时及等待不扣预算；恢复只补到期未成功探针。
- Engine 自身归因仅改变并发方式，保留它原有失败即终结规则，不引入 Sub2 的 503 重试。

## 正确性验证

WSL Ubuntu 24.04、Go 1.27.3、`GOEXPERIMENT=greenteagc,jsonv2`，使用模拟上游和 Docker 隔离 PostgreSQL 18.1 / Redis 8.4。

| 检查 | 结果 |
| --- | --- |
| Engine TTL 服务定向测试 | 通过；合法/非法字段、显式零、applicable、摘要、当前/旧快照定价、交付门禁 |
| 新增 TTL 数据库测试 | 3 个顶层测试及 2 个交付子用例通过，无跳过；JSONB 恢复、TTL 列、金额、去重、同 ID 改桶冲突 |
| Engine 计费与归因检查点集成回归 | 通过，9.035s；使用 `CI=true`，数据库不可用时会报错 |
| 服务合并范围 race | 通过，3.178s；包含归因与 Engine 计费相关测试 |
| 并行专门测试 | 通过；屏障验证同时在途、乱序结果、单/多 503 恢复、普通预算、429、取消、授权变化、工作协程及协调器 panic |
| 后端 `go build ./...` | 通过 |
| 前端归因及 i18n | 4 个文件、98 个测试通过 |
| 定向 ESLint / 类型检查 / 生产构建 | 通过；类型检查由 `vue-tsc -b` 执行 |

前端构建保留已有 Browserslist 数据陈旧、混合静态/动态导入与大 chunk 提示，未升级依赖。仓库远端 Actions 按既有状态关闭，本轮不以远端 CI 作为通过依据。

复现命令（在 backend 下）：

```sh
export GOEXPERIMENT=greenteagc,jsonv2
go test -race ./internal/service -run 'TestAttribution|TestCodexEngineBilling|TestCodexEngineFrozenPricing|TestCodexEngineCurrentPricing' -count=1 -timeout=240s
CI=true go test -tags integration ./internal/repository -run 'TestAttributionParallelCheckpointPreservesOutOfOrderResults|TestAttribution.*Retry|TestAttribution.*Checkpoint|TestCodexEngineBilling' -count=1 -timeout=300s
go build ./...
go test ./internal/service -run '^$' -bench '^BenchmarkCodexEngineBillingUsageEvent$' -benchmem -count=3
```

## 分配量

协议原实现为每个 `applicable_usage` 字段构造临时 JSON 再解码，只为校验字段名。本次改为共享纯 switch 校验，不创建这些 JSON 或白名单 map。以下比较本次 TTL 接线完成后、该局部优化前后的同一夹具，均为三轮中位数；不是整站性能压测。

| 事件 | 优化前 B/op | 优化后 B/op | allocs/op 前 → 后 | ns/op 前 → 后 |
| --- | ---: | ---: | ---: | ---: |
| 聚合用量 | 12,176 | 5,762 | 214 → 143 | 18,684 → 11,898 |
| 完整 TTL | 16,548 | 7,024 | 272 → 164 | 26,652 → 16,075 |

归因工作协程仅持有本次挑战字符串和执行结果；字符串共享只读数据，协调者按索引保存回答，不深复制三份完整任务。持久化仍在成功或 503 进度改变时写检查点，未新增周期性正文复制。

原始记录见 [performance/engine-attribution-20261112](performance/engine-attribution-20261112/)。

## 升级和回退

先部署支持新增字段的 Sub2API，再切换可能发送 TTL 字段的 Engine；旧消费者会拒绝未知计量字段。归因检查点需排空旧执行器后统一升级；回退前完成或取消 v2 活动任务，旧历史保持不动。本次不执行部署。

## English summary

Confirmed billing accepts complete, source-backed 30m/12h cache-write buckets while retaining aggregate-only fallback. Presence is preserved, summaries may mix detailed and aggregate-only events, and immutable event IDs cannot be amended later. Existing current/legacy pricing paths and persisted TTL columns are reused; delivery gates and disconnected-request no-charge behavior remain intact.

Each attribution round now has at most three concurrent probes. One coordinator owns state, records outcomes by challenge index, drains workers before finalization and resumes only due unfinished probes. Checkpoint v2 preserves individual retries and successful answers, with v1 conversion. Engine retains its own existing terminal-failure policy. Tests used mocked models and isolated databases; no production calls or deployment occurred.

The parser's name validation avoids temporary JSON round trips, reducing median allocations from 12,176 to 5,762 B/op for aggregate events and from 16,548 to 7,024 B/op for TTL events. Raw measurements, database results and frontend build output accompany this report.
