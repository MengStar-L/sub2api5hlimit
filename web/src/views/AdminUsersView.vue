<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, reactive, ref } from 'vue'
import { UserPlus, Search, MoreHorizontal, KeyRound, UserX, UserCheck, RefreshCw, Trash2, Save, Unlink, Users, ShieldCheck, RotateCcw, ChevronDown, CircleAlert, Gauge } from 'lucide-vue-next'
import AppShell from '@/layouts/AppShell.vue'
import SideDrawer from '@/components/SideDrawer.vue'
import ModalDialog from '@/components/ModalDialog.vue'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import StatusPill from '@/components/StatusPill.vue'
import EmptyState from '@/components/EmptyState.vue'
import CompactQuotaCell from '@/components/CompactQuotaCell.vue'
import { api, ApiError } from '@/lib/api'
import { formatDateTime } from '@/lib/format'
import { toast } from '@/state/toast'
import type { AdminUser, QuotaResetJob, QuotaResetJobItem, UpstreamKey, UserStatus } from '@/types'

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
const resettingUserID = ref<string>('')
const limitsModalOpen = ref(false)
const limitsForm = reactive({ userId: '' as string | number, displayName: '', limit5h: 0, limit7d: 0 })
const savingLimits = ref(false)
const limitsError = ref('')
const confirmDialog = reactive({ open: false, title: '', description: '', points: [] as string[], confirmLabel: '确认', tone: 'default' as 'default' | 'danger' })
let confirmAction: (() => Promise<void>) | null = null
const batchJob = ref<QuotaResetJob | null>(null)
const jobDetailsOpen = ref(false)
let jobPoll: ReturnType<typeof setTimeout> | undefined
let jobPollGeneration = 0
let pollFailureNotified = false
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
const batchActive = computed(() => ['queued', 'running'].includes(batchJob.value?.status || ''))
const batchCompleted = computed(() => (batchJob.value?.succeeded_count || 0) + (batchJob.value?.failed_count || 0) + (batchJob.value?.unknown_count || 0) + (batchJob.value?.skipped_count || 0))
const failedItems = computed(() => batchJob.value?.items?.filter(item => ['failed', 'unknown'].includes(item.status)) || [])
const detailItems = computed(() => batchJob.value?.items?.filter(item => ['failed', 'unknown', 'skipped'].includes(item.status)) || [])

function userDisplayStatus(user: AdminUser) {
  if (user.status !== 'active') return user.status
  return user.binding?.binding_state || user.binding?.status || user.status
}

function quotaResetItemMessage(item: QuotaResetJobItem) {
  if (item.status === 'unknown') return '结果未知，系统不会自动重试'
  if (item.status === 'failed') return '重置失败'
  const skippedReasons: Record<string, string> = {
    USER_UNBOUND: '执行前用户已解绑 Key',
    BINDING_CHANGED: '执行前 Key 已换绑',
    BINDING_NOT_RESETTABLE: '当前 Key 不可重置',
    NO_BINDING: '用户未绑定 Key',
  }
  return skippedReasons[item.error_code || ''] || '已跳过'
}

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

function removeUser(user: AdminUser) {
  askConfirm({
    title: '删除该用户',
    description: `即将删除「${user.display_name || user.username}」（@${user.username}）的门户账号。`,
    points: ['该用户将无法再登录门户', '上游 Key 及其额度不会被修改', '记录保留在数据库中，可由管理员恢复'],
    confirmLabel: '删除用户',
    tone: 'danger',
  }, async () => {
    try { await api.deleteUser(user.id); toast.success('用户已删除', '上游 Key 保持不变。'); await load() }
    catch (cause) { toast.error('删除失败', cause instanceof ApiError ? cause.message : undefined) }
  })
}

function askConfirm(options: { title: string; description: string; points?: string[]; confirmLabel: string; tone?: 'default' | 'danger' }, action: () => Promise<void>) {
  confirmDialog.title = options.title
  confirmDialog.description = options.description
  confirmDialog.points = options.points || []
  confirmDialog.confirmLabel = options.confirmLabel
  confirmDialog.tone = options.tone || 'default'
  confirmAction = action
  confirmDialog.open = true
}

function closeConfirm() {
  confirmDialog.open = false
  confirmAction = null
}

async function runConfirm() {
  const action = confirmAction
  closeConfirm()
  if (action) await action()
}

