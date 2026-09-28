import type { Account } from '@/types'
import { supportsManagedOpenAIOAuthIdentity } from './openaiOAuthOS'

type ExcelAccount = Pick<Account, 'platform' | 'type' | 'credentials' | 'parent_account_id'>

export function supportsOpenAIExcelUpstream(account?: ExcelAccount | null): boolean {
  return !!account && !account.parent_account_id && supportsManagedOpenAIOAuthIdentity(account)
}

export function usesOpenAIExcelUpstream(account?: (ExcelAccount & Pick<Account, 'extra'>) | null): boolean {
  return supportsOpenAIExcelUpstream(account) && account?.extra?.openai_excel_upstream_enabled === true
}
