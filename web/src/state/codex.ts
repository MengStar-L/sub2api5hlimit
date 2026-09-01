import { computed, reactive } from 'vue'
import { api } from '@/lib/api'
import type { CodexForecast } from '@/types'

interface CodexState {
  forecast: CodexForecast | null
  sourceUrl: string
  disclaimer: string
  loading: boolean
  loaded: boolean
  error: string
  modalOpen: boolean
}

const state = reactive<CodexState>({
  forecast: null,
  sourceUrl: '',
  disclaimer: '',
  loading: false,
  loaded: false,
  error: '',
  modalOpen: false,
})

let inflight: Promise<void> | null = null

async function load() {
  if (inflight) return inflight
  state.loading = true
  inflight = (async () => {
    try {
      const view = await api.codexForecast()
      state.forecast = view.forecast
      state.sourceUrl = view.source_url
      state.disclaimer = view.disclaimer
      state.error = ''
      state.loaded = true
    } catch {
      // 第三方数据抓不到不影响门户本身，只把外层展示降级
      state.error = '暂时无法获取预测数值'
    } finally {
      state.loading = false
      inflight = null
    }
  })()
  return inflight
}

function openModal() { state.modalOpen = true }
function closeModal() { state.modalOpen = false }

function reset() {
  state.forecast = null
  state.loaded = false
  state.error = ''
  state.modalOpen = false
}

// 上游长时间没刷新时，数值参考价值下降，外层用它加一条陈旧提示。
const stale = computed(() => {
  const at = state.forecast?.source_fetched_at ?? state.forecast?.last_success_at
  if (!at) return false
  return Date.now() / 1000 - at > 3 * 3600
})

export const codexStore = {
  state,
  load,
  openModal,
  closeModal,
  reset,
  stale,
  hasScore: computed(() => Boolean(state.forecast && state.forecast.last_success_at)),
}
