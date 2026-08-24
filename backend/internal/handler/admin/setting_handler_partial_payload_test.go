//go:build unit

package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/stretchr/testify/require"
)

// Saving settings is a whole-document PUT. A client that sends only the field it
// cares about must not reset everything else: a payload as small as
// `{"risk_control_enabled":true}` used to clear site_name, after which
// getStringOrDefault rendered the empty value as the built-in default and the
// login page silently changed name.

func TestUpdateSettingsPartialPayloadKeepsUnsentKeys(t *testing.T) {
	h, repo := newStepUpSwitchTestHandler(t, map[string]string{
		service.SettingKeySiteName:         "Example Gateway",
		service.SettingKeySiteSubtitle:     "Example Gateway Platform",
		service.SettingKeySMTPHost:         "smtp.example.com",
		service.SettingKeySMTPFrom:         "noreply@example.com",
		service.SettingKeyTurnstileEnabled: "true",
	})

	rec := doUpdateSettings(t, h, map[string]any{"risk_control_enabled": true}, nil)
	require.Equal(t, http.StatusOK, rec.Code)

	require.Equal(t, "true", repo.values[service.SettingKeyRiskControlEnabled],
		"the field the caller actually sent must be written")

	require.Equal(t, "Example Gateway", repo.values[service.SettingKeySiteName])
	require.Equal(t, "Example Gateway Platform", repo.values[service.SettingKeySiteSubtitle])
	require.Equal(t, "smtp.example.com", repo.values[service.SettingKeySMTPHost])
	require.Equal(t, "noreply@example.com", repo.values[service.SettingKeySMTPFrom])
	require.Equal(t, "true", repo.values[service.SettingKeyTurnstileEnabled])
}

func TestUpdateSettingsResponseIncludesReadOnlyBuiltinCodexVersion(t *testing.T) {
	h, repo := newStepUpSwitchTestHandler(t, map[string]string{})

	rec := doUpdateSettings(t, h, map[string]any{
		"risk_control_enabled":                true,
		"openai_codex_client_version_builtin": "9.9.9",
	}, nil)
	require.Equal(t, http.StatusOK, rec.Code)

	var body struct {
		Data map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Equal(t, "0.149.1", body.Data["openai_codex_client_version_builtin"])
	require.NotContains(t, repo.values, "openai_codex_client_version_builtin")
	require.NotContains(t, repo.lastUpdates, "openai_codex_client_version_builtin")
}

func TestUpdateSettingsCodexFingerprintPolicyExplicitFieldsArePersistedAndPublished(t *testing.T) {
	tests := []struct {
		name string
		key  string
		want bool
	}{
		{name: "master enabled", key: service.SettingKeyEnableOpenAICodexFingerprintNormalization, want: true},
		{name: "installation disabled", key: service.SettingKeyEnableOpenAICodexInstallationIDNormalization, want: false},
		{name: "UUIDv7 enabled", key: service.SettingKeyEnableOpenAIUUIDv7SessionIdentity, want: true},
		{name: "client identity disabled", key: service.SettingKeyEnableOpenAICodexClientIdentityNormalization, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stored := codexFingerprintPolicyValues(false)
			stored[tt.key] = boolSettingValue(!tt.want)
			h, repo := newStepUpSwitchTestHandler(t, stored)

			rec := doUpdateSettings(t, h, map[string]any{tt.key: tt.want}, nil)
			require.Equal(t, http.StatusOK, rec.Code)
			require.Equal(t, boolSettingValue(tt.want), repo.values[tt.key])
			require.Equal(t, boolSettingValue(tt.want), repo.lastUpdates[tt.key],
				"an explicitly provided pointer field must be included in the write")

			for _, key := range codexFingerprintPolicySettingKeys() {
				if key != tt.key {
					require.NotContains(t, repo.lastUpdates, key,
						"other independently managed policy fields must remain omitted")
				}
			}

			assertCodexFingerprintPolicyMatchesStored(t,
				h.settingService.GetOpenAICodexFingerprintPolicy(context.Background()), repo.values)
		})
	}
}

