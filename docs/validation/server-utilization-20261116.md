# 服务端资源占用修复 / Server utilization fixes

基线：`a2a1ac1fa`，日期：2026-11-16。此次处理 OAuth 429 重试窗口被并发成功请求延长，以及遥测容量检查重复读取历史 JSON 两个问题。

## 现场依据

只读排查期间，PostgreSQL 占约 2.3–3.1 个 CPU 核心，应用占约 1.3–1.5 个核心。15 分钟应用平均分配速率约 376.8 MiB/s，发生 656 次 GC。账单队列未积压。

- 一段 15 分钟日志包含 6,669 次同账号重试，其中 6,630 次为 429。最长完整序列持续 650.5 秒，重试 72 次。
- 同账号其他请求成功时会清除服务层的限流起点；下一次 429 创建新窗口，原请求随之获得新的截止时间。
- 遥测容量统计和格式检查反复扫描同一池的 `turn/attempt/sampling` JSON。现场一个 4,096 条记录池的两次查询分别约 134ms 和 60ms。

## 改动

### 请求级重试上限

HTTP 请求及 WS 当前推理轮保存各账号首次取得的重试截止时间，后续只接受更早的截止时间。账号健康恢复不再续期该请求。`Retry-After` 等待同时受截止时间约束，醒来后再次检查，避免过期后多发一次请求。

覆盖 Responses、Chat/Messages、图片、搜索、Grok 媒体及 WS。WS 收到下一轮真实请求时开启新预算；同一失败轮的重放继续使用旧预算。普通重试次数、错误专用次数上限、两分钟 OAuth 窗口、账号切换和取消规则保持原有含义。新增状态仅保存账号 ID 和时间，首次出现带截止时间的错误才分配，不复制正文、凭据或请求头。

### 遥测容量与格式检查

新增数据库生成列 `runtime_slots`：每个运行态记录贡献一个槽位，活跃 turn 的 inflight 项另计；聚合所依赖字段格式异常时保存负值，查询仍拒绝损坏状态。非运行态标记不进入容量索引。被实际读取的记录继续做完整 Go 类型校验。

容量查询仅汇总小型数值，不再反复解压、解析历史 profile JSON。单次池事务复用容量与校验结果；运行态写入使结果失效，删除已完成记录直接调整计数。缓存仅在已锁定池的事务内存在，不跨实例或跨事务共享。数据库生成列自动跟随旧版写入和事务回滚，无需后台重建任务。

## 升级与回退

- `288_codex_telemetry_runtime_slots.sql` 添加函数和存储生成列，并计算现有记录。首次升级会重写该表，持有表级锁，应为启动迁移预留时间。
- `289_codex_telemetry_runtime_slots_notx.sql` 使用现有非事务迁移机制并发创建覆盖索引，沿用失败后恢复机制。
- 重复执行迁移、旧版显式列写入、并发事务与回滚均纳入测试。此次没有改动原始遥测 JSON、已封批内容、发送协议、计费或生产配置。
- 如需回退应用，新增列和函数可保留；旧版写入会自动维护生成值。不要在新版实例运行时删除该列。

## 三轮本地基准

WSL Ubuntu 24.04、Linux amd64、Ryzen 7 9700X、Go 1.27.3、`GOEXPERIMENT=greenteagc,jsonv2`，默认 GOMAXPROCS 12；隔离 PostgreSQL 18.1 / Redis 8.4。同一固定随机种子生成 16 KiB 不易压缩的 profile，分别保留 32 与 4,096 条已完成 turn。每次执行过期检查、容量读取和一条小型 sampling 更新；每轮 10 次，共三轮。数据创建、索引统计及清理不计时。

基线使用 `git archive a2a1ac1fa`，只加入同一份基准文件，优化前后依次运行，无生产模型调用。

| 运行态数量 | 优化前 ms/op（三轮） | 优化后 ms/op（三轮） | 平均耗时降幅 |
|---|---|---|---:|
| 32 | 14.615 / 15.700 / 18.178 | 8.953 / 9.517 / 8.993 | 43.4% |
| 4,096 | 595.722 / 628.666 / 600.326 | 9.252 / 9.684 / 10.978 | 98.4% |

