export interface SubscriptionTopupSKU {
  id: number
  product_id: number
  name: string
  price: number
  currency?: string
  enabled: boolean
  sort_order: number
  available_stock: number
}

export interface SubscriptionTopupProduct {
  id: number
  name: string
  description: string
  instructions: string
  enabled: boolean
  sort_order: number
  skus: SubscriptionTopupSKU[]
}

export type TopupCardStatus = 'available' | 'reserved' | 'issued' | 'disabled'

export interface SubscriptionTopupCard {
  id: number
  sku_id: number
  masked_code: string
  status: TopupCardStatus
  order_id?: number | null
  created_at: string
}

export interface TopupCardPage {
  items: SubscriptionTopupCard[]
  total: number
  page: number
  page_size: number
  counts: Record<TopupCardStatus, number>
}

export interface SubscriptionTopupOrderCards {
  order_id: number
  product_name: string
  sku_name: string
  quantity: number
  instructions: string
  cards: string[]
  delivery_status: string
  email_status: string
}

export interface TopupProductInput {
  name: string
  description: string
  instructions: string
  enabled: boolean
  sort_order: number
}

export interface TopupSKUInput {
  name: string
  price: number
  enabled: boolean
  sort_order: number
}
