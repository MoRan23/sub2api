# OpenAI 官方历史状态条 / Official status history

Date: 2026-11-20. Baseline: `a723acf1d`.

## 实现

- 渠道监控顶部的 APIs、ChatGPT、Codex 卡片增加 91 个 UTC 日状态格，以及官网分组可用率。桌面三列、移动端单列，共用一个悬浮提示框。
- 使用官网公开 incident.io 目录和影响区间；动态选择参与可用率展示的组件。只在后台刷新时解析并聚合，页面读取共享不可变历史。
- 当前状态、事件、历史独立更新；失败保留最近成功数据并标记延迟。损坏或不完整历史不补成正常状态。Redis 快照和租约升级为 v2，无数据库迁移。

## 验证

工具链：Go 1.27.4，`GOEXPERIMENT=greenteagc,jsonv2`，`GOTOOLCHAIN=local`，`GOMAXPROCS=2`。

- `go test -p 1 -tags=unit -count=1 ./internal/service -run '^TestOfficialStatus'`：通过。
- 同范围 `-race`：通过。覆盖 UTC 跨日、午夜结束、持续故障、未来区间、成员变更、无效数据、三类来源独立失败及共享快照。
- 官网公开 JSON 样本离线解析：三个产品各 91 格，聚合可用率分别为 99.97%、99.26%、99.94%。临时样本测试已移除，未将实时内容纳入仓库。
- `CI=1 go test -p 1 -tags=integration -count=1 -timeout 5m ./internal/repository -run '^TestOfficialStatusStore' -v`：通过。隔离 PostgreSQL 18.1 / Redis 8.4 测试环境，覆盖恢复、租约竞争、旧格式和 14 类损坏缓存、非法可用率及重新保存恢复。
- `CGO_ENABLED=0 go build -p 1 -trimpath -o /tmp/sub2-official-history-server-linux ./cmd/server`：通过。
- 前端官方状态组件 10 项测试、语言键完整性 3 项测试、定向 ESLint、`pnpm run build`（含类型检查）：通过。

## 读取分配

`BenchmarkOfficialStatusGet`，300ms，三轮。此项测量快照读取，不包含 HTTP JSON 序列化。

| 数据 | ns/op（三轮） | B/op | allocs/op |
|---|---|---|---|
| 无历史 | 114.3 / 115.0 / 116.5 | 352 | 1 |
| 三组各 91 格 | 132.0 / 133.0 / 133.4 | 352 | 1 |

历史数据没有增加读取分配；一次分配仅复制三个产品的响应元数据。序列化时会发送新增历史字段。后台外部查询固定每 120 秒共享执行，不随页面用户数增加。

## Delivery

The official history uses 91 UTC days and the provider's aggregate uptime. Missing data remains gray; refresh failures preserve the last successful history with a stale notice. Unit/race tests, isolated Redis integration, backend build, frontend tests, lint, type checking and production build passed. Snapshot reads allocate 352 bytes once with or without history.

Public status data was read for format verification. No real model calls, production configuration changes, Engine changes or deployment were performed. Repository Actions are disabled; remote CI was not run.
