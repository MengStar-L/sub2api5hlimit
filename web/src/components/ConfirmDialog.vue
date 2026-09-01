<script setup lang="ts">
import { CircleAlert } from 'lucide-vue-next'
import ModalDialog from '@/components/ModalDialog.vue'

withDefaults(defineProps<{
  open: boolean
  title: string
  description?: string
  /** 逐条列出后果，让用户在确认前看清影响范围 */
  points?: string[]
  confirmLabel?: string
  cancelLabel?: string
  /** danger 用赤陶色确认键，标记不可撤销的破坏性操作 */
  tone?: 'default' | 'danger'
  busy?: boolean
}>(), { confirmLabel: '确认', cancelLabel: '取消', tone: 'default', busy: false })

defineEmits<{ close: []; confirm: [] }>()
</script>

<template>
  <ModalDialog :open="open" :title="title" :description="description" @close="$emit('close')">
    <ul v-if="points?.length" class="confirm-points">
      <li v-for="point in points" :key="point"><CircleAlert :size="14" /><span>{{ point }}</span></li>
    </ul>
    <slot />
    <template #footer>
      <button class="secondary-button" type="button" :disabled="busy" @click="$emit('close')">{{ cancelLabel }}</button>
      <button class="primary-button" :class="{ danger: tone === 'danger' }" type="button" :disabled="busy" @click="$emit('confirm')"><span v-if="busy" class="spinner"></span><slot v-else name="confirm-icon" />{{ confirmLabel }}</button>
    </template>
  </ModalDialog>
</template>
