# 订阅代充 / Subscription top-ups

## 中文

“充值／订阅代充”页面销售外部服务的代充卡密，由管理员在“订单管理 → 订阅代充（卡密）”配置商品、规格和卡密池。商品无需绑定 Sub2API 分组或设置站内订阅有效期。原套餐继续在“站内订阅套餐”中管理，已分配权益和历史订单保留；代充订单只交付外部服务卡密。

### 配置与购买

1. 创建商品，填写名称、说明和使用方法；添加规格，为各规格设置人民币价格及启用状态。新商品和规格默认停用，无需填写原价或排序。
2. 给每个规格独立导入卡密，每行一条，每次最多 1,000 行，单条最多 512 字节。忽略空行、去掉首尾空白，保留大小写和内部空格；重复卡密会使整个批次失败，包括已售卡密的重复导入。
3. 确认库存后启用商品和规格。用户每单购买一个规格，数量 1–100，使用兼容 CNY 的现有在线支付方式。代充不使用余额购买、充值赠送、邀请返利或原站内订阅的美元换算。
4. 用户付款成功后，在“我的订单”或登录后的支付结果页查看、复制卡密；同一批卡密会通过邮件发送。CDK 外部购买页新增 USDT 指引，跳转到站内充值标签。

新导入卡密与已有兑换码一样直接存储明文，导入、启售、购买及查看新卡密无需配置 `TOTP_ENCRYPTION_KEY`。

库存列表默认只显示遮罩内容，支持分页、状态筛选及停用／恢复未售卡密。查看完整卡密需要独立管理员权限。归档商品或规格不删除历史订单及已售卡密。

### 支付、库存和邮件

- 创建订单时整单预占库存。取消、过期或创建支付失败后释放。
- 已核验的迟到付款仍可交付；释放后的库存已经售出时，订单进入“已付款／等待补货”。按同一规格的付款顺序整单发放，不抢占其他订单预留卡密。补货后后台自动重试。
- 发卡、订单完成和邮件入队在同一事务中提交。重复回调或发货重试不会重复发卡，订单中始终显示原来交付的卡密。
- 邮件任务持久化并使用领取租约，发送失败最多尝试 5 次；管理员可查看邮件状态并重新发送。邮件失败不会撤销已交付卡密。任务表及操作日志不存储明文卡密，导入请求正文不进入操作日志。
- 新代充订单不支持站内退款操作，页面显示“退款请联系管理员”。原余额充值和站内订阅订单继续使用原退款流程。

受限管理 Key 需要分别授予 `payment.topups.read`、`payment.topups.write`、`payment.topups.secrets`、`payment.topups.resend`；普通订单查看权限不授予卡密查看权。用户卡密接口只允许订单所属用户访问，明文响应设置 `Cache-Control: no-store`。

### 升级与回退

迁移 `307_subscription_topup.sql` 新建商品、规格、卡密、邮件待发记录，并向支付订单增加代充字段。后续迁移 `308_subscription_topup_plain_codes.sql` 增加明文 `code` 列，新导入直接写入该列；原迁移不修改，旧密文不丢弃。

升级前备份数据库并统一更新应用实例。若曾用旧版导入加密卡密，保留原加密密钥以读取这部分历史记录；读取失败不会返回密文本身。TOTP 两步验证继续使用原有密钥配置。升级后先配置测试商品和模拟支付验证，再导入正式库存；本次代码交付不会自动导入库存、调用支付网关或部署。

开始接受代充订单后，回退需同时恢复匹配的应用和数据库备份（存在旧密文或 TOTP 时同时恢复原密钥），并先停止新订单及回调处理。旧版应用不能读取新版导入的明文卡密。

## English

The **Recharge / Subscription top-ups** storefront sells codes for external services. Configure products, specifications and card pools under **Order management → Subscription top-ups (codes)**, without linking a Sub2API group or specifying a site subscription duration. Existing plans remain under **Site subscription plans**; assigned entitlements and historical orders are preserved. Top-up orders deliver external-service codes.

### Setup and purchase

1. Create a product with a description and instructions, then add specifications with CNY prices and enablement. New products and specifications start disabled. Neither original prices nor sorting inputs are required.
2. Import a separate card pool for each specification: one code per line, up to 1,000 lines per import and 512 bytes per code. Blank lines and outer whitespace are removed; case and internal spaces remain unchanged. A duplicate, including a previously sold code, rejects the whole batch.
3. Enable the product and specification after checking stock. Each order purchases one specification with a quantity of 1–100 through an existing CNY-compatible online payment method. Wallet purchases, recharge gifts, referral rebates and the previous subscription USD conversion do not apply.
4. After payment, the authenticated owner can view and copy codes in My Orders or the payment result page. The same delivered codes are sent by email. The external CDK page also links USDT users to the internal recharge tab.

New cards are stored in plaintext, just like existing redeem codes. Importing, enabling sales, purchasing and reading new cards do not require `TOTP_ENCRYPTION_KEY`.

Inventory lists show masked codes and support pagination, status filters and disabling or enabling unsold cards. Revealing a full code requires separate administrator permission. Archiving does not delete historical orders or issued cards.

### Delivery and recovery

- Order creation reserves the complete quantity. Cancellation, expiration and payment creation failure release it.
- Verified late payments remain fulfillable. If released stock is no longer available, the paid order waits for restocking. Fulfillment is whole-order FIFO within each specification and never takes another order's reservation. A background worker retries after replenishment.
- Issuing cards, completing the order and queuing its delivery email are atomic. Duplicate callbacks and retries return the same delivered codes.
- Durable email tasks use claim leases and up to five attempts. Administrators can inspect status and resend. Email failure does not reverse delivery. Task rows and audit logs do not contain plaintext codes, and card import bodies are excluded from audit capture.
- In-app refunds are unavailable for this new order type; users are directed to contact an administrator. Existing recharge and site-subscription refund behavior is retained.

Restricted administrator keys use separate `payment.topups.read`, `.write`, `.secrets` and `.resend` permissions. Ordinary order access does not grant code disclosure. User delivery access checks ownership, and plaintext responses use `Cache-Control: no-store`.

### Upgrade and rollback

Migration `307_subscription_topup.sql` adds products, specifications, cards, durable mail records and top-up order fields. The subsequent `308_subscription_topup_plain_codes.sql` migration adds the plaintext `code` column for new imports. The original migration and existing ciphertext remain intact.

Back up the database and upgrade application instances together. If cards were encrypted by the previous release, retain the original key to read those legacy records; decryption failures never return ciphertext as a code. TOTP two-factor authentication keeps its existing key configuration. Validate with test inventory and simulated payments before importing live stock. This implementation does not deploy, import real inventory or call payment gateways automatically.

Once top-up orders are accepted, rollback requires matching application and database backups, plus the original key when legacy ciphertext or TOTP is in use. Pause order creation and callback processing before restoration. Older applications cannot read newly imported plaintext cards.
