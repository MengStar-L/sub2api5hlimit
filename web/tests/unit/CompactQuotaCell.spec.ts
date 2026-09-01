import { describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'
import CompactQuotaCell from '@/components/CompactQuotaCell.vue'

describe('CompactQuotaCell', () => {
  it('renders amount, percentage, and the precise reset time tooltip', () => {
    const wrapper = mount(CompactQuotaCell, {
      props: { label: '5h', window: { limit_usd: 20, used_usd: 5, remaining_usd: 15, percent: 25, reset_at: 1_900_000_000 } },
    })
    expect(wrapper.text()).toContain('$5.00')
    expect(wrapper.text()).toContain('25%')
    expect(wrapper.find('time').attributes('title')).not.toBe('—')
    expect(wrapper.find('time').attributes('tabindex')).toBe('0')
    expect(wrapper.find('time').attributes('aria-label')).toContain('绝对重置时间')
    expect(wrapper.find('time').attributes('aria-describedby')).toBe(wrapper.find('[role="tooltip"]').attributes('id'))
    expect(wrapper.find('[role="tooltip"]').text()).toBe(wrapper.find('time').attributes('title'))
  })

  it('distinguishes an unbound key from invalid limits and stale data', () => {
    const unbound = mount(CompactQuotaCell, { props: { label: '7d', bindingStatus: 'unbound' } })
    const invalid = mount(CompactQuotaCell, { props: { label: '7d', window: { limit_usd: 0, used_usd: 0, remaining_usd: 0, percent: 0 } } })
    const stale = mount(CompactQuotaCell, { props: { label: '7d', stale: true, window: { limit_usd: 20, used_usd: 5, remaining_usd: 15, percent: 25, reset_at: 1_900_000_000 } } })
    expect(unbound.text()).toContain('未绑定')
    expect(invalid.text()).toContain('限额异常')
    expect(stale.text()).toContain('数据陈旧')
    expect(stale.text()).toContain('$5.00')
  })

  it('does not present an old snapshot as current when the upstream key is missing', () => {
    const wrapper = mount(CompactQuotaCell, {
      props: {
        label: '5h', bindingStatus: 'missing',
        window: { limit_usd: 20, used_usd: 5, remaining_usd: 15, percent: 25, reset_at: 1_900_000_000 },
      },
    })
    expect(wrapper.text()).toContain('Key 已缺失')
    expect(wrapper.text()).not.toContain('$5.00')
  })

  it('keeps the not-started reset state visible on a stale snapshot', () => {
    const wrapper = mount(CompactQuotaCell, {
      props: {
        label: '5h', stale: true,
        window: { limit_usd: 20, used_usd: 0, remaining_usd: 20, percent: 0, reset_at: null },
      },
    })
    expect(wrapper.text()).toContain('数据陈旧')
    expect(wrapper.text()).toContain('尚未启动')
  })
})
