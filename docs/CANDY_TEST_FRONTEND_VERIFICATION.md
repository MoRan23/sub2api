# 糖果题测试与 turn-state 退役：前端验证记录

验证基线为 `35ccf3aa7`，使用本任务合并后的工作树及现有 pnpm 工具链。验证没有连接真实业务、授权或遥测端点。

## 自动检查

| 检查 | 结果 |
| --- | --- |
| `pnpm run typecheck` | 通过 |
| `pnpm run lint:check` | 通过；未运行自动修复 |
| `pnpm run test:run` | 366 个文件、2776 个测试通过 |
| `pnpm run build` | i18n 检查、Vue TypeScript 编译与 Vite 构建通过 |
| 身份／响应证据／普通连接测试专项 | 5 个文件、61 个测试通过 |
| 糖果弹窗后续异步与分页回归 | 11 个测试通过；包括失效异步响应和清理后的 `retained_total` 分页 |

构建仍提示已有的大于 500 kB bundle，测试工具仍提示 Browserslist 数据较旧；没有新增编译、lint 或测试失败。`pnpm run test:run` 的日志为工作树中的 `frontend-full-test.log`，其他运行日志以 `frontend-*.log` 保留在本地，不纳入源码。

## 覆盖内容

- 账号页删除 turn-state 列、菜单、弹窗、专用批量轮询及设置字段；旧浏览器列配置不会重新显示已退役入口。
- 常规 OpenAI OAuth 身份资格迁至独立函数，保留默认系统、三系统身份及 auth.json 导出；PAT、Agent Identity 和 API Key 不误用该身份资格。
- 普通连接测试继续显示上游返回模型，不再判断票据长度或形态。
- 指纹观测通过 `response_evidence` 显示 JSON 模型、模型关系、声明冲突及独立 safety-buffering 头；不再显示票据或 Cookie 包诊断。
- 糖果题单项和批量入口适用于 OpenAI 各凭据类型，跨页选中的全部 ID 会冻结交给弹窗，不随当前页或后续筛选缩减。
- 关闭弹窗停止前端轮询而不取消后台任务；重新打开恢复已有批次。主动取消、每五秒轮询、提交幂等键、最近五次历史、异常／失败／跳过区分和响应文本 XSS 防护均有组件测试。
- 表单退役回归继续覆盖身份配置、共享授权导入、遥测三开关及对旧配置字段的忽略。

## 模拟浏览器验收

使用本地 Vite `127.0.0.1:4187` 和独立 Chromium 会话。浏览器请求拦截器合成管理员登录与全部 API 响应，拒绝非 localhost 主机；从未调用上游推理或真实后台。浏览器及本地服务器在验收后均已停止。

已手动验收：

1. 账号页显示糖果题列，OpenAI OAuth、API Key 可进入，其他平台单元格为横线；旧 turn-state 列不存在。
2. 单账号选择 `gpt-6-astra` 和 `xhigh`，启动后显示测试中；关闭并重新打开仍展示同一活动批次。
3. 主动取消后展示取消结果和历史，同时账号状态仍为正常、调度仍开启。
4. 全选三个合成账号后启动批量测试，两个 OpenAI 项运行、非 OpenAI 项明确跳过。
5. 合成完成结果显示正常与异常；详情并排显示四项回答和 `32 / 29 / 40 / 38` 标准值、请求／出站／上游模型、耗时及原始最终回答。

本地截图：`output/playwright/candy-batch.png`、`output/playwright/candy-results.png`。可复用的纯合成 API 拦截脚本：`output/playwright/candy-mock.cjs`、`output/playwright/candy-results.cjs`。这些浏览器产物属于忽略的本地验证输出，不含真实凭据。

初始模拟页面的公告接口使用通用占位响应，触发了一条公告列表类型错误；该错误来自非目标页面的合成夹具，糖果操作及其网络请求未出现运行时错误。没有以此替代真实后端的集成验证。
