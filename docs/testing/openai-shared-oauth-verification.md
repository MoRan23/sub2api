# OpenAI OAuth 共享授权验证

基于 `dev@97c65416b`，对照引入系统独立授权前的 `a5411ee97^` 实现。本次只使用
合成凭据、模拟 HTTP 服务以及隔离 PostgreSQL/Redis，不调用真实 OAuth、业务、采集
或遥测端点。

## 刷新与请求

保留原账号级 cache → 提前刷新 → 统一 `RefreshIfNeeded` → 锁等待/竞争恢复 →
缓存写入流程。3 分钟提前刷新、5 分钟缓存余量及有效旧 token 临时刷新失败后的
短 TTL 均保持原策略。三个系统不再作为刷新队列或刷新锁的分区。

相关 service/handler 回归与竞态测试覆盖：

- 三系统共用完整 token、授权代次及 revision；UA、installation ID、根、系统池独立。
- 共享授权摘要、旧摘要默认系统兼容、Spark 母账号、Top-K、粘性会话及插件账号目录。
- Responses、透传、Chat/Messages、Live、模型目录、用量辅助调用与 WS 身份冻结。
- 原刷新 API 的本地/Redis 锁、数据库重读、refresh-token 竞争恢复与临时失败短缓存。
- 三系统同时刷新只执行一次；永久失败按实际快照 CAS，迟到失败不损伤新授权。
- RT-only 凭据即使带未来 expires_at，也先刷新获得 access token；锁等待不能返回
  已过期缓存 token。撤销/重新授权使所有旧身份授权快照失效。
- 账号级授权与撤销、旧 OS 路由兼容、auth.json 导出、旧多系统备份选取完整单份凭据。

根任务在 WSL 中以 `-race` 执行 service、handler、admin、routes 扩大回归。第二轮
457 项测试/子测试通过；一个旧插件目录断言仍要求过滤未授权 OS，已按共享授权
契约修正并单独复测。其余两个失败经原始 HEAD 副本验证属于既有问题，详见下文。
各负责模块的定向回归与竞态检查也分别通过。
最终服务端入口 `go test ./cmd/server -run '^$' -count=1` 编译通过（5.478 秒）。

## PostgreSQL / Redis

使用 WSL Ubuntu-24.04 的 Docker/Testcontainers，每次创建独立 PostgreSQL
18.1-alpine3.23 与 Redis 8.4-alpine。设置 `CI=true`、`GOEXPERIMENT=jsonv2`，不允许
Docker 不可用时静默跳过。未使用业务数据库或既有业务容器。

- 完整 `TestAccountRepoSuite`、251/255 迁移及共享仓储竞态测试通过，14.880 秒。
  包含默认可用授权优先、其他授权接续、过期且缺 RT、失败凭据保留、撤销不复活、
  非目标账号、幂等、跨 OS CAS 单胜、旧快照保护和三系统运行态版本同步。
- 根任务的运行态/Cookie/历史/代理/迁移扩大集成第一轮有 141 项通过，无跳过。
  两个失败定位为测试夹具：旧三授权顺序绑定会使先前缓存代次失效；历史夹具把
  Team/Business 异常长度误写成 352，正确值为 356。
- 夹具修正后，在新建 PostgreSQL/Redis 中对两项失败及子例启用 `-race` 复测，
  全部通过，5.700 秒，无跳过或竞态。未放宽生产授权、票据形态或代理缓存保留校验。
- 新用例覆盖共享授权撤销后旧 OS 元数据不能读写缓存、历史从共享凭据读取套餐、
  同一共享授权代次下三系统 Cookie 分池，以及代理修改保留三系统正常缓存。

## 前端

10 个相关测试文件、170 项测试通过；类型检查、完整 `lint:check`、构建通过。
覆盖账号级授权/撤销、三身份展示、默认身份切换、测试系统不受旧授权状态限制、
无 OS 选择的 auth.json 导出、step-up，以及创建/编辑回归。

本地模拟 Chrome 验收通过，所有网络请求限定到 127.0.0.1 的 mock，无 pageerror。
覆盖桌面 1440×1100、小屏 390×844、统一授权卡和三身份卡、旧未授权 Linux 可设
为默认身份、三个系统均可用于连接测试，以及不带 OS 参数的 auth.json 下载。
截图保存在本机 `.git/task-artifacts/shared-oauth/desktop-shared-authorization.png`
和 `mobile-shared-authorization.png`。模拟服务与浏览器已关闭。

## 既有失败与测试夹具

在 `git archive 97c65416b backend` 生成的未修改源码副本中，以同样 `-race` 独立
复现了以下两个失败，未改变生产行为或放宽其断言：

1. `TestOpenAIHTTPPassthroughStripsOnlyOAuthLegacyResponsesBeta` 的 API Key 子例：
   期望保留 `responses=experimental`，现有代码将其移除。
2. `TestOpenAISetupTokenIdentityFlagOffAndAPIKeyStayNoOp`：旧断言要求正文完全不变，
   现有缺省 Codex 指令补充会改变正文。

首轮扩大回归还暴露共享身份测试 stub 缺少异步 `UpdateExtra` 实现导致的 panic。
已补齐该测试替身方法；未改变业务生产代码。第二轮未再出现此 panic。

本机详细日志保存在 `.git/task-artifacts/shared-oauth/`：`regression-final.jsonl`、
`integration.jsonl`、`runtime-fixes.jsonl`、`baseline-known-failures.jsonl`；原始测试
日志与基线副本不纳入版本控制。升级须按 [共享授权说明](../OPENAI_SHARED_OAUTH_AUTHORIZATION.md)
排空旧进程后迁移，不混跑旧版本。未部署、未发布标签、未发送真实上游请求。
