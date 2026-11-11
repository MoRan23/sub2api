# 通用主题与操作意图组合规则 / Configurable combination policies

## 管理方式

管理员「提示词审计」中的组合规则支持自行新增、命名、启停、排序和删除规则组。每组编辑两份列表：主题规则和操作意图规则；每行一个字面词或 `regex:` Go/RE2 正则。自定义规则保存后热更新，以后增加检测主题无需发布新版。

CBRN（核、生物、化学、放射性危害）和政府目标攻击是可选预设，与自定义组使用同一匹配器、存储和界面。添加预设时默认关闭，规则可以修改或删除。升级保留现有 CBRN 的开关、两份规则及检测范围，不自动开启政府或其他新增规则。

- 同一列表内为 OR，两份列表之间为 AND。主题可以来自本次请求携带的文本和历史；操作意图仅取最新用户输入。
- 破甲关键词先执行，然后按页面顺序检查启用的组合组，最后检查其他关键词。首个匹配组直接返回 HTTP 403；未拦截时继续原有审核流程，不额外调用 Guard。
- 先检查全局关键词白名单，命中后跳过破甲、组合和其他关键词检测。未命中时，各组合再检查自己的白名单；仅跳过对应组合，继续其他组合、其他关键词和后续审核。两层白名单均精确到「用户＋API Key」，不豁免同用户的其他 Key，也不豁免 Guard。
- 组合组共用关键词的生效分组，每个组的开关和单独白名单独立，受风险控制总开关约束。
- 没有可识别的最新用户文本时不触发组合检测。工具输出不充当当前用户意图，不额外保存历史或通过 `previous_response_id` 查询历史。
- 不同文本块可以分别提供主题与意图；单条规则不跨消息拼接。Gemini 批量子请求独立匹配，不在不同子请求间组合命中。
- 新闻、科研、防护、否定表达没有自动豁免，文本同时满足两侧条件仍会拦截。规则选择和范围由管理员决定。

所有组的主题与意图规则合计最多 10,000 条。字面词最多 200 个 Unicode 字符且忽略大小写；正则含 `regex:` 前缀最多 2,000 个字符，大小写策略由表达式决定，预设使用 `(?i)`。启用的组必须两侧都有有效规则，拒绝非法及匹配空字符串的正则。配置发布时编译，运行时复用不可变快照。同次组合检查只解析一次正文，按需规范化后的文本在各组间复用，准备视图随请求释放。

匹配视图使用 Unicode NFKC，清除 U+200B、U+200C、U+200D、U+2060、U+FEFF，再跳过文件路径及带扩展名的文件名。过滤同时用于主题和意图，也用于自定义组合；请求正文、审核原文及破甲／其他关键词匹配行为不变。

路径规则覆盖 Windows 盘符、UNC、Unix、相对路径和带英文引号／反引号的空格路径。HTTP(S) URL 只保留主机名供匹配，路径、查询参数等不参与组合检测；裸 `www.gov.cn/path` 按路径排除。政府预设的域名规则仅识别 `gov.cn`、`.gov.cn` 及其子域，不再包含其他国家或通用 `.gov` 后缀；政府机构名称规则仍单独存在。路径两侧文字不会拼接成一次正则命中。带空格的路径需要使用引号，例如 `"C:\\工作资料\\政府 攻击报告.txt"`。

路径与规范化视图按文本段惰性建立，同一请求的不同组合复用；保留可匹配区间而不为每条规则复制全文。正则的 `^`／`$` 作用于这些保留区间，匹配不能跨越排除范围。只有实际命中时才计算原文证据位置。新版预设只用于新增预设组，不覆盖已经保存的自定义规则或线上配置。

## 旧关键词迁移

每组可选择填写「启用时迁移的精确关键词」。保存启用的组时，从破甲和其他关键词列表移除对应字面词，比较时忽略大小写和首尾空白。此项支持自定义词，不限于任何内置预设；禁止填写正则。

政府预设默认声明 `.gov`、`.gov.`。启用并保存后只移除这两条精确字面词；`.gov.cn`、`agency.gov`、自定义正则等保留。若本次移除使两类关键词列表均为空，同时关闭关键词总开关。保持该组启用时，旧客户端再次提交这些字面词也会在保存时移除；关闭或删除组合组不会恢复旧词。保存返回最终配置，页面同步显示迁移结果。

## 接口和兼容

复用原配置接口，新增 `combination_policies` 有序数组，例如：

```json
{
  "combination_policies": [
    {
      "id": "custom-topic",
      "name": "自定义组合",
      "enabled": true,
      "topic_rules": ["示例主题", "regex:(?i)example-topic"],
      "intent_rules": ["示例操作"],
      "migrate_keywords": ["旧的独立词条"],
      "keyword_whitelist_keys": [{"user_id": 2, "api_key_id": 1931}]
    }
  ]
}
```

