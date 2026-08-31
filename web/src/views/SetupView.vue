<script setup lang="ts">
import { computed, ref } from 'vue'
import { useRouter } from 'vue-router'
import { ArrowLeft, ArrowRight, Check, Database, ExternalLink, KeyRound, LockKeyhole, Server, ShieldCheck, UserRound } from 'lucide-vue-next'
import BrandMark from '@/components/BrandMark.vue'
import { api, ApiError } from '@/lib/api'
import { sessionStore } from '@/state/session'

const router = useRouter()
const step = ref(1)
const loading = ref(false)
const error = ref('')
const form = ref({
  setup_token: '', admin_username: 'admin', admin_display_name: '平台管理员', admin_password: '', confirm_password: '',
  base_url: '', admin_api_key: '', owner_user_id: '', allow_insecure_http: false, confirm_non_simple: false,
})

const passwordValid = computed(() => form.value.admin_password.length >= 12 && form.value.admin_password === form.value.confirm_password)
const stepOneValid = computed(() => form.value.setup_token.trim() && form.value.admin_username.trim() && passwordValid.value)
const stepTwoValid = computed(() => {
  try {
    const url = new URL(form.value.base_url)
    return Boolean(form.value.admin_api_key.trim() && form.value.owner_user_id && form.value.confirm_non_simple && (url.protocol === 'https:' || (url.protocol === 'http:' && form.value.allow_insecure_http)))
  } catch { return false }
})

function next() {
  error.value = ''
  if (step.value === 1 && !stepOneValid.value) {
    error.value = '请填写完整信息；管理员密码至少 12 位且两次输入需一致。'
    return
  }
  if (step.value === 2 && !stepTwoValid.value) {
    error.value = '请检查连接地址、管理员 Key、所有者 ID，并确认限额模式。'
    return
  }
  step.value++
}

async function complete() {
  loading.value = true
  error.value = ''
  try {
    const payload = {
      token: form.value.setup_token,
      username: form.value.admin_username,
      display_name: form.value.admin_display_name,
      password: form.value.admin_password,
      base_url: form.value.base_url,
      admin_api_key: form.value.admin_api_key,
      owner_user_id: Number(form.value.owner_user_id),
      allow_private_http: form.value.allow_insecure_http,
      non_simple_ack: form.value.confirm_non_simple,
    }
    await api.completeSetup(payload)
    sessionStore.markSetupComplete()
    await router.replace({ path: '/login', query: { setup: 'complete' } })
  } catch (cause) {
    error.value = cause instanceof ApiError ? cause.message : '初始化失败，请检查配置后重试。'
  } finally { loading.value = false }
}
</script>

