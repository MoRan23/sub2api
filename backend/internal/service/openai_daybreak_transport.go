package service

import (
	"context"
	"encoding/json"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// applyOpenAIDaybreakForPlan uses the same immutable authorization slot as the
// physical request. Keep account-local preferences while resolving capabilities
// against its credential owner; never let a later default-OS change choose them.
// Call only at a physical inference-send boundary, after preserving retry input.
func (s *OpenAIGatewayService) applyOpenAIDaybreakForPlan(ctx context.Context, account *Account, plan OpenAIOAuthIdentityPlan, body []byte) []byte {
	if account == nil || !account.IsOpenAIOAuth() {
		return body
	}
	scoped := *account
	if plan.CredentialOS != "" {
		scoped.OpenAIOAuthCredentialOS = plan.CredentialOS
		scoped.OpenAIOAuthCredentialOwnerID = plan.OSOwnerID
		scoped.OpenAIOAuthAuthorizationGeneration = plan.AuthorizationGeneration
	}
	return s.applyOpenAIDaybreak(ctx, &scoped, body)
}

// restoreOpenAIClientAccessPrograms carries the original JSON value around
// typed Chat/Messages adapters. Nulls, invalid program values and extensions
// belong to the caller and must remain intact for upstream validation.
func restoreOpenAIClientAccessPrograms(body []byte, value json.RawMessage) ([]byte, error) {
	if len(value) == 0 {
		return body, nil
	}
	return sjson.SetRawBytes(body, "access_programs", value)
}

// The HTTP bridge retains its finalized identity projection for protocol repair.
// Restore just the caller's program value before retaining that retry source,
// so an automatically selected program is reconsidered for every send attempt.
func restoreOpenAIAccessProgramsForRetry(body, source []byte) ([]byte, error) {
	original := gjson.GetBytes(source, "access_programs")
	if original.Raw == gjson.GetBytes(body, "access_programs").Raw {
		return body, nil
	}
	if original.Exists() {
		return restoreOpenAIClientAccessPrograms(body, json.RawMessage(original.Raw))
	}
	return sjson.DeleteBytes(body, "access_programs")
}
