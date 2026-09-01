<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { Check, CircleAlert, Download, ExternalLink, LoaderCircle, RefreshCw, RotateCcw, ServerCog, ShieldCheck } from 'lucide-vue-next'
import AppShell from '@/layouts/AppShell.vue'
import { api, ApiError } from '@/lib/api'
import { formatDateTime } from '@/lib/format'
import { toast } from '@/state/toast'
import type { UpdateOperation, UpdateView } from '@/types'

const update = ref<UpdateView | null>(null)
const loading = ref(true)
const checking = ref(false)
const applying = ref(false)
const reconnecting = ref(false)
let reconnectTimer: ReturnType<typeof setInterval> | undefined

const operation = computed<UpdateOperation | null>(() => update.value?.operation || null)
const active = computed(() => ['queued', 'running'].includes(operation.value?.state || '') || applying.value)
const recoveryRequired = computed(() => operation.value?.phase === 'rollback_failed')
const controlsBlocked = computed(() => active.value || recoveryRequired.value)
const operationMismatch = computed(() => operation.value?.state === 'succeeded' && update.value?.current.version !== operation.value.target_version)
const phaseIndex = computed(() => {
	const phase = operation.value?.phase || ''
	if (operation.value?.rolled_back || phase === 'rolled_back') return 4
	if (['downloading', 'download', 'downloaded'].includes(phase)) return 1
	if (['verifying', 'verify', 'verified', 'backing_up'].includes(phase)) return 2
	if (['installing', 'restarting', 'health_check', 'replace', 'restart', 'healthcheck', 'starting'].includes(phase)) return 3
	return operation.value?.state === 'succeeded' || phase === 'completed' ? 4 : 0
})
const phaseLabel = computed(() => {
	const labels: Record<string, string> = {
		queued: '已排队', checking: '确认最新稳定版', downloading: '下载更新包', download: '下载更新包',
		verifying: '校验更新包', verify: '校验更新包', backing_up: '备份现有程序', installing: '替换程序',
		restarting: '重启服务', restart: '重启服务', health_check: '健康检查', healthcheck: '健康检查',
		completed: '更新完成', complete: '更新完成', rolled_back: '已恢复旧版本', rollback_failed: '回滚失败',
		manual_required: '需要手动升级', up_to_date: '已是最新版本',
	}
  return labels[operation.value?.phase || ''] || (active.value ? '正在准备更新' : '')
})
const operationResult = computed(() => {
  if (!operation.value) return ''
  if (operation.value.rolled_back) return '更新未通过健康检查，已自动恢复旧版本。'
  if (operationMismatch.value) return `更新状态已完成，但当前运行版本仍是 ${update.value?.current.version || '未知'}，请检查服务日志。`
  if (operation.value.state === 'succeeded') return `已安装 ${operation.value.target_version}。`
  if ((!active.value || recoveryRequired.value) && operation.value.error_code) return `更新失败：${operation.value.error_code}`
  return ''
})

function stopReconnect() { if (reconnectTimer) { clearInterval(reconnectTimer); reconnectTimer = undefined } }

async function load(quiet = false) {
  try {
	update.value = await api.update()
    reconnecting.value = false
	if (controlsBlocked.value) {
		startOperationPolling()
	} else stopReconnect()
  } catch (cause) {
    if (controlsBlocked.value || applying.value || reconnecting.value) {
      reconnecting.value = true
      if (!reconnectTimer) reconnectTimer = setInterval(() => void load(true), 2_000)
      return
    }
    if (!quiet) toast.error('无法读取更新状态', cause instanceof ApiError ? cause.message : undefined)
  } finally { loading.value = false }
}

function startOperationPolling() {
  if (!reconnectTimer) reconnectTimer = setInterval(() => void load(true), 2_000)
}

async function check() {
  checking.value = true
  try {
    update.value = await api.checkUpdate()
	if (update.value.status === 'check_failed') toast.error('检查失败', '无法连接 GitHub，已保留最近一次成功结果。')
	else toast.success('已检查最新版本', update.value.update_available ? `发现 ${update.value.latest?.version}。` : '当前已是最新稳定版。')
  } catch (cause) { toast.error('检查失败', cause instanceof ApiError ? cause.message : undefined) }
  finally { checking.value = false }
}

async function apply() {
  const target = update.value?.latest?.version
  if (!target || !update.value?.compatible || !update.value.updater_available) return
  if (!window.confirm(`确认下载并安装 ${target}？服务会短暂重启；若健康检查失败会自动回滚到当前版本。`)) return
  applying.value = true
  try {
    const result = await api.applyUpdate(target)
    update.value = { ...update.value, operation: { operation_id: result.operation_id, target_version: result.target_version, state: result.state, phase: 'queued', rolled_back: false } }
    toast.info('更新请求已提交', '正在下载、校验并重启服务。')
	startOperationPolling()
  } catch (cause) { toast.error('无法开始更新', cause instanceof ApiError ? cause.message : undefined) }
  finally { applying.value = false }
}

onMounted(() => void load())
onBeforeUnmount(stopReconnect)
</script>

