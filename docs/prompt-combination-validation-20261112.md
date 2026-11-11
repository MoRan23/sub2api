# 组合规则路径过滤与白名单验证 / Combination rules validation

## 范围与环境 / Scope and environment

本次验证通用组合规则的路径过滤、按策略设置的精确用户＋API Key 白名单，以及
配置保存和读取的兼容性，保留性能夹具、原始数据和验证记录。
未调用真实模型，也未连接生产数据库或修改线上配置。

- 基线：`65e3c9f8b`，通过 `git archive` 提取到仓库外并单独编译。
- 新版：本次最终工作区代码。移除路径的语义只存在于新版，不能把新旧路径场景
  视为执行了完全相同的工作；首次实现与最终实现另做同语义分配比较。
- Go 1.27.3，`GOEXPERIMENT=greenteagc,jsonv2`，Linux amd64，WSL Ubuntu 24.04。
- CPU：AMD Ryzen 7 9700X；基准固定 `GOMAXPROCS=4`、`-test.cpu=4`。
- 数据库：隔离 `postgres:18.1`、`redis:8.4-alpine`，测试各自创建隔离 schema。
- 三轮交错顺序：基线→新版、新版→基线、基线→新版；每场景 `250ms`。
  对 8 MiB 图片场景额外执行三组交错 `1s` 测量。

This validation covers generic combination path filtering, exact per-policy
user/API-Key exemptions, and configuration persistence/compatibility. The
baseline archive and final test binary use the same toolchain, input data and
concurrency settings. Only synthetic requests and isolated PostgreSQL/Redis
were used. Timings below are microbenchmarks, not end-to-end production latency.

## 已执行验证 / Executed checks

- 配置和白名单定向测试：27 个顶层测试通过。
- 最终完整 `internal/securityaudit`：251 个顶层测试通过；PostgreSQL 与 Redis
  集成测试实际执行，未因缺失服务跳过。
- 最终 `securityaudit -race` 使用相同隔离 PostgreSQL／Redis，通过，20.605 秒；
  handler `Combination|Keyword` 定向测试通过，0.030 秒；`go build ./...` 通过。
- 持久化覆盖：新增白名单归属验证、非法归属保存回滚、同策略省略字段保留、显式
  空数组清空、已失效旧 Key 不妨碍无关保存、换策略新增授权仍检查归属、跨实例重载。
- 请求覆盖：原关键词优先、全局豁免、单策略豁免、同用户不同 Key、不同所有者、
  Guard 仍执行、不可变配置共享与对外副本隔离。
- 路径覆盖：文件名、目录、Windows/Unix 路径、URL hostname 保留、边界不跨路径、
  空白与正则语义、中文紧贴政府域名、长转义路径、复杂 authority 与非法 URL。
- 显式未执行的两个受控堆实验：`TestPromptAuditHeapRetention`、
  `TestPromptDecodedTextOwnershipHeap`，均要求 `PROMPT_AUDIT_HEAP_MEASURE=1`。
  本轮执行了独立 allocation profile，但没有重测长期驻留堆、RSS 或生产流量。

The final full package passed 251 top-level tests with real isolated database
and Redis integration enabled. The two opt-in retained-heap experiments were
not enabled. Allocation profiles were captured separately; retained heap,
process RSS and production traffic were outside this validation run.
The full race run with database integration, targeted handler tests and backend
build also passed. The two dedicated test containers were removed after all
verification completed; no production container was touched.

首次直接运行测试二进制时工作目录误设为 `backend`，导致相对迁移文件查找失败；
切换至 `backend/internal/securityaudit` 后通过，无需修改实现或测试保护条件。

前端首次 7 个文件、77 项测试通过，新增
名单总量限制测试后定向复跑通过，合计覆盖 **78 项不同测试**。最后一次
`configToDraft` 优化后重跑 25 项 viewModel 测试、定向 ESLint 与完整
`pnpm build`（i18n 检查、`vue-tsc -b`、Vite）通过。所有改动文件的 ESLint 与
独立 `vue-tsc --noEmit` 亦通过。构建保留既有 Browserslist 数据、DEP0190、
混合导入及大 chunk 提示。复现命令见
[前端验证记录](performance/prompt-combination-20261112/frontend-validation.txt)。

Frontend validation covered 78 distinct tests across the relevant component,
view-model, API and locale suites. Changed-file ESLint, TypeScript checks and
the production build passed. The final view-model adjustment was followed by
its 25 tests and a full build; pre-existing build warnings remain recorded.

## 性能结果 / Performance results

完整三轮原始数据及每项波动见 [对比表](performance/prompt-combination-20261112/comparison.md)。
原有 `BenchmarkPromptAuditMemory` 的分配量基本不变；短测中 8 MiB 图片场景出现
8.4% 耗时波动，延长交错验证得到基线 5.200 ms、新版 4.966 ms，中位数下降 4.5%。
其余原有场景及强制扫描纯文本场景未出现超过 5% 的稳定中位数回退。

