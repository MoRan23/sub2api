<template>
  <AccountTestModal :show="show" :account="account" @close="$emit('close')" />
</template>

<script setup lang="ts">
import AccountTestModal from '@/components/admin/account/AccountTestModal.vue'
import type { Account } from '@/types'
defineProps<{ show: boolean; account: Account | null }>()
defineEmits<{ close: [] }>()
</script>
