package service

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFilterUserVisibleMenuItemsPreservesPurchaseMode(t *testing.T) {
	raw := `[
		{"id":"store","url":"https://pay.ldxp.cn/shop/XR56YHVH","visibility":"user","purchase_mode":true},
		{"id":"admin","url":"https://admin.example.com","visibility":"admin","purchase_mode":true}
	]`

	filtered := filterUserVisibleMenuItems(raw)
	var items []map[string]any
	require.NoError(t, json.Unmarshal(filtered, &items))
	require.Len(t, items, 1)
	require.Equal(t, "store", items[0]["id"])
	require.Equal(t, true, items[0]["purchase_mode"])
}

func TestParseCustomMenuItemURLsIncludesPurchaseStorefront(t *testing.T) {
	raw := `[{"id":"store","url":"https://pay.ldxp.cn/shop/XR56YHVH","visibility":"user","purchase_mode":true}]`
	require.Equal(t, []string{"https://pay.ldxp.cn/shop/XR56YHVH"}, parseCustomMenuItemURLs(raw))
}
