<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { RefreshCw, Clock3, ShieldAlert, WifiOff, Sparkles } from 'lucide-vue-next'
import AppShell from '@/layouts/AppShell.vue'
import QuotaBand from '@/components/QuotaBand.vue'
import PoolTable from '@/components/PoolTable.vue'
import StatusPill from '@/components/StatusPill.vue'
import SkeletonBlock from '@/components/SkeletonBlock.vue'
import EmptyState from '@/components/EmptyState.vue'
import { api, ApiError } from '@/lib/api'
import { formatDateTime } from '@/lib/format'
import { toast } from '@/state/toast'
import type { DashboardData, KeyWindowView } from '@/types'

const data = ref<DashboardData | null>(null)
const loading = ref(true)
const refreshing = ref(false)
const error = ref('')
let timer = 0

const key5h = computed(() => data.value?.key?.window_5h || data.value?.key?.usage_5h as KeyWindowView | undefined)
const key7d = computed(() => data.value?.key?.window_7d || data.value?.key?.usage_7d as KeyWindowView | undefined)
const maskedKey = computed(() => data.value?.key?.masked_key || data.value?.key?.key_masked || 'sk-…••••')
const snapshot = computed(() => data.value?.key?.snapshot || data.value?.snapshot)

async function load(manual = false) {
  if (manual) refreshing.value = true
  error.value = ''
  try {
    data.value = await api.dashboard()
    if (manual) toast.success('状态已刷新', '已获取最新可用快照。')
  } catch (cause) {
    error.value = cause instanceof ApiError ? cause.message : '暂时无法获取配额状态。'
  } finally {
    loading.value = false
    refreshing.value = false
  }
}

onMounted(() => {
  void load()
  timer = window.setInterval(() => void load(), 30_000)
})
onBeforeUnmount(() => window.clearInterval(timer))
</script>

<template>
  <AppShell>
    <header class="page-heading">
      <div>
        <span class="eyebrow"><Sparkles :size="13" /> 配额实时视图</span>
        <h1>你好，{{ data?.user.display_name || data?.user.username || '用户' }}</h1>
        <p>你的 Key 用量与平台公开账号状态。</p>
      </div>
      <button class="secondary-button" type="button" :disabled="refreshing" @click="load(true)">
        <RefreshCw :size="15" :class="{ spinning: refreshing }" />刷新状态
      </button>
    </header>

    <div v-if="error" class="notice tone-warning">
      <WifiOff :size="17" />
      <div><strong>实时数据暂不可用</strong><p>{{ error }}<span v-if="data"> 当前仍显示最后成功快照。</span></p></div>
    </div>

    <section class="panel accent-cobalt">
      <div class="panel-head">
        <div><h2>我的 Key</h2><p>5h 与 7d 美元额度由 Sub2API 原生执行</p></div>
        <div class="panel-tools"><StatusPill v-if="data?.key" :status="data.key.status || 'healthy'" :stale="snapshot?.stale" /></div>
      </div>

      <div class="panel-body">
        <div v-if="loading" class="key-loading"><SkeletonBlock width="13rem" height="1.5rem" /><SkeletonBlock height="4.5rem" /></div>
        <EmptyState v-else-if="!data?.key" title="尚未绑定 Key" description="管理员绑定合规 Key 后，这里会显示你的 5h 与 7d 配额。" />
        <template v-else>
          <div class="key-identity">
            <div>
              <small>{{ data.key.name || 'Sub2API Key' }}</small>
              <span class="key-mask">{{ maskedKey }}</span>
            </div>
            <span class="snapshot-time"><Clock3 :size="14" />数据时间<strong>{{ formatDateTime(snapshot?.source_updated_at || snapshot?.as_of) }}</strong></span>
          </div>

          <div v-if="['missing', 'invalid_limits'].includes(data.key.status || '')" class="notice tone-danger" style="margin-bottom: 18px">
            <ShieldAlert :size="17" />
            <span>{{ data.key.status === 'missing' ? '上游 Key 已不存在，请联系管理员换绑。' : 'Key 的 5h/7d 限额配置不完整，请联系管理员。' }}</span>
          </div>

          <div class="quota-grid">
            <QuotaBand label="5 小时额度" kind="key" accent="cobalt" :window="key5h" />
            <QuotaBand label="7 天额度" kind="key" accent="violet" :window="key7d" />
          </div>
        </template>
      </div>
    </section>

    <section class="panel accent-mint">
      <div class="panel-head">
        <div><h2>平台账号池</h2><p>管理员公开的账号状态，共 {{ data?.pool?.length || 0 }} 个</p></div>
        <div class="panel-tools"><span class="passive-note"><i></i>被动快照</span></div>
      </div>
      <div class="panel-body">
        <PoolTable :accounts="data?.pool || []" :loading="loading" />
      </div>
    </section>
  </AppShell>
</template>