ID 必须唯一、长 1–64 个 ASCII 字母/数字/下划线/短横线且以字母或数字开头；名称长 1–80 个 Unicode 字符，不含控制字符。读取返回 `combination_templates` 预设及 `combination_blocking_active`。更新省略 `combination_policies` 保留现有组，显式空数组删除所有组。

单组 `keyword_whitelist_keys` 在更新时省略或传 `null`，按组 ID 保留已有名单；显式 `[]` 清空。新组缺失时为空名单。每组最多 1,000 个 Key，各组合合计最多 10,000 个条目。保存时仅对同一组 ID 下新增的用户／Key 配对批量校验归属和删除状态；已保存后失效的条目可以保留或移除，新组则校验全部配对。运行时使用已编译的不可变索引，不查询数据库。管理页面通过现有身份查询接口显示用户邮箱、用户名和 Key 名称，不展示完整密钥。

没有新数组的旧 CBRN 配置在读取时转换为 `id=cbrn` 的组，下次保存持久化；显式新数组是权威配置，包括空数组。旧 `cbrn_*` 字段保留兼容投影。旧客户端省略数组仅可更新仍存在的 CBRN 组，不会丢失其他自定义组。已删除的 CBRN 不会被旧页面快照重新添加；重新添加请使用通用页面或显式提交新数组。

新组合拦截统一使用 `prompt_combination_blocked`。客户端仅显示限长、转义后的组名、警告和成功记录的次数，不返回主题、意图片段或正则。管理员事件详情 `combination_match` 冻结组 ID/名称和双方证据，每项包括规则、原文内容、来源段、段序号及原文起止字符位置。字符位置从零开始，结束位置不包含在命中内。普通事件列表不附带这些详细证据，外部 Guard 不能写入本地保留证据。处罚流程见 [关键词分类、违规计数与自动封禁](prompt-audit-enforcement.md)。

已保存的 `prompt_cbrn_blocked` / `cbrn_match` 历史仍正常展示，不按当前配置重新推算。无需数据库列迁移。部署不修改线上规则；首次读取兼容旧配置，管理员保存后使用新格式。若回退到仅支持 CBRN 的版本，自定义组合组不会执行，应先导出配置并确认所需策略。

## English

Administrators can add, rename, enable, reorder, edit and delete arbitrary combination policy groups without a new release. CBRN and government-target rules are optional, editable templates using the same runtime as custom groups. Templates start disabled. Existing CBRN settings and history are retained.

Each group ORs its topic rules and intent rules, then ANDs the two results. Topics may come from text/history carried by the request; intent comes only from the latest actual user input. Jailbreak keywords run first, followed by enabled groups in configured order, then other keywords. The first matching group blocks locally; otherwise existing auditing continues. Group scope and the risk-control master switch are shared. No remote history lookup occurs. Independent batch entries cannot supply matches for each other. Research, defensive, news or negated wording does not grant an automatic exception.

The global exact user/API-key whitelist runs first and exempts all local keyword and combination checks. Each combination then checks its own `keyword_whitelist_keys`: a match skips only that combination, leaving other combinations, ordinary keywords and Guard active. Neither level exempts another key owned by the same user. Missing/null per-policy lists preserve existing entries by policy ID; `[]` clears them. New policies default to an empty list. Lists allow 1,000 keys each and 10,000 entries across all combinations. Saving batch-validates newly added pairs under each policy ID; previously saved pairs that became unavailable may be retained or removed, while a new policy must validate every pair. Published runtime lookup sets are immutable and require no database query.

Rules accept literals or `regex:` Go/RE2 expressions. All groups share a 10,000-rule limit, with 200-character literals and 2,000-character regexes including the prefix. Enabled groups require valid nonempty sides; invalid and empty-matching patterns are rejected. Patterns compile at configuration activation. Only the combination matching view uses NFKC, zero-width removal and path exclusion; original request bytes and keyword matching stay unchanged.

All combination topics and intents skip Windows/UNC/Unix/relative paths and filenames with extensions. Quote paths containing spaces. HTTP(S) URLs retain only the hostname; bare `www.gov.cn/path` is excluded as a path. The government domain preset now recognizes only `gov.cn`, `.gov.cn` and their subdomains. The separate government-organization rule remains. Excluded ranges form hard boundaries, so a single regex cannot bridge a removed path; `^`/`$` refer to the retained spans. Views are prepared lazily once per text segment and shared across policies; only actual matches project evidence back to the original text. Updated templates do not overwrite saved rules.