function resetQuota(user: AdminUser) {
  if (batchActive.value || !user.resettable) return
  askConfirm({
    title: '重置该用户额度',
    description: `即将重置「${user.display_name || user.username}」（@${user.username}）绑定的上游 Key 额度。`,
    points: ['5h、7d 用量将清零，恢复完整可用额度', '上游隐藏的 1d 窗口同时被清除', '该操作无法撤销，且会写入审计日志'],
    confirmLabel: '重置额度',
  }, async () => {
    resettingUserID.value = String(user.id)
    try {
      const result = await api.resetUserQuota(user.id)
      toast.success('额度已重置', result.snapshot_updated === false ? '重置已完成，额度快照正在刷新。' : '5h、1d、7d 窗口已重置。')
      await load()
    } catch (cause) { toast.error('额度重置失败', cause instanceof ApiError ? cause.message : undefined) }
    finally { resettingUserID.value = '' }
  })
}

function openLimitsModal(user: AdminUser) {
  if (!user.binding) return
  limitsForm.userId = user.id
  limitsForm.displayName = user.display_name || user.username
  limitsForm.limit5h = user.binding.rate_limit_5h ?? 0
  limitsForm.limit7d = user.binding.rate_limit_7d ?? 0
  limitsError.value = ''
  limitsModalOpen.value = true
}

async function saveLimits() {
  limitsError.value = ''
  if (limitsForm.limit5h <= 0 || limitsForm.limit7d <= 0) {
    limitsError.value = '5h 与 7d 限额必须大于 0'
    return
  }
  if (limitsForm.limit5h > 1_000_000 || limitsForm.limit7d > 1_000_000) {
    limitsError.value = '限额不能超过 $1,000,000'
    return
  }
  savingLimits.value = true
  try {
    const result = await api.setUserLimits(limitsForm.userId, limitsForm.limit5h, limitsForm.limit7d)
    limitsModalOpen.value = false
    const message = result.warning_code === 'SNAPSHOT_REFRESH_FAILED'
      ? '限额已修改，额度快照正在刷新。'
      : '5h 与 7d 限额已更新。'
    toast.success('限额已修改', message)
    await load()
  } catch (cause) {
    limitsError.value = cause instanceof ApiError ? cause.message : '网络请求失败'
  } finally { savingLimits.value = false }
}

function stopJobPolling() {
  jobPollGeneration++
  if (jobPoll) { clearTimeout(jobPoll); jobPoll = undefined }
}

function startJobPolling(id: QuotaResetJob['id']) {
  stopJobPolling()
  const generation = jobPollGeneration
  pollFailureNotified = false

  const poll = async () => {
    if (generation !== jobPollGeneration) return
    try {
      const next = await api.quotaReset(id)
      if (generation !== jobPollGeneration) return
      batchJob.value = next
      pollFailureNotified = false
      if (!batchActive.value) {
        jobPoll = undefined
        await load()
        if (generation !== jobPollGeneration) return
        if (next.failed_count || next.unknown_count) toast.error('批量重置已完成', '部分用户未能确认重置结果，请展开查看明细。')
        else {
          const skipped = next.skipped_count ? `，跳过 ${next.skipped_count} 位用户` : ''
          toast.success('批量重置已完成', `已成功重置 ${next.succeeded_count} 位用户${skipped}。`)
        }
        return
      }
    } catch (cause) {
      if (generation !== jobPollGeneration) return
      if (!pollFailureNotified) {
        pollFailureNotified = true
        toast.error('暂时无法获取批量进度', `${cause instanceof ApiError ? cause.message : '网络请求失败。'} 系统将自动重试。`)
      }
    }
    if (generation === jobPollGeneration) jobPoll = setTimeout(() => void poll(), 2_000)
  }

  void poll()
}

async function loadCurrentJob() {
  try {
    const current = await api.currentQuotaReset()
    batchJob.value = current
    if (batchActive.value) startJobPolling(current.id)
    else {
      try { batchJob.value = await api.quotaReset(current.id) }
      catch (cause) { toast.error('无法读取批量重置明细', cause instanceof ApiError ? cause.message : undefined) }
    }
  } catch (cause) {
    if (!(cause instanceof ApiError) || cause.status !== 404) toast.error('无法读取额度重置状态', cause instanceof ApiError ? cause.message : undefined)
  }
}

function resetAllQuota() {
  if (batchActive.value || resettingUserID.value) return
  const targets = users.value.filter(user => user.resettable).length
  askConfirm({
    title: '重置全部用户额度',
    description: `即将为所有未删除用户批量重置额度，当前可重置 ${targets} 位用户。`,
    points: ['每位用户的 5h、7d 用量将清零，恢复完整可用额度', '每个上游 Key 隐藏的 1d 窗口同时被清除', '执行期间无法进行其他额度重置或限额修改', '该操作无法撤销，且会写入审计日志'],
    confirmLabel: '重置全部额度',
  }, async () => {
    try {
      const job = await api.createQuotaReset()
      batchJob.value = job
      jobDetailsOpen.value = false
      toast.info('批量重置已开始', `将依次处理 ${job.total_count} 位用户。`)
      startJobPolling(job.id)
    } catch (cause) { toast.error('无法开始批量重置', cause instanceof ApiError ? cause.message : undefined) }
  })
}

