<script setup lang="ts">
import { ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ArrowRight, Eye, EyeOff, KeyRound, ShieldCheck, Waves } from 'lucide-vue-next'
import BrandMark from '@/components/BrandMark.vue'
import { api, ApiError } from '@/lib/api'
import { sessionStore } from '@/state/session'

const route = useRoute()
const router = useRouter()
const username = ref('')
const password = ref('')
const showPassword = ref(false)
const loading = ref(false)
const error = ref(route.query.unavailable ? '配额中心暂时不可用，请稍后重试。' : '')
const setupComplete = route.query.setup === 'complete'

async function submit() {
  error.value = ''
  loading.value = true
  try {
    const session = await api.login(username.value.trim(), password.value)
    sessionStore.acceptSession(session)
    const defaultRoute = session.user.role === 'admin' ? '/admin/users' : '/dashboard'
    const requested = typeof route.query.redirect === 'string' && route.query.redirect.startsWith('/') ? route.query.redirect : defaultRoute
    const redirect = session.user.role === 'admin' && requested === '/dashboard' ? defaultRoute : requested
    await router.replace(redirect)
  } catch (cause) {
    error.value = cause instanceof ApiError ? cause.message : '登录失败，请检查用户名和密码。'
  } finally {
    loading.value = false
  }
}
</script>

<template>
  <main class="auth-page">
    <section class="auth-context">
      <BrandMark />
      <div class="auth-statement">
        <span class="eyebrow"><Waves :size="15" /> 实时状态面板</span>
        <h1>额度、窗口与账号状态，一眼清楚。</h1>
        <p>查看自己的 Key 配额，以及平台公开账号池的可用状态。</p>
      </div>
      <div class="auth-signals" aria-hidden="true">
        <div><span class="signal cobalt"></span><strong>5h</strong><i style="--value: 38%"></i><small>窗口健康</small></div>
        <div><span class="signal mint"></span><strong>7d</strong><i style="--value: 67%"></i><small>余量充足</small></div>
        <div><span class="signal violet"></span><strong>Pool</strong><i style="--value: 83%"></i><small>持续同步</small></div>
      </div>
      <p class="auth-security"><ShieldCheck :size="16" /> 门户仅展示脱敏 Key，不参与模型请求转发</p>
    </section>

    <section class="auth-form-wrap">
      <form class="auth-form" @submit.prevent="submit">
        <span class="form-icon"><KeyRound :size="22" /></span>
        <h2>欢迎回来</h2>
        <p>登录以查看当前配额与重置时间</p>

        <div v-if="setupComplete" class="form-success" role="status">初始化完成，请使用管理员账户登录。</div>
        <div v-if="error" class="form-alert" role="alert">{{ error }}</div>
        <label class="field">
          <span>用户名</span>
          <input v-model="username" name="username" autocomplete="username" required autofocus placeholder="输入用户名" />
        </label>
        <label class="field">
          <span>密码</span>
          <span class="input-with-action">
            <input v-model="password" name="password" :type="showPassword ? 'text' : 'password'" autocomplete="current-password" required placeholder="输入密码" />
            <button type="button" :aria-label="showPassword ? '隐藏密码' : '显示密码'" @click="showPassword = !showPassword">
              <EyeOff v-if="showPassword" :size="18" /><Eye v-else :size="18" />
            </button>
          </span>
        </label>
        <button class="primary-button wide" type="submit" :disabled="loading">
          <span v-if="loading" class="spinner"></span>
          <span>{{ loading ? '正在登录' : '登录配额中心' }}</span>
          <ArrowRight v-if="!loading" :size="18" />
        </button>
      </form>
      <p class="auth-footnote">Sub2API Limit Portal <span>·</span> 私有部署</p>
    </section>
  </main>
</template>
