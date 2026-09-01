<script setup lang="ts">
import { computed, onMounted } from 'vue'
import { BellRing, Check, Megaphone } from 'lucide-vue-next'
import SideDrawer from '@/components/SideDrawer.vue'
import ModalDialog from '@/components/ModalDialog.vue'
import EmptyState from '@/components/EmptyState.vue'
import { announcementStore } from '@/state/announcements'
import { formatDateTime } from '@/lib/format'
import { toast } from '@/state/toast'
import type { Announcement } from '@/types'

const state = announcementStore.state
const unread = announcementStore.unreadCount
const popup = computed(() => state.popup)

onMounted(() => { void announcementStore.load({ autoPopup: true }).catch(() => {}) })

function openList() {
  announcementStore.openDrawer()
  if (!state.loaded) void announcementStore.load().catch(() => {})
}

async function dismiss(item: Announcement) {
  try {
    await announcementStore.dismiss(item.id)
    toast.success('已记录', '这条公告不会再自动弹出。')
  } catch {
    toast.error('操作失败', '请稍后重试。')
  }
}
</script>

<template>
  <button class="announce-button" type="button" :aria-label="`公告${unread ? `，${unread} 条未读` : ''}`" @click="openList">
    <Megaphone :size="16" />
    <span>公告</span>
    <em v-if="unread" class="announce-badge">{{ unread > 99 ? '99+' : unread }}</em>
  </button>

  <SideDrawer :open="state.drawerOpen" title="平台公告" description="管理员发布的通知，按发布时间排序" @close="announcementStore.closeDrawer()">
    <div v-if="state.loading && !state.loaded" class="announce-loading"><span class="skeleton skeleton-row"></span><span class="skeleton skeleton-row"></span></div>
    <EmptyState v-else-if="state.items.length === 0" title="暂无公告" description="管理员发布公告后会显示在这里，并在你首次登录时提醒一次。" />
    <ol v-else class="announce-list">
      <li v-for="item in state.items" :key="item.id" :class="{ read: item.dismissed }">
        <div class="announce-item-head">
          <h3>{{ item.title }}</h3>
          <time :datetime="String(item.published_at)">{{ formatDateTime(item.published_at) }}</time>
        </div>
        <p class="announce-body">{{ item.body }}</p>
        <button v-if="!item.dismissed" class="text-button" type="button" @click="dismiss(item)"><Check :size="14" />已了解，不再弹出此公告</button>
        <span v-else class="announce-read-tag"><Check :size="13" />已了解</span>
      </li>
    </ol>
  </SideDrawer>

  <ModalDialog
    :open="Boolean(popup)"
    :title="popup?.title || '平台公告'"
    :description="popup ? `发布时间 ${formatDateTime(popup.published_at)}` : undefined"
    persistent
  >
    <div class="announce-popup">
      <span class="announce-popup-icon"><BellRing :size="18" /></span>
      <p class="announce-body">{{ popup?.body }}</p>
    </div>
    <template #footer>
      <button class="secondary-button" type="button" @click="announcementStore.closePopup()">稍后再看</button>
      <button class="primary-button" type="button" @click="popup && dismiss(popup)"><Check :size="16" />已了解，不再弹出此公告</button>
    </template>
  </ModalDialog>
</template>
