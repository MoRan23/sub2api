<template>
  <ReAuthAccountModal :show="show" :account="account" :initial-o-s="initialOS" @close="$emit('close')" @reauthorized="$emit('reauthorized', $event)" />
</template>

<script setup lang="ts">
import ReAuthAccountModal from '@/components/admin/account/ReAuthAccountModal.vue'
import type { Account, OpenAIOAuthOS } from '@/types'
defineProps<{ show: boolean; account: Account | null; initialOS?: OpenAIOAuthOS }>()
defineEmits<{ close: []; reauthorized: [account: Account] }>()
</script>
