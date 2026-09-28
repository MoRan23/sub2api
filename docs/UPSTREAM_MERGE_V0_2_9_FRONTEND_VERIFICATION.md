# 上游 v0.2.9 前端验证

## 合并范围

前端保留本地糖果题实时上游模型目录和原始模型 ID、三系统身份界面、最新模型目录与账号配置，同时合入 v0.2.9 的视频独立倍率、分组模型通配符、CC Switch 路径、Codex Windows 配置路径、用量倒计时和弹窗清理。

## 自动检查

- `pnpm test:run`: **368 个测试文件、2799 个测试通过**。
- `pnpm typecheck`: **通过**。
- `pnpm lint:check`: **通过**。
- `pnpm build`: **通过**；Vite 仅报告现有大 chunk 警告。

## 浏览器验收

使用 Playwright、隔离 Vite 页面和合成 API 响应；阻断外部主机，不发送真实上游请求。

- 糖果题从每个账号读取实时目录；选择和提交使用原始上游模型 ID；账号目录失败单独显示，不回退到本地白名单。
- 视频独立倍率支持 `0` 和 `0.5`，图片独立倍率保持隔离。
- 零用量且存在未来重置时间时显示倒计时；没有重置时间时显示“Now”。
- CC Switch 根地址和已有 `/v1` 均生成唯一 `/v1/usage` 查询路径。
- Windows Codex 配置输出 `~/.codex/codex-models.json`。
- 分组编辑接受中间通配符 `gpt-*-codex` 并在保存请求中保留该条目。

证据：`.git/task-artifacts/merge-upstream-v0-2-9/frontend-browser.log`、`browser-report.json`、`browser-*.png`。
