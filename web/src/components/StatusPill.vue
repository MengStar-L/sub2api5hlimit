<script setup lang="ts">
import { computed } from 'vue'
import { CircleCheck, CircleAlert, CircleMinus, Clock3 } from 'lucide-vue-next'
import { statusLabel } from '@/lib/format'

const props = defineProps<{ status?: string; stale?: boolean; label?: string }>()

const tone = computed(() => {
  if (props.stale || ['stale', 'degraded', 'warning'].includes(props.status || '')) return 'warning'
  if (['active', 'healthy', 'normal', 'available'].includes(props.status || '')) return 'success'
  if (['missing', 'invalid_limits', 'upstream_inactive', 'rate_limited', 'error', 'unavailable', 'deleted'].includes(props.status || '')) return 'danger'
  return 'neutral'
})
</script>

<template>
  <span class="status-pill" :class="`tone-${tone}`">
    <Clock3 v-if="tone === 'warning'" :size="13" />
    <CircleCheck v-else-if="tone === 'success'" :size="13" />
    <CircleAlert v-else-if="tone === 'danger'" :size="13" />
    <CircleMinus v-else :size="13" />
    {{ label || (stale ? '数据陈旧' : statusLabel(status)) }}
  </span>
</template>
