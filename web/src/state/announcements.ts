import { computed, reactive } from 'vue'
import { api } from '@/lib/api'
import type { Announcement } from '@/types'

interface AnnouncementState {
  items: Announcement[]
  loading: boolean
  loaded: boolean
  popup: Announcement | null
  drawerOpen: boolean
}

const state = reactive<AnnouncementState>({
  items: [],
  loading: false,
  loaded: false,
  popup: null,
  drawerOpen: false,
})

// AppShell 会随路由切换重新挂载，模块级标记保证「本次登录只自动弹一次」。
let autoPopupUsed = false
let inflight: Promise<void> | null = null

async function load(options: { autoPopup?: boolean } = {}) {
  if (inflight) return inflight
  state.loading = true
  inflight = (async () => {
    try {
      const feed = await api.myAnnouncements()
      state.items = feed.announcements || []
      state.loaded = true
      if (options.autoPopup && !autoPopupUsed && feed.popup) {
        autoPopupUsed = true
        state.popup = feed.popup
      }
    } finally {
      state.loading = false
      inflight = null
    }
  })()
  return inflight
}

async function dismiss(id: Announcement['id']) {
  await api.dismissAnnouncement(id)
  const item = state.items.find(entry => String(entry.id) === String(id))
  if (item) item.dismissed = true
  if (state.popup && String(state.popup.id) === String(id)) state.popup = null
}

function closePopup() { state.popup = null }
function openDrawer() { state.drawerOpen = true }
function closeDrawer() { state.drawerOpen = false }

// 退出登录时复位，下一位用户登录仍会收到自动弹窗。
function reset() {
  state.items = []
  state.loaded = false
  state.popup = null
  state.drawerOpen = false
  autoPopupUsed = false
}

export const announcementStore = {
  state,
  load,
  dismiss,
  closePopup,
  openDrawer,
  closeDrawer,
  reset,
  unreadCount: computed(() => state.items.filter(item => !item.dismissed).length),
}