onMounted(() => { void load(); void loadCurrentJob() })
onBeforeUnmount(stopJobPolling)
</script>

<template>
  <AppShell wide>
    <header class="page-heading">
      <div><span class="eyebrow"><Users :size="13" /> 平台权限</span><h1>用户管理</h1><p>创建门户用户，并为每位用户绑定一个合规 Key。</p></div>
      <div class="heading-actions"><button class="icon-button" type="button" title="刷新" @click="load(true)"><RefreshCw :size="16" :class="{ spinning: refreshing }" /></button><button class="secondary-button quota-reset-all" type="button" :disabled="batchActive || Boolean(resettingUserID)" @click="resetAllQuota"><RotateCcw :size="15" />重置全部额度</button><button class="primary-button" type="button" @click="openCreate"><UserPlus :size="16" />添加用户</button></div>
    </header>

    <div class="metric-row stagger">
      <div><span class="metric-dot cobalt"></span><strong class="numeral">{{ stats.total }}</strong><small>普通用户</small></div>
      <div><span class="metric-dot mint"></span><strong class="numeral">{{ stats.active }}</strong><small>当前启用</small></div>
      <div><span class="metric-dot violet"></span><strong class="numeral">{{ stats.bound }}</strong><small>已绑定 Key</small></div>
      <div class="metric-note"><ShieldCheck :size="15" /><span>每个 Key 仅能绑定一位用户</span></div>
    </div>

    <section class="panel accent-violet">
      <div v-if="batchJob" class="quota-job" :class="{ active: batchActive }">
        <div><span class="job-icon" :class="batchActive ? 'cobalt' : failedItems.length ? 'amber' : 'mint'"><RotateCcw :size="17" :class="{ spinning: batchActive }" /></span><span><strong>{{ batchActive ? '正在批量重置额度' : '最近一次批量重置' }}</strong><small>已完成 {{ batchCompleted }} / {{ batchJob.total_count }} · 成功 {{ batchJob.succeeded_count }} · 跳过 {{ batchJob.skipped_count }}<template v-if="batchJob.failed_count || batchJob.unknown_count"> · 异常 {{ batchJob.failed_count + batchJob.unknown_count }}</template></small></span></div>
        <button v-if="detailItems.length" class="text-button" type="button" :aria-expanded="jobDetailsOpen" @click="jobDetailsOpen = !jobDetailsOpen">{{ jobDetailsOpen ? '收起明细' : '展开明细' }}<ChevronDown :size="15" :class="{ rotated: jobDetailsOpen }" /></button>
      </div>
      <Transition name="reveal">
        <div v-if="jobDetailsOpen && detailItems.length" class="quota-job-failures">
          <div v-for="item in detailItems" :key="item.id"><CircleAlert :size="13" /><span><strong>{{ item.display_name || item.username }}</strong><small>@{{ item.username }} · {{ quotaResetItemMessage(item) }}{{ item.error_code ? `（${item.error_code}）` : '' }}</small></span></div>
        </div>
      </Transition>
      <div class="table-tools">
        <label class="search-field"><Search :size="16" /><input v-model="query" type="search" placeholder="搜索用户或 Key" aria-label="搜索用户" /></label>
		<select v-model="statusFilter" class="select-control" aria-label="状态筛选"><option value="all">全部状态</option><option value="active">正常</option><option value="disabled">已停用</option></select>
      </div>
      <div v-if="loading" class="pool-loading"><span v-for="n in 5" :key="n" class="skeleton skeleton-row"></span></div>
      <EmptyState v-else-if="filtered.length === 0" title="没有匹配的用户" description="调整搜索条件，或创建首位普通用户。"><button class="secondary-button" type="button" @click="openCreate"><UserPlus :size="15" />添加用户</button></EmptyState>
      <div v-else class="responsive-table">
        <table class="data-table">
          <thead><tr><th>用户</th><th>Key</th><th>5h</th><th>7d</th><th>状态</th><th><span class="sr-only">操作</span></th></tr></thead>
          <TransitionGroup tag="tbody" name="row">
            <tr v-for="user in filtered" :key="user.id">
              <td data-label="用户"><div class="identity"><span class="avatar-chip">{{ (user.display_name || user.username).slice(0, 1).toUpperCase() }}</span><div><strong>{{ user.display_name || user.username }}</strong><small class="mono">@{{ user.username }} · {{ formatDateTime(user.created_at) }}</small></div></div></td>
              <td data-label="Key 绑定"><div v-if="user.binding" class="binding-cell"><KeyRound :size="14" /><span><strong>{{ user.binding.key_name || '已绑定 Key' }}</strong><small class="mono">{{ user.binding.masked_key || 'sk-…••••' }}</small></span></div><span v-else class="muted">未绑定</span></td>
              <td data-label="5h"><CompactQuotaCell label="5h" :window="user.window_5h" :stale="user.snapshot?.stale" :binding-status="user.binding?.binding_state || user.binding?.status" /></td>
              <td data-label="7d"><CompactQuotaCell label="7d" :window="user.window_7d" :stale="user.snapshot?.stale" :binding-status="user.binding?.binding_state || user.binding?.status" /></td>
              <td data-label="状态"><div class="user-status"><StatusPill :status="userDisplayStatus(user)" :stale="user.status === 'active' && user.snapshot?.stale" /><small v-if="user.binding?.binding_state && user.binding.binding_state !== 'healthy'">{{ user.binding.binding_state === 'missing' ? '上游 Key 不存在' : user.binding.binding_state === 'invalid_limits' ? 'Key 限额异常' : '' }}</small></div></td>
              <td data-label="操作" class="row-actions"><template v-if="user.role !== 'admin' && user.status !== 'deleted'"><button class="icon-button subtle" type="button" title="修改 5h / 7d 限额" :disabled="batchActive || !user.binding" @click="openLimitsModal(user)"><Gauge :size="16" /></button><button class="icon-button subtle" type="button" title="重置 5h、1d、7d 额度" :disabled="batchActive || !user.resettable || resettingUserID === String(user.id)" @click="resetQuota(user)"><span v-if="resettingUserID === String(user.id)" class="spinner dark"></span><RotateCcw v-else :size="16" /></button><button class="icon-button subtle" type="button" :title="user.status === 'active' ? '停用用户' : '启用用户'" @click="toggleStatus(user)"><UserX v-if="user.status === 'active'" :size="16" /><UserCheck v-else :size="16" /></button><button class="icon-button subtle" type="button" title="编辑用户" @click="openEdit(user)"><MoreHorizontal :size="18" /></button><button class="icon-button subtle danger" type="button" title="软删除用户" @click="removeUser(user)"><Trash2 :size="16" /></button></template></td>
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

    <ModalDialog :open="limitsModalOpen" title="修改限额" :description="`调整 ${limitsForm.displayName} 绑定的上游 Key 的 5h 与 7d 限额。新限额立即生效，已用额度不变。`" @close="limitsModalOpen = false">
      <form id="limits-form" class="drawer-form" @submit.prevent="saveLimits">
        <div v-if="limitsError" class="form-alert" role="alert">{{ limitsError }}</div>
        <label class="field"><span>5 小时限额（美元）</span><input v-model.number="limitsForm.limit5h" type="number" step="0.01" min="0.01" max="1000000" required placeholder="例如 25" /><small>上游原生 rate_limit_5h，需大于 0</small></label>
        <label class="field"><span>7 天限额（美元）</span><input v-model.number="limitsForm.limit7d" type="number" step="0.01" min="0.01" max="1000000" required placeholder="例如 150" /><small>上游原生 rate_limit_7d，需大于 0</small></label>
      </form>
      <template #footer>
        <button class="secondary-button" type="button" :disabled="savingLimits" @click="limitsModalOpen = false">取消</button>
        <button class="primary-button" type="submit" form="limits-form" :disabled="savingLimits"><span v-if="savingLimits" class="spinner"></span><Save v-else :size="16" />{{ savingLimits ? '正在保存' : '保存限额' }}</button>
      </template>
    </ModalDialog>

    <ConfirmDialog
      :open="confirmDialog.open"
      :title="confirmDialog.title"
      :description="confirmDialog.description"
      :points="confirmDialog.points"
      :confirm-label="confirmDialog.confirmLabel"
      :tone="confirmDialog.tone"
      @close="closeConfirm"
      @confirm="runConfirm"
    >
      <template #confirm-icon>
        <Trash2 v-if="confirmDialog.tone === 'danger'" :size="16" />
        <RotateCcw v-else :size="16" />
      </template>
    </ConfirmDialog>
  </AppShell>
</template>
