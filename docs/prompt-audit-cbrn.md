# CBRN 主题与操作意图拦截 / Topic-and-intent blocking

> **当前版本**：CBRN 已统一为可自行扩展的组合规则组，与政府规则同为可编辑预设。新增、改名、排序和删除均可在页面完成；旧 CBRN 配置和历史兼容。当前用法与接口见 [通用组合规则](prompt-audit-combination.md)。以下保留首版实现及其验证记录，`cbrn_*` 字段用于兼容，新拦截统一使用 `prompt_combination_blocked`。
>
> **Current version:** CBRN is now an editable preset of the generic combination-policy feature. See [configurable combination policies](prompt-audit-combination.md) for current UI/API usage. The original release notes and verification below are retained for historical reference.

## 配置和行为

在管理员「提示词审计」中开启独立的「核、生物、化学、放射性危害」开关。默认关闭；主题列表预置武器主题与重点物质／病原体规则，操作意图列表预置制造、获取、投放及索取步骤等规则。原有关键词列表不变。

两个列表内部任意一条匹配即可；必须同时命中主题和操作意图才返回 HTTP 403，错误码为 `prompt_cbrn_blocked`。这是本地直接拦截，不增加 Guard 请求。原关键词优先，两者都未拦截时继续现有审核流程。单独出现主题不产生新的拦截记录。

- 操作意图仅匹配最新用户输入；主题可来自本次请求携带的文本及历史。不会额外保存会话历史或查询 `previous_response_id`。
- 最新一条没有可识别用户文本时不触发新规则；工具返回不充当当前用户意图。
- 同一请求中分散在不同文本块的主题和意图可以组合；单条正则不会跨消息拼接匹配。
- Gemini 批量结构中的独立子请求分别配对主题与意图；任何一条命中即可拒绝本批，不在不同子请求之间组合命中。
- 开关独立于原关键词和 Guard 开关，仍受风险控制总开关约束；共用关键词生效分组与精确到用户和 Key 的白名单。
- 「科研、小说、教学、防护」不自动豁免。例如同时出现危险主题和制造操作词的防护、否定表达仍可能被直接拦截；选择这些规则时应考虑这一行为。

每行一个字面词或以小写 `regex:` 开头的 Go/RE2 正则。字面词忽略大小写；正则自行设置 `(?i)`，预置规则已启用。主题与意图合计最多 10,000 条，字面词最多 200 个 Unicode 字符，正则含前缀最多 2,000 个。启用前两侧都须有规则；拒绝无效和可匹配空字符串的正则。保存时编译，运行时共享不可变匹配器。

仅新检测器的匹配视图应用 Unicode NFKC 和零宽字符清理（U+200B、U+200C、U+200D、U+2060、U+FEFF）。上游正文、原关键词匹配与审核原文不被改写。客户端仅收到限长、转义后的原文命中内容，不收到正则。管理员详情额外显示主题和意图的规则、来源段以及原文字符位置；位置为从零起始的 Unicode 字符索引，结束位置不包含在命中内。

## 接口与兼容

原配置接口增加 `cbrn_blocking_enabled`、`cbrn_topic_rules`、`cbrn_intent_rules`。旧配置默认关闭；旧客户端省略新字段时保留其现值。事件详情增加可选 `cbrn_match`（`topic`、`intent`），每项包含 `rule`、`content`、`source`、`segment_index`、`start`、`end`。仅本地检测器能写入该证据；普通列表和外部 Guard 输出不会暴露或填充保留字段。

复用现有设置及审计事件 JSON，不新增数据库迁移。部署新版后由管理员手动开启。回退前关闭新规则；旧版本不会执行该策略。历史事件缺少证据时不按当前配置推算。

## English

CBRN blocking is a separate, default-off local policy. The topic rules and intent rules each use OR; both sides must match to block. Only the latest actual user input supplies intent. Topics may come from text and history carried by the request. No remote history lookup or extra Guard call occurs. Existing keyword rules run first, and normal auditing continues if neither local policy blocks.

The policy shares keyword group scope and the exact user/API-key allowlist, while its switch is independent. Research, fiction, educational and protective wording is not an automatic exemption; protective or negated phrases can still satisfy this direct combination rule.

Independent entries in Gemini batches are evaluated separately. A matching entry blocks the batch; a topic in one entry and intent in another never form a match.

Use one literal or `regex:` Go/RE2 expression per line. Presets use `(?i)`. The combined limit is 10,000 rules; literals allow 200 Unicode characters and regex rules 2,000 including the prefix. Invalid and empty-matching regexes are rejected. Patterns compile once when configuration is activated.

Only the new matching view uses NFKC and removes U+200B/U+200C/U+200D/U+2060/U+FEFF. Original request content and legacy keyword behavior remain unchanged. The HTTP 403 response (`prompt_cbrn_blocked`) includes bounded, quoted original matches, never regex definitions. Administrator event details include both matches and original segment/rune locations. Old records remain readable without inferred evidence.

The existing settings and event JSON formats are extended; no schema migration or automatic activation is needed. Disable the new policy before rolling back to a version without CBRN support.

## 验证记录 / Verification (2026-11-11)

- Go 1.27.3，`GOEXPERIMENT=greenteagc,jsonv2`：审核包单元测试及竞态检查、HTTP／WS CBRN 错误适配测试、后端编译通过。
- 隔离 PostgreSQL 18.1／Redis 8.4 中执行完整审核包：配置持久化、旧客户端更新、事件详情往返、列表隐藏及外部 Guard 证据隔离通过，零跳过。容器随后清理。
- 前端相关测试 67 项通过；类型检查、定向 ESLint、语言键完整性及生产构建通过。构建仍提示现有依赖数据库过旧、动态导入和 chunk 大小警告。
- 本地大文本 benchmark 使用预编译的默认规则、末尾命中：64 KiB 约 26.5 ms／85 KB 分配；1 MiB 约 347 ms／1.09 MB 分配。命中后才定位原文，不为每个输入字符建立长期位置表。
- 无线上规则修改、部署或真实模型调用。

Reproduction commands (run in `backend/`, with test-only database variables for the integration run):

```bash
GOEXPERIMENT=greenteagc,jsonv2 go test ./internal/securityaudit -count=1
GOEXPERIMENT=greenteagc,jsonv2 go test -race ./internal/securityaudit -count=1
go test ./internal/handler -run Combination -count=1
go test ./internal/securityaudit -run '^$' -bench BenchmarkCBRNMatcherLargeRequest -benchmem
go build -o /tmp/sub2api-cbrn ./cmd/server
```

Set `PROMPT_AUDIT_TEST_POSTGRES_DSN` and `PROMPT_AUDIT_TEST_REDIS_ADDR` to isolated instances to run all persistence tests. Frontend checks use `pnpm test:run src/features/prompt-audit`, `pnpm typecheck`, directed ESLint and `pnpm build`. No production systems are required.
