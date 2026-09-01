import { createRouter, createWebHistory } from 'vue-router'
import { sessionStore } from '@/state/session'
import LoginView from '@/views/LoginView.vue'
import SetupView from '@/views/SetupView.vue'
import DashboardView from '@/views/DashboardView.vue'
import AdminUsersView from '@/views/AdminUsersView.vue'
import AdminPoolView from '@/views/AdminPoolView.vue'
import AdminUpdateView from '@/views/AdminUpdateView.vue'
import SettingsView from '@/views/SettingsView.vue'
import ProfileView from '@/views/ProfileView.vue'

declare module 'vue-router' {
  interface RouteMeta {
    requiresAuth?: boolean
    admin?: boolean
    title?: string
  }
}

export const router = createRouter({
  history: createWebHistory(),
  routes: [
    { path: '/', redirect: '/dashboard' },
    { path: '/setup', name: 'setup', component: SetupView, meta: { title: '初始化' } },
    { path: '/login', name: 'login', component: LoginView, meta: { title: '登录' } },
    { path: '/dashboard', name: 'dashboard', component: DashboardView, meta: { requiresAuth: true, title: '配额概览' } },
    { path: '/profile', name: 'profile', component: ProfileView, meta: { requiresAuth: true, title: '账户安全' } },
    { path: '/admin/users', name: 'users', component: AdminUsersView, meta: { requiresAuth: true, admin: true, title: '用户管理' } },
    { path: '/admin/pool', name: 'pool', component: AdminPoolView, meta: { requiresAuth: true, admin: true, title: '账号池' } },
    { path: '/admin/settings', name: 'settings', component: SettingsView, meta: { requiresAuth: true, admin: true, title: '连接设置' } },
    { path: '/admin/update', name: 'update', component: AdminUpdateView, meta: { requiresAuth: true, admin: true, title: '程序更新' } },
    { path: '/:pathMatch(.*)*', redirect: '/dashboard' },
  ],
  scrollBehavior: () => ({ top: 0 }),
})

router.beforeEach(async to => {
  try {
    await sessionStore.bootstrap()
  } catch {
    if (to.name !== 'login' && to.name !== 'setup') return { name: 'login', query: { unavailable: '1' } }
  }

  const setupComplete = sessionStore.state.setup?.complete
  if (setupComplete === false && to.name !== 'setup') return { name: 'setup' }
  if (setupComplete && to.name === 'setup') return sessionStore.isAuthenticated.value ? { name: 'dashboard' } : { name: 'login' }
  if (to.meta.requiresAuth && !sessionStore.isAuthenticated.value) return { name: 'login', query: { redirect: to.fullPath } }
  if (to.meta.admin && !sessionStore.isAdmin.value) return { name: 'dashboard' }
  if (to.name === 'dashboard' && sessionStore.isAdmin.value) return { name: 'users' }
  if (to.name === 'login' && sessionStore.isAuthenticated.value) return { name: sessionStore.isAdmin.value ? 'users' : 'dashboard' }
  document.title = `${to.meta.title || '配额中心'} · Sub2API`
  return true
})