<template>
  <main class="setup-page">
    <header class="setup-top"><BrandMark /><span>首次初始化</span></header>
    <div class="setup-layout">
      <aside class="setup-steps" aria-label="初始化进度">
        <div v-for="item in [{ n: 1, icon: UserRound, title: '管理员账户', copy: '创建唯一管理员' }, { n: 2, icon: Server, title: '连接 Sub2API', copy: '验证上游与所有者' }, { n: 3, icon: ShieldCheck, title: '确认并启用', copy: '保存加密配置' }]" :key="item.n" :class="{ active: step === item.n, done: step > item.n }">
          <span><Check v-if="step > item.n" :size="17" /><component :is="item.icon" v-else :size="17" /></span>
          <div><strong>{{ item.title }}</strong><small>{{ item.copy }}</small></div>
        </div>
        <div class="setup-privacy"><LockKeyhole :size="18" /><p><strong>本地加密</strong><br />凭据使用 AES-256-GCM 加密保存。</p></div>
      </aside>

      <section class="setup-content">
        <Transition name="step" mode="out-in">
          <form v-if="step === 1" key="admin" class="setup-form" @submit.prevent="next">
            <span class="eyebrow">步骤 1 / 3</span><h1>创建管理员账户</h1><p>Setup Token 在服务启动日志中生成，30 分钟内有效。</p>
            <div v-if="error" class="form-alert" role="alert">{{ error }}</div>
            <label class="field full"><span>Setup Token</span><input v-model="form.setup_token" required autocomplete="one-time-code" placeholder="粘贴启动日志中的 Token" /></label>
            <div class="form-grid">
              <label class="field"><span>用户名</span><input v-model="form.admin_username" required autocomplete="username" /></label>
              <label class="field"><span>显示名称</span><input v-model="form.admin_display_name" required /></label>
              <label class="field"><span>密码</span><input v-model="form.admin_password" type="password" required autocomplete="new-password" placeholder="至少 12 位" /></label>
              <label class="field"><span>确认密码</span><input v-model="form.confirm_password" type="password" required autocomplete="new-password" /></label>
            </div>
            <div class="setup-actions"><button class="primary-button" type="submit">继续<ArrowRight :size="17" /></button></div>
          </form>

          <form v-else-if="step === 2" key="upstream" class="setup-form" @submit.prevent="next">
            <span class="eyebrow">步骤 2 / 3</span><h1>连接 Sub2API</h1><p>将读取版本、用户、Key 和账号状态；不会代理任何模型请求。</p>
            <div v-if="error" class="form-alert" role="alert">{{ error }}</div>
            <label class="field full"><span>Sub2API Base URL</span><input v-model="form.base_url" required type="url" placeholder="https://sub2api.example.com" /></label>
            <label class="field full"><span>Admin API Key</span><input v-model="form.admin_api_key" required type="password" autocomplete="off" placeholder="仅用于管理员接口" /></label>
            <label class="field full"><span>固定 Key 所有者 ID</span><input v-model="form.owner_user_id" required inputmode="numeric" placeholder="Sub2API 用户 ID" /></label>
            <label class="check-row"><input v-model="form.allow_insecure_http" type="checkbox" /><span><strong>允许私网 HTTP</strong><small>仅适用于可信内网，公网部署请保持关闭。</small></span></label>
            <label class="check-row important"><input v-model="form.confirm_non_simple" type="checkbox" /><span><strong>我确认 Sub2API 未使用 simple 模式</strong><small>5h 与 7d 限额必须由 Sub2API 原生执行。</small></span></label>
            <div class="setup-actions"><button class="secondary-button" type="button" @click="step--"><ArrowLeft :size="17" />返回</button><button class="primary-button" type="submit">继续<ArrowRight :size="17" /></button></div>
          </form>

          <div v-else key="review" class="setup-form">
            <span class="eyebrow">步骤 3 / 3</span><h1>确认配置</h1><p>完成后可在后台管理用户、绑定 Key，并发布账号池状态。</p>
            <div v-if="error" class="form-alert" role="alert">{{ error }}</div>
            <dl class="review-list">
              <div><dt><UserRound :size="17" />管理员</dt><dd>{{ form.admin_display_name }} <small>@{{ form.admin_username }}</small></dd></div>
              <div><dt><Server :size="17" />上游地址</dt><dd class="mono">{{ form.base_url }}</dd></div>
              <div><dt><KeyRound :size="17" />Key 所有者</dt><dd class="mono">User #{{ form.owner_user_id }}</dd></div>
              <div><dt><Database :size="17" />本地存储</dt><dd>SQLite + 加密凭据</dd></div>
            </dl>
            <div class="setup-callout"><ShieldCheck :size="20" /><p><strong>敏感数据保护已启用</strong><br />完整分发 Key 与最后使用 IP 不会写入本地数据库。</p></div>
            <div class="setup-actions"><button class="secondary-button" type="button" @click="step--"><ArrowLeft :size="17" />返回</button><button class="primary-button" type="button" :disabled="loading" @click="complete"><span v-if="loading" class="spinner"></span>{{ loading ? '正在验证' : '验证并启用' }}<ExternalLink v-if="!loading" :size="17" /></button></div>
          </div>
        </Transition>
      </section>
    </div>
  </main>
</template>
