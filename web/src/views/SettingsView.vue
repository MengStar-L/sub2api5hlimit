<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { Settings2, Save, RefreshCw, KeyRound, Users, Activity, Clock3, ShieldAlert, CheckCircle2 } from 'lucide-vue-next'
import AppShell from '@/layouts/AppShell.vue'
import { api, ApiError } from '@/lib/api'
import { formatDateTime } from '@/lib/format'
import { toast } from '@/state/toast'
import type { ConnectionSettings } from '@/types'

const form = reactive({ base_url: '', owner_user_id: '' as string | number, admin_api_key: '', allow_insecure_http: false, confirm_non_simple: true })
const current = ref<ConnectionSettings | null>(null)
const loading = ref(true)
const saving = ref(false)
const syncing = ref<string>('')
const error = ref('')

async function load() {
  try {
    current.value = await api.settings()
    form.base_url = current.value.base_url || ''; form.owner_user_id = current.value.owner_user_id || ''; form.allow_insecure_http = Boolean(current.value.allow_insecure_http)
  } catch (cause) { error.value = cause instanceof ApiError ? cause.message : '无法读取连接设置。' }
  finally { loading.value = false }
}

async function save() {
  error.value = ''; saving.value = true
  try {
    current.value = await api.updateSettings({ base_url: form.base_url.trim(), owner_user_id: form.owner_user_id, admin_api_key: form.admin_api_key || undefined, allow_insecure_http: form.allow_insecure_http, confirm_non_simple: form.confirm_non_simple })
    form.admin_api_key = ''
    toast.success('连接设置已保存', '凭据已加密，旧快照按需要清理。')
  } catch (cause) { error.value = cause instanceof ApiError ? cause.message : '保存失败。' }
  finally { saving.value = false }
}

async function sync(scope: 'all' | 'keys' | 'accounts' | 'usage') {
  syncing.value = scope
  try { await api.sync(scope); toast.success('同步任务已启动', scope === 'all' ? 'Key、账号和用量将依次刷新。' : '可稍后刷新页面查看结果。') }
  catch (cause) { toast.error('无法启动同步', cause instanceof ApiError ? cause.message : undefined) }
  finally { syncing.value = '' }
}

onMounted(() => void load())
</script>

<template>
  <AppShell>
    <header class="page-heading"><div><span class="eyebrow"><Settings2 :size="13" /> 系统配置</span><h1>连接设置</h1><p>管理 Sub2API 管理员连接，并查看各同步任务状态。</p></div><button class="secondary-button" type="button" :disabled="Boolean(syncing)" @click="sync('all')"><RefreshCw :size="15" :class="{ spinning: syncing === 'all' }" />全部同步</button></header>
    <div v-if="error" class="notice tone-danger"><ShieldAlert :size="17" /><div><strong>配置未保存</strong><p>{{ error }}</p></div></div>

    <div class="settings-layout">
      <section class="panel accent-cobalt">
        <div class="panel-head"><div><h2>Sub2API 连接</h2><p>用于管理员接口读取，不会转发模型请求</p></div><div class="panel-tools"><span v-if="current?.upstream_version" class="version-badge">v{{ current.upstream_version }}</span></div></div>
        <div class="panel-body">
          <div v-if="loading" class="pool-loading"><span v-for="n in 4" :key="n" class="skeleton skeleton-row"></span></div>
          <form v-else class="settings-form" @submit.prevent="save">
            <label class="field full"><span>Base URL</span><input v-model="form.base_url" type="url" required placeholder="https://sub2api.example.com" /></label>
            <label class="field full"><span>固定 Key 所有者 ID</span><input v-model="form.owner_user_id" required inputmode="numeric" /><small v-if="current?.owner_username">当前所有者：{{ current.owner_username }}</small></label>
            <label class="field full"><span>轮换 Admin API Key</span><input v-model="form.admin_api_key" type="password" autocomplete="off" placeholder="留空则保持当前凭据" /></label>
            <label class="check-row"><input v-model="form.allow_insecure_http" type="checkbox" /><span><strong>允许私网 HTTP</strong><small>公网地址必须使用 HTTPS。</small></span></label>
            <label class="check-row important"><input v-model="form.confirm_non_simple" type="checkbox" /><span><strong>确认上游未使用 simple 模式</strong><small>变更连接时必须重新确认。</small></span></label>
            <div class="notice tone-warning"><ShieldAlert :size="16" /><p>更换 Base URL 或所有者前，必须先解绑全部用户并取消账号发布。</p></div>
            <button class="primary-button" type="submit" :disabled="saving"><span v-if="saving" class="spinner"></span><Save v-else :size="16" />{{ saving ? '正在保存' : '保存设置' }}</button>
          </form>
        </div>
      </section>

      <section class="panel accent-mint">
        <div class="panel-head"><div><h2>同步状态</h2><p>被动快照可能早于门户轮询时间</p></div></div>
        <div class="panel-body">
          <div class="sync-overview"><span class="sync-health"><CheckCircle2 :size="18" /><span><strong>本地服务正常</strong><small>上游离线不影响就绪检查</small></span></span><span class="last-sync"><Clock3 :size="14" />最近成功 {{ formatDateTime(current?.last_success_at) }}</span></div>
          <div class="sync-jobs">
            <div><span class="job-icon cobalt"><KeyRound :size="15" /></span><span><strong>用户 Key</strong><small>每 15 秒 · {{ formatDateTime(current?.key_last_success_at) }}</small></span><button class="icon-button" type="button" title="同步 Key" @click="sync('keys')"><RefreshCw :size="15" :class="{ spinning: syncing === 'keys' }" /></button></div>
            <div><span class="job-icon violet"><Users :size="15" /></span><span><strong>账号清单</strong><small>每 5 分钟 · {{ formatDateTime(current?.account_last_success_at) }}</small></span><button class="icon-button" type="button" title="同步账号" @click="sync('accounts')"><RefreshCw :size="15" :class="{ spinning: syncing === 'accounts' }" /></button></div>
            <div><span class="job-icon mint"><Activity :size="15" /></span><span><strong>公开用量</strong><small>每 60 秒 · {{ formatDateTime(current?.usage_last_success_at) }}</small></span><button class="icon-button" type="button" title="同步用量" @click="sync('usage')"><RefreshCw :size="15" :class="{ spinning: syncing === 'usage' }" /></button></div>
          </div>
        </div>
      </section>
    </div>
  </AppShell>
</template>
