import { ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAppStore } from '@/stores/app'
import { adminAPI } from '@/api/admin'
import { extractApiErrorMessage, extractI18nErrorMessage } from '@/utils/apiError'
import type { Account, OpenAIOAuthOS } from '@/types'

export interface OpenAITokenInfo {
  account?: Account
  os?: OpenAIOAuthOS
  access_token?: string
  refresh_token?: string
  client_id?: string
  id_token?: string
  token_type?: string
  expires_in?: number
  expires_at?: number
  scope?: string
  email?: string
  name?: string
  plan_type?: string
  subscription_expires_at?: string
  privacy_mode?: string
  // OpenAI specific IDs (extracted from ID Token)
  chatgpt_account_id?: string
  chatgpt_user_id?: string
  organization_id?: string
  [key: string]: unknown
}

export type OpenAIOAuthPlatform = 'openai'

export function useOpenAIOAuth() {
  const appStore = useAppStore()
  const { t } = useI18n()
  const endpointPrefix = '/admin/openai'

  // State
  const authUrl = ref('')
  const sessionId = ref('')
  const oauthState = ref('')
  const loading = ref(false)
  const error = ref('')
  const boundOS = ref<OpenAIOAuthOS | null>(null)
  const boundAccountId = ref<number | null>(null)
  let generationVersion = 0

  // Reset state
  const resetState = () => {
    generationVersion++
    authUrl.value = ''
    sessionId.value = ''
    oauthState.value = ''
    loading.value = false
    error.value = ''
    boundOS.value = null
    boundAccountId.value = null
  }

  // Generate auth URL for OpenAI OAuth
  const generateAuthUrl = async (
    proxyId?: number | null,
    redirectUri?: string,
    os: OpenAIOAuthOS = 'windows',
    accountId?: number
  ): Promise<boolean> => {
    const requestVersion = ++generationVersion
    loading.value = true
    authUrl.value = ''
    sessionId.value = ''
    boundOS.value = null
    boundAccountId.value = null
    oauthState.value = ''
    error.value = ''

    try {
      const payload: { proxy_id?: number; redirect_uri?: string; os: OpenAIOAuthOS; account_id?: number; purpose: 'create' | 'authorize' } = {
        os, ...(accountId ? { account_id: accountId } : {}), purpose: accountId ? 'authorize' : 'create'
      }
      if (proxyId) {
        payload.proxy_id = proxyId
      }
      if (redirectUri) {
        payload.redirect_uri = redirectUri
      }

      const response = await adminAPI.accounts.generateAuthUrl(
        `${endpointPrefix}/generate-auth-url`,
        payload
      )
      if (requestVersion !== generationVersion) return false
      authUrl.value = response.auth_url
      sessionId.value = response.session_id
      boundOS.value = os
      boundAccountId.value = accountId ?? null
      try {
        const parsed = new URL(response.auth_url)
        oauthState.value = parsed.searchParams.get('state') || ''
      } catch {
        oauthState.value = ''
      }
      return true
    } catch (err: any) {
      if (requestVersion !== generationVersion) return false
      error.value = extractApiErrorMessage(err, t('admin.accounts.oauth.openai.failedToGenerateUrl'))
      appStore.showError(error.value)
      return false
    } finally {
      if (requestVersion === generationVersion) loading.value = false
    }
  }

  // Exchange auth code for tokens
  const exchangeAuthCode = async (
    code: string,
    currentSessionId: string,
    state: string,
    proxyId?: number | null
  ): Promise<OpenAITokenInfo | null> => {
    if (!code.trim() || !currentSessionId || !state.trim()) {
      error.value = 'Missing auth code, session ID, or state'
      return null
    }

    loading.value = true
    error.value = ''

    try {
      const payload: { session_id: string; code: string; state: string; proxy_id?: number } = {
        session_id: currentSessionId,
        code: code.trim(),
        state: state.trim()
      }
      if (proxyId) {
        payload.proxy_id = proxyId
      }

      const tokenInfo = await adminAPI.accounts.exchangeCode(`${endpointPrefix}/exchange-code`, payload)
      return tokenInfo as OpenAITokenInfo
    } catch (err: any) {
      error.value = extractI18nErrorMessage(
        err,
        t,
        'admin.accounts.oauth.openai.errors',
        t('admin.accounts.oauth.openai.failedToExchangeCode')
      )
      appStore.showError(error.value)
      return null
    } finally {
      loading.value = false
    }
  }

  // Validate refresh token and get full token info
  // clientId: 指定 OAuth client_id（用于第三方渠道获取的 RT，如 app_LlGpXReQgckcGGUo2JrYvtJK）
  const validateRefreshToken = async (
    refreshToken: string,
    proxyId?: number | null,
    clientId?: string,
    os: OpenAIOAuthOS = 'windows',
    accountId?: number
  ): Promise<OpenAITokenInfo | null> => {
    if (!refreshToken.trim()) {
      error.value = 'Missing refresh token'
      return null
    }

    loading.value = true
    error.value = ''

    try {
      // Use dedicated refresh-token endpoint
      const tokenInfo = await adminAPI.accounts.refreshOpenAIToken(
        refreshToken.trim(),
        proxyId,
        `${endpointPrefix}/refresh-token`,
        clientId,
        os,
        accountId
      )
      return tokenInfo as OpenAITokenInfo
    } catch (err: any) {
      error.value = extractI18nErrorMessage(
        err,
        t,
        'admin.accounts.oauth.openai.errors',
        t('admin.accounts.oauth.openai.failedToValidateRT')
      )
      appStore.showError(error.value)
      return null
    } finally {
      loading.value = false
    }
  }

  // Build credentials for OpenAI OAuth account (aligned with backend BuildAccountCredentials)
  const buildCredentials = (tokenInfo: OpenAITokenInfo): Record<string, unknown> => {
    const creds: Record<string, unknown> = {
      access_token: tokenInfo.access_token,
      expires_at: tokenInfo.expires_at
    }

    // 仅在返回了新的 refresh_token 时才写入，防止用空值覆盖已有令牌
    if (tokenInfo.refresh_token) {
      creds.refresh_token = tokenInfo.refresh_token
    }
    if (tokenInfo.id_token) {
      creds.id_token = tokenInfo.id_token
    }
    if (tokenInfo.email) {
      creds.email = tokenInfo.email
    }
    if (tokenInfo.chatgpt_account_id) {
      creds.chatgpt_account_id = tokenInfo.chatgpt_account_id
    }
    if (tokenInfo.chatgpt_user_id) {
      creds.chatgpt_user_id = tokenInfo.chatgpt_user_id
    }
    if (tokenInfo.organization_id) {
      creds.organization_id = tokenInfo.organization_id
    }
    if (tokenInfo.plan_type) {
      creds.plan_type = tokenInfo.plan_type
    }
    if (tokenInfo.subscription_expires_at) {
      creds.subscription_expires_at = tokenInfo.subscription_expires_at
    }
    if (tokenInfo.client_id) {
      creds.client_id = tokenInfo.client_id
    }

    return creds
  }

  // Build extra info from token response
  const buildExtraInfo = (tokenInfo: OpenAITokenInfo): Record<string, string> | undefined => {
    const extra: Record<string, string> = {}
    if (tokenInfo.email) {
      extra.email = tokenInfo.email
    }
    if (tokenInfo.name) {
      extra.name = tokenInfo.name
    }
    if (tokenInfo.privacy_mode) {
      extra.privacy_mode = tokenInfo.privacy_mode
    }
    return Object.keys(extra).length > 0 ? extra : undefined
  }

  return {
    // State
    authUrl,
    sessionId,
    oauthState,
    loading,
    error,
    boundOS,
    boundAccountId,
    // Methods
    resetState,
    generateAuthUrl,
    exchangeAuthCode,
    validateRefreshToken,
    buildCredentials,
    buildExtraInfo
  }
}
