import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { AnnouncementFeed } from '@/types'

const mocks = vi.hoisted(() => ({
  myAnnouncements: vi.fn(),
  dismissAnnouncement: vi.fn(),
  success: vi.fn(),
  error: vi.fn(),
  info: vi.fn(),
}))

vi.mock('@/lib/api', () => ({
  ApiError: class ApiError extends Error {
    status = 0
    code = 'test_error'
  },
  api: { myAnnouncements: mocks.myAnnouncements, dismissAnnouncement: mocks.dismissAnnouncement },
}))
vi.mock('@/state/toast', () => ({ toast: { success: mocks.success, error: mocks.error, info: mocks.info } }))

import AnnouncementCenter from '@/components/AnnouncementCenter.vue'
import { announcementStore } from '@/state/announcements'
import { formatDateTime } from '@/lib/format'

const feed: AnnouncementFeed = {
  announcements: [
    { id: 2, title: '周日维护窗口', body: '02:00-03:00 期间配额同步暂停。', published_at: 1_756_000_000, dismissed: false },
    { id: 1, title: '欢迎使用配额中心', body: '侧栏可随时回看公告。', published_at: 1_755_000_000, dismissed: true },
  ],
  popup: { id: 2, title: '周日维护窗口', body: '02:00-03:00 期间配额同步暂停。', published_at: 1_756_000_000, dismissed: false },
  unread_count: 1,
}

describe('AnnouncementCenter', () => {
  // 组件订阅的是模块级 store，失败的断言若跳过 unmount，
  // 残留实例会在下一个用例里继续 patch 已被清空的 teleport 节点。
  let wrappers: VueWrapper[] = []
  const render = () => {
    const wrapper = mount(AnnouncementCenter)
    wrappers.push(wrapper)
    return wrapper
  }

  beforeEach(() => {
    vi.clearAllMocks()
    announcementStore.reset()
    mocks.myAnnouncements.mockResolvedValue(structuredClone(feed))
    mocks.dismissAnnouncement.mockResolvedValue({ dismissed: true })
  })

  afterEach(() => {
    wrappers.forEach(wrapper => wrapper.unmount())
    wrappers = []
  })

  it('auto-pops the newest undismissed announcement with its publish time', async () => {
    render()
    await flushPromises()
    expect(announcementStore.state.popup?.id).toBe(2)
    // 弹窗 teleport 到 body，从文档而非组件树里断言
    expect(document.body.textContent).toContain('周日维护窗口')
    expect(document.body.textContent).toContain('已了解，不再弹出此公告')
    expect(document.body.textContent).toContain(formatDateTime(1_756_000_000))
  })

  it('records the dismissal and stops popping that announcement', async () => {
    render()
    await flushPromises()
    const confirm = Array.from(document.body.querySelectorAll('button'))
      .find(button => button.textContent?.includes('已了解，不再弹出此公告'))
    expect(confirm).toBeTruthy()
    confirm!.click()
    await flushPromises()
    expect(mocks.dismissAnnouncement).toHaveBeenCalledWith(2)
    expect(announcementStore.state.popup).toBeNull()
    expect(announcementStore.unreadCount.value).toBe(0)
  })

  it('only auto-pops once per login even when the shell remounts', async () => {
    const first = render()
    await flushPromises()
    announcementStore.closePopup()
    first.unmount()
    wrappers = wrappers.filter(wrapper => wrapper !== first)

    render()
    await flushPromises()
    expect(announcementStore.state.popup).toBeNull()
  })

  it('lists every announcement with its publish time in the drawer', async () => {
    const wrapper = render()
    await flushPromises()
    announcementStore.closePopup()
    await wrapper.find('.announce-button').trigger('click')
    await flushPromises()
    const drawer = document.body.querySelector('.announce-list')
    expect(drawer?.textContent).toContain('周日维护窗口')
    expect(drawer?.textContent).toContain('欢迎使用配额中心')
    expect(drawer?.textContent).toContain(formatDateTime(1_755_000_000))
    expect(drawer?.querySelectorAll('li').length).toBe(2)
    // 已确认过的那条不再显示按钮
    expect(drawer?.querySelector('li.read')).toBeTruthy()
  })

  it('keeps the badge count from the unread announcements', async () => {
    const wrapper = render()
    await flushPromises()
    expect(wrapper.find('.announce-badge').text()).toBe('1')
  })
})
