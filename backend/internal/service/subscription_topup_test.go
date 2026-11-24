package service

import (
	"context"
	"math"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTopupCardImportValidation(t *testing.T) {
	cards, err := prepareTopupCodes([]string{"  Abc-12345  ", "", "abc-12345"})
	require.NoError(t, err)
	require.Len(t, cards, 2)
	require.Equal(t, "Abc-12345", cards[0].code)
	require.NotEqual(t, cards[0].hash, cards[1].hash)
	require.Equal(t, "********", maskTopupCode("short"))
	for _, codes := range [][]string{
		{" duplicate ", "duplicate"}, {"line\nbreak"}, {"nul\x00code"},
		{strings.Repeat("x", 513)}, {string([]byte{0xff})}, {}, make([]string, 1001),
	} {
		_, err := prepareTopupCodes(codes)
		require.Error(t, err)
	}
}

func TestTopupPriceAndPersistentKey(t *testing.T) {
	for _, price := range []float64{0, -1, math.NaN(), math.Inf(1), 1.001, 1e12 + 1} {
		require.Error(t, validateTopupPrice(&price, true))
	}
	for _, price := range []float64{0.01, 12.34, 1e12} {
		require.NoError(t, validateTopupPrice(&price, true))
	}
	require.NoError(t, validateTopupPrice(nil, false))
	require.Error(t, validateTopupPrice(nil, true))
	svc := NewSubscriptionTopupService(nil, nil, nil)
	_, err := svc.GetQuote(context.Background(), 1, 1)
	require.ErrorContains(t, err, "persistent encryption key")
	_, err = svc.ImportCards(context.Background(), 1, []string{"code"})
	require.Error(t, err)
}
