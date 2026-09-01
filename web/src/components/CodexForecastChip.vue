<script setup lang="ts">
import { computed, onMounted } from 'vue'
import { CircleAlert, ExternalLink, Info, TriangleAlert } from 'lucide-vue-next'
import ModalDialog from '@/components/ModalDialog.vue'
import { codexStore } from '@/state/codex'
import { formatDateTime } from '@/lib/format'

const state = codexStore.state
const forecast = computed(() => state.forecast)

onMounted(() => { void codexStore.load() })

// 上游用 0-100 的分值表达「近期会重置」的可能性
const score = computed(() => (forecast.value ? Math.round(forecast.value.score) : null))
const tone = computed(() => {
  const value = score.value
  if (value === null) return 'neutral'
  if (value >= 67) return 'jade'
  if (value >= 34) return 'saffron'
  return 'muted'
})
const stateLabel = computed(() => {
  const labels: Record<string, string> = {
    likely: '可能性较高', possible: '存在可能', unlikely: '可能性较低',
    imminent: '即将重置', announced: '已有重置信号', unknown: '信号不足',
  }
  const raw = forecast.value?.forecast_state || ''
  return labels[raw] || raw || '—'
})
const tierLabel = computed(() => {
  const labels: Record<string, string> = { strong: '证据充分', moderate: '证据中等', weak: '证据薄弱', none: '暂无证据' }
  const raw = forecast.value?.evidence_tier || ''
  return labels[raw] || raw || '—'
})
const fetchedAt = computed(() => forecast.value?.source_fetched_at || forecast.value?.last_success_at || null)
const sinceReset = computed(() => {
  const days = forecast.value?.days_since_reset
  const hours = forecast.value?.hours_since_reset
  if (days === null || days === undefined) return hours ? `${hours} 小时` : '—'
  return `${days} 天`
})
</script>

<template>
  <button
    class="codex-chip"
    :class="`tone-${tone}`"
    type="button"
    :disabled="state.loading && !state.loaded"
    :aria-label="score === null ? 'Codex 重置预测：暂无数据' : `Codex 重置预测 ${score} 分，点击查看数据来源与说明`"
    @click="codexStore.openModal()"
  >
    <span class="codex-chip-label">Codex 重置预测<em>第三方预测</em></span>
    <span v-if="score !== null" class="codex-chip-score">{{ score }}<i>/100</i></span>
    <span v-else class="codex-chip-score muted-score">—</span>
  </button>

  <ModalDialog
    :open="state.modalOpen"
    title="Codex 配额重置预测"
    description="来自第三方站点的推算结果，仅作参考"
    wide
    @close="codexStore.closeModal()"
  >
    <div class="codex-detail">
      <div class="codex-gauge" :class="`tone-${tone}`">
        <strong>{{ score === null ? '—' : score }}</strong>
        <small>满分 100</small>
        <span class="codex-gauge-state">{{ stateLabel }}</span>
        <span class="codex-gauge-track"><i :style="{ width: `${score ?? 0}%` }"></i></span>
        <dl class="codex-gauge-meta">
          <div><dt>预测窗口</dt><dd>{{ forecast?.horizon_hours ? `未来 ${forecast.horizon_hours} 小时` : '—' }}</dd></div>
          <div><dt>距上次重置</dt><dd>{{ sinceReset }}</dd></div>
          <div><dt>证据强度</dt><dd>{{ tierLabel }}</dd></div>
        </dl>
      </div>

      <div class="codex-notes">
        <div class="notice tone-warning">
          <TriangleAlert :size="16" />
          <div><strong>这是预测值，不可全信</strong><p>{{ state.disclaimer || '该数值由第三方站点依据公开信号推算，属于预测而非官方公告，仅供参考。' }}</p></div>
        </div>

        <dl class="codex-meta">
          <div><dt>数据来源</dt><dd><a :href="state.sourceUrl || 'https://www.willcodexquotareset.com/'" target="_blank" rel="noreferrer noopener">willcodexquotareset.com <ExternalLink :size="12" /></a></dd></div>
          <div><dt>数据获取时间</dt><dd>{{ formatDateTime(fetchedAt) }}</dd></div>
          <div><dt>本地检查时间</dt><dd>{{ formatDateTime(forecast?.checked_at) }}</dd></div>
          <div v-if="forecast?.latest_reset_at"><dt>上游记录的最近重置</dt><dd>{{ formatDateTime(forecast.latest_reset_at) }}</dd></div>
          <div v-if="forecast?.model_version"><dt>上游模型版本</dt><dd class="mono">{{ forecast.model_version }}</dd></div>
        </dl>

        <div v-if="codexStore.stale.value" class="notice tone-warning">
          <CircleAlert :size="16" /><span>距上次成功获取已超过 3 小时，数值可能已经过时。</span>
        </div>
        <div v-else-if="state.error || forecast?.last_error_code" class="notice tone-warning">
          <CircleAlert :size="16" /><span>最近一次抓取未成功{{ forecast?.last_error_code ? `（${forecast.last_error_code}）` : '' }}，正在展示上一次成功的结果。</span>
        </div>

        <div v-if="forecast?.breakdown?.length" class="codex-breakdown">
          <h3>上游给出的加分项</h3>
          <ul>
            <li v-for="item in forecast.breakdown" :key="item.label"><span>{{ item.label }}</span><em>{{ item.points > 0 ? `+${item.points}` : item.points }}</em></li>
          </ul>
        </div>

        <p class="codex-foot"><Info :size="13" />数值由门户后台定时抓取并缓存，不会把你的浏览器暴露给第三方站点。</p>
      </div>
    </div>
  </ModalDialog>
</template>
