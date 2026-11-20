# Upstream v0.2.24 merge / 上游 v0.2.24 合并

## 基线与交付范围

- 本地基线：`f987b4d0e3ae836b81184946577eac6057c6a887`。
- 上游基线：`2af3f1e15c98c893a89255a037f551046883b8c0`；本次合入 `328db26f450edbdf9f8d4344a3ea903ac6396842`。
- 合并提交：`4bae9e380`，双亲分别为本地基线和本次上游提交。
- 上游范围：110 个提交、190 个文件，`+11,853 / -610` 行。普通 merge 保留双方历史；预演的 22 个冲突文件逐项融合。
- 无新增 SQL 迁移、Ent schema 或依赖升级。只从合并后的注入源重新生成 Wire。
- 保留全部已有未跟踪排查、构建及性能分析文件；不修改 Engine 项目，不调用真实模型，不修改生产配置，不自动部署。

## 兼容处理

### 协议与目录

- 保留工具原名及当前官方缺省 instructions 规则，接入 ToolSearch、hosted output、搜索来源及工具参数兼容。非流式输出重建使用同一份必要缓冲，终态已有内容不被推断结果覆盖。
- 独立上下文共享结构判定补齐 IDE、untrusted content 和 proposed plan；保留合法具名独立工具输出及原 ID。上下文可作为审核证据，不作为最新用户操作意图。
- 保留 typed reasoning、Cyber max、逐回合强度记录、OAuth stream options、时区、完整性检查和续链隔离。不引入独立服务端 Ultra 档位。
- Kimi Chat 桥接使用一次正文解析保留动态工具，修复嵌入解码器吞掉工具字段的问题；数值推理强度保持 uint64 精度，只有数组 input 参与配置更新扫描。
- 官方投影、固定目录及合成别名保持同名公开描述符优先；保留未知字段、null、Cyber 附件、unified_exec、Blue 展示能力以及真实能力证据隔离。
- 声明支持且实际路由支持的 Responses↔Chat 音频继续可用；只有模型 ID 的 API Key 目录不推测补入音频。
- Anthropic 长上下文候选使用共享精简目录投影与缓存，按真实 metadata 和合法映射扩展，不逐请求重新加载整组账号。
- 保留 Codex 0.171 Guardian、trace、metadata 预算、UA 优先级、zstd 遥测及 OpenAI 官方服务状态卡片。

### 正文、审核与授权

- 合入重复协议字段检测。HTTP 复用预读正文，WS 校验结果只用于当前帧，避免多层重复扫描；JSON 合法性与重复字段检测分别保证。
- 保留模型白名单拒绝优先级。控制帧只做适用的格式检查，实际推理回合仍检查最终继承的模型；格式拒绝不参与提示词处罚或计费。
- 合入根遍历、图片意图预筛、namespace 按需修改及严格 schema 快速扫描；保留本地必要条件预筛，不叠加重复全量校验。
- 保留审核 prepared 共享、只读配置、关键词分类、组合白名单、处罚事务及异步任务不持有完整正文；吸收 UTF-8 分块优化。
- 429 重试复用只读事实，最终正文及 patch 逐尝试生成；保留身份、Daybreak、时区及发送前完整性处理。
- OAuth 不可逆令牌轮换后的有界保存融入本地字段级 CAS，保留 owner、OS、授权代际及 revision；撤销或重新授权后的迟到结果不能覆盖新凭据。保存后失效正确的 token key 并发布最新账号快照。
- 调度缓存仅在序列化投影中移除冗余分组对象，保留共享候选、精简凭据、Engine 同步状态及管理员设置保护。

### 计费与管理

- Grok 最终成功搜索用量覆盖条目推断，显式零有效；确认后不受重复终态或迟到条目影响。补齐 raw Chat 和 WS→HTTP 桥，按实际调用／回合隔离。
- 安全拒绝仍使用原有 CallID 原子结算；搜索及媒体成功单位归零，固定费和可信 token 费用按原规则结算，不重试、不处罚账号。
- Grok 编辑支持最多 5 张源图，mask 不计入；超限在上游调用前返回 400。Images 仅整理小型 usage JSON，保留原生图片规则和单次结算。
- 上游 408 只在未输出且未取消时允许换号。Antigravity 无明确原因的 429 在同一请求内最多冷却一个账号，配额查询跟随业务转发地址及代理。
- 默认平台额度在用户事务提交后初始化，未配置不创建记录，显式零保留；注册来源、邀请和双钱包规则不变。
- Claude 5h／7d 窗口独立估算并使用有界缓存；批量设置未选择的字段继续省略。Exa 作为搜索提供商选项接入，不自动启用。
- 管理端按有效样本计算加权生成速度；渠道卡片吞吐量继续使用 TPM÷60。免费 Fast／Ultrafast、账号优先及不支持 Ultrafast 时回退 Fast 的现有规则不变。
- Engine 保留完整交付、幂等结算及断连不补扣费行为。

## 验证

使用 Go 1.27.4、`GOEXPERIMENT=greenteagc,jsonv2`；数据库验证仅使用隔离 PostgreSQL／Redis。详见[验证报告](validation/merge-v0.2.24/README.md)、[存储验证](validation/merge-v0.2.24/integration.md)和[性能对比](validation/merge-v0.2.24/performance.md)。

前端 234 项相关测试、类型检查、生产构建及定向 lint 通过；后端新增协议和兼容回归、handler/server 套件、相关 race、最终 Linux 构建及隔离存储验证通过。84 个性能子例完成三轮测量，初始差异复测后无稳定超过 5% 的耗时回退。完整 service 套件中的 26 项合并前已有失败在报告中单独列出。GitHub Actions 的只读检查返回 `enabled: false`，未执行远端 CI。

## English release notes

This normal merge integrates upstream v0.2.24 without changing dependency versions or database schemas. Local Engine, Daybreak, authorization generations, billing, prompt-audit penalties, permanent quota subscriptions, SMTP, Codex 0.171 telemetry and official status integration remain intact.

ToolSearch and buffered hosted outputs preserve original tool names. Catalogs preserve official descriptor fields and supported Chat audio routing. Request validation and schema processing reuse request-local read-only data; retries do not multiply full-body validation or copies. OAuth refresh rotation persists through the existing field-level authorization CAS rather than introducing a second credentials writer.

Grok authoritative terminal search counts, including zero, replace inferred counts once per call. Safety rejection settlement remains atomic and excludes successful search/media units. Grok edits accept five source images. Antigravity bare-429 cooldown applies to at most one account per request, and quota requests follow the forwarding endpoint. Existing Engine delivery and disconnect billing rules are unchanged.

Deployment is separate from this merge. No production configuration or model endpoint is exercised by validation. Use the matching application version when reverting; this merge adds no database migration.
