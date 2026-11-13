package handler

import (
	"context"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/pkg/runtimeobs"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"go.uber.org/zap"
)

// Only owned billing inputs and this scalar error-handling policy cross the
// queue boundary. In particular no caller-supplied callback can retain Gin,
// request bodies, selected accounts, or a mutable WebSocket turn.
type usageRecordTaskOptions struct {
	mandatory    bool
	component    string
	failureEvent string
	model        string
	videoClaimID string
}

func (o usageRecordTaskOptions) snapshot() usageRecordTaskOptions {
	o.component, o.failureEvent = strings.Clone(o.component), strings.Clone(o.failureEvent)
	o.model, o.videoClaimID = strings.Clone(o.model), strings.Clone(o.videoClaimID)
	return o
}

func (h *GatewayHandler) submitFrozenUsageRecord(parent context.Context, input *service.RecordUsageInput, options usageRecordTaskOptions) {
	record := service.SnapshotRecordUsageInput(input)
	if record == nil {
		return
	}
	options = options.snapshot()
	task := func(ctx context.Context) {
		err := h.gatewayService.RecordUsage(ctx, record)
		runtimeobs.Default.ObserveSettlement(err)
		if err != nil {
			requestID := ""
			failureOptions := options
			if record.Result != nil {
				requestID = record.Result.RequestID
				if failureOptions.model == "" {
					failureOptions.model = record.Result.Model
				}
			}
			logUsageRecordFailure(ctx, failureOptions, record.User, record.APIKey, record.Account, requestID, err)
		}
	}
	if options.mandatory {
		h.submitMandatoryUsageRecordTask(parent, task)
	} else {
		h.submitUsageRecordTask(parent, task)
	}
}

func (h *OpenAIGatewayHandler) submitFrozenOpenAIUsageRecord(parent context.Context, input *service.OpenAIRecordUsageInput, options usageRecordTaskOptions) {
	record := service.SnapshotOpenAIRecordUsageInput(input)
	if record == nil {
		return
	}
	options = options.snapshot()
	if record.Result != nil && record.Result.GrokSafetyRejection != nil {
		options.mandatory = true
	}
	task := func(ctx context.Context) {
		err := h.gatewayService.RecordUsage(ctx, record)
		// Engine's ordinary RecordUsage call is a no-op; its durable ledger owns
		// settlement and is observed separately.
		if record.Account == nil || !record.Account.IsCodexEngine() {
			runtimeobs.Default.ObserveSettlement(err)
		}
		if err != nil {
			if options.videoClaimID != "" && record.User != nil && record.APIKey != nil {
				if releaseErr := h.gatewayService.ReleaseGrokVideoBilling(ctx, options.videoClaimID, record.User.ID, record.APIKey.ID); releaseErr != nil {
					logger.L().Warn("grok_media.video_billing_claim_release_failed", zap.String("request_id", options.videoClaimID), zap.Error(releaseErr))
				}
			}
			requestID := ""
			failureOptions := options
			if record.Result != nil {
				requestID = record.Result.RequestID
				if failureOptions.model == "" {
					failureOptions.model = record.Result.Model
				}
			}
			logUsageRecordFailure(ctx, failureOptions, record.User, record.APIKey, record.Account, requestID, err)
		}
	}
	if options.mandatory {
		h.submitMandatoryUsageRecordTask(parent, task)
	} else {
		h.submitOpenAIUsageRecordTask(parent, record.Result, task)
	}
}

func logUsageRecordFailure(ctx context.Context, options usageRecordTaskOptions, user *service.User, key *service.APIKey, account *service.Account, requestID string, err error) {
	fields := []zap.Field{zap.String("component", options.component), zap.String("model", options.model), zap.Error(err)}
	if requestID != "" {
		fields = append(fields, zap.String("request_id", requestID))
	}
	if ctx != nil {
		if gatewayID, _ := ctx.Value(ctxkey.RequestID).(string); gatewayID != "" {
			fields = append(fields, zap.String("gateway_request_id", gatewayID))
		}
		if clientID, _ := ctx.Value(ctxkey.ClientRequestID).(string); clientID != "" {
			fields = append(fields, zap.String("client_request_id", clientID))
		}
	}
	if user != nil {
		fields = append(fields, zap.Int64("user_id", user.ID))
	}
	if key != nil {
		fields = append(fields, zap.Int64("api_key_id", key.ID), zap.Any("group_id", key.GroupID))
	}
	if account != nil {
		fields = append(fields, zap.Int64("account_id", account.ID))
	}
	logger.L().Error(options.failureEvent, fields...)
}