func TestUpdateSettingsCodexFingerprintPolicyOmissionPreservesExplicitFalse(t *testing.T) {
	h, repo := newStepUpSwitchTestHandler(t, codexFingerprintPolicyValues(false))

	rec := doUpdateSettings(t, h, map[string]any{"risk_control_enabled": true}, nil)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "true", repo.values[service.SettingKeyRiskControlEnabled])

	for _, key := range codexFingerprintPolicySettingKeys() {
		require.Equal(t, "false", repo.values[key])
		require.NotContains(t, repo.lastUpdates, key,
			"an omitted policy field must not replay the handler's pre-read value")
	}
	assertCodexFingerprintPolicyMatchesStored(t,
		h.settingService.GetOpenAICodexFingerprintPolicy(context.Background()), repo.values)
}

func TestUpdateSettingsCodexFingerprintPolicyNullPreservesExplicitFalse(t *testing.T) {
	h, repo := newStepUpSwitchTestHandler(t, codexFingerprintPolicyValues(false))
	payload := map[string]any{"risk_control_enabled": true}
	for _, key := range codexFingerprintPolicySettingKeys() {
		payload[key] = nil
	}

	rec := doUpdateSettings(t, h, payload, nil)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "true", repo.values[service.SettingKeyRiskControlEnabled])

	for _, key := range codexFingerprintPolicySettingKeys() {
		require.Equal(t, "false", repo.values[key])
		require.NotContains(t, repo.lastUpdates, key,
			"JSON null decodes to a nil pointer and must be treated as omitted")
	}
	assertCodexFingerprintPolicyMatchesStored(t,
		h.settingService.GetOpenAICodexFingerprintPolicy(context.Background()), repo.values)
}

func TestUpdateSettingsFingerprintObservationPartialPayloadPublishesRuntimeState(t *testing.T) {
	service.SetFingerprintObservationEnabled(false)
	t.Cleanup(func() { service.SetFingerprintObservationEnabled(false) })

	h, repo := newStepUpSwitchTestHandler(t, map[string]string{
		service.SettingKeyInstallationObservationEnabled: "false",
	})

	enabled := doUpdateSettings(t, h, map[string]any{
		service.SettingKeyInstallationObservationEnabled: true,
	}, nil)
	require.Equal(t, http.StatusOK, enabled.Code)
	require.Equal(t, "true", repo.values[service.SettingKeyInstallationObservationEnabled])
	require.True(t, service.IsFingerprintObservationEnabled())

	disabled := doUpdateSettings(t, h, map[string]any{
		service.SettingKeyInstallationObservationEnabled: false,
	}, nil)
	require.Equal(t, http.StatusOK, disabled.Code)
	require.Equal(t, "false", repo.values[service.SettingKeyInstallationObservationEnabled])
	require.False(t, service.IsFingerprintObservationEnabled())
}

func TestUpdateSettingsPartialPayloadOmitsIndependentOpenAIToggles(t *testing.T) {
	omitted := omittedSettingKeys(map[string]json.RawMessage{
		"risk_control_enabled": json.RawMessage("true"),
	})

	for _, key := range codexFingerprintPolicySettingKeys() {
		require.Contains(t, omitted, key)
	}
	require.Contains(t, omitted, service.SettingKeyInstallationObservationEnabled)
}

func TestUpdateSettingsNullIndependentOpenAITogglesAreOmitted(t *testing.T) {
	sent := map[string]json.RawMessage{
		service.SettingKeyInstallationObservationEnabled: json.RawMessage(" null "),
	}
	for _, key := range codexFingerprintPolicySettingKeys() {
		sent[key] = json.RawMessage("null")
	}
	omitted := omittedSettingKeys(sent)

	for _, key := range codexFingerprintPolicySettingKeys() {
		require.Contains(t, omitted, key)
	}
	require.Contains(t, omitted, service.SettingKeyInstallationObservationEnabled)
}