4,096 条场景客户端分配从约 23.35 KB/op 降至 20.31 KB/op，约减少 13.0%；allocs/op 从 387–388 降至 364。主要收益发生在数据库，表中数值是本地事务基准，线上 CPU/RSS 收益需部署后观察。

原始记录：[baseline.txt](../performance/server-utilization-20261116/baseline.txt)、[optimized.txt](../performance/server-utilization-20261116/optimized.txt)。

复现基准（在 `backend`，需要本地 Docker）：

```bash
GOEXPERIMENT=greenteagc,jsonv2 go test -tags=integration ./internal/repository \
  -run '^$' -bench '^BenchmarkCodexTelemetryRuntimeCapacity$' -benchtime=10x -count=3
```

## English

An account-wide OAuth 429 timer could be cleared by unrelated successful requests, renewing the deadline for an already retrying request. The handler now retains the earliest account deadline for each HTTP request or WS logical turn. Waiting is bounded by that deadline and checked again before retrying. A new WS turn gets a new budget; replay of the failed turn keeps its existing budget. The two-minute window, ordinary/error-specific retry limits, cancellation, routing and billing policies remain unchanged.

Telemetry capacity and aggregate validation now read a stored generated integer instead of repeatedly detoasting retained runtime JSON. Negative values preserve rejection of malformed aggregate fields; point reads retain full typed validation. A covering partial index excludes permanent markers. Capacity is cached only inside the pool-locked transaction, invalidated by runtime writes, and adjusted when completed rows are removed. There is no cross-instance cache and no extra copy of request bodies, credentials or headers.

Migration 288 computes existing rows and takes a table lock for its initial rewrite. Migration 289 creates the covering index concurrently using the existing non-transactional migration runner. Generated values stay correct with legacy explicit-column writers and transaction rollback. An application rollback can leave these additions in place; the new application requires the column.

The same deterministic local benchmark runs ten pool transactions per round, three rounds, against isolated PostgreSQL/Redis. With 4,096 retained 16 KiB profiles, mean transaction time falls from 608.238ms to 9.971ms (98.4%); client allocations fall approximately 13.0%. This measures the affected database workflow. Production resource changes are to be measured after deployment. No production configuration or deployment was performed.

## 验证 / Verification

| 项目 / Check | 结果 / Result |
|---|---|
| 遥测及旧迁移相关隔离数据库测试 | PASS，13.235s |
| 同组数据库测试加 race，含新事务缓存查询次数检查 | PASS，14.963s |
| HTTP/WS 重试、取消、窗口及相关 handler race | PASS，23.208s |
| 遥测运行态与 OAuth 429 service race | PASS，12.840s |
| 最终 Grok 次数上限隔离补充后 handler race | PASS，23.346s |
| `go build ./...` | PASS |

新增验证覆盖：窗口不被其他请求续期、不同请求及账号独立、截止前后等待与取消、Grok 错误次数上限隔离；历史数据迁移及重复执行、旧版直接写入、损坏字段、inflight 计数、事务回滚、12 个并发实例事务、完成记录清理后的容量一致性，以及事务内重复读取不再查询数据库。

本次未修改前端，未执行前端检查；没有运行不相关的 Go 全套测试或生产压测。远端 CI 状态未核验，交付不包含部署。原始测试结果与三轮基准一起保存在 `docs/performance/server-utilization-20261116/`。

Affected tests, race detection, isolated database migration/transaction tests and the backend build passed. The frontend is unchanged. Remote CI status was not verified; this delivery does not deploy the application.

复现（在 `backend`）：

```bash
export GOEXPERIMENT=greenteagc,jsonv2
go test -race -p 1 ./internal/handler ./internal/service \
  -run 'Test(SameAccount|WaitSameAccount|Failover|HandleFailover|EffectiveSameAccount|NeedForceCache|OpenAIResponsesWebSocket|OpenAIResponses_.*(Retry|Failover)|OpenAIGatewayHandlerResponses_Failover|CodexTelemetry|OpenAI.*429|ShouldStopOpenAIOAuth429|OpenAIWSCurrentTurn|OpenAIPoolModeTempRule)' -count=1
go test -race -tags=integration ./internal/repository \
  -run 'Test(CodexTelemetry|Migration27[569])' -count=1
go build ./...
```
