CREATE TABLE IF NOT EXISTS topup_products (
 id BIGSERIAL PRIMARY KEY, name VARCHAR(100) NOT NULL, description TEXT NOT NULL DEFAULT '', instructions TEXT NOT NULL DEFAULT '',
 enabled BOOLEAN NOT NULL DEFAULT FALSE, sort_order INT NOT NULL DEFAULT 0, archived_at TIMESTAMPTZ,
 created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(), updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS topupproduct_enabled_sort_order ON topup_products(enabled,sort_order);
CREATE TABLE IF NOT EXISTS topup_skus (
 id BIGSERIAL PRIMARY KEY, product_id BIGINT NOT NULL REFERENCES topup_products(id), name VARCHAR(100) NOT NULL,
 price DECIMAL(20,2) NOT NULL CHECK(price>0), enabled BOOLEAN NOT NULL DEFAULT FALSE, sort_order INT NOT NULL DEFAULT 0, archived_at TIMESTAMPTZ,
 created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(), updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS topupsku_product_id_sort_order ON topup_skus(product_id,sort_order);
ALTER TABLE payment_orders ADD COLUMN IF NOT EXISTS topup_sku_id BIGINT REFERENCES topup_skus(id);
ALTER TABLE payment_orders ADD COLUMN IF NOT EXISTS topup_quantity INT NOT NULL DEFAULT 0;
ALTER TABLE payment_orders ADD COLUMN IF NOT EXISTS topup_snapshot JSONB;
ALTER TABLE payment_orders ADD COLUMN IF NOT EXISTS topup_delivery_status VARCHAR(20) NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS payment_orders_topup_waiting ON payment_orders(topup_sku_id,paid_at,id) WHERE topup_delivery_status='waiting_stock' AND status='PAID';
CREATE TABLE IF NOT EXISTS topup_cards (
 id BIGSERIAL PRIMARY KEY, sku_id BIGINT NOT NULL REFERENCES topup_skus(id), code_hash VARCHAR(64) NOT NULL UNIQUE,
 ciphertext TEXT NOT NULL, masked_code VARCHAR(32) NOT NULL, status VARCHAR(20) NOT NULL DEFAULT 'available',
 order_id BIGINT REFERENCES payment_orders(id), issued_at TIMESTAMPTZ,
 created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(), updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 CHECK(status IN ('available','reserved','issued','disabled'))
);
CREATE INDEX IF NOT EXISTS topupcard_sku_id_status_id ON topup_cards(sku_id,status,id);
CREATE INDEX IF NOT EXISTS topupcard_order_id_status ON topup_cards(order_id,status);
CREATE TABLE IF NOT EXISTS topup_mails (
 id BIGSERIAL PRIMARY KEY, order_id BIGINT NOT NULL UNIQUE REFERENCES payment_orders(id), recipient VARCHAR(255) NOT NULL,
 locale VARCHAR(20) NOT NULL DEFAULT 'zh', status VARCHAR(20) NOT NULL DEFAULT 'queued', attempts INT NOT NULL DEFAULT 0,
 available_at TIMESTAMPTZ NOT NULL DEFAULT NOW(), lease_until TIMESTAMPTZ, claim_token VARCHAR(64) NOT NULL DEFAULT '',
 message_id VARCHAR(255) NOT NULL, last_error VARCHAR(255) NOT NULL DEFAULT '', sent_at TIMESTAMPTZ,
 created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(), updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS topupmail_status_available_at ON topup_mails(status,available_at);