func TestUpdateSettingsUnrelatedPartialPayloadReusesAuthoritativeFullReadback(t *testing.T) {
	baseRepo := &settingHandlerRepoStub{values: codexFingerprintPolicyValues(false)}
	repo := &codexPolicyReadFailingRepo{settingHandlerRepoStub: baseRepo}
	svc := service.NewSettingService(repo, &config.Config{Default: config.DefaultConfig{UserConcurrency: 5}})
	h := NewSettingHandler(svc, nil, nil, nil, nil, nil, nil)

	rec := doUpdateSettings(t, h, map[string]any{"risk_control_enabled": true}, nil)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Zero(t, repo.policyReadAttempts,
		"the partial update's full readback should avoid a separate policy-row query")

	for _, key := range codexFingerprintPolicySettingKeys() {
		require.Equal(t, "false", repo.values[key])
		require.NotContains(t, repo.lastUpdates, key)
	}
	assertCodexFingerprintPolicyMatchesStored(t,
		svc.GetOpenAICodexFingerprintPolicy(context.Background()), repo.values)
	require.Zero(t, repo.policyReadAttempts,
		"the published authoritative snapshot must satisfy the runtime cache read")
}

type codexPolicyReadFailingRepo struct {
	*settingHandlerRepoStub
	policyReadAttempts int
}

func (r *codexPolicyReadFailingRepo) GetMultiple(ctx context.Context, keys []string) (map[string]string, error) {
	if isCodexFingerprintPolicyRead(keys) {
		r.policyReadAttempts++
		return nil, errors.New("injected Codex fingerprint policy read failure")
	}
	return r.settingHandlerRepoStub.GetMultiple(ctx, keys)
}

func isCodexFingerprintPolicyRead(keys []string) bool {
	if len(keys) != len(codexFingerprintPolicySettingKeys()) {
		return false
	}
	wanted := make(map[string]struct{}, len(keys))
	for _, key := range codexFingerprintPolicySettingKeys() {
		wanted[key] = struct{}{}
	}
	for _, key := range keys {
		if _, ok := wanted[key]; !ok {
			return false
		}
	}
	return true
}

func codexFingerprintPolicySettingKeys() []string {
	return []string{
		service.SettingKeyEnableOpenAICodexFingerprintNormalization,
		service.SettingKeyEnableOpenAICodexInstallationIDNormalization,
		service.SettingKeyEnableOpenAIUUIDv7SessionIdentity,
		service.SettingKeyEnableOpenAICodexClientIdentityNormalization,
	}
}

func codexFingerprintPolicyValues(enabled bool) map[string]string {
	value := boolSettingValue(enabled)
	return map[string]string{
		service.SettingKeyEnableOpenAICodexFingerprintNormalization:    value,
		service.SettingKeyEnableOpenAICodexInstallationIDNormalization: value,
		service.SettingKeyEnableOpenAIUUIDv7SessionIdentity:            value,
		service.SettingKeyEnableOpenAICodexClientIdentityNormalization: value,
	}
}

func boolSettingValue(value bool) string {
	if value {
		return "true"
	}
	return "false"
}

func assertCodexFingerprintPolicyMatchesStored(
	t *testing.T,
	policy service.CodexFingerprintPolicySnapshot,
	stored map[string]string,
) {
	t.Helper()
	require.Equal(t, stored[service.SettingKeyEnableOpenAICodexFingerprintNormalization] == "true", policy.MasterEnabled)
	require.Equal(t, stored[service.SettingKeyEnableOpenAICodexInstallationIDNormalization] == "true", policy.InstallationIDEnabled)
	require.Equal(t, stored[service.SettingKeyEnableOpenAIUUIDv7SessionIdentity] == "true", policy.TurnIdentityEnabled)
	require.Equal(t, stored[service.SettingKeyEnableOpenAICodexClientIdentityNormalization] == "true", policy.ClientIdentityEnabled)
}

