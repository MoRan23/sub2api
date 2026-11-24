# 订阅代充验证记录 / Subscription top-up validation

日期：2026-11-24。基线：`c4ad315b26ee2e4b91d929848c05ac620e2db321`。

## 验证范围

- 商品／规格、CNY 定价、部分更新、持久密钥准入及卡密批量输入校验。
- 独立管理权限、导入正文不进入审计日志、订单所属用户校验、匿名支付结果不返回卡密。
- PostgreSQL 整单预占、并发库存竞争、重复回调、释放与迟到付款、缺货 FIFO、不同规格独立恢复、发卡和邮件入队事务回滚。
- 新代充类型退款阻止、旧充值／订阅金额与微信支付恢复参数回归。
- 卡密邮件 HTML 转义、本机 SMTP、稳定 Message-ID、显式重发、持久化领取租约及重试。
- 用户商品购买、数量限制、CNY 支付方式、订单卡密、支付回跳、管理员库存与异步详情竞态。
- CDK 页 USDT 指引和 `/purchase?tab=recharge` 跳转。

## 环境与命令

Go 1.27.4，`GOEXPERIMENT=greenteagc,jsonv2`，WSL Ubuntu 24.04。Go 检查使用 `GOMAXPROCS=2`、`-p 1`；数据库验证使用一次性 PostgreSQL，邮件发送仅使用本地测试 SMTP。

```sh
go run github.com/google/wire/cmd/wire ./cmd/server
go test -p 1 ./internal/server/middleware ./internal/pkg/adminaccess -run 'TestSubscriptionTopup|TestAudit' -count=1
go test -p 1 -tags=unit ./internal/service ./internal/handler ./internal/handler/admin -run 'TestTopupPayment|TestApplyWeChatPaymentResumeClaims|TestSanitizeTopupOrder|TestValidateOrderInput|TestFulfillmentRejectsUnknownOrderType|TestCalculateCreateOrderPayAmount|TestCreateWeChatPaymentResumeToken|TestParseWeChatPaymentResumeToken|TestPaymentServiceParseWeChatPaymentResumeToken|TestWeChatPaymentResume|TestWeChatPaymentOAuthCallback|TestPaymentOrder|TestVerifyOrderPublic|TestResolveOrderPublicByResumeToken'
go test -p 1 -tags=integration ./internal/repository -run '^TestSubscriptionTopup' -count=1 -timeout=8m
go test -race -p 1 -tags=integration ./internal/repository -run '^TestSubscriptionTopup' -count=1 -timeout=8m
CGO_ENABLED=0 go build -p 1 -o /tmp/sub2api-topup-check ./cmd/server
pnpm run build
git diff --check
```

前端定向测试包括 `SubscriptionTopupStorefront`、`SubscriptionTopupDelivery`、`PaymentView`、`PaymentResultView`、`UserOrdersView`、`CustomPageView`、`paymentWechatResume`、`paymentFlow`、`AdminSubscriptionTopupsView` 和 `AdminOrdersView.giftRefund`。仅对涉及文件执行 ESLint。生产构建同时执行中英文键完整性检查、Vue 类型检查和 Vite 编译。

## 已确认结果

- Wire、Ent 生成完成，依赖锁文件未变更。
- 管理权限及导入审计脱敏测试通过。
- 支付与协议恢复定向单元测试通过。
- 核心 PostgreSQL 集成测试通过，包含迁移重复执行、跨规格补货及订单所属用户检查。
- 全部代充核心与实际 PaymentService PostgreSQL 集成 race 通过（6.983 秒）。本地 EasyPay 模拟覆盖真实渠道选择、创建失败释放、签名及付款核验、原子发卡、重复回调和长语言头；余额、赠送余额及累计充值均保持不变。
- 本地 SMTP、邮件服务生命周期和用户／管理员接口竞态测试通过；隔离 PostgreSQL 邮件领取／过期恢复／重试上限测试也在 race 模式下通过。
- 用户前端 84 项测试通过；管理员库存及订单测试通过；定向 ESLint 通过。
- 后端编译和前端生产构建通过。

构建保留现有 Browserslist 数据过期、混合静态／动态 import 和大 chunk 提示；未为本功能升级依赖。远端 CI 未在本次本地验证中执行；本机未安装 `gh`，未重新读取 Actions 设置。

验证过程中修复了长 `Accept-Language` 写入邮件语言字段、支付回跳仅有公开订单字段而无法显示卡密、管理员详情乱序覆盖及缺货规格阻塞其他规格恢复的问题。首次模拟支付断言按现有手续费向上取整口径从 25.17 修正为 25.18，计价实现未改变。

## 数据与性能边界

目录查询只读取商品、规格和库存计数。卡密库存列表只选取遮罩及状态字段，只有授权查看和发货邮件解密卡密。管理员订单列表不逐行查询邮件，详情才读取一项邮件状态。导入上限在 HTTP 读取和条目校验处约束，原文不复制进操作审计；加密在库存事务外完成。库存事务按规格、订单、卡密顺序加锁；后台重试有批量与超时限制，SMTP 在事务外发送。

本次未进行真实支付、真实卡密导入、真实 SMTP 发送、部署或生产配置修改。已有排查文件保留。

## English summary

Validated product/SKU configuration, CNY checkout, encrypted inventory, owner-only code retrieval, separate admin permissions, reservation concurrency, late payments, whole-order FIFO restocking, atomic delivery/outbox persistence, refund exclusion, payment resume compatibility, and the user/admin interfaces. PostgreSQL instances were isolated and SMTP tests used localhost. Wire/Ent generation, focused tests, backend compilation, frontend type checking, production build and targeted lint passed as recorded above. No live payment, inventory import, production email or deployment was performed.
