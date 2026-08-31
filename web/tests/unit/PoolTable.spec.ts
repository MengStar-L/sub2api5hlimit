import { describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'
import PoolTable from '@/components/PoolTable.vue'

const accounts = [
  {
    id: 1, masked_account: 'raou***@gmail.com', provider: 'OpenAI', status: 'active',
    window_5h: { supported: true, utilization: 18, reset_at: '2026-08-31T10:00:00Z' },
    window_7d: { supported: true, utilization: 46, reset_at: '2026-09-04T10:00:00Z' },
    snapshot: { stale: false, as_of: '2026-08-31T05:00:00Z' },
  },
  {
    id: 2, masked_account: 'ops***@example.com', provider: 'Anthropic', status: 'error',
    window_5h: { supported: false }, window_7d: { supported: false },
    snapshot: { stale: true, as_of: '2026-08-31T04:00:00Z' },
  },
]

describe('PoolTable', () => {
  it('filters accounts by text and attention state', async () => {
    const wrapper = mount(PoolTable, { props: { accounts } })
    expect(wrapper.text()).toContain('raou***@gmail.com')
    expect(wrapper.text()).toContain('ops***@example.com')

    await wrapper.get('input[type="search"]').setValue('OpenAI')
    expect(wrapper.text()).toContain('raou***@gmail.com')
    expect(wrapper.text()).not.toContain('ops***@example.com')

    await wrapper.get('input[type="search"]').setValue('')
    await wrapper.findAll('button').find(button => button.text() === '需关注')?.trigger('click')
    expect(wrapper.text()).not.toContain('raou***@gmail.com')
    expect(wrapper.text()).toContain('ops***@example.com')
  })
})
