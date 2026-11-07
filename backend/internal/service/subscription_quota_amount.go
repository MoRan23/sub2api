package service

import (
	"math"
	"strconv"
	"strings"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// ValidateSubscriptionQuotaAmount validates an explicitly supplied DECIMAL(20,8)
// grant. A missing or zero amount never means unlimited subscription credit.
func ValidateSubscriptionQuotaAmount(amount *float64) error {
	if amount == nil || math.IsNaN(*amount) || math.IsInf(*amount, 0) || *amount <= 0 || *amount >= 1e12 {
		return infraerrors.BadRequest("SUBSCRIPTION_QUOTA_AMOUNT_INVALID", "quota_amount must be a finite positive amount with at most 8 decimal places")
	}
	decimal := strconv.FormatFloat(*amount, 'f', -1, 64)
	if dot := strings.IndexByte(decimal, '.'); dot >= 0 && len(decimal)-dot-1 > 8 {
		return infraerrors.BadRequest("SUBSCRIPTION_QUOTA_AMOUNT_INVALID", "quota_amount must have at most 8 decimal places")
	}
	return nil
}

// ValidateSubscriptionRedeemGrant is shared by creation, redemption and locked
// batch updates, so changing a code's group cannot bypass grant validation.
func ValidateSubscriptionRedeemGrant(group *Group, amount *float64, days int) error {
	if group == nil || !group.IsSubscriptionType() {
		return infraerrors.BadRequest("REDEEM_CODE_GROUP_INVALID", "group must be a subscription group")
	}
	if !group.IsTotalQuotaSubscriptionType() {
		if days == 0 {
			return infraerrors.BadRequest("REDEEM_CODE_VALIDITY_INVALID", "validity_days must not be zero for a windowed subscription")
		}
		return nil
	}
	if err := ValidateSubscriptionQuotaAmount(amount); err != nil {
		return infraerrors.BadRequest("REDEEM_CODE_QUOTA_AMOUNT_INVALID", "total quota subscription codes require a positive quota_amount with at most 8 decimal places; recreate legacy codes without an amount")
	}
	if !group.TotalQuotaNoExpiry && (days <= 0 || days > MaxValidityDays) {
		return infraerrors.BadRequest("REDEEM_CODE_VALIDITY_INVALID", "validity_days must be positive and within the supported range for a finite total quota subscription")
	}
	return nil
}

func copySubscriptionQuotaAmount(amount *float64) *float64 {
	if amount == nil {
		return nil
	}
	value := *amount
	return &value
}
