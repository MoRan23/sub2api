# add-openai-compatible-prompt-audit

在不改变现有内容审核行为的前提下，新增独立的 OpenAI 兼容 Qwen3Guard 提示词安全审计模块，完整支持异步审计、同步阻断、持久任务队列、事件工作台与独立管理页面。

阅读顺序：`proposal.md` → `source-baseline.md` / `source-feature-map.md` → `design.md` → 三个 `specs/*/spec.md` → `implementation-guide.md` → `tasks.md` → `verification.md`。

## 关键词拦截复核

- 关键词输入框支持直接按回车逐行添加；编辑时保留换行和空白，保存的列表仍去除空项、首尾空格及大小写重复项。
- 新的关键词拦截事件保存当次实际触发阻断的首个配置关键词，管理员可在详情的“审计摘要”和“具体风险”中查看“命中的关键词”。沿用现有按配置顺序匹配、首次命中即阻断的规则。
- 命中词复用现有事件 JSON 存储，无需数据库迁移；日志和客户端拦截响应保持原有格式。旧事件只记录了哈希时，详情显示“未记录”。
