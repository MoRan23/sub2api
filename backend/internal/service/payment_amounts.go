package service

import (
	"math"

	"github.com/Wei-Shaw/sub2api/internal/payment"
	"github.com/shopspring/decimal"
)

const (
	defaultBalanceRechargeMultiplier = 1.0
	giftRatioDecimalPlaces           = int32(4)
)

func quantizeGiftRatio(ratio float64) (float64, bool) {
	if math.IsNaN(ratio) || math.IsInf(ratio, 0) || ratio < 0 || ratio > 100 {
		return 0, false
	}
	value := decimal.NewFromFloat(ratio)
	quantized := value.Round(giftRatioDecimalPlaces)
	if !value.Equal(quantized) {
		return 0, false
	}
	return quantized.InexactFloat64(), true
}

func normalizeGiftRatio(ratio float64) float64 {
	quantized, ok := quantizeGiftRatio(ratio)
	if !ok {
		return 0
	}
	return quantized
}

func normalizeBalanceRechargeMultiplier(multiplier float64) float64 {
	if math.IsNaN(multiplier) || math.IsInf(multiplier, 0) || multiplier <= 0 {
		return defaultBalanceRechargeMultiplier
	}
	return multiplier
}

// normalizeSubscriptionUSDToCNYRate 将非法值归一为 0（换算关闭）。
// 与余额倍率不同，0 是合法状态：表示订阅保持 price 直付的存量行为。
func normalizeSubscriptionUSDToCNYRate(rate float64) float64 {
	if math.IsNaN(rate) || math.IsInf(rate, 0) || rate < 0 {
		return 0
	}
	return rate
}

func calculateCreditedBalance(paymentAmount, multiplier float64) float64 {
	return decimal.NewFromFloat(paymentAmount).
		Mul(decimal.NewFromFloat(normalizeBalanceRechargeMultiplier(multiplier))).
		Round(2).
		InexactFloat64()
}

func calculateGiftBalance(ordinaryBalance, ratio float64) float64 {
	ratio = normalizeGiftRatio(ratio)
	if ordinaryBalance <= 0 || ratio <= 0 {
		return 0
	}
	return decimal.NewFromFloat(ordinaryBalance).
		Mul(decimal.NewFromFloat(ratio)).
		Div(decimal.NewFromInt(100)).
		Round(8).
		InexactFloat64()
}

func calculateWalletDelta(before, after float64) float64 {
	return decimal.NewFromFloat(after).
		Sub(decimal.NewFromFloat(before)).
		Round(8).
		InexactFloat64()
}

func calculateRefundGiftAmount(orderAmount, orderGiftAmount, refundAmount float64, _ string) float64 {
	if orderAmount <= 0 || orderGiftAmount <= 0 || refundAmount <= 0 {
		return 0
	}
	orderWalletAmount := decimal.NewFromFloat(orderAmount).Round(8)
	refundWalletAmount := decimal.NewFromFloat(refundAmount).Round(8)
	if orderWalletAmount.IsZero() {
		return 0
	}
	if refundWalletAmount.Equal(orderWalletAmount) {
		return decimal.NewFromFloat(orderGiftAmount).Round(8).InexactFloat64()
	}
	return decimal.NewFromFloat(orderGiftAmount).
		Mul(refundWalletAmount).
		Div(orderWalletAmount).
		Round(8).
		InexactFloat64()
}

func walletAmountLessThan(actual, requested float64) bool {
	return decimal.NewFromFloat(actual).Round(8).
		LessThan(decimal.NewFromFloat(requested).Round(8))
}

func calculateGatewayRefundAmount(orderAmount, payAmount, refundAmount float64, currency string) float64 {
	if orderAmount <= 0 || payAmount <= 0 || refundAmount <= 0 {
		return 0
	}
	fractionDigits := int32(payment.CurrencyMaxFractionDigits(currency))
	if math.Abs(refundAmount-orderAmount) <= paymentAmountToleranceForCurrency(currency) {
		return decimal.NewFromFloat(payAmount).Round(fractionDigits).InexactFloat64()
	}
	return decimal.NewFromFloat(payAmount).
		Mul(decimal.NewFromFloat(refundAmount)).
		Div(decimal.NewFromFloat(orderAmount)).
		Round(fractionDigits).
		InexactFloat64()
}