func TestUpdateSettingsOmittedFingerprintObservationDoesNotPublishStaleRuntimeState(t *testing.T) {
	service.SetFingerprintObservationEnabled(true)
	t.Cleanup(func() { service.SetFingerprintObservationEnabled(false) })

	h, _ := newStepUpSwitchTestHandler(t, map[string]string{
		service.SettingKeyInstallationObservationEnabled: "false",
	})

	rec := doUpdateSettings(t, h, map[string]any{"risk_control_enabled": true}, nil)
	require.Equal(t, http.StatusOK, rec.Code)
	require.True(t, service.IsFingerprintObservationEnabled(),
		"an unrelated partial PUT must not publish its stale settings snapshot")
}

// A full payload keeps whole-document semantics: fields explicitly set to their
// zero value are still cleared.
func TestUpdateSettingsFullPayloadStillClearsSentEmptyFields(t *testing.T) {
	h, repo := newStepUpSwitchTestHandler(t, map[string]string{
		service.SettingKeySiteName: "Example Gateway",
	})

	rec := doUpdateSettings(t, h, map[string]any{"site_name": ""}, nil)
	require.Equal(t, http.StatusOK, rec.Code)

	require.Equal(t, "", repo.values[service.SettingKeySiteName],
		"an explicitly sent empty value is a deliberate clear, not an omission")
}

// smtp_from_email is the one request field whose JSON name differs from its
// setting key; the alias keeps it from being treated as always-omitted.
func TestUpdateSettingsSMTPFromAliasIsWritable(t *testing.T) {
	h, repo := newStepUpSwitchTestHandler(t, map[string]string{
		service.SettingKeySMTPFrom: "old@example.com",
	})

	rec := doUpdateSettings(t, h, map[string]any{"smtp_from_email": "new@example.com"}, nil)
	require.Equal(t, http.StatusOK, rec.Code)

	require.Equal(t, "new@example.com", repo.values[service.SettingKeySMTPFrom])
}

func TestUpdateSettingsGrokDefaultBaseURLModeIsWritable(t *testing.T) {
	h, repo := newStepUpSwitchTestHandler(t, map[string]string{
		service.SettingKeyGrokDefaultBaseURLMode: service.GrokDefaultBaseURLModeCLI,
	})

	rec := doUpdateSettings(t, h, map[string]any{
		"grok_default_base_url_mode": service.GrokDefaultBaseURLModeEUWest1,
	}, nil)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, service.GrokDefaultBaseURLModeEUWest1, repo.values[service.SettingKeyGrokDefaultBaseURLMode])
}

func TestUpdateSettingsRejectsTwoCaptchaProviders(t *testing.T) {
	h, _ := newStepUpSwitchTestHandler(t, map[string]string{
		service.SettingKeyTurnstileEnabled:   "true",
		service.SettingKeyTurnstileSiteKey:   "site-key",
		service.SettingKeyTurnstileSecretKey: "turnstile-secret",
	})

	rec := doUpdateSettings(t, h, map[string]any{
		"turnstile_enabled":                true,
		"turnstile_site_key":               "site-key",
		"turnstile_secret_key":             "turnstile-secret",
		"tencent_captcha_enabled":          true,
		"tencent_captcha_app_id":           "123456789",
		"tencent_captcha_app_secret_key":   "app-secret",
		"tencent_captcha_cloud_secret_id":  "cloud-secret-id",
		"tencent_captcha_cloud_secret_key": "cloud-secret-key",
	}, nil)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), "cannot be enabled at the same time")
}

func TestUpdateSettingsRequiresFourTencentCaptchaCredentialsWhenEnabled(t *testing.T) {
	h, _ := newStepUpSwitchTestHandler(t, map[string]string{})

	rec := doUpdateSettings(t, h, map[string]any{
		"tencent_captcha_enabled": true,
		"tencent_captcha_app_id":  "123456789",
	}, nil)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), "AppSecretKey")
}

