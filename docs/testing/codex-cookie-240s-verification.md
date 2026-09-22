# Codex HTTP Cookie 与 240 秒 turn-state 验证

本记录对应 `dev@a373c64ea` 上的本次工作树改动。仅使用合成凭据、本地模拟端点和隔离测试数据库，不访问真实业务、OAuth 授权端点或遥测端点。

## 隔离存储环境

- Windows 工作树通过 WSL `Ubuntu-24.04` 执行；Go `1.26.4 linux/amd64`，Docker `29.4.0`。
- 使用仓库 `internal/repository/integration_harness_test.go` 的 Testcontainers：PostgreSQL `18.1-alpine3.23`、Redis `8.4-alpine`；每次测试命令创建独立容器及数据库，并执行完整数据库迁移。
- `CI=true` 将 Docker 不可用视为失败，避免把整套跳过误报为通过；`GOEXPERIMENT=jsonv2` 与现有集成测试环境一致。
- 不使用已有数据库或业务容器。合成授权、Cookie、票据和代理均由测试夹具产生，不需要真实凭据。

从 WSL 中的 `backend` 目录执行：

```bash
unset OPENAI_API_KEY DATABASE_URL TEST_DATABASE_URL REDIS_URL
export CI=true GOEXPERIMENT=jsonv2
go test -race -p 2 -tags=integration ./internal/repository \
  -run '^(TestOpenAIHTTPCookieStore.*|TestCodex(State|TurnState|Collector|History|Demand|MultiProxy).*|TestOAuthOS.*|TestMigrationsRunner.*|TestAccountRepoSuite)$/^([^T].*|TestOAuthOS.*|TestCodexTurnState.*)$' \
  -count=1 -json
```

Git Bash 调用 WSL 脚本时添加 `MSYS_NO_PATHCONV=1`，防止 `/mnt/d/...` 被改写为 Windows 程序目录。最初一次启动遇到此路径转换错误，修正后测试实际执行。

## 首轮结果与夹具修正

首轮真实存储竞态测试执行 64 个顶层测试：61 个通过、3 个失败，含子测试共 141 个通过、7 个失败；没有跳过，也没有竞态报告。

- Cookie 的加密持久化、绝对期限、并发合并、授权代次隔离、撤销锁与异常数据处理五项全部通过。
- 240 秒迁移、采集状态 CAS、需求恢复、跨系统隔离、Redis 单飞及通知、代理变更保留缓存等定向测试通过。随后增加的更早到期与迁移边界用例需要纳入最终轮。
- `TestCodexMultiProxyPostgresConfiguration` 和 `TestCodexMultiProxyPostgresMissingProxyAndDeletionLock` 的旧断言直接比较账号兼容代次与系统槽代次。现有夹具及配置更新返回的是系统投影，读取后现已按同一系统投影再比较；保留原回滚与代理删除并发断言。
- `TestOAuthOSIdentityFacadeRealRedisIsolation` 的旧夹具只有安装身份，没有系统授权。现已为三个系统分别绑定合成授权，再验证真实 Redis 的身份隔离；没有放宽未授权系统的生产准入。

首轮日志保存在本机 `.git/task-artifacts/codex-cookie-240s/integration-race.jsonl`，不加入版本控制。

## 最终隔离存储结果

Cookie entry revision/tombstone 协议、迁移 253/254、较早到期边界及上述夹具修正全部落盘后，以同一命令重新创建隔离 PostgreSQL/Redis 并运行竞态测试：**67 个顶层测试全部通过，含子测试共 157 项通过，用时 15.337 秒，无跳过、无竞态报告**。

- migration253 的 11 个子例通过，包括旧 60 分钟状态收敛到 240 秒、保留更早到期时间、有效代理切换缓存保持到实际到期、在途预留清理，以及保留真实限流和账号冷却。
- 单条和批量修改采集代理时，更早到期缓存的票据及原期限保留；临期扫描不提前唤醒此保留窗口。
- migration254 随全量迁移实际执行。CookieStore 的 7 个顶层测试通过，包含同条目并发提交版本、跨 Manager session 删除/替换后失效、加密数据、绝对过期时间、授权隔离和撤销行锁。
- PostgreSQL/Redis 下的按系统授权、身份映射、采集单飞、版本 CAS、通知补偿、迁移并发与幂等，以及之前失败的三个夹具测试全部通过。

最终日志：本机 `.git/task-artifacts/codex-cookie-240s/integration-race-final.jsonl`，不加入版本控制。

随后仅增加 migration253 的 `retained_proxy_demand` 边界子例，生产 SQL 未再次改变。单独以 `-run '^TestCodexStateShortLifetimeMigrationPreservesHistoryAndLimits$'` 重新创建隔离存储并运行竞态测试，最终 **12 个迁移子例全部通过**（含父测试共 13 项，5.630 秒，无跳过、无竞态）。该例验证旧需求非空时，正常代理切换缓存仍保留至实际到期；两个保留窗口的展示原因也按实际扫描结果断言。日志位于本机 `.git/task-artifacts/codex-cookie-240s/migration-boundary-race.jsonl`。

## 其他相关检查

以下为同一工作树各负责者回传的验证结果：

- 前端：7 个相关测试文件、117 项测试通过；类型检查、改动文件 ESLint 和构建通过。本地模拟接口浏览器验收覆盖桌面 1440px、小屏 390px，以及弹窗 5 秒刷新和关闭取消，通过。
- Cookie 核心包竞态测试通过，覆盖持久 Cookie 与本地 session 的版本协调、跨实例删除/替换、时钟偏差、创建顺序、缺少 scope 和 WS 跳过。
- Cookie 辅助 API 的 12 条模拟流路径竞态测试通过；根任务针对实际 `Do` 的普通 HTTP、原生 HTTP、跨代理 Cookie 与 scope 测试通过。最终仓储 Cookie/native transport 竞态回归用时 6.526 秒；API Key 模型目录清理继承 OAuth Cookie scope 的补充竞态回归通过，6.590 秒。服务端入口编译通过。对应本机日志为 `tmp-cookie-root-repo-race.log`、`tmp-cookie-root-scope-final.log`、`tmp-cookie-server-compile.log`。
- `TestCodexTurnStateWorkersBoundConcurrencyAndStopCancelsCollectors` 在 Linux 下启用 `-race` 通过：17 个独立凭据账号排队，最多 16 个 collector 同时开始；Stop 取消全部在途任务并退出。
- 新模型准入覆盖现有 OpenAI 明确拼写别名；Grok 别名、日期和 `latest` 后缀不被用来猜测 Codex 模型等价。对应模型证据定向测试通过。
- turn-state 相关回归中尚有一项与本次功能无关的断言失败：`TestCodexStateIntegrityTrustsOnlyExactPatch`（`openai_codex_state_http_test.go:216`）期望 `input[0]...` 路径，当前请求完整性检查器返回 `input`。本次未修改该检查器或此断言，不将整套测试记为通过。

没有部署、发布标签、等待 CI 或发送真实收费请求。容器由 Testcontainers 自动回收；首轮及最终补测结束后未发现残留的 Testcontainers 容器。
