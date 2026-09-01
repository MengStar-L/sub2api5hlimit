import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { UpdateView } from '@/types'

const mocks = vi.hoisted(() => ({
  update: vi.fn(),
  checkUpdate: vi.fn(),
  applyUpdate: vi.fn(),
  success: vi.fn(),
  error: vi.fn(),
  info: vi.fn(),
}))

vi.mock('@/lib/api', () => ({
  ApiError: class ApiError extends Error {
    status = 0
    code = 'test_error'
  },
  api: { update: mocks.update, checkUpdate: mocks.checkUpdate, applyUpdate: mocks.applyUpdate },
}))
vi.mock('@/state/toast', () => ({ toast: { success: mocks.success, error: mocks.error, info: mocks.info } }))
vi.mock('@/layouts/AppShell.vue', () => ({ default: { template: '<div><slot /></div>' } }))

import AdminUpdateView from '@/views/AdminUpdateView.vue'

const available: UpdateView = {
  current: { version: 'v0.2.0', os: 'linux', arch: 'amd64' },
  latest: { version: 'v0.2.1', release_url: 'https://github.com/MengStar-L/sub2api5hlimit/releases/tag/v0.2.1', mode: 'binary', min_updater_version: 'v0.2.0' },
  status: 'update_available', update_available: true, compatible: true, updater_available: true,
}

describe('AdminUpdateView', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    mocks.update.mockResolvedValue(structuredClone(available))
    mocks.checkUpdate.mockResolvedValue(structuredClone(available))
    mocks.applyUpdate.mockResolvedValue({ operation_id: 'op-1', target_version: 'v0.2.1', state: 'queued' })
  })

  it('shows the current/latest versions and submits only the checked target', async () => {
    const wrapper = mount(AdminUpdateView)
    await flushPromises()
    expect(wrapper.text()).toContain('v0.2.0')
    expect(wrapper.text()).toContain('v0.2.1')

    const install = wrapper.findAll('button').find(button => button.text().includes('安装 v0.2.1'))
    expect(install).toBeTruthy()
    await install!.trigger('click')
    await flushPromises()
    // 安装先弹自绘确认框，请求要等用户在弹窗里按下确认
    expect(mocks.applyUpdate).not.toHaveBeenCalled()
    expect(document.body.textContent).toContain('替换前自动备份现有程序')

    const confirm = Array.from(document.body.querySelectorAll<HTMLButtonElement>('.modal-footer button'))
      .find(button => button.textContent?.includes('下载并安装'))
    expect(confirm).toBeTruthy()
    confirm!.click()
    await flushPromises()
    expect(mocks.applyUpdate).toHaveBeenCalledWith('v0.2.1')
	expect(wrapper.text()).toContain('已排队')
    wrapper.unmount()
  })

  it('surfaces an offline check instead of claiming the build is current', async () => {
    const failed = { ...available, status: 'check_failed', last_error_code: 'RELEASE_CHECK_FAILED' }
    mocks.checkUpdate.mockResolvedValue(failed)
    const wrapper = mount(AdminUpdateView)
    await flushPromises()
    const check = wrapper.findAll('button').find(button => button.text().includes('检查更新'))
    expect(check).toBeTruthy()
    await check!.trigger('click')
    await flushPromises()
    expect(mocks.error).toHaveBeenCalledWith('检查失败', '无法连接 GitHub，已保留最近一次成功结果。')
    wrapper.unmount()
  })

  it('verifies the runtime version after a reported successful restart', async () => {
    mocks.update.mockResolvedValue({
      ...available,
      operation: {
        operation_id: 'op-mismatch', target_version: 'v0.2.1', state: 'succeeded',
        phase: 'completed', rolled_back: false,
      },
    })
    const wrapper = mount(AdminUpdateView)
    await flushPromises()
    expect(wrapper.text()).toContain('当前运行版本仍是 v0.2.0')
    expect(wrapper.text()).toContain('journalctl -u sub2api-limit-portal-update.service')
    wrapper.unmount()
  })

  it('keeps update controls locked while rollback recovery is required', async () => {
    mocks.update.mockResolvedValue({
      ...available,
      operation: {
        operation_id: 'op-recovery', target_version: 'v0.2.1', state: 'failed',
        phase: 'rollback_failed', error_code: 'ROLLBACK_FAILED', rolled_back: false,
      },
    })
    const wrapper = mount(AdminUpdateView)
    await flushPromises()
    const check = wrapper.findAll('button').find(button => button.text().includes('检查更新'))
    const install = wrapper.findAll('button').find(button => button.text().includes('安装 v0.2.1'))
    expect(check?.attributes('disabled')).toBeDefined()
    expect(install?.attributes('disabled')).toBeDefined()
    expect(wrapper.text()).toContain('回滚失败')
	expect(wrapper.text()).toContain('journalctl -u sub2api-limit-portal-update.service')
	expect(wrapper.find('.update-operation .section-icon .spinning').exists()).toBe(false)
    wrapper.unmount()
  })
})
