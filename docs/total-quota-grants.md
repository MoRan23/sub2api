# 按次发放的总额度订阅 / Per-grant total quota subscriptions

## 使用方式

分组只定义订阅模式，不再填写或读取分组的旧总额度作为发放金额。创建总额度分组时可选择“额度永不过期”，默认关闭；创建后不可修改，复制分组保留这个选择。永久分组不能改成其他计费类型。

- 添加订阅：填写本次追加额度；批量添加填写每人追加额度。
- 创建兑换码：填写每码额度，金额冻结在兑换码中。
- 有限期总额度：还需填写有效天数，从发放或兑换时开始计算。
- 永久总额度：只填金额，显示“永久有效”。兑换码本身的兑换截止时间仍然有效。
- 同组追加直接增加剩余额度，不覆盖原有金额或消费记录。永久订阅只显示“剩余额度”，不展示剩余／总额比例或额度进度条。
- 额度为正数，最多八位小数；余额为零时不能继续使用。永久订阅仍可撤销。
- 总额度通过添加订阅或兑换码追加，不能通过“延长期限／重置额度”操作追加。

管理 API 与 MCP 的订阅分配使用 `quota_amount`；创建总额度分组使用 `total_quota_no_expiry`。单个／批量添加订阅请求携带 `Idempotency-Key`，MCP 对应 `idempotency_key`；同一次操作重试复用该值，新一笔追加使用新值。普通日／周／月订阅保持原有发放语义。以下是请求正文片段：

```json
{"user_id": 12, "group_id": 34, "quota_amount": 25.125, "notes": "追加额度"}
```

上例适用于永久分组；有限期分组还需设置 `validity_days`。后台每次追加一条独立额度事件，先消费更早到期的额度，永久额度排在有限期额度之后。主订阅及额度事件使用 `expires_at: null` 表示永久，不用远未来日期。

## 升级与回退

迁移 `279_total_quota_grant_amount_and_permanent.sql`：

1. 增加分组永久模式与兑换码独立金额字段，允许订阅和额度事件到期时间为空。
2. 停用所有尚未兑换且缺少金额快照的旧总额度订阅码；保留记录，需重新创建带金额的新码。已兑换记录和已发放额度保持不变。
3. 从全局及各认证来源的默认赠送配置中移除总额度订阅；普通期限订阅赠送保留，服务端也拒绝重新配置总额度自动赠送。

升级前备份数据库和应用版本，排空旧实例后统一切换新版。旧程序不认识永久期限，不得混跑。订阅 Redis 缓存使用新版命名空间及显式期限类型，旧条目自然过期；缓存损坏时回源。

回退须停止新版并恢复匹配的旧应用和升级前数据库备份。不要只回退可执行文件；备份之后新增的额度和消费需按实际账本另行核对恢复。本次变更不开放总额度支付套餐，也不操作支付网关。

## English

Groups define the subscription mode, not the grant amount. Creating a total-quota group optionally enables permanent credit; the default is finite expiry. This setting is immutable after creation and is inherited when copying a group. Permanent groups cannot switch billing type.

Administrators enter an explicit positive `quota_amount` (up to eight decimal places) when assigning a subscription or generating a code. Single and bulk assignment requests use `Idempotency-Key` (`idempotency_key` in MCP): retain it for retries and use a new value for each new grant. Bulk amounts apply per user or per code. Repeated grants append credit to the existing remaining amount; they do not overwrite spending history. Finite grants also require `validity_days`, while permanent grants have no subscription expiry. A code's redemption deadline remains independent.

Permanent subscriptions display only the remaining credit and “Never expires”, without a total-credit ratio or progress bar. Exhausted credit blocks admission and administrators can still revoke the subscription. Each grant remains an independent ledger event. Consumption uses the earliest finite expiry first, followed by permanent credit. Both subscription and event expiry use SQL `NULL` / JSON `null` for permanence.

Migration 279 disables unused legacy total-quota codes that lack a frozen amount and removes total-quota entries from automatic signup / first-bind grants. Existing issued credit, used codes and windowed subscription defaults remain intact. Legacy disabled codes must be replaced by newly generated codes with an amount.

Back up the database and application before upgrading. Drain old instances and switch all instances together; old binaries cannot handle nullable expiry. Subscription cache data uses a new namespace and an explicit expiry mode. Rollback requires the matching application and pre-upgrade database backup, with subsequent grants and spending reconciled separately. Total-quota payment-plan sales remain outside this feature.
