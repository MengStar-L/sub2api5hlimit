import type {
  AdminUser,
  Announcement,
  AnnouncementFeed,
  CodexForecastView,
  ConnectionSettings,
  DashboardData,
  PoolAccount,
  QuotaResetJob,
  SessionData,
  SetupStatus,
  UpdateApplyResult,
  UpdateView,
  UpstreamKey,
} from '@/types'

interface SuccessEnvelope<T> { data: T }
interface ErrorEnvelope { error: { code?: string; message?: string } }

export class ApiError extends Error {
  readonly status: number
  readonly code: string

  constructor(message: string, status = 0, code = 'request_failed') {
    super(message)
    this.name = 'ApiError'
    this.status = status
    this.code = code
  }
}

let csrfToken = ''

export function setCSRFToken(token?: string | null) {
  csrfToken = token ?? ''
}

async function request<T>(path: string, init: RequestInit = {}): Promise<T> {
  const headers = new Headers(init.headers)
  headers.set('Accept', 'application/json')
  if (init.body && !headers.has('Content-Type')) headers.set('Content-Type', 'application/json')
  if (csrfToken && init.method && init.method !== 'GET') headers.set('X-CSRF-Token', csrfToken)

  let response: Response
  try {
    response = await fetch(path, { ...init, headers, credentials: 'same-origin' })
  } catch {
    throw new ApiError('无法连接到配额中心，请稍后重试。', 0, 'network_error')
  }

  const body = await response.json().catch(() => null) as SuccessEnvelope<T> | ErrorEnvelope | null
  if (!response.ok) {
    const error = body && 'error' in body ? body.error : undefined
    throw new ApiError(error?.message || `请求失败（${response.status}）`, response.status, error?.code)
  }
  if (!body || !('data' in body)) throw new ApiError('服务返回了无法识别的数据。', response.status, 'invalid_response')
  return body.data
}

const json = (value: unknown) => JSON.stringify(value)

export const api = {
  setupStatus: () => request<SetupStatus>('/api/setup/status'),
  completeSetup: (payload: Record<string, unknown>) => request<{ complete: boolean; version?: string }>('/api/setup/complete', { method: 'POST', body: json({ ...payload, owner_user_id: Number(payload.owner_user_id) }) }),
  session: () => request<SessionData>('/api/auth/session'),
  login: (username: string, password: string) => request<SessionData>('/api/auth/login', { method: 'POST', body: json({ username, password }) }),
  logout: () => request<void>('/api/auth/logout', { method: 'POST' }),
  changePassword: (current_password: string, new_password: string) => request<void>('/api/auth/password', { method: 'PUT', body: json({ current_password, new_password }) }),
  dashboard: () => request<DashboardData>('/api/me/dashboard'),
  myAnnouncements: () => request<AnnouncementFeed>('/api/me/announcements'),
  dismissAnnouncement: (id: Announcement['id']) => request<{ dismissed: boolean }>(`/api/me/announcements/${encodeURIComponent(id)}/dismiss`, { method: 'POST', body: json({}) }),
  codexForecast: () => request<CodexForecastView>('/api/codex-forecast'),
  announcements: async () => normalizeList<Announcement>(await request<Announcement[] | { announcements: Announcement[] }>('/api/admin/announcements'), 'announcements'),
  createAnnouncement: (title: string, body: string) => request<Announcement>('/api/admin/announcements', { method: 'POST', body: json({ title, body }) }),
  updateAnnouncement: (id: Announcement['id'], title: string, body: string) => request<Announcement>(`/api/admin/announcements/${encodeURIComponent(id)}`, { method: 'PUT', body: json({ title, body }) }),
  deleteAnnouncement: (id: Announcement['id']) => request<{ deleted: boolean }>(`/api/admin/announcements/${encodeURIComponent(id)}`, { method: 'DELETE' }),
  users: async () => normalizeList<AdminUser>(await request<AdminUser[] | { users: AdminUser[] }>('/api/admin/users'), 'users'),
  createUser: (payload: Record<string, unknown>) => request<AdminUser>('/api/admin/users', { method: 'POST', body: json({ ...payload, upstream_key_id: payload.upstream_key_id ? Number(payload.upstream_key_id) : null }) }),
  updateUser: (id: AdminUser['id'], payload: Record<string, unknown>) => request<AdminUser>(`/api/admin/users/${encodeURIComponent(id)}`, { method: 'PUT', body: json(payload) }),
  deleteUser: (id: AdminUser['id']) => request<void>(`/api/admin/users/${encodeURIComponent(id)}`, { method: 'DELETE' }),
  resetUserPassword: (id: AdminUser['id'], password: string) => request<void>(`/api/admin/users/${encodeURIComponent(id)}/password`, { method: 'PUT', body: json({ password }) }),
  resetUserQuota: (id: AdminUser['id']) => request<{ snapshot_updated?: boolean }>(`/api/admin/users/${encodeURIComponent(id)}/quota-reset`, { method: 'POST', body: json({}) }),
  createQuotaReset: () => request<QuotaResetJob>('/api/admin/quota-resets', { method: 'POST', body: json({ scope: 'all_non_deleted' }) }),
  currentQuotaReset: () => request<QuotaResetJob>('/api/admin/quota-resets/current'),
  quotaReset: (id: QuotaResetJob['id']) => request<QuotaResetJob>(`/api/admin/quota-resets/${encodeURIComponent(id)}`),
  bindUserKey: (id: AdminUser['id'], upstream_key_id: UpstreamKey['id']) => request<void>(`/api/admin/users/${encodeURIComponent(id)}/binding`, { method: 'PUT', body: json({ upstream_key_id: Number(upstream_key_id) }) }),
  unbindUserKey: (id: AdminUser['id']) => request<void>(`/api/admin/users/${encodeURIComponent(id)}/binding`, { method: 'DELETE' }),
  upstreamKeys: async () => normalizeList<UpstreamKey>(await request<UpstreamKey[] | { keys: UpstreamKey[] }>('/api/admin/upstream-keys'), 'keys'),
  pool: async () => normalizeList<PoolAccount>(await request<PoolAccount[] | { accounts: PoolAccount[] }>('/api/admin/pool'), 'accounts'),
  publishPool: (account_ids: Array<PoolAccount['id']>, published: boolean) => request<void>('/api/admin/pool', { method: 'PUT', body: json({ account_ids: account_ids.map(Number), published }) }),
  settings: () => request<ConnectionSettings>('/api/admin/settings'),
  updateSettings: (payload: Record<string, unknown>) => request<ConnectionSettings>('/api/admin/settings', { method: 'PUT', body: json({ ...payload, owner_user_id: Number(payload.owner_user_id) }) }),
  sync: (scope: 'all' | 'keys' | 'accounts' | 'usage') => request<{ started?: boolean; message?: string }>('/api/admin/sync', { method: 'POST', body: json({ scope }) }),
  update: () => request<UpdateView>('/api/admin/update'),
  checkUpdate: () => request<UpdateView>('/api/admin/update/check', { method: 'POST', body: json({}) }),
  applyUpdate: (targetVersion: string) => request<UpdateApplyResult>('/api/admin/update/apply', { method: 'POST', body: json({ target_version: targetVersion }) }),
}

function normalizeList<T>(value: T[] | Record<string, T[]>, key: string): T[] {
  if (Array.isArray(value)) return value
  return Array.isArray(value[key]) ? value[key] : []
}
