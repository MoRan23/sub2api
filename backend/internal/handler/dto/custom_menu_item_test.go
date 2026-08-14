package dto

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCustomMenuItemPurchaseModeRoundTripAndVisibility(t *testing.T) {
	raw := `[
		{"id":"store","label":"Store","icon_svg":"","url":"https://pay.example.com","visibility":"user","sort_order":1,"purchase_mode":true},
		{"id":"ops","label":"Ops","icon_svg":"","url":"https://admin.example.com","visibility":"admin","sort_order":2,"purchase_mode":true}
	]`

	items := ParseCustomMenuItems(raw)
	require.Len(t, items, 2)
	require.True(t, items[0].PurchaseMode)
	require.True(t, items[1].PurchaseMode)

	visible := ParseUserVisibleMenuItems(raw)
	require.Len(t, visible, 1)
	require.Equal(t, "store", visible[0].ID)
	require.True(t, visible[0].PurchaseMode)

	encoded, err := json.Marshal(visible)
	require.NoError(t, err)
	require.JSONEq(t, `[{"id":"store","label":"Store","icon_svg":"","url":"https://pay.example.com","visibility":"user","sort_order":1,"purchase_mode":true}]`, string(encoded))
}

func TestCustomMenuItemPurchaseModeIsOptional(t *testing.T) {
	items := ParseCustomMenuItems(`[{"id":"docs","label":"Docs","icon_svg":"","url":"https://docs.example.com","visibility":"user","sort_order":0}]`)
	require.Len(t, items, 1)
	require.False(t, items[0].PurchaseMode)

	encoded, err := json.Marshal(items)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "purchase_mode")
}
