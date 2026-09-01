import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { AdminUser, QuotaResetJob } from '@/types'

const mocks = vi.hoisted(() => ({
  users: vi.fn(),
  upstreamKeys: vi.fn(),
  currentQuotaReset: vi.fn(),
  quotaReset: vi.fn(),
  success: vi.fn(),
  error: vi.fn(),
  info: vi.fn(),
}))

vi.mock('@/lib/api', () => {
  class ApiError extends Error {
    constructor(message: string, public status = 0, public code = 'test_error') { super(message) }
  }
  return {
    ApiError,
    api: {
      users: mocks.users,
      upstreamKeys: mocks.upstreamKeys,
      currentQuotaReset: mocks.currentQuotaReset,
      quotaReset: mocks.quotaReset,
    },
  }
})
vi.mock('@/state/toast', () => ({ toast: { success: mocks.success, error: mocks.error, info: mocks.info } }))
vi.mock('@/layouts/AppShell.vue', () => ({ default: { template: '<div><slot /></div>' } }))
vi.mock('@/components/CompactQuotaCell.vue', () => ({ default: { props: ['label'], template: '<div>{{ label }}</div>' } }))
vi.mock('@/components/SideDrawer.vue', () => ({ default: { template: '<div><slot /><slot name="footer" /></div>' } }))

import { ApiError } from '@/lib/api'
import AdminUsersView from '@/views/AdminUsersView.vue'

const user: AdminUser = {
  id: 2,
  username: 'alice',
  display_name: '林晓',
  role: 'user',
  status: 'disabled',
  created_at: '2026-08-31T12:00:00Z',
  resettable: true,
  binding: { upstream_key_id: 41, key_name: '团队 Key', masked_key: 'sk-…7f9a', binding_state: 'healthy' },
  snapshot: { stale: false },
}

const completedJob: QuotaResetJob = {
  id: 9,
  status: 'completed',
  total_count: 1,
  pending_count: 0,
  running_count: 0,
  succeeded_count: 0,
  failed_count: 0,
  unknown_count: 0,
  skipped_count: 1,
  items: [{
    id: 91,
    job_id: 9,
    user_id: 2,
    username: 'alice',
    display_name: '林晓',
    status: 'skipped',
    error_code: 'BINDING_CHANGED',
  }],
}

describe('AdminUsersView quota state', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    mocks.users.mockResolvedValue([structuredClone(user)])
    mocks.upstreamKeys.mockResolvedValue([])
    mocks.currentQuotaReset.mockRejectedValue(new ApiError('暂无批量任务', 404, 'NOT_FOUND'))
  })

  afterEach(() => vi.useRealTimers())

  it('keeps a disabled account visible even when its binding is healthy', async () => {
    const wrapper = mount(AdminUsersView)
    await flushPromises()
    expect(wrapper.find('.user-status .status-pill').text()).toContain('已停用')
    expect(wrapper.find('.user-status .status-pill').text()).not.toContain('正常')
    wrapper.unmount()
  })

  it('loads a completed historical job silently and shows skipped reasons', async () => {
    mocks.currentQuotaReset.mockResolvedValue({ ...structuredClone(completedJob), items: undefined })
    mocks.quotaReset.mockResolvedValue(structuredClone(completedJob))
    const wrapper = mount(AdminUsersView)
    await flushPromises()

		expect(mocks.quotaReset).toHaveBeenCalledWith(completedJob.id)
    expect(mocks.success).not.toHaveBeenCalledWith('批量重置已完成', expect.anything())
    expect(mocks.error).not.toHaveBeenCalledWith('批量重置已完成', expect.anything())

    const details = wrapper.findAll('button').find(button => button.text().includes('展开明细'))
    expect(details).toBeTruthy()
    await details!.trigger('click')
    expect(wrapper.text()).toContain('执行前 Key 已换绑（BINDING_CHANGED）')
    wrapper.unmount()
  })

  it('polls single-flight and retries after a transient failure', async () => {
    vi.useFakeTimers()
    const active: QuotaResetJob = {
      ...completedJob,
      status: 'running',
      pending_count: 1,
      skipped_count: 0,
      items: [],
    }
    mocks.currentQuotaReset.mockResolvedValue(active)

    let rejectFirst!: (reason?: unknown) => void
    mocks.quotaReset
      .mockImplementationOnce(() => new Promise<QuotaResetJob>((_resolve, reject) => { rejectFirst = reject }))
      .mockResolvedValueOnce({ ...completedJob, succeeded_count: 1, skipped_count: 0, items: [] })

    const wrapper = mount(AdminUsersView)
    await flushPromises()
    expect(mocks.quotaReset).toHaveBeenCalledTimes(1)

    await vi.advanceTimersByTimeAsync(10_000)
    expect(mocks.quotaReset).toHaveBeenCalledTimes(1)

    rejectFirst(new Error('temporary network failure'))
    await flushPromises()
    expect(mocks.error).toHaveBeenCalledWith('暂时无法获取批量进度', expect.stringContaining('自动重试'))

    await vi.advanceTimersByTimeAsync(1_999)
    expect(mocks.quotaReset).toHaveBeenCalledTimes(1)
    await vi.advanceTimersByTimeAsync(1)
    await flushPromises()
    expect(mocks.quotaReset).toHaveBeenCalledTimes(2)
    expect(mocks.success).toHaveBeenCalledWith('批量重置已完成', expect.stringContaining('已成功重置 1 位用户'))
    wrapper.unmount()
  })
})
