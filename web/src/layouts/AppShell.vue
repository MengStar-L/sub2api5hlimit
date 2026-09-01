<script setup lang="ts">
import { computed, ref } from 'vue'
import { useRoute, useRouter, RouterLink } from 'vue-router'
import {
  Activity, Users, ServerCog, Settings2, UserRound, LogOut, Menu, X, ShieldCheck, ChevronRight, Download,
} from 'lucide-vue-next'
import BrandMark from '@/components/BrandMark.vue'
import { sessionStore } from '@/state/session'

const route = useRoute()
const router = useRouter()
const mobileOpen = ref(false)

const baseItems = [{ to: '/dashboard', label: '配额概览', icon: Activity }]
const adminItems = [
  { to: '/admin/users', label: '用户管理', icon: Users },
  { to: '/admin/pool', label: '账号池', icon: ServerCog },
  { to: '/admin/settings', label: '连接设置', icon: Settings2 },
  { to: '/admin/update', label: '程序更新', icon: Download },
]
const items = computed(() => sessionStore.isAdmin.value ? adminItems : baseItems)
const displayName = computed(() => sessionStore.user.value?.display_name || sessionStore.user.value?.username || '用户')

async function logout() {
  await sessionStore.signOut()
  await router.replace('/login')
}
</script>

<template>
  <div class="app-shell">
    <aside class="sidebar" :class="{ open: mobileOpen }">
      <div class="sidebar-top">
        <BrandMark />
        <button class="icon-button sidebar-close" type="button" aria-label="关闭导航" @click="mobileOpen = false"><X :size="19" /></button>
      </div>
      <nav aria-label="主导航">
        <p class="nav-caption">工作台</p>
        <RouterLink v-for="item in items" :key="item.to" :to="item.to" @click="mobileOpen = false">
          <component :is="item.icon" :size="18" />
          <span>{{ item.label }}</span>
          <ChevronRight class="nav-arrow" :size="15" />
        </RouterLink>
      </nav>
      <div class="sidebar-foot">
        <div class="security-note"><ShieldCheck :size="17" /><span>Key 全程脱敏显示</span></div>
        <RouterLink class="profile-link" to="/profile" @click="mobileOpen = false">
          <span class="avatar">{{ displayName.slice(0, 1).toUpperCase() }}</span>
          <span><strong>{{ displayName }}</strong><small>{{ sessionStore.isAdmin.value ? '平台管理员' : '配额用户' }}</small></span>
          <ChevronRight :size="15" />
        </RouterLink>
      </div>
    </aside>
    <div v-if="mobileOpen" class="sidebar-scrim" @click="mobileOpen = false"></div>

    <div class="shell-main">
      <header class="topbar">
        <button class="icon-button menu-button" type="button" aria-label="打开导航" @click="mobileOpen = true"><Menu :size="20" /></button>
        <div class="breadcrumb"><span>Sub2API</span><ChevronRight :size="13" /><strong>{{ route.meta.title }}</strong></div>
        <div class="topbar-actions">
          <span class="live-indicator"><i></i>服务在线</span>
          <RouterLink class="icon-button" to="/profile" title="账户设置"><UserRound :size="18" /></RouterLink>
          <button class="icon-button" type="button" title="退出登录" aria-label="退出登录" @click="logout"><LogOut :size="18" /></button>
        </div>
      </header>
      <main class="page-main"><slot /></main>
    </div>

    <nav class="mobile-nav" aria-label="移动端导航">
      <RouterLink v-for="item in items.slice(0, 4)" :key="item.to" :to="item.to">
        <component :is="item.icon" :size="19" /><span>{{ item.label.replace('管理', '') }}</span>
      </RouterLink>
    </nav>
  </div>
</template>
