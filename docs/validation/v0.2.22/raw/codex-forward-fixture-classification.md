# Codex forwarding fixture classification

Baseline executable: `C:/Users/Admin/AppData/Local/Temp/sub2-v022-baseline.1aE8h9/service-baseline.test.exe`.

The baseline reproduces both existing Anthropic compatibility assertion failures:

- `TestForwardAsAnthropic_AstraContinuationRestoresHistoryAndDisablesUnsupportedSession`: the expected literal session ID conflicts with the existing isolated prompt cache identity.
- `TestForwardAsAnthropic_ResponsesSupportedAccountStillUsesResponsesEndpoint`: the expected third-party User-Agent conflicts with the existing normalized Codex identity.

Raw output: `baseline-anthropic-compat-identity-assertions.log`. Neither assertion nor production behavior was changed in this classification pass.

The baseline `-test.list` selector for the two OAuth Ultrafast tests returns no test names (see `baseline-ultrafast-new-tests-list.log`). Both tests were introduced by the v0.2.22 merge:

- `TestOpenAIGatewayService_OAuthPassthrough_GroupForceOpenAIUltrafastOverridesClientTier`
- `TestOpenAIGatewayService_OAuthLegacy_GroupForceOpenAIUltrafast`

Their fixtures omitted the account repository and an upstream capability declaration. Under the local authorization and capability rules, the catalog refresh therefore panicked before an HTTP request while reloading credentials from the missing repository. The fixtures now provision the existing test authorization repository and a loopback HTTP catalog explicitly declaring `gpt-6.1-sol` Ultrafast support. Model response traffic stays in `httpUpstreamRecorder`. Production code and capability rules are unchanged.

Verification: `GOEXPERIMENT=greenteagc,jsonv2 GOMAXPROCS=4 go test -p 2 -tags unit ./internal/service -run '^Test(OpenAIGatewayService_(OAuthPassthrough_GroupForceOpenAIUltrafastOverridesClientTier|OAuthLegacy_GroupForceOpenAIUltrafast)|ApplyOpenAIFastPolicyToBody_GroupForceUltrafast.*|ApplyOpenAIFastPolicyToBody_GroupForceFastAndUltrafastTogether|WSResponseCreate_GroupForceUltrafast|OpenAIUltrafastCapabilityUsesExactUpstreamEntries|OpenAIGroupForcedServiceTier)$' -count=1`.

Result: PASS, 6.037 seconds. Raw output: `merged-ultrafast-forward-fixtures.log`.
