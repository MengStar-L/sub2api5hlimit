<script setup lang="ts">
import { computed, onBeforeUnmount, ref, useId } from 'vue'
import { Clock3, TriangleAlert } from 'lucide-vue-next'
import { formatCountdown, formatDateTime, formatUSD, keyPercent } from '@/lib/format'
import type { KeyWindowView } from '@/types'

const props = defineProps<{ label: string; window?: KeyWindowView | null; stale?: boolean; bindingStatus?: string }>()
const now = ref(Date.now())
const resetTooltipID = useId()
const timer = setInterval(() => { now.value = Date.now() }, 30_000)
onBeforeUnmount(() => clearInterval(timer))

const issue = computed(() => {
	if (props.bindingStatus === 'missing') return 'Key 已缺失'
	if (props.bindingStatus === 'invalid_limits') return '限额异常'
	if (!props.window) return props.bindingStatus === 'missing' ? 'Key 已缺失' : '未绑定'
  if (!Number.isFinite(props.window.limit_usd) || props.window.limit_usd <= 0) return '限额异常'
  return ''
})
const percent = computed(() => keyPercent(props.window))

function resetAriaLabel(value?: number | string | null) {
  return value ? `${props.label} 绝对重置时间：${formatDateTime(value)}` : `${props.label} 重置时间尚未启动`
}
</script>

<template>
  <div class="compact-quota" :class="{ issue: Boolean(issue) }">
    <template v-if="window && !issue">
      <div class="compact-quota-head"><strong>{{ label }}</strong><span>{{ Math.round(percent) }}%</span></div>
      <div class="compact-quota-money">{{ formatUSD(window.used_usd) }} <small>/ {{ formatUSD(window.limit_usd) }}</small></div>
      <div class="compact-quota-track" aria-hidden="true"><span :style="{ width: `${percent}%` }"></span></div>
      <time class="compact-quota-reset" :class="{ stale }" :title="formatDateTime(window.reset_at)" :tabindex="window.reset_at ? 0 : undefined" :aria-label="resetAriaLabel(window.reset_at)" :aria-describedby="window.reset_at ? resetTooltipID : undefined"><Clock3 :size="11" /><span v-if="stale">数据陈旧 · </span>{{ formatCountdown(window.reset_at, now) }}<span v-if="window.reset_at" :id="resetTooltipID" class="quota-reset-tooltip" role="tooltip">{{ formatDateTime(window.reset_at) }}</span></time>
    </template>
    <template v-else>
      <div class="compact-quota-head"><strong>{{ label }}</strong></div>
      <span class="compact-quota-issue"><TriangleAlert :size="12" />{{ issue || '尚未启动' }}</span>
      <time v-if="window?.reset_at" class="compact-quota-reset" :title="formatDateTime(window.reset_at)" tabindex="0" :aria-label="resetAriaLabel(window.reset_at)" :aria-describedby="resetTooltipID"><Clock3 :size="11" />{{ formatCountdown(window.reset_at, now) }}<span :id="resetTooltipID" class="quota-reset-tooltip" role="tooltip">{{ formatDateTime(window.reset_at) }}</span></time>
    </template>
  </div>
</template>
