package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMigration224AppendsCDKSelfRechargeMenuIdempotently(t *testing.T) {
	content, err := FS.ReadFile("224_add_cdk_self_recharge_custom_menu.sql")
	require.NoError(t, err)

	sql := string(content)
	require.Contains(t, sql, "COALESCE(TRIM(v_raw), '') = ''")
	require.Contains(t, sql, "v_items := '[]'::jsonb")
	require.Contains(t, sql, "jsonb_typeof(v_items) IS DISTINCT FROM 'array'")
	require.Contains(t, sql, "FROM jsonb_array_elements(v_items) AS elem")
	require.Contains(t, sql, "WHERE elem ->> 'id' = 'cdk_self_recharge'")
	require.Contains(t, sql, "v_items := v_items || jsonb_build_array(v_new_item)")
	require.Contains(t, sql, "ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value")

	duplicateGuard := strings.Index(sql, "WHERE elem ->> 'id' = 'cdk_self_recharge'")
	appendStatement := strings.Index(sql, "v_items := v_items || jsonb_build_array(v_new_item)")
	require.Greater(t, duplicateGuard, -1)
	require.Greater(t, appendStatement, duplicateGuard)
	require.Contains(t, sql[duplicateGuard:appendStatement], "RETURN;")
}

func TestMigration224UsesExactCDKMenuFieldsAndNextSortOrder(t *testing.T) {
	content, err := FS.ReadFile("224_add_cdk_self_recharge_custom_menu.sql")
	require.NoError(t, err)

	sql := string(content)
	require.Contains(t, sql, "MAX(")
	require.Contains(t, sql, ") + 1")
	require.Contains(t, sql, "'id',            'cdk_self_recharge'")
	require.Contains(t, sql, "'label',         'CDK 自助充值'")
	require.Contains(t, sql, "'url',           'https://pay.ldxp.cn/shop/XR56YHVH'")
	require.Contains(t, sql, "'visibility',    'user'")
	require.Contains(t, sql, "'purchase_mode', true")
	require.Contains(t, sql, "'sort_order',    v_sort_order")
	require.Contains(t, sql, "stroke-linecap=\"round\"")
	require.NotContains(t, sql, "jsonb_set(v_items")
}
