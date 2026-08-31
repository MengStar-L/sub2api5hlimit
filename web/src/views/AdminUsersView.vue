<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { UserPlus, Search, MoreHorizontal, KeyRound, UserX, UserCheck, RefreshCw, Trash2, Save, Unlink, Users, ShieldCheck } from 'lucide-vue-next'
import AppShell from '@/layouts/AppShell.vue'
import SideDrawer from '@/components/SideDrawer.vue'
import StatusPill from '@/components/StatusPill.vue'
import EmptyState from '@/components/EmptyState.vue'
import { api, ApiError } from '@/lib/api'
import { formatDateTime } from '@/lib/format'
import { toast } from '@/state/toast'
import type { AdminUser, UpstreamKey, UserStatus } from '@/types'

const users = ref<AdminUser[]>([])
const keys = ref<UpstreamKey[]>([])
const loading = ref(true)
const refreshing = ref(false)
const query = ref('')
const statusFilter = ref<'all' | UserStatus>('all')
const drawerOpen = ref(false)
const drawerMode = ref<'create' | 'edit'>('create')
const saving = ref(false)
const formError = ref('')
const originalKeyId = ref<string>('')
const form = reactive({
  id: '' as string | number,
  username: '', display_name: '', password: '', status: 'active' as UserStatus, upstream_key_id: '',
})

const filtered = computed(() => users.value.filter(user => {
  const text = query.value.trim().toLowerCase()
  const matches = !text || `${user.username} ${user.display_name || ''} ${user.binding?.key_name || ''}`.toLowerCase().includes(text)
  return matches && (statusFilter.value === 'all' || user.status === statusFilter.value)
}))
const stats = computed(() => ({
  total: users.value.filter(u => u.role !== 'admin' && u.status !== 'deleted').length,
  active: users.value.filter(u => u.role !== 'admin' && u.status === 'active').length,
  bound: users.value.filter(u => u.role !== 'admin' && u.binding).length,
}))
const selectableKeys = computed(() => keys.value.filter(key => !key.bound_user_id || String(key.bound_user_id) === String(form.id)))

async function load(showToast = false) {
  refreshing.value = true
  try {
    const [userList, keyList] = await Promise.all([api.users(), api.upstreamKeys()])
    users.value = userList
    keys.value = keyList
    if (showToast) toast.success('列表已更新')
  } catch (cause) { toast.error('加载失败', cause instanceof ApiError ? cause.message : undefined) }
  finally { loading.value = false; refreshing.value = false }
}

function openCreate() {
  drawerMode.value = 'create'; form.id = ''; form.username = ''; form.display_name = ''; form.password = ''; form.status = 'active'; form.upstream_key_id = ''; originalKeyId.value = ''; formError.value = ''; drawerOpen.value = true
}

function openEdit(user: AdminUser) {
  drawerMode.value = 'edit'; form.id = user.id; form.username = user.username; form.display_name = user.display_name || ''; form.password = ''; form.status = user.status; form.upstream_key_id = String(user.binding?.upstream_key_id ?? user.binding?.key_id ?? ''); originalKeyId.value = form.upstream_key_id; formError.value = ''; drawerOpen.value = true
}

async function submit() {
  formError.value = ''
  if (!form.username.trim()) { formError.value = '请输入用户名。'; return }
  if (drawerMode.value === 'create' && form.password.length < 12) { formError.value = '初始密码至少需要 12 位。'; return }
  if (drawerMode.value === 'create' && !form.upstream_key_id) { formError.value = '创建用户时必须选择一个 5h/7d 限额均合规的 Key。'; return }
  saving.value = true
  try {
    if (drawerMode.value === 'create') {
      await api.createUser({ username: form.username.trim(), display_name: form.display_name.trim(), password: form.password, upstream_key_id: form.upstream_key_id || null })
      toast.success('用户已创建', '用户与 Key 已原子绑定。')
    } else {
      await api.updateUser(form.id, { display_name: form.display_name.trim(), status: form.status })
      if (form.password) {
        if (form.password.length < 12) throw new ApiError('重置密码至少需要 12 位。')
        await api.resetUserPassword(form.id, form.password)
      }
      if (form.upstream_key_id !== originalKeyId.value) {
        if (form.upstream_key_id) await api.bindUserKey(form.id, form.upstream_key_id)
        else await api.unbindUserKey(form.id)
      }
      toast.success('用户已更新', form.password ? '密码已重置，原会话已撤销。' : undefined)
    }
    drawerOpen.value = false
    await load()
  } catch (cause) { formError.value = cause instanceof ApiError ? cause.message : '保存失败，请稍后重试。' }
  finally { saving.value = false }
}

