<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { Megaphone, Plus, RefreshCw, Save, Trash2, Pencil, Clock3 } from 'lucide-vue-next'
import AppShell from '@/layouts/AppShell.vue'
import SideDrawer from '@/components/SideDrawer.vue'
import EmptyState from '@/components/EmptyState.vue'
import { api, ApiError } from '@/lib/api'
import { formatDateTime } from '@/lib/format'
import { toast } from '@/state/toast'
import type { Announcement } from '@/types'

const items = ref<Announcement[]>([])
const loading = ref(true)
const refreshing = ref(false)
const drawerOpen = ref(false)
const mode = ref<'create' | 'edit'>('create')
const saving = ref(false)
const formError = ref('')
const form = reactive({ id: '' as string | number, title: '', body: '' })

const titleCount = computed(() => form.title.trim().length)
const bodyCount = computed(() => form.body.trimEnd().length)
const latest = computed(() => items.value[0] || null)

async function load(showToast = false) {
  refreshing.value = true
  try {
    items.value = await api.announcements()
    if (showToast) toast.success('公告已刷新', `当前共 ${items.value.length} 条。`)
  } catch (cause) {
    toast.error('无法读取公告', cause instanceof ApiError ? cause.message : undefined)
  } finally {
    loading.value = false
    refreshing.value = false
  }
}

function openCreate() {
  mode.value = 'create'
  formError.value = ''
  Object.assign(form, { id: '', title: '', body: '' })
  drawerOpen.value = true
}

function openEdit(item: Announcement) {
  mode.value = 'edit'
  formError.value = ''
  Object.assign(form, { id: item.id, title: item.title, body: item.body })
  drawerOpen.value = true
}

async function submit() {
  if (saving.value) return
  formError.value = ''
  saving.value = true
  try {
    if (mode.value === 'create') {
      await api.createAnnouncement(form.title, form.body)
      toast.success('公告已发布', '用户下次登录时会自动看到这条公告。')
    } else {
      await api.updateAnnouncement(form.id, form.title, form.body)
      toast.success('公告已更新', '已保存标题与正文。')
    }
    drawerOpen.value = false
    await load()
  } catch (cause) {
    formError.value = cause instanceof ApiError ? cause.message : '保存失败，请稍后重试。'
  } finally {
    saving.value = false
  }
}

async function remove(item: Announcement) {
  if (!window.confirm(`确认删除公告「${item.title}」？用户将不再看到它。`)) return
  try {
    await api.deleteAnnouncement(item.id)
    toast.success('公告已删除')
    await load()
  } catch (cause) {
    toast.error('删除失败', cause instanceof ApiError ? cause.message : undefined)
  }
}

onMounted(() => void load())
</script>

<template>
  <AppShell>
    <header class="page-heading">
      <div><span class="eyebrow"><Megaphone :size="13" /> 平台通知</span><h1>公告发布</h1><p>发布后，用户首次登录会自动弹出最新一条，并可在侧栏随时回看。</p></div>
      <div class="heading-actions"><button class="icon-button" type="button" title="刷新" @click="load(true)"><RefreshCw :size="16" :class="{ spinning: refreshing }" /></button><button class="primary-button" type="button" @click="openCreate"><Plus :size="16" />发布公告</button></div>
    </header>

    <div class="metric-row stagger">
      <div><span class="metric-dot violet"></span><strong class="numeral">{{ items.length }}</strong><small>公告总数</small></div>
      <div><span class="metric-dot cobalt"></span><strong class="numeral">{{ latest ? formatDateTime(latest.published_at) : '—' }}</strong><small>最近发布</small></div>
      <div class="metric-note"><Clock3 :size="15" /><span>用户可对每条公告单独选择不再弹出</span></div>
    </div>

    <section class="panel accent-violet">
      <div class="panel-head"><div><h2>已发布公告</h2><p>按发布时间从新到旧排列</p></div></div>
      <div class="panel-body">
        <div v-if="loading" class="announce-loading"><span class="skeleton skeleton-row"></span><span class="skeleton skeleton-row"></span></div>
        <EmptyState v-else-if="items.length === 0" title="还没有公告" description="发布第一条公告，用户下次登录时就会看到。"><button class="secondary-button" type="button" @click="openCreate"><Plus :size="15" />发布公告</button></EmptyState>
        <TransitionGroup v-else tag="ol" name="row" class="announce-list admin">
          <li v-for="item in items" :key="item.id">
            <div class="announce-item-head">
              <h3>{{ item.title }}</h3>
              <time :datetime="String(item.published_at)">{{ formatDateTime(item.published_at) }}</time>
            </div>
            <p class="announce-body">{{ item.body }}</p>
            <div class="announce-item-actions">
              <button class="text-button" type="button" @click="openEdit(item)"><Pencil :size="14" />编辑</button>
              <button class="text-button danger-text" type="button" @click="remove(item)"><Trash2 :size="14" />删除</button>
            </div>
          </li>
        </TransitionGroup>
      </div>
    </section>

    <SideDrawer :open="drawerOpen" :title="mode === 'create' ? '发布公告' : '编辑公告'" :description="mode === 'create' ? '标题 1-120 字，正文 1-4000 字' : '修改后不会重新弹给已确认的用户'" @close="drawerOpen = false">
      <form id="announcement-form" class="drawer-form" @submit.prevent="submit">
        <div v-if="formError" class="form-alert" role="alert">{{ formError }}</div>
        <div class="form-section">
          <h3>内容</h3>
          <label class="field full"><span>标题</span><input v-model="form.title" required maxlength="120" placeholder="例如：本周日 02:00 维护窗口" /><small>{{ titleCount }} / 120</small></label>
          <label class="field full"><span>正文</span><textarea v-model="form.body" required rows="9" maxlength="4000" placeholder="说明影响范围、时间与需要用户配合的事项"></textarea><small>{{ bodyCount }} / 4000</small></label>
        </div>
      </form>
      <template #footer><button class="secondary-button" type="button" @click="drawerOpen = false">取消</button><button class="primary-button" type="submit" form="announcement-form" :disabled="saving"><span v-if="saving" class="spinner"></span><Save v-else :size="17" />{{ mode === 'create' ? '发布' : '保存' }}</button></template>
    </SideDrawer>
  </AppShell>
</template>
