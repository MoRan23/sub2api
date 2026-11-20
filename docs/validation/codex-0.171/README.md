# Codex 0.171 and official status validation / 验证记录

Date: 2026-11-20. Baseline: `3b3554c8b`. Reference source: local `D:\Code\codex\codex`, version 0.171.0. Implementation commits: official status `10c616291`, protocol/catalog `b3bfd6d81`, telemetry `263fd91f9`. [Executed commands / 执行命令](commands.md).

## 环境与结果

Go 1.27.4，`GOEXPERIMENT=greenteagc,jsonv2`；Windows amd64 / Ryzen 7 9700X，以及 WSL Ubuntu 24.04。数据库测试使用临时 PostgreSQL 18.1 和 Redis 8.4 容器，`CI=true` 防止 Docker 缺失时跳过测试。构建与测试未启动业务服务。

| 检查 | 结果 | 记录 |
|---|---|---|
| 官方状态解析、独立失败、共享租约、取消、并发读取、响应大小限制 | 通过 | [unit/race/route/build](official-status-checks.log) |
| V2 开关及 cleanup/Wire | 通过 | [路由与接线](wiring-routes.log) |
| 目录 nullable bool、账号交集、apps 原值保留 | 通过 | [目录测试](catalog-unit.log) |
| Codex 0.171 metadata、trace、WS 比较与物理连接复用 | 通过 | [协议 race](protocol-race.log) |
| 全部 `TestCodexTelemetry*` | 通过 | [遥测测试](telemetry-unit-compression-benchmark.log) |
| 遥测 worker 停止、开关取消、压缩及冻结身份 race | 通过 | [遥测 race](telemetry-race.log) |
| PostgreSQL v3→v4 遥测重启恢复 | 通过 | [集成首轮](integration-first-run.log) |
| Redis 状态恢复、坏快照修复、租约竞争与 TTL | 通过 | [集成最终轮](integration-final-linux-build.log) |
| Windows / Linux 后端构建 | 通过 | [Windows](official-status-checks.log)、[Linux](integration-final-linux-build.log) |
| Linux 二进制 `-version` | 通过 | [版本冒烟](linux-version.log) |
| 前端 5 个相关测试文件，最终共 72 项 | 通过 | [首轮 71 项](frontend-tests.log)、[卡片最终 6 项](frontend-status-tests.log) |
| 前端类型检查、生产构建、定向 ESLint | 通过 | [类型](frontend-typecheck.log)、[构建](frontend-build.log)、[lint](frontend-lint.log) |

前端构建另执行已有的 3 项 i18n 检查。未增加依赖，构建保留现有 Browserslist 数据过期、静态/动态导入共存、大 chunk 和 Node DEP0190 提示。

## 修复过程与检查范围

- 官方状态 Redis 集成首次在末尾 TTL 断言失败，测试查询遗漏既有 `leader:lock:` 键前缀；修正测试后同项完整重跑通过。快照恢复和竞争逻辑未因此改动。
- 协议较广范围回归发现无 trace 请求新增观察标记导致旧 header 数量断言失败。改为仅在实际存在 trace 时写入内部观察标记，修复后该回归及新增测试纳入最终 race 并通过。
- 遥测新增 fixture 的 source 和 lock-hold 标签预期在首次运行后修正；最终遥测单元和 race 通过。
- 本次按受影响范围验证，未运行全项目测试套件；未调用真实模型、未修改 Engine、未部署或修改生产配置。远端 CI 未由本次执行或核实。

## 性能

基线从 `3b3554c8b` 导出，使用相同 Go/实验开关、fixture、三轮 `-benchmem` 对照。普通 metadata 预算分支直接返回现有字符串；只有超预算 remote 会构建筛选数据。

- 官方状态接口读取 100 条缓存事件时，只复制三个状态 DTO：三轮 **103.0 / 143.2 / 118.5 ns/op，192 B/op，1 alloc/op**，不复制事件正文，不访问 Redis、数据库或外网。[原始记录](official-status-benchmark.log)
- 35,660 字节 metrics 批次压至原体积 **8.071%**，每次约 **9.88 KB / 6 allocations**；285,133 字节聚合压至 **1.719%**，约 **12.57 KB / 5 allocations**。三轮耗时分别 62–80µs 与 76–190µs，期间存在其它构建，压缩耗时作为开销范围记录。[原始记录](telemetry-unit-compression-benchmark.log)
- 压缩 encoder 由四个 worker 各自惰性创建，内部并发为 1；只保留一次发送的压缩结果。已封批、队列及重试使用的 JSON 不变。
- 固定 CPU 的跨二进制对照曾出现 8 MiB 耗时回退；CPU profile 和汇编将差异定位到未修改的 JSON 扫描循环及链接对齐。仅在临时诊断构建中将两版函数均对齐到 64 字节后，规范化中位差异 **−0.11%**、指纹 **+3.27%**。生产构建参数保持不变。指纹观测稳定增加约 **56 B / 2 allocations**，与正文大小无关。

完整请求规范化、指纹、WS 比较与固定 CPU 对照数据见 [性能对比](performance.md)。早期基线日志保留：[规范化/指纹](baseline-normalization-fingerprint.log)、[WS 比较](baseline-ws-comparison.log)。主机调度波动明显，报告区分固定分配开销与测得耗时。

## English

Targeted backend unit/race checks, catalog cases, V2 gating and shutdown, isolated PostgreSQL/Redis recovery, both platform builds, and the Linux version smoke test passed. The five affected frontend suites contain 72 tests after the final card regression case; type checking, production build, i18n compilation and scoped lint passed.

The first Redis integration run exposed a missing existing lock-key prefix in the test's TTL assertion. The corrected test passed. A protocol regression concerning unnecessary trace-observation headers was fixed and included in the final race run. Full-project suites and remote CI were not run for this scoped change. No production deployment, configuration edits, Engine changes or real model calls were performed.

The official-status read path uses one 192-byte allocation regardless of incident count. Compression is bounded to four single-thread encoders at the sender boundary and leaves persisted JSON unchanged. See [performance.md](performance.md) and the raw logs for three-run timings and allocation comparisons.