func TestUpdateSettingsRetainsStoredTencentCaptchaCredentialsWhenInputsEmpty(t *testing.T) {
	h, repo := newStepUpSwitchTestHandler(t, map[string]string{
		service.SettingKeyTencentCaptchaAppSecretKey:   "stored-app-secret",
		service.SettingKeyTencentCaptchaCloudSecretID:  "stored-cloud-secret-id",
		service.SettingKeyTencentCaptchaCloudSecretKey: "stored-cloud-secret-key",
	})

	rec := doUpdateSettings(t, h, map[string]any{
		"tencent_captcha_enabled":          true,
		"tencent_captcha_app_id":           "123456789",
		"tencent_captcha_app_secret_key":   "",
		"tencent_captcha_cloud_secret_id":  "",
		"tencent_captcha_cloud_secret_key": "",
	}, nil)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "stored-app-secret", repo.values[service.SettingKeyTencentCaptchaAppSecretKey])
	require.Equal(t, "stored-cloud-secret-id", repo.values[service.SettingKeyTencentCaptchaCloudSecretID])
	require.Equal(t, "stored-cloud-secret-key", repo.values[service.SettingKeyTencentCaptchaCloudSecretKey])
}

// 天御站点决定前端加载哪个 SDK 与服务端打哪个接入点，两端必须一致。
// 部分载荷把它重置回中国站，会让已配国际站的部署在下一次任意保存后整体失效。
func TestUpdateSettingsPartialPayloadKeepsTencentCaptchaRegion(t *testing.T) {
	h, repo := newStepUpSwitchTestHandler(t, map[string]string{
		service.SettingKeyTencentCaptchaRegion: service.TencentCaptchaRegionINTL,
	})

	rec := doUpdateSettings(t, h, map[string]any{"risk_control_enabled": true}, nil)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, service.TencentCaptchaRegionINTL,
		repo.values[service.SettingKeyTencentCaptchaRegion])
}

func TestUpdateSettingsNormalizesUnknownTencentCaptchaRegion(t *testing.T) {
	h, repo := newStepUpSwitchTestHandler(t, map[string]string{
		service.SettingKeyTencentCaptchaRegion: service.TencentCaptchaRegionINTL,
	})

	rec := doUpdateSettings(t, h, map[string]any{"tencent_captcha_region": "sgp"}, nil)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, service.TencentCaptchaRegionCN,
		repo.values[service.SettingKeyTencentCaptchaRegion],
		"未知站点必须落回中国站，不能写入无法识别的值")
}

func TestUpdateSettingsWritesTencentCaptchaRegionWhenSent(t *testing.T) {
	h, repo := newStepUpSwitchTestHandler(t, map[string]string{})

	rec := doUpdateSettings(t, h, map[string]any{"tencent_captcha_region": "intl"}, nil)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, service.TencentCaptchaRegionINTL,
		repo.values[service.SettingKeyTencentCaptchaRegion])
}

func TestUpdateSettingsValidatesTencentCaptchaAppIDWhenEnabledFlagIsOmitted(t *testing.T) {
	h, _ := newStepUpSwitchTestHandler(t, map[string]string{
		service.SettingKeyTencentCaptchaEnabled:        "true",
		service.SettingKeyTencentCaptchaAppID:          "123456789",
		service.SettingKeyTencentCaptchaAppSecretKey:   "stored-app-secret",
		service.SettingKeyTencentCaptchaCloudSecretID:  "stored-cloud-secret-id",
		service.SettingKeyTencentCaptchaCloudSecretKey: "stored-cloud-secret-key",
	})

	rec := doUpdateSettings(t, h, map[string]any{
		"tencent_captcha_app_id": "not-a-number",
	}, nil)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), "positive integer")
}