白名单通过真实 `ConfigManager` 测量，未使用简化配置替身。请求从 64 KiB 增到
8 MiB，且每条策略名单增到 1,000 项后，完全豁免路径仍为 **64 B/op、1 alloc/op**。
8 条策略各含 1,000 项的单策略豁免检查约 219 ns/op；独立哈希查询 1／100／1,000
项均为零分配，约 10.5–15.2 ns/op。未解析请求正文，也未复制整份配置。

The exemption fast path uses the real `ConfigManager`. Both 64 KiB and 8 MiB
requests take 64 B/op and one allocation, including configurations with eight
1,000-entry policy lists. Exact pair hash lookup allocates nothing. Ordinary
path-free text scanning also allocates nothing. A single URL with an escaped
authority and a 64 KiB or 8 MiB escaped suffix uses the same **320 B/op and four
allocations**, because the excluded suffix is validated without decoding it.

### 新路径能力的同语义优化 / Same-semantics allocation optimization

初版 profile 显示，路径数组扩容和逐 URL `url.Parse` 占该样本分配的 75.66%。
最终版使用紧凑边界块与 authority 范围验证，原始文本继续只读共享。

| 8 MiB 场景 / Scenario | 首次实现 B/op | 最终 B/op | 分配减少 | 首次→最终耗时中位数 |
|---|---:|---:|---:|---:|
| 密集文件路径 / filesystem | 45,672,725 | 17,985,540 | 60.6% | 105.61 → 125.67 ms |
| 密集 URL / URL | 60,062,888 | 9,207,696 | 84.7% | 493.64 → 513.46 ms |

紧凑边界解码以额外计算换取大幅降低的存储量；文件路径场景较中间版增加约 20 ms。
相比未过滤路径的原始基线，最终文件路径场景仍从 1,909.74 ms 降至 125.67 ms。
新增 `/a /a` 密集短路径场景保留空白语义，8 MiB 输入最终为 15,148,072 B/op、
423.43 ms/op；边界存储随需要保留的片段增长，没有生成另一份完整正文。

Compact boundary decoding trades some CPU for bounded, substantially smaller
metadata. The filesystem case is about 20 ms slower than the intermediate
implementation while remaining much faster than the unfiltered baseline.
The short-path stress case deliberately preserves every whitespace region;
the metadata cost therefore scales with the number of retained boundaries.

## 复现 / Reproduction

编译时固定实验开关。直接运行测试二进制时，应从包目录执行，以匹配集成测试的
相对迁移路径。数据库地址必须指向专用测试实例。

```bash
cd backend
export GOEXPERIMENT=greenteagc,jsonv2
go test -c ./internal/securityaudit -o /tmp/securityaudit-current.test
cd internal/securityaudit
export PROMPT_AUDIT_TEST_POSTGRES_DSN='postgres://sub2test:sub2test@127.0.0.1:55439/sub2test?sslmode=disable'
export PROMPT_AUDIT_TEST_REDIS_ADDR='127.0.0.1:56389'
/tmp/securityaudit-current.test -test.v -test.count=1

GOMAXPROCS=4 /tmp/securityaudit-current.test -test.run='^$' \
  -test.bench='^BenchmarkPromptAuditMemory$/(text|image)/[0-9]+/combination$' \
  -test.benchtime=250ms -test.cpu=4 -test.count=3 -test.benchmem
GOMAXPROCS=4 /tmp/securityaudit-current.test -test.run='^$' \
  -test.bench='^BenchmarkPromptCombination' \
  -test.benchtime=250ms -test.cpu=4 -test.count=3 -test.benchmem
```

基线归档只复制可移植的 `prompt_combination_performance_test.go` 新夹具，
不复制依赖新增白名单 API 的文件。三轮正式比较应交错运行两个已编译二进制；
上面 `-test.count=3` 是单个实现的便捷复现方式。

## 原始记录 / Raw records

- [旧基线](performance/prompt-combination-20261112/baseline.txt) /
  [最终版本](performance/prompt-combination-20261112/current.txt)
- [白名单、无路径和转义 URL](performance/prompt-combination-20261112/whitelist-path-fastpath.txt)
- [8 MiB 图片交错复核](performance/prompt-combination-20261112/paired-image-8m.txt)
- [Race 与数据库](performance/prompt-combination-20261112/race.txt) /
  [Handler 定向测试](performance/prompt-combination-20261112/handler-tests.txt) /
  [后端构建](performance/prompt-combination-20261112/backend-build.txt)
- [首次实现](performance/prompt-combination-20261112/initial-current.txt) /
  [首次配对基线](performance/prompt-combination-20261112/initial-baseline.txt)
- [首次 allocation profile 摘要](performance/prompt-combination-20261112/initial-alloc-profile.txt) /
  [最终 allocation profile 摘要](performance/prompt-combination-20261112/final-alloc-profile.txt)

原始大二进制 profile 与测试二进制保存在本地 DevCache，不加入仓库。
全部验证结束后，已删除本轮两个专用测试容器 `sub2-combo-pg-20261112` 与
`sub2-combo-redis-20261112`。后端构建日志为空表示成功且无诊断输出。
