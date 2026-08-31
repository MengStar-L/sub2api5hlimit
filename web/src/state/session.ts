import { computed, reactive } from 'vue'
import { api, ApiError, setCSRFToken } from '@/lib/api'
import type { SessionData, SetupStatus } from '@/types'

interface SessionState {
  initialized: boolean
  initializing: boolean
  setup: SetupStatus | null
  session: SessionData | null
}

const state = reactive<SessionState>({
  initialized: false,
  initializing: false,
  setup: null,
  session: null,
})

let bootstrapPromise: Promise<void> | null = null

async function bootstrap() {
  if (state.initialized) return
  if (bootstrapPromise) return bootstrapPromise
  state.initializing = true
  bootstrapPromise = (async () => {
    try {
      state.setup = await api.setupStatus()
      if (state.setup.complete) {
        try {
          state.session = await api.session()
          setCSRFToken(state.session.csrf_token)
        } catch (error) {
          if (!(error instanceof ApiError) || (error.status !== 401 && error.status !== 403)) throw error
          state.session = null
        }
      }
    } finally {
      state.initialized = true
      state.initializing = false
      bootstrapPromise = null
    }
  })()
  return bootstrapPromise
}

function acceptSession(session: SessionData) {
  state.session = session
  state.setup = { complete: true }
  setCSRFToken(session.csrf_token)
}

function markSetupComplete() {
  state.setup = { complete: true }
  state.session = null
  setCSRFToken('')
}

async function signOut() {
  try { await api.logout() } finally {
    state.session = null
    setCSRFToken('')
  }
}

export const sessionStore = {
  state,
  bootstrap,
  acceptSession,
  markSetupComplete,
  signOut,
  user: computed(() => state.session?.user ?? null),
  isAdmin: computed(() => state.session?.user.role === 'admin'),
  isAuthenticated: computed(() => Boolean(state.session?.user)),
}
