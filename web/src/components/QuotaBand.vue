<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { Clock3, Gauge, CircleSlash2 } from 'lucide-vue-next'
import { formatCountdown, formatUSD, keyPercent, poolPercent } from '@/lib/format'
import type { KeyWindowView, PoolWindowView } from '@/types'

const props = withDefaults(defineProps<{
  label: string
  kind: 'key' | 'pool'
  window?: KeyWindowView | PoolWindowView | null
  accent?: 'cobalt' | 'mint' | 'violet' | 'amber'
}>(), { accent: 'cobalt' })

const isKey = computed(() => props.kind === 'key')
const supported = computed(() => isKey.value || Boolean((props.window as PoolWindowView | undefined)?.supported))
const target = computed(() => isKey.value ? keyPercent(props.window as KeyWindowView) : poolPercent(props.window as PoolWindowView))
const animated = ref(0)
let frame = 0

watch(target, (next, previous = 0) => {
  cancelAnimationFrame(frame)
  const end = next ?? 0
  if (window.matchMedia?.('(prefers-reduced-motion: reduce)').matches) {
    animated.value = end
    return
  }
  const start = previous ?? 0
  const started = performance.now()
  const tick = (time: number) => {
    const progress = Math.min(1, (time - started) / 650)
    const eased = 1 - Math.pow(1 - progress, 3)
    animated.value = start + (end - start) * eased
    if (progress < 1) frame = requestAnimationFrame(tick)
  }
  frame = requestAnimationFrame(tick)
}, { immediate: true })

onBeforeUnmount(() => cancelAnimationFrame(frame))

const resetAt = computed(() => props.window?.reset_at)
const ringOffset = computed(() => 138.23 * (1 - Math.min(100, animated.value) / 100))
</script>

<template>
  <article class="quota-band" :class="[`accent-${accent}`, { unsupported: !supported }]">
    <div class="quota-ring" :aria-label="`${label} 已使用 ${Math.round(animated)}%`">
      <svg viewBox="0 0 52 52" aria-hidden="true">
        <circle class="ring-track" cx="26" cy="26" r="22" />
        <circle v-if="supported" class="ring-value" cx="26" cy="26" r="22" :style="{ strokeDashoffset: ringOffset }" />
      </svg>
      <div v-if="supported" class="ring-number"><strong>{{ Math.round(animated) }}</strong><span>%</span></div>
      <CircleSlash2 v-else :size="22" />
    </div>

    <div class="quota-copy">
      <div class="quota-heading">
        <span class="quota-label"><Gauge :size="15" />{{ label }}</span>
        <strong v-if="isKey && window">{{ formatUSD((window as KeyWindowView).remaining_usd) }} 可用</strong>
        <strong v-else-if="supported">{{ Math.round(animated) }}% 已用</strong>
        <strong v-else>未提供</strong>
      </div>
      <div class="quota-track" :aria-hidden="true">
        <span v-if="supported" :style="{ width: `${animated}%` }"></span>
      </div>
      <div class="quota-meta">
        <span v-if="isKey && window">已用 {{ formatUSD((window as KeyWindowView).used_usd) }} / {{ formatUSD((window as KeyWindowView).limit_usd) }}</span>
        <span v-else-if="supported">Provider 用量窗口</span>
        <span v-else>当前账号未返回此窗口</span>
        <span><Clock3 :size="13" />{{ supported ? formatCountdown(resetAt) : '—' }}</span>
      </div>
    </div>
  </article>
</template>