The optional per-group `migrate_keywords` field retires exact legacy literals when the enabled group is saved. Government's template declares `.gov` and `.gov.`. Matching is case-insensitive and trims outer whitespace; regexes and other literals remain. An emptied keyword list is disabled only after an actual removal. Disabling/deleting the group does not restore retired literals.

The ordered `combination_policies` array is authoritative: omitted updates preserve groups, an explicit empty array deletes them. Legacy CBRN-only storage is projected into a `cbrn` group, and old-client saves retain custom groups. Read responses provide templates and active state. No database column migration is needed.

New blocks return HTTP 403 with `prompt_combination_blocked`, the bounded/quoted policy name, a warning and the committed violation count. Matches and regex definitions are administrator-only. Administrator detail uses `combination_match` with frozen policy identity and original-text evidence. External scanners cannot create local evidence. Legacy CBRN records remain readable. See [enforcement behavior](prompt-audit-enforcement.en.md). Downgrades to versions without per-policy whitelists resume matching those exempted keys; export configuration and review required policies before rollback.

## 验证 / Verification

Run backend checks in `backend/` using Go 1.27.3:

```bash
GOEXPERIMENT=greenteagc,jsonv2 go test ./internal/securityaudit -count=1
GOEXPERIMENT=greenteagc,jsonv2 go test -race ./internal/securityaudit -count=1
GOEXPERIMENT=greenteagc,jsonv2 go test ./internal/handler -run 'Combination|Keyword' -count=1
GOEXPERIMENT=greenteagc,jsonv2 go build ./cmd/server
```

Set `PROMPT_AUDIT_TEST_POSTGRES_DSN` and `PROMPT_AUDIT_TEST_REDIS_ADDR` to isolated instances for persistence tests. In `frontend/`, run `pnpm test:run src/features/prompt-audit`, `pnpm typecheck`, targeted ESLint, and `pnpm build`. No production model calls are needed.

### 既有基线验证 / Previous baseline results (2026-11-11)

- 审核包单元、HTTP／WS 错误适配、Go race 和后端编译通过；覆盖自定义领域、顺序调整、旧配置迁移、删除不复活、原文定位与外部证据隔离。
- 隔离 PostgreSQL 18.1／Redis 8.4 中完整审核包通过，0 失败、0 跳过；测试容器已清理。配置、旧词迁移、证据、事务回滚和重启恢复通过。
- 前端 75 项相关测试、类型检查、定向 lint、语言键完整性与生产构建通过。构建保留现有 Browserslist 数据、混合动态导入和大 chunk 提示。
- 单请求准备视图的短基准：8 组规则、1 MiB 历史＋最新短输入、均未命中时，重复准备约 8.41 MB／315 次分配，复用准备约 1.05 MB／39 次分配；64 KiB 对应约 540 KB／312 次降至 67.5 KB／39 次。单组分配基本不变。一次 100ms 基准用于定位解析复用收益，未做生产压测。

Backend unit, protocol-adapter, race and build checks passed. The full audit package passed against isolated PostgreSQL/Redis with no failures or skips. Frontend checks passed (75 related tests, types, targeted lint, locale completeness and production build); existing build advisories remain. The request-local prepared view avoids repeated JSON parsing and normalization across policy groups; the short synthetic benchmark above compares eight groups without changing match priority or cross-request ownership.

```bash
GOEXPERIMENT=greenteagc,jsonv2 go test ./internal/securityaudit -run '^$' -bench BenchmarkCombinationMatcherPreparedPolicies -benchmem -benchtime=100ms
```

No deployment, live rule changes or production model calls were performed.

### 本次路径过滤和独立白名单验证 / Path filtering and per-policy whitelist results (2026-11-12)

- 全部审核包及隔离 PostgreSQL／Redis 测试通过；完整审核包 race、关键词／组合网关测试、`go build ./...` 通过。
- 前端 78 项定向测试、类型检查、定向 lint、生产构建通过。
- 三轮基准记录了旧版本、新增过滤初版及优化后的实现；白名单豁免在 64 KiB／8 MiB 正文下均为 64 B/op、1 alloc/op。长转义 URL 的辅助分配固定为 320 B/op、4 allocs/op，不随被排除后缀长度增长。
- 原始数据、运行命令、额外区间存储成本及未执行项目见[验证报告](prompt-combination-validation-20261112.md)。已保存的生产配置保持不变。

Audit tests with isolated PostgreSQL/Redis, the audit race suite, keyword/combination gateway tests and the backend build passed. Frontend validation passed 78 focused tests, type checking, targeted lint and the production build. Three paired benchmark rounds separate the cost of the new path-filtering feature from the subsequent allocation optimization. See the [validation report](prompt-combination-validation-20261112.md) for raw data, commands and explicitly unexecuted checks. Existing production rules were not modified.
