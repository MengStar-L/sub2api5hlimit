<script setup lang="ts">
import { ref } from 'vue'
import { useRouter } from 'vue-router'
import { Save, ShieldCheck, UserRound } from 'lucide-vue-next'
import AppShell from '@/layouts/AppShell.vue'
import { api, ApiError } from '@/lib/api'
import { toast } from '@/state/toast'
import { sessionStore } from '@/state/session'

const currentPassword = ref('')
const router = useRouter()
const newPassword = ref('')
const confirmPassword = ref('')
const loading = ref(false)
const error = ref('')

async function submit() {
  error.value = ''
  if (newPassword.value.length < 12) { error.value = '新密码至少需要 12 位。'; return }
  if (newPassword.value !== confirmPassword.value) { error.value = '两次输入的新密码不一致。'; return }
  loading.value = true
  try {
    await api.changePassword(currentPassword.value, newPassword.value)
    currentPassword.value = ''; newPassword.value = ''; confirmPassword.value = ''
    await sessionStore.signOut().catch(() => undefined)
    toast.success('密码已更新', '全部会话已撤销，请重新登录。')
    await router.replace('/login')
  } catch (cause) { error.value = cause instanceof ApiError ? cause.message : '密码更新失败。' }
  finally { loading.value = false }
}
</script>

<template>
  <AppShell>
    <header class="page-heading compact"><div><span class="eyebrow"><ShieldCheck :size="13" /> 账户安全</span><h1>个人设置</h1><p>管理你的登录密码和账户信息。</p></div></header>
    <div class="profile-layout">
      <section class="profile-summary">
        <span class="large-avatar">{{ (sessionStore.user.value?.display_name || sessionStore.user.value?.username || 'U').slice(0, 1).toUpperCase() }}</span>
        <div><h2>{{ sessionStore.user.value?.display_name || sessionStore.user.value?.username }}</h2><p class="mono">@{{ sessionStore.user.value?.username }}</p></div>
        <span class="role-badge"><UserRound :size="13" />{{ sessionStore.isAdmin.value ? '管理员' : '普通用户' }}</span>
      </section>
      <section class="panel accent-amber">
        <div class="panel-head"><div><h2>修改密码</h2><p>更新后会撤销其他设备上的会话</p></div></div>
        <div class="panel-body">
          <form class="password-form" @submit.prevent="submit">
            <div v-if="error" class="form-alert" role="alert">{{ error }}</div>
            <label class="field full"><span>当前密码</span><input v-model="currentPassword" type="password" autocomplete="current-password" required /></label>
            <div class="form-grid"><label class="field"><span>新密码</span><input v-model="newPassword" type="password" autocomplete="new-password" required placeholder="至少 12 位" /></label><label class="field"><span>确认新密码</span><input v-model="confirmPassword" type="password" autocomplete="new-password" required /></label></div>
            <button class="primary-button" type="submit" :disabled="loading"><span v-if="loading" class="spinner"></span><Save v-else :size="16" />{{ loading ? '正在保存' : '保存新密码' }}</button>
          </form>
        </div>
      </section>
    </div>
  </AppShell>
</template>
