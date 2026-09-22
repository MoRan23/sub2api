<template>
  <ReAuthAccountModal :show="show" :account="account" @close="$emit('close')" @reauthorized="$emit('reauthorized', $event)" />
</template>

<script setup lang="ts">
import ReAuthAccountModal from '@/components/admin/account/ReAuthAccountModal.vue'
import type { Account } from '@/types'
defineProps<{ show: boolean; account: Account | null }>()
defineEmits<{ close: []; reauthorized: [account: Account] }>()
</script>
