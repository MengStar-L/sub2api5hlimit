import { describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'
import QuotaBand from '@/components/QuotaBand.vue'

describe('QuotaBand', () => {
  it('renders key dollars, percentage, and an unstarted reset window', () => {
    const wrapper = mount(QuotaBand, {
      props: {
        label: '5 小时额度', kind: 'key',
        window: { limit_usd: 50, used_usd: 12, remaining_usd: 38, percent: 24, reset_at: null },
      },
    })
    expect(wrapper.text()).toContain('5 小时额度')
    expect(wrapper.text()).toContain('$38.00 可用')
    expect(wrapper.text()).toContain('已用 $12.00 / $50.00')
    expect(wrapper.text()).toContain('尚未启动')
  })

  it('never displays an unsupported provider window as zero usage', () => {
    const wrapper = mount(QuotaBand, {
      props: { label: '7d', kind: 'pool', window: { supported: false } },
    })
    expect(wrapper.text()).toContain('未提供')
    expect(wrapper.text()).not.toContain('0% 已用')
  })
})
