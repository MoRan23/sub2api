# 网络冷却与客户端示例上报验证 / Validation

日期：2026-11-25。基线：`6a57ed4eb`。

## 范围 / Scope

- 网络错误来源、HTTP/WS/图片/Chat 转换/独立搜索、已发送插件请求、重试耗尽和中央调度入口。
- Cyber 在状态策略、限流、模型屏蔽、自定义冷却及健康熔断前退出，原结算路径保持。
- Token 刷新网络故障保留任务重试与提供商退避，不修改账号状态。
- Codex HTTP、WS、PAT、Grok、路由示例与 CC Switch 导出统一关闭 analytics 和 OTEL 三类 exporter。

## 验证记录 / Checks

工具链：WSL Ubuntu 24.04，Go 1.27.4，`GOEXPERIMENT=greenteagc,jsonv2`，`GOMAXPROCS=2`；前端使用仓库现有 pnpm/Vitest/Vue 工具链。

- 前端相关测试：58 项通过（UseKeyModal 36、CC Switch 19、locale completeness 3）。
- 前端类型检查、定向 ESLint、生产构建通过。构建保留现有 Browserslist 数据过期、动态/静态混合导入及大 chunk 提示。
- 首轮后端回归确认三个旧测试的超时冷却断言与新策略冲突，已改为验证零账号处罚；保留失败响应、清理、usage 和提供商退避断言。
- 一个既有 Antigravity 成功刷新测试曾触发真实隐私设置请求并收到 401；现已用“隐私已配置”测试状态避免外部访问。本轮没有调用真实模型。

- 最终 Go `-tags=unit` 定向回归：service、handler、repository、config 全部通过；包含旧状态保留、真实 5xx/429、网络错误来源、重试耗尽、Cyber、图片、WS/桥接、刷新与中央调度失败计数。
- Go `-race -tags=unit`：service、handler 的网络/Cyber/WS/刷新定向测试通过。
- `go build -p 1 -o /tmp/sub2-network-cooldown-server ./cmd/server` 通过。
- `git diff --check` 通过。未执行全仓库测试；无数据库迁移，本轮未启动数据库集成测试。

## 性能 / Performance

错误来源仅存储布尔标记或保留原始 error cause。新增分类发生于失败路径；正常请求不读取数据库、不复制完整正文，不增加重试次数。

正常路径复用 `BenchmarkCodex0169Compatibility`，覆盖 64 KiB、1 MiB、8 MiB，分别检查请求规范化与指纹采集。首轮与编译并行的测量受到本机负载影响，未用于耗时结论。编译完成后使用 `GOMAXPROCS=2 taskset -c 0,1`，按基线→修改后交替执行三轮 `-benchmem -benchtime=500ms`，结果取中位数：

| 场景 | 基线 ns/op | 修改后 ns/op | 耗时变化 | B/op 基线→修改后 | allocs/op 两版本 |
|---|---:|---:|---:|---:|---:|
| normalize 64 KiB | 82,749 | 81,058 | -2.04% | 9,708→9,707 | 165 |
| fingerprint 64 KiB | 88,394 | 88,500 | +0.12% | 8,332→8,331 | 156 |
| normalize 1 MiB | 790,794 | 803,501 | +1.61% | 9,714→9,713 | 165 |
| fingerprint 1 MiB | 877,482 | 801,988 | -8.60% | 8,329→8,329 | 156 |
| normalize 8 MiB | 6,893,813 | 6,460,792 | -6.28% | 9,833→9,838 | 165 |
| fingerprint 8 MiB | 6,315,646 | 6,540,901 | +3.57% | 8,337→8,335 | 156 |

分配次数保持一致，B/op 仅有池化测量波动；未增加随正文大小增长的副本。耗时中位数最大增加 3.57%。原始结果见 `network-cooldown-baseline-bench.log`、`network-cooldown-current-bench.log`。

## 交付边界 / Delivery

无数据库结构变更。未执行生产部署或生产配置修改；现有未跟踪排查文件保留。最终测试使用本地 fixture 和模拟上游。提交前 GitHub API 返回 CI、CLA Assistant、Release、Security Scan 四个工作流为 active；推送后的执行状态在交付时记录，不以工作流启用状态代替 CI 结果。

English: focused unit tests, service/handler race tests, backend build, 58 frontend tests, type checking, lint and production build passed. Full-repository and database integration suites were not run. Allocation counts remain unchanged; interleaved three-run benchmark medians range from -8.60% to +3.57% in elapsed time. The change adds no successful-request database work or full-body copies. Existing timeout-penalty expectations were updated to the approved policy. One legacy privacy-setting test performed an external request and received HTTP 401; its fixture now prevents that request. No real model calls or production changes were made.