<template>
  <AppShell>
    <header class="page-heading">
      <div><span class="eyebrow"><Download :size="14" /> 受控发布</span><h1>程序更新</h1><p>检查 GitHub 稳定版，并在校验成功后安全重启门户。</p></div>
      <div class="heading-actions"><button class="icon-button" type="button" title="刷新状态" :disabled="loading" @click="load()"><RefreshCw :size="17" :class="{ spinning: loading }" /></button><button class="secondary-button" type="button" :disabled="checking || controlsBlocked" @click="check"><LoaderCircle v-if="checking" :size="16" class="spinning" /><RefreshCw v-else :size="16" />检查更新</button></div>
    </header>

    <section v-if="loading && !update" class="section-block update-loading"><span class="skeleton skeleton-row"></span><span class="skeleton skeleton-row"></span></section>
    <template v-else-if="update">
      <section class="update-overview">
        <article><span>当前版本</span><strong class="mono">{{ update.current.version }}</strong><small><ServerCog :size="13" />{{ update.current.os || '—' }} / {{ update.current.arch || '—' }}</small></article>
        <article><span>最新稳定版</span><strong class="mono">{{ update.latest?.version || '尚未发现' }}</strong><a v-if="update.latest?.release_url" :href="update.latest.release_url" target="_blank" rel="noreferrer">查看 Release <ExternalLink :size="13" /></a><small v-else>请先检查更新</small></article>
        <article :class="{ warning: update.status === 'check_failed' || update.status === 'manual_required' }"><span>检查状态</span><strong>{{ update.status === 'check_failed' ? '检查失败（使用缓存）' : update.status === 'manual_required' ? '需要手动升级' : update.update_available ? '可更新' : '已是最新' }}</strong><small>上次成功：{{ formatDateTime(update.last_success_at || update.checked_at) }}</small></article>
      </section>

      <section class="section-block update-detail">
        <div class="section-heading"><div><span class="section-icon cobalt"><ShieldCheck :size="19" /></span><span><h2>更新兼容性</h2><p>仅安装与当前部署兼容的二进制更新包。</p></span></div><a v-if="update.latest?.release_url" class="text-button" :href="update.latest.release_url" target="_blank" rel="noreferrer">Release <ExternalLink :size="14" /></a></div>
		<div class="update-compatibility"><div><span>更新方式</span><strong>{{ !update.latest ? '尚未获取' : update.latest.mode === 'manual' ? '手动升级' : '二进制更新' }}</strong></div><div><span>更新服务</span><strong>{{ update.updater_available ? '已就绪' : '不可用' }}</strong></div><div><span>最低更新器版本</span><strong class="mono">{{ update.latest?.min_updater_version || '—' }}</strong></div></div>
        <div v-if="update.status === 'check_failed'" class="update-notice warning"><CircleAlert :size="17" /><span>无法连接 GitHub，页面正在展示最近一次成功检查结果。{{ update.last_error_code ? ` 错误：${update.last_error_code}` : '' }}</span></div>
        <div v-else-if="update.status === 'manual_required'" class="update-notice warning"><CircleAlert :size="17" /><span>该版本涉及部署配置变更。请按 Release 或 README 的手动升级说明操作，避免半更新。</span></div>
		<div class="update-actions"><span v-if="update.update_available && update.compatible && update.updater_available">{{ update.latest?.version }} 已准备好下载；服务将短暂离线。</span><span v-else-if="!update.update_available">没有可安装的新稳定版。</span><span v-else>自动更新当前不可用。</span><button v-if="update.update_available" class="primary-button" type="button" :disabled="!update.compatible || !update.updater_available || controlsBlocked" @click="apply"><Download :size="17" />安装 {{ update.latest?.version || '更新' }}</button></div>
      </section>

      <section v-if="operation || reconnecting" class="section-block update-operation">
		<div class="section-heading"><div><span class="section-icon" :class="operation?.rolled_back || operationMismatch || recoveryRequired ? 'amber' : operation?.state === 'succeeded' ? 'mint' : 'cobalt'"><RotateCcw v-if="operation?.rolled_back" :size="19" /><CircleAlert v-else-if="recoveryRequired" :size="19" /><Check v-else-if="operation?.state === 'succeeded'" :size="19" /><LoaderCircle v-else :size="19" :class="{ spinning: active || reconnecting }" /></span><span><h2>{{ reconnecting ? '正在等待服务恢复' : phaseLabel || '最近一次更新' }}</h2><p>{{ reconnecting ? '浏览器将自动重新连接，并核对实际运行版本。' : operationResult || `目标版本：${operation?.target_version}` }}</p></span></div></div>
		<div class="update-phases"><div :class="{ done: phaseIndex > 1, active: phaseIndex === 1 }"><i></i><span>下载</span><small>下载更新包</small></div><div :class="{ done: phaseIndex > 2, active: phaseIndex === 2 }"><i></i><span>校验</span><small>校验 digest</small></div><div :class="{ done: phaseIndex > 3, active: phaseIndex === 3 }"><i></i><span>重启</span><small>健康检查</small></div><div :class="{ done: phaseIndex >= 4, active: phaseIndex === 4 }"><i></i><span>完成</span><small>{{ operation?.rolled_back ? '已回滚' : '核对版本' }}</small></div></div>
		<div v-if="operation?.rolled_back || operationMismatch || recoveryRequired || (operation && !active && operation.error_code)" class="update-notice danger"><CircleAlert :size="17" /><span>{{ operationResult || '更新没有完成。' }} 可运行 <code>journalctl -u sub2api-limit-portal-update.service -n 100</code> 查看系统日志。</span></div>
      </section>
    </template>
  </AppShell>
</template>
