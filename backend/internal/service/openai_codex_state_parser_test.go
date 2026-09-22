package service

import (
	"encoding/base64"
	"encoding/binary"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestInspectCodexTurnStateEnvelopeClassifiesWithoutAccountType(t *testing.T) {
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		blocks, length int
		shape          string
	}{
		{10, 292, CodexTurnStateObservedPersonalTarget},
		{11, 312, CodexTurnStateObservedPersonalExtended},
		{12, 332, CodexTurnStateObservedTeamBusinessTarget},
		{13, 356, CodexTurnStateObservedTeamBusinessExtended},
	} {
		t.Run(tc.shape, func(t *testing.T) {
			observed, err := InspectCodexTurnStateEnvelope(codexStateTestToken(tc.blocks, now), now)
			require.NoError(t, err)
			require.Equal(t, tc.shape, observed.ObservedShape)
			require.Equal(t, tc.blocks, observed.CipherBlocks)
			require.Equal(t, tc.length, observed.TokenLength)
			require.Equal(t, now, observed.IssuedAt)
			require.Equal(t, now.Add(CodexTurnStateLifetime), observed.ExpiresAt)
			require.Empty(t, observed.ValidationReason)
		})
	}
}

func TestInspectCodexTurnStateEnvelopeExpiresFourMinutesAfterIssue(t *testing.T) {
	issuedAt := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	expiresAt := issuedAt.Add(240 * time.Second)
	for _, blocks := range []int{10, 11, 12, 13} {
		token := codexStateTestToken(blocks, issuedAt)
		for _, observedAt := range []time.Time{issuedAt, issuedAt.Add(2 * time.Minute), expiresAt.Add(-time.Nanosecond)} {
			observed, err := InspectCodexTurnStateEnvelope(token, observedAt)
			require.NoError(t, err, "blocks=%d observedAt=%s", blocks, observedAt)
			require.Equal(t, issuedAt, observed.IssuedAt)
			require.Equal(t, expiresAt, observed.ExpiresAt, "repeated observation must not extend the issue-based lifetime")
		}
		observed, err := InspectCodexTurnStateEnvelope(token, expiresAt)
		require.EqualError(t, err, "expired", "blocks=%d", blocks)
		require.Equal(t, expiresAt, observed.ExpiresAt)
	}
}

func TestCodexTurnState356ObservationDoesNotRelaxAccountAdmission(t *testing.T) {
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	token := codexStateTestToken(13, now)
	for _, tc := range []struct {
		accountType, shape, reason string
	}{
		{"", CodexTurnStateShapeInvalid, "account_type_unknown"},
		{"personal", CodexTurnStateShapeInvalid, "unexpected_shape"},
		{"team_business", CodexTurnStateShapeExtended, ""},
	} {
		t.Run(tc.accountType, func(t *testing.T) {
			observed, err := InspectCodexTurnStateEnvelope(token, now)
			require.NoError(t, err)
			require.Equal(t, CodexTurnStateObservedTeamBusinessExtended, observed.ObservedShape)
			parsed, err := ParseCodexTurnState(token, tc.accountType, now)
			if tc.reason == "" {
				require.NoError(t, err)
			} else {
				require.EqualError(t, err, tc.reason)
			}
			require.Equal(t, tc.shape, parsed.Shape)
			require.NotEqual(t, CodexTurnStateShapeTarget, parsed.Shape)
		})
	}
}

func TestInspectCodexTurnStateEnvelopeRejectsMalformed356(t *testing.T) {
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	valid := codexStateTestToken(13, now)
	decoded, err := base64.URLEncoding.DecodeString(valid)
	require.NoError(t, err)
	wrongVersion := append([]byte(nil), decoded...)
	wrongVersion[0] = 0x81
	for name, token := range map[string]string{
		"length_only":    strings.Repeat("A", 356),
		"invalid_base64": "!" + valid[1:],
		"wrong_version":  base64.URLEncoding.EncodeToString(wrongVersion),
		"misaligned":     base64.URLEncoding.EncodeToString(append(decoded, 0)),
	} {
		t.Run(name, func(t *testing.T) {
			require.Len(t, token, 356)
			observed, err := InspectCodexTurnStateEnvelope(token, now)
			require.EqualError(t, err, "invalid_envelope")
			require.Equal(t, "invalid_envelope", observed.ValidationReason)
			require.Equal(t, CodexTurnStateShapeInvalid, observed.ObservedShape)
			require.Zero(t, observed.CipherBlocks)
			parsed, err := ParseCodexTurnState(token, "team_business", now)
			require.Error(t, err)
			require.Equal(t, CodexTurnStateShapeInvalid, parsed.Shape)
		})
	}
}

func TestInspectCodexTurnStateEnvelopePreservesShapeForInvalidTime(t *testing.T) {
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		reason string
		issued time.Time
	}{
		{"expired", now.Add(-CodexTurnStateLifetime)},
		{"future_issued_at", now.Add(31 * time.Second)},
	} {
		t.Run(tc.reason, func(t *testing.T) {
			token := codexStateTestToken(13, tc.issued)
			observed, err := InspectCodexTurnStateEnvelope(token, now)
			require.EqualError(t, err, tc.reason)
			require.Equal(t, tc.reason, observed.ValidationReason)
			require.Equal(t, CodexTurnStateObservedTeamBusinessExtended, observed.ObservedShape)
			require.Equal(t, 13, observed.CipherBlocks)
			if tc.reason == "expired" {
				require.Equal(t, tc.issued, observed.IssuedAt)
				require.Equal(t, now, observed.ExpiresAt)
			}
			for _, accountType := range []string{"personal", "team_business"} {
				parsed, err := ParseCodexTurnState(token, accountType, now)
				require.EqualError(t, err, tc.reason)
				require.Equal(t, CodexTurnStateShapeInvalid, parsed.Shape)
			}
		})
	}
	decoded, err := base64.URLEncoding.DecodeString(codexStateTestToken(13, now))
	require.NoError(t, err)
	binary.BigEndian.PutUint64(decoded[1:9], ^uint64(0))
	observed, err := InspectCodexTurnStateEnvelope(base64.URLEncoding.EncodeToString(decoded), now)
	require.EqualError(t, err, "future_issued_at")
	require.Equal(t, CodexTurnStateObservedTeamBusinessExtended, observed.ObservedShape)
	require.True(t, observed.IssuedAt.IsZero(), "an unsigned timestamp must not wrap into a historical date")
}

func TestInspectCodexTurnStateEnvelopeRequiresCanonicalPaddingAndKnownShape(t *testing.T) {
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct{ name, token, reason string }{
		{"missing_padding", strings.TrimRight(codexStateTestToken(13, now), "="), "invalid_encoding"},
		{"surrounding_space", " " + codexStateTestToken(13, now), "invalid_encoding"},
		{"unsupported_blocks", codexStateTestToken(14, now), "unexpected_shape"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			observed, err := InspectCodexTurnStateEnvelope(tc.token, now)
			require.EqualError(t, err, tc.reason)
			require.Equal(t, tc.reason, observed.ValidationReason)
			require.Equal(t, CodexTurnStateShapeInvalid, observed.ObservedShape)
		})
	}
}
