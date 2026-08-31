<script setup lang="ts">
import { computed, ref } from 'vue'
import { Search, SlidersHorizontal, Clock3 } from 'lucide-vue-next'
import QuotaBand from '@/components/QuotaBand.vue'
import StatusPill from '@/components/StatusPill.vue'
import EmptyState from '@/components/EmptyState.vue'
import { formatDateTime, poolPercent } from '@/lib/format'
import type { PoolAccount } from '@/types'

const props = withDefaults(defineProps<{ accounts: PoolAccount[]; loading?: boolean }>(), { loading: false })
const query = ref('')
const filter = ref<'all' | 'healthy' | 'attention'>('all')

function accountName(account: PoolAccount) {
  return account.masked_account || account.account || account.display_name || account.alias || `账号 ${account.id}`
}

function accountNeedsAttention(account: PoolAccount) {
  const highUsage = [account.window_5h || account.usage_5h, account.window_7d || account.usage_7d]
    .some(window => (poolPercent(window) ?? 0) >= 85)
  return account.snapshot?.stale || account.status === 'error' || account.status === 'unavailable' || highUsage
}

const filtered = computed(() => {
  const text = query.value.trim().toLowerCase()
  return props.accounts.filter(account => {
    const matchesText = !text || `${accountName(account)} ${account.provider || ''}`.toLowerCase().includes(text)
    const attention = accountNeedsAttention(account)
    const matchesFilter = filter.value === 'all' || (filter.value === 'attention' ? attention : !attention)
    return matchesText && matchesFilter
  })
})
</script>

<template>
  <div class="table-tools">
    <label class="search-field">
      <Search :size="16" />
      <input v-model="query" type="search" placeholder="搜索账号或 Provider" aria-label="搜索账号池" />
    </label>
    <div class="segmented" aria-label="状态筛选">
      <SlidersHorizontal :size="15" />
      <button v-for="option in ([['all', '全部'], ['healthy', '正常'], ['attention', '需关注']] as const)" :key="option[0]" type="button" :class="{ active: filter === option[0] }" @click="filter = option[0]">
        {{ option[1] }}
      </button>
    </div>
  </div>

  <div v-if="loading" class="pool-loading" aria-label="正在加载账号池">
    <span v-for="n in 4" :key="n" class="skeleton skeleton-row"></span>
  </div>
  <EmptyState v-else-if="filtered.length === 0" title="没有匹配的账号" description="调整筛选条件，或等待管理员发布账号。" />
  <div v-else class="responsive-table">
    <table class="pool-table">
      <thead>
        <tr><th>账号</th><th>状态</th><th>5h 窗口</th><th>7d 窗口</th><th>数据时间</th></tr>
      </thead>
      <TransitionGroup tag="tbody" name="row">
        <tr v-for="account in filtered" :key="account.id">
          <td data-label="账号">
            <div class="account-identity">
              <span class="provider-dot" :class="`provider-${(account.provider || 'generic').toLowerCase()}`"></span>
              <div><strong class="mono">{{ accountName(account) }}</strong><small>{{ account.provider || '未知 Provider' }}</small></div>
            </div>
          </td>
          <td data-label="状态"><StatusPill :status="account.status || 'active'" :stale="account.snapshot?.stale" /></td>
          <td data-label="5h 窗口"><QuotaBand label="5h" kind="pool" accent="mint" :window="account.window_5h || account.usage_5h" /></td>
          <td data-label="7d 窗口"><QuotaBand label="7d" kind="pool" accent="violet" :window="account.window_7d || account.usage_7d" /></td>
          <td data-label="数据时间">
            <span class="timestamp"><Clock3 :size="14" />{{ formatDateTime(account.snapshot?.source_updated_at || account.snapshot?.as_of) }}</span>
          </td>
        </tr>
      </TransitionGroup>
    </table>
  </div>
</template>
