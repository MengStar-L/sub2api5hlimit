import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { CodexForecastView } from '@/types'

const mocks = vi.hoisted(() => ({ codexForecast: vi.fn() }))

vi.mock('@/lib/api', () => ({
  ApiError: class ApiError extends Error {
    status = 0
    code = 'test_error'
  },
  api: { codexForecast: mocks.codexForecast },
}))

import CodexForecastChip from '@/components/CodexForecastChip.vue'
import { codexStore } from '@/state/codex'

const nowSeconds = Math.floor(Date.now() / 1000)
const view: CodexForecastView = {
  forecast: {
    score: 72,
    breakdown: [{ label: '社区报告增多', points: 18 }],
    horizon_hours: 24,
    days_since_reset: 6,
    reset_announced: false,
    forecast_state: 'likely',
    evidence_tier: 'moderate',
    model_version: 'v3',
    source_fetched_at: nowSeconds - 600,
    checked_at: nowSeconds - 300,
    last_success_at: nowSeconds - 300,
  },
  source_url: 'https://www.willcodexquotareset.com/',
  disclaimer: '该数值由第三方站点依据公开信号推算，属于预测而非官方公告，仅供参考。',
}

describe('CodexForecastChip', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    codexStore.reset()
    mocks.codexForecast.mockResolvedValue(structuredClone(view))
  })

  it('shows the score without opening the modal', async () => {
    const wrapper = mount(CodexForecastChip)
    await flushPromises()
    expect(wrapper.find('.codex-chip-score').text()).toContain('72')
    expect(document.body.querySelector('.modal-panel')).toBeNull()
    wrapper.unmount()
  })

  it('explains the source, the prediction caveat and the fetch time on click', async () => {
    const wrapper = mount(CodexForecastChip)
    await flushPromises()
    await wrapper.find('.codex-chip').trigger('click')
    await flushPromises()
    const modal = document.body.querySelector('.modal-panel')
    expect(modal).toBeTruthy()
    expect(modal!.textContent).toContain('这是预测值，不可全信')
    expect(modal!.textContent).toContain('willcodexquotareset.com')
    expect(modal!.textContent).toContain('数据获取时间')
    expect(modal!.querySelector('a')?.getAttribute('href')).toBe('https://www.willcodexquotareset.com/')
    expect(modal!.querySelector('.codex-gauge strong')?.textContent).toBe('72')
    wrapper.unmount()
  })

  it('flags a stale value instead of presenting it as current', async () => {
    mocks.codexForecast.mockResolvedValue({
      ...structuredClone(view),
      forecast: { ...structuredClone(view).forecast, source_fetched_at: nowSeconds - 5 * 3600 },
    })
    const wrapper = mount(CodexForecastChip)
    await flushPromises()
    await wrapper.find('.codex-chip').trigger('click')
    await flushPromises()
    expect(document.body.textContent).toContain('已超过 3 小时')
    wrapper.unmount()
  })

  it('degrades to a dash when the upstream value was never fetched', async () => {
    mocks.codexForecast.mockRejectedValue(new Error('unreachable'))
    const wrapper = mount(CodexForecastChip)
    await flushPromises()
    expect(wrapper.find('.codex-chip-score').text()).toBe('—')
    wrapper.unmount()
  })
})
