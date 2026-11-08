package service

import (
	"math"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidateSubscriptionQuotaAmount_MatchesEventLedgerCapacity(t *testing.T) {
	for _, amount := range []float64{1e-8, 1, 123.12345678, math.Nextafter(1e10, 0)} {
		require.NoError(t, ValidateSubscriptionQuotaAmount(&amount), "amount=%g", amount)
	}
	for _, amount := range []float64{0, -1, 1e-9, 1e10, math.Nextafter(1e10, math.Inf(1)), 1e11, math.NaN(), math.Inf(1)} {
		require.Error(t, ValidateSubscriptionQuotaAmount(&amount), "amount=%g", amount)
	}
	for _, permanent := range []bool{false, true} {
		group := &Group{SubscriptionType: SubscriptionTypeTotalQuota, TotalQuotaNoExpiry: permanent}
		amount := 1e10
		require.Error(t, ValidateSubscriptionRedeemGrant(group, &amount, 30), "permanent=%t", permanent)
	}
}
