import { apiClient } from '../client'
import type {
  SubscriptionTopupProduct, SubscriptionTopupSKU, TopupCardPage,
  TopupCardStatus, TopupProductInput, TopupSKUInput,
} from '@/types/subscriptionTopup'

const base = '/admin/payment'

export const adminSubscriptionTopupsAPI = {
  listProducts(signal?: AbortSignal) {
    return apiClient.get<SubscriptionTopupProduct[]>(`${base}/topup-products`, { signal })
  },
  createProduct(data: TopupProductInput) {
    return apiClient.post<SubscriptionTopupProduct>(`${base}/topup-products`, data)
  },
  updateProduct(id: number, data: Partial<TopupProductInput>) {
    return apiClient.put<SubscriptionTopupProduct>(`${base}/topup-products/${id}`, data)
  },
  archiveProduct(id: number) {
    return apiClient.delete(`${base}/topup-products/${id}`)
  },
  createSKU(productID: number, data: TopupSKUInput) {
    return apiClient.post<SubscriptionTopupSKU>(`${base}/topup-products/${productID}/skus`, data)
  },
  updateSKU(id: number, data: Partial<TopupSKUInput>) {
    return apiClient.put<SubscriptionTopupSKU>(`${base}/topup-skus/${id}`, data)
  },
  archiveSKU(id: number) {
    return apiClient.delete(`${base}/topup-skus/${id}`)
  },
  listCards(id: number, params: { page: number; page_size: number; status?: TopupCardStatus }, signal?: AbortSignal) {
    return apiClient.get<TopupCardPage>(`${base}/topup-skus/${id}/cards`, { params, signal })
  },
  importCards(id: number, codes: string[]) {
    return apiClient.post<{ imported: number }>(`${base}/topup-skus/${id}/cards/import`, { codes })
  },
  revealCard(id: number) {
    return apiClient.post<{ code: string }>(`${base}/topup-cards/${id}/reveal`)
  },
  setCardEnabled(id: number, enabled: boolean) {
    return apiClient.put(`${base}/topup-cards/${id}`, { enabled })
  },
  resendCards(orderID: number) {
    return apiClient.post(`${base}/orders/${orderID}/resend-cards`)
  },
}
