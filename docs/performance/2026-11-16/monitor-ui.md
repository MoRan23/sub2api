# Monitor and administration UI performance / 监控与管理页面性能

## Behavior / 行为

- V2 `group-overview` accepts `include_legacy_attribution` (default `true`). Current cards pass `false`, skipping the obsolete `latest/recent` query. The 200-minute history and current attribution result retain their existing rules. A process-local singleflight/LRU cache retains only non-personalized aggregate metrics and attribution: 10-second TTL, at most 256 entries and a conservative 32 MiB budget. Oversized results are returned without residency. Authorization and active/monitored scope are checked on every request; current group names, user rates and throughput visibility are applied afterwards. Responses own their small mutable leaves.
- V2 新卡片跳过旧归因查询；共享缓存不含用户身份、个人倍率、凭据或正文。过期、容量、取消和跨用户响应所有权均有测试覆盖。统计时间使用实际缓存采样时间。
- V1 `window=7d|15d|30d` adds the selected-window availability to the list response. Latest status, timeline and availability are queried in batches. Older-server fallback is limited to four concurrent detail reads, shares refreshes of the same in-flight window and aborts obsolete windows.
- 分组用量、容量摘要接受可选 `group_ids`：正整数、去重排序、最多 1,000 个。省略参数保留旧行为，显式空列表返回空结果；当前管理页只查询需要展示的本页分组。ID 范围在历史聚合、尾部原始日志、分组输出及容量账号加载之前生效。
- The users table computes public groups once, indexes only current-page users and mounts one hover/focus tooltip. Account dialogs use direct dynamic imports and first-open mounting. Initially open dialogs initialize correctly. Account statistics, test-model reads and scheduled-plan/result reads cancel on relevant account/visibility changes and ignore late results.

## Export UI / 导出界面

- Export freezes the currently selected filters, language and timezone in one task-creation request. The dialog lists up to 50 persisted tasks, including after leaving and reopening the page. It polls active tasks every three seconds for at most 200 repetitions; a manual refresh starts a new bounded session. Errors stop automatic polling until a refresh.
- 下载先申请短期票据，再让浏览器直接下载；不通过 Axios Blob 持有文件。关闭页面只取消浏览器请求，不取消服务器任务；取消和删除是单独的明确操作。`deleting` 状态等待后台清理，继续有界轮询；隐藏取消、重复删除与下载按钮。权限失败原样通过现有错误提示显示。
- The server owns queue, file and disk quotas. The browser does not assemble rows or XLSX workbooks. The now-unused `xlsx` dependency and its manual vendor-chunk rule were removed. Existing `file-saver` usage elsewhere is preserved.

## Verification / 验证

Windows Go automatically selected Go 1.27.3 from `go.mod`; commands used `GOEXPERIMENT=greenteagc,jsonv2 GOMAXPROCS=4` and `-p 2`. WSL was unavailable during these checks.

| Check | Result | Raw record |
|---|---|---|
| Go monitor/service/repository/handler/scoped summary tests | Passed | `raw/monitor-ui-go-tests.log` |
| Monitor cache / V1 window race tests | Passed | `raw/monitor-ui-race.log` |
| Frontend targeted suites | 16 files, 175 tests passed | `raw/monitor-ui-tests.log` |
| Final in-flight reuse / scheduled-read cancellation changes | 3 files, 17 tests passed; type check and targeted lint passed | `raw/monitor-ui-cancellation.log`, `raw/monitor-ui-cancellation-lint.log` |
| Type checking | Passed | `raw/monitor-ui-typecheck.log` |
| Targeted lint | Passed | `raw/monitor-ui-lint.log` |
| Production build | Passed | `raw/monitor-ui-build.log` |

前端覆盖后台导出创建、权限失败、票据直连、重进页面、主动取消/删除、卸载仅取消请求、删除处理中重试；监控覆盖批量窗口、旧后端四并发、页面摘要范围与账号弹窗初始化。主检查中预期失败操作的 console error 属于测试夹具。

### DOM measurement / DOM 测量

The same jsdom fixture mounts 100 users and 1,000 groups in three rounds. Closed tooltips contribute no per-user group-name subtree; opening one public-group tooltip adds only 501 elements. This is a DOM-scaling check, not a browser CPU or RSS measurement.

| Round | Initial elements | One tooltip open | Mount + open (ms) |
|---|---:|---:|---:|
| 1 | 3,244 | 3,745 | 315.849 |
| 2 | 3,244 | 3,745 | 213.856 |
| 3 | 3,244 | 3,745 | 212.512 |

### Reproduction / 复现

```sh
cd backend
GOEXPERIMENT=greenteagc,jsonv2 GOMAXPROCS=4 go test -p 2 ./internal/service ./internal/repository ./internal/handler ./internal/handler/admin -run 'ChannelMonitor|GroupSummaryScope|GroupUsageScope|GroupCapacity'
GOEXPERIMENT=greenteagc,jsonv2 GOMAXPROCS=4 go test -race -p 2 ./internal/service -run 'TestChannelMonitorV2OverviewCache|TestChannelMonitorWindow'
GOEXPERIMENT=greenteagc,jsonv2 GOMAXPROCS=4 go test -p 2 ./internal/service -run '^$' -bench BenchmarkChannelMonitorV2OverviewCache -benchmem -count=3 -benchtime=1s
cd ../frontend
pnpm exec vue-tsc --noEmit
pnpm exec vitest run src/views/admin/__tests__/UsersView.spec.ts src/components/admin/usage/__tests__/UsageExportTasks.spec.ts src/api/__tests__/admin.usageExports.spec.ts src/views/user/__tests__/ChannelStatusV1View.details.spec.ts
pnpm build
```

The cache microbenchmark separates aggregate reuse from per-viewer projection with 100 groups and 18 buckets. Database latency is excluded. These are cold-versus-cached measurements of the new path, not a checkout comparison against the old implementation. Raw record: `raw/monitor-ui-bench.log`.

| Round | Cold ns/op | Cached ns/op | Cold B/op | Cached B/op | Cold allocations | Cached allocations |
|---|---:|---:|---:|---:|---:|---:|
| 1 | 320,417 | 237,578 | 550,269 | 388,598 | 445 | 322 |
| 2 | 287,878 | 246,721 | 550,247 | 388,586 | 445 | 322 |
| 3 | 303,253 | 343,867 | 550,290 | 388,546 | 445 | 322 |

缓存命中后的分配下降约 29.4%，分配次数下降 27.6%；三轮耗时中位数下降约 18.6%。第三轮计时有反向波动，同机同时存在集成构建，因此这里只记录耗时范围，不把它作为稳定 CPU 或 p95 收益。真实数据库查询减少由缓存/单飞调用次数测试和 V1 批量查询测试验证。
