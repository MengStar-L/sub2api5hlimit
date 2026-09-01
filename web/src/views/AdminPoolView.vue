<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { Search, ServerCog, Eye, EyeOff, RefreshCw, CheckSquare, Clock3 } from 'lucide-vue-next'
import AppShell from '@/layouts/AppShell.vue'
import QuotaBand from '@/components/QuotaBand.vue'
import StatusPill from '@/components/StatusPill.vue'
import EmptyState from '@/components/EmptyState.vue'
import { api, ApiError } from '@/lib/api'
import { formatDateTime } from '@/lib/format'
import { toast } from '@/state/toast'
import type { PoolAccount } from '@/types'

const accounts = ref<PoolAccount[]>([])
const selected = ref(new Set<string>())
const query = ref('')
const visibility = ref<'all' | 'published' | 'private'>('all')
const loading = ref(true)
const refreshing = ref(false)
const saving = ref(false)

const filtered = computed(() => accounts.value.filter(account => {
  const text = query.value.trim().toLowerCase()
  const label = `${account.masked_account || account.account || account.alias || ''} ${account.display_name || ''} ${account.provider || ''}`.toLowerCase()
  return (!text || label.includes(text)) && (visibility.value === 'all' || (visibility.value === 'published' ? account.published : !account.published))
}))
const allSelected = computed(() => filtered.value.length > 0 && filtered.value.every(account => selected.value.has(String(account.id))))
const publishedCount = computed(() => accounts.value.filter(account => account.published).length)

function name(account: PoolAccount) { return account.masked_account || account.account || account.display_name || account.alias || `账号 ${account.id}` }
function toggle(id: PoolAccount['id']) { const next = new Set(selected.value); const key = String(id); next.has(key) ? next.delete(key) : next.add(key); selected.value = next }
function toggleAll() { const next = new Set(selected.value); if (allSelected.value) filtered.value.forEach(a => next.delete(String(a.id))); else filtered.value.forEach(a => next.add(String(a.id))); selected.value = next }

async function load(showToast = false) {
  refreshing.value = true
  try { accounts.value = await api.pool(); if (showToast) toast.success('账号清单已更新') }
  catch (cause) { toast.error('加载失败', cause instanceof ApiError ? cause.message : undefined) }
  finally { loading.value = false; refreshing.value = false }
}

async function publish(published: boolean, ids = [...selected.value]) {
  if (!ids.length) return
  saving.value = true
  try {
    await api.publishPool(ids, published)
    accounts.value.forEach(a => { if (ids.includes(String(a.id))) a.published = published })
    selected.value = new Set()
    toast.success(published ? '账号已公开' : '账号已取消公开', `已更新 ${ids.length} 个账号。`)
  } catch (cause) { toast.error('更新失败', cause instanceof ApiError ? cause.message : undefined) }
  finally { saving.value = false }
}

onMounted(() => void load())
</script>

<template>
  <AppShell>
    <header class="page-heading"><div><span class="eyebrow"><ServerCog :size="13" /> 公开账号池</span><h1>账号发布</h1><p>选择普通用户可见的账号状态；邮箱将在用户端脱敏。</p></div><button class="secondary-button" type="button" @click="load(true)"><RefreshCw :size="15" :class="{ spinning: refreshing }" />同步清单</button></header>

    <div class="metric-row stagger"><div><span class="metric-dot mint"></span><strong class="numeral">{{ accounts.length }}</strong><small>账号总数</small></div><div><span class="metric-dot cobalt"></span><strong class="numeral">{{ publishedCount }}</strong><small>已公开</small></div><div><span class="metric-dot amber"></span><strong class="numeral">{{ selected.size }}</strong><small>当前选择</small></div><div class="metric-note"><Eye :size="15" /><span>用户只看到脱敏账号和用量窗口</span></div></div>

    <section class="panel accent-mint">
      <div class="table-tools">
        <label class="search-field"><Search :size="15" /><input v-model="query" type="search" placeholder="搜索账号或 Provider" aria-label="搜索账号" /></label>
        <select v-model="visibility" class="select-control" aria-label="可见性筛选"><option value="all">全部账号</option><option value="published">已公开</option><option value="private">未公开</option></select>
        <Transition name="selection"><div v-if="selected.size" class="batch-actions"><span>已选 {{ selected.size }} 项</span><button class="small-button" type="button" :disabled="saving" @click="publish(true)"><Eye :size="13" />公开</button><button class="small-button neutral" type="button" :disabled="saving" @click="publish(false)"><EyeOff :size="13" />取消公开</button></div></Transition>
      </div>
      <div v-if="loading" class="pool-loading"><span v-for="n in 5" :key="n" class="skeleton skeleton-row"></span></div>
      <EmptyState v-else-if="filtered.length === 0" title="没有匹配的账号" description="等待上游账号同步，或调整当前筛选。" />
      <div v-else class="responsive-table">
        <table class="data-table">
          <thead><tr><th><button class="checkbox-button" type="button" :aria-label="allSelected ? '取消全选' : '全选'" @click="toggleAll"><CheckSquare :size="16" :class="{ checked: allSelected }" /></button></th><th>名称</th><th>账号</th><th>状态</th><th>5h</th><th>7d</th><th>数据时间</th><th>用户可见</th></tr></thead>
          <TransitionGroup tag="tbody" name="row">
            <tr v-for="account in filtered" :key="account.id" :class="{ selected: selected.has(String(account.id)) }">
              <td data-label="选择"><button class="checkbox-button" type="button" :aria-label="`选择 ${name(account)}`" @click="toggle(account.id)"><CheckSquare :size="16" :class="{ checked: selected.has(String(account.id)) }" /></button></td>
              <td data-label="名称"><span class="pool-name">{{ account.display_name || '—' }}</span></td>
              <td data-label="账号"><div class="identity"><span class="provider-dot" :class="`provider-${(account.provider || 'generic').toLowerCase()}`"></span><div><strong class="mono">{{ name(account) }}</strong><small>{{ account.provider || '未知 Provider' }}</small></div></div></td>
              <td data-label="状态"><StatusPill :status="account.status || 'active'" :stale="account.snapshot?.stale" /></td>
              <td data-label="5h"><QuotaBand label="5h" kind="pool" accent="mint" dense :window="account.window_5h || account.usage_5h" /></td>
              <td data-label="7d"><QuotaBand label="7d" kind="pool" accent="violet" dense :window="account.window_7d || account.usage_7d" /></td>
              <td data-label="数据时间"><span class="timestamp"><Clock3 :size="12" />{{ formatDateTime(account.snapshot?.source_updated_at || account.snapshot?.as_of) }}</span></td>
              <td data-label="用户可见"><button class="publish-toggle" type="button" :aria-pressed="account.published" :title="account.published ? '取消公开' : '公开账号'" @click="publish(!account.published, [String(account.id)])"><span></span><Eye v-if="account.published" :size="13" /><EyeOff v-else :size="13" /></button></td>
            </tr>
          </TransitionGroup>
        </table>
      </div>
    </section>
  </AppShell>
</template>
