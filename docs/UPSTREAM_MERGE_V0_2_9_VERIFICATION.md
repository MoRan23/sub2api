# 上游 v0.2.9 合并验证

## 工具链与边界

- 本地基线：`dev@50d89cffb`；合并树：`codex/merge-upstream-v0.2.9`。
- 上游固定提交：`9a62841fd124d026cf3694fcf9b79e98addcdbdc`。
- Go 1.27.0，`GOEXPERIMENT=jsonv2`、`TYPESAFE_LIVE_TEST=0`、`GOMAXPROCS=2`，后端测试和构建使用 `-p 1`。
- 验证仅使用本地测试夹具，没有发送真实业务、上游模型目录、糖果测试、授权、遥测或收费请求。
- 原始输出位于 `.git/task-artifacts/merge-upstream-v0-2-9/{baseline,merged}/`。

## 检查结果

| 检查 | 基线 | 合并树 | 对照 |
|---|---:|---:|---|
| 全包编译 `go test -run '^$' ./...` | 通过 | 通过 | 无编译回归 |
| server 构建 | 通过 | 通过 | 无构建回归 |
| default suite | 8742 pass / 12 skip / 4 fail | 8814 pass / 12 skip / 4 fail | 无新增失败 |
| unit suite | 14966 pass / 16 skip / 8 fail | 15098 pass / 16 skip / 7 fail | 既有失败减少 1 |
| golangci-lint（无截断） | 391 项 | 391 项 | 无新增诊断 |

default 和 unit 的剩余失败均属于固定基线的既有失败；其中两个 nil pointer fixture panic 会在两棵树以同样方式出现，并保留完整原始日志。unit 合并树消失的失败为 `TestFilterGrokFreeQuotaAccountsOnlyBlocksExplicitFreeOAuth`。

合并树的新增 lint 诊断已随测试文件格式和资源关闭检查修复；最终无截断结果与基线同为 391 项，没有新增诊断。

## 重点回归

- 糖果测试目录查询、原始上游模型发送及账号状态隔离由专项测试覆盖；该功能不回退到业务白名单或模型映射。
- 合并后的额度、WebSocket 窗口切换、协议转换、计费和管理页面检查包含在对应后端与前端专项测试中。
- 本记录不把基线失败或当前新增 lint 视为已通过；修复新增诊断后应更新本表及对应日志。