async function toggleStatus(user: AdminUser) {
  const status = user.status === 'active' ? 'disabled' : 'active'
  try { await api.updateUser(user.id, { display_name: user.display_name || '', status }); user.status = status; toast.success(status === 'active' ? '用户已启用' : '用户已停用') }
  catch (cause) { toast.error('操作失败', cause instanceof ApiError ? cause.message : undefined) }
}

async function removeUser(user: AdminUser) {
  if (!window.confirm(`确认软删除用户“${user.display_name || user.username}”？上游 Key 不会被修改。`)) return
  try { await api.deleteUser(user.id); toast.success('用户已删除', '上游 Key 保持不变。'); await load() }
  catch (cause) { toast.error('删除失败', cause instanceof ApiError ? cause.message : undefined) }
}

onMounted(() => void load())
</script>

<template>
  <AppShell>
    <header class="page-heading">
      <div><span class="eyebrow"><Users :size="14" /> 平台权限</span><h1>用户管理</h1><p>创建门户用户，并为每位用户绑定一个合规 Key。</p></div>
      <div class="heading-actions"><button class="icon-button" type="button" title="刷新" @click="load(true)"><RefreshCw :size="17" :class="{ spinning: refreshing }" /></button><button class="primary-button" type="button" @click="openCreate"><UserPlus :size="17" />添加用户</button></div>
    </header>

    <div class="stats-strip">
      <div><span class="metric-dot cobalt"></span><small>普通用户</small><strong>{{ stats.total }}</strong></div>
      <div><span class="metric-dot mint"></span><small>当前启用</small><strong>{{ stats.active }}</strong></div>
      <div><span class="metric-dot violet"></span><small>已绑定 Key</small><strong>{{ stats.bound }}</strong></div>
      <div class="stats-note"><ShieldCheck :size="17" /><span>每个 Key 仅能绑定一位用户</span></div>
    </div>

    <section class="section-block admin-table-section">
      <div class="table-tools">
        <label class="search-field"><Search :size="16" /><input v-model="query" type="search" placeholder="搜索用户或 Key" aria-label="搜索用户" /></label>
        <select v-model="statusFilter" class="select-control" aria-label="状态筛选"><option value="all">全部状态</option><option value="active">正常</option><option value="disabled">已停用</option><option value="deleted">已删除</option></select>
      </div>
      <div v-if="loading" class="pool-loading"><span v-for="n in 5" :key="n" class="skeleton skeleton-row"></span></div>
      <EmptyState v-else-if="filtered.length === 0" title="没有匹配的用户" description="调整搜索条件，或创建首位普通用户。"><button class="secondary-button" type="button" @click="openCreate"><UserPlus :size="16" />添加用户</button></EmptyState>
      <div v-else class="responsive-table">
        <table class="admin-table">
          <thead><tr><th>用户</th><th>角色</th><th>Key 绑定</th><th>状态</th><th>创建时间</th><th><span class="sr-only">操作</span></th></tr></thead>
          <TransitionGroup tag="tbody" name="row">
            <tr v-for="user in filtered" :key="user.id">
              <td data-label="用户"><div class="user-cell"><span class="mini-avatar">{{ (user.display_name || user.username).slice(0, 1).toUpperCase() }}</span><div><strong>{{ user.display_name || user.username }}</strong><small class="mono">@{{ user.username }}</small></div></div></td>
              <td data-label="角色"><span class="role-text">{{ user.role === 'admin' ? '管理员' : '普通用户' }}</span></td>
              <td data-label="Key 绑定"><div v-if="user.binding" class="binding-cell"><KeyRound :size="15" /><span><strong>{{ user.binding.key_name || '已绑定 Key' }}</strong><small class="mono">{{ user.binding.masked_key || 'sk-…••••' }}</small></span></div><span v-else class="muted">未绑定</span></td>
              <td data-label="状态"><StatusPill :status="user.status" /></td>
              <td data-label="创建时间"><span class="timestamp">{{ formatDateTime(user.created_at) }}</span></td>
              <td data-label="操作" class="row-actions"><template v-if="user.role !== 'admin' && user.status !== 'deleted'"><button class="icon-button subtle" type="button" :title="user.status === 'active' ? '停用用户' : '启用用户'" @click="toggleStatus(user)"><UserX v-if="user.status === 'active'" :size="16" /><UserCheck v-else :size="16" /></button><button class="icon-button subtle" type="button" title="编辑用户" @click="openEdit(user)"><MoreHorizontal :size="18" /></button><button class="icon-button subtle danger" type="button" title="软删除用户" @click="removeUser(user)"><Trash2 :size="16" /></button></template></td>
            </tr>
          </TransitionGroup>
        </table>
      </div>
    </section>

    <SideDrawer :open="drawerOpen" :title="drawerMode === 'create' ? '添加用户' : '编辑用户'" :description="drawerMode === 'create' ? '创建账户并原子绑定一个合规 Key' : `管理 @${form.username} 的状态与绑定`" @close="drawerOpen = false">
      <form id="user-form" class="drawer-form" @submit.prevent="submit">
        <div v-if="formError" class="form-alert" role="alert">{{ formError }}</div>
        <div class="form-section"><h3>基本信息</h3><label class="field full"><span>用户名</span><input v-model="form.username" required :disabled="drawerMode === 'edit'" autocomplete="off" placeholder="例如 alice" /></label><label class="field full"><span>显示名称</span><input v-model="form.display_name" placeholder="用于界面展示" /></label></div>
        <div v-if="drawerMode === 'edit'" class="form-section"><h3>账户状态</h3><label class="field full"><span>状态</span><select v-model="form.status"><option value="active">正常</option><option value="disabled">停用</option></select></label></div>
        <div class="form-section"><h3>{{ drawerMode === 'create' ? '初始密码' : '重置密码' }}</h3><label class="field full"><span>{{ drawerMode === 'create' ? '密码' : '新密码（留空则不修改）' }}</span><input v-model="form.password" type="password" :required="drawerMode === 'create'" autocomplete="new-password" placeholder="至少 12 位" /></label></div>
        <div class="form-section"><h3>Key 绑定</h3><label class="field full"><span>上游 Key</span><select v-model="form.upstream_key_id" :required="drawerMode === 'create'"><option v-if="drawerMode === 'edit'" value="">解除当前绑定</option><option v-else value="" disabled>选择一个合规 Key</option><option v-for="key in selectableKeys" :key="key.id" :value="String(key.id)" :disabled="key.eligible === false">{{ key.name }} · {{ key.masked_key || '已脱敏' }} · 5h ${{ key.rate_limit_5h }} / 7d ${{ key.rate_limit_7d }}{{ key.eligible === false ? '（限额不合规）' : '' }}</option></select></label><button v-if="drawerMode === 'edit' && form.upstream_key_id" class="text-button danger-text" type="button" @click="form.upstream_key_id = ''"><Unlink :size="15" />解除绑定</button></div>
      </form>
      <template #footer><button class="secondary-button" type="button" @click="drawerOpen = false">取消</button><button class="primary-button" type="submit" form="user-form" :disabled="saving"><span v-if="saving" class="spinner"></span><Save v-else :size="17" />{{ saving ? '正在保存' : '保存用户' }}</button></template>
    </SideDrawer>
  </AppShell>
</template>
