export type Role = 'admin' | 'user'
export type UserStatus = 'active' | 'disabled' | 'deleted'

export interface ApiErrorBody {
  code: string
  message: string
}

export interface SessionUser {
  id: number | string
  username: string
  display_name?: string
  role: Role
  status: UserStatus
}

export interface SessionData {
  user: SessionUser
  csrf_token: string
}

export interface SetupStatus {
  complete: boolean
  expires_at?: number | string | null
}

export interface KeyWindowView {
  limit_usd: number
  used_usd: number
  remaining_usd: number
  percent: number
  reset_at?: number | string | null
}

export interface PoolWindowView {
  supported: boolean
  utilization?: number | null
  reset_at?: number | string | null
}

export interface SnapshotMeta {
  as_of?: number | string | null
  source_updated_at?: number | string | null
  last_success_at?: number | string | null
  stale: boolean
}

export type BindingStatus = 'healthy' | 'missing' | 'invalid_limits' | 'unbound' | 'stale' | string

export interface KeySummary {
  id?: number | string
  name?: string
  masked_key?: string
  key_masked?: string
  status?: BindingStatus
  rate_limit_5h?: number
  rate_limit_7d?: number
  window_5h?: KeyWindowView | null
  window_7d?: KeyWindowView | null
  usage_5h?: KeyWindowView | null
  usage_7d?: KeyWindowView | null
  snapshot?: SnapshotMeta
}

export interface PoolAccount {
  id: number | string
  display_name?: string
  alias?: string
  masked_account?: string
  account?: string
  provider?: string
  status?: string
  status_message?: string
  published?: boolean
  window_5h?: PoolWindowView | null
  window_7d?: PoolWindowView | null
  usage_5h?: PoolWindowView | null
  usage_7d?: PoolWindowView | null
  snapshot?: SnapshotMeta
}

export interface DashboardData {
  user: SessionUser
  key?: KeySummary | null
  pool: PoolAccount[]
  snapshot?: SnapshotMeta
}

export interface AdminBinding {
  upstream_key_id?: number | string
  key_id?: number | string
  key_name?: string
  masked_key?: string
  status?: BindingStatus
  binding_state?: BindingStatus
}

export interface AdminUser extends SessionUser {
  created_at?: number | string
  updated_at?: number | string
  binding?: AdminBinding | null
  window_5h?: KeyWindowView | null
  window_7d?: KeyWindowView | null
  snapshot?: SnapshotMeta
  resettable?: boolean
}

export type QuotaResetItemStatus = 'pending' | 'running' | 'succeeded' | 'failed' | 'unknown' | 'skipped'

export interface QuotaResetJobItem {
  id: number | string
  job_id: number | string
  user_id: number | string
  username: string
  display_name?: string
  user_status?: UserStatus | string
  upstream_key_id?: number | string | null
  key_mask?: string
  status: QuotaResetItemStatus
  error_code?: string
  created_at?: number | string
  updated_at?: number | string
}

export interface QuotaResetJob {
  id: number | string
  job_id?: number | string
  status: 'queued' | 'running' | 'completed' | string
  total_count: number
  pending_count: number
  running_count: number
  succeeded_count: number
  failed_count: number
  unknown_count: number
  skipped_count: number
  created_at?: number | string
  started_at?: number | string
  completed_at?: number | string
  items?: QuotaResetJobItem[]
}

export interface UpdateOperation {
  operation_id: string
  target_version: string
  state: 'queued' | 'running' | 'succeeded' | 'failed' | 'rolled_back' | string
  phase: string
  error_code?: string
  started_at?: number | string
  updated_at?: number | string
  finished_at?: number | string
  rolled_back: boolean
}

export interface UpdateView {
  current: { version: string; os?: string; arch?: string }
  latest?: {
    version: string
    release_url: string
    published_at?: number | string
    mode: 'binary' | 'manual' | string
    min_updater_version?: string
  } | null
  status: 'development' | 'check_failed' | 'manual_required' | 'update_available' | 'up_to_date' | string
  checked_at?: number | string
  last_success_at?: number | string
  last_error_code?: string
  update_available: boolean
  compatible: boolean
  updater_available: boolean
  operation?: UpdateOperation | null
}

export interface UpdateApplyResult {
  operation_id: string
  target_version: string
  state: string
}

export interface UpstreamKey {
  id: number | string
  name: string
  masked_key?: string
  rate_limit_5h: number
  rate_limit_7d: number
  bound_user_id?: number | string | null
  bound_username?: string | null
  eligible?: boolean
  status?: string
}

export interface ConnectionSettings {
  base_url: string
  owner_user_id: number | string
  owner_username?: string
  allow_insecure_http?: boolean
  upstream_version?: string
  last_success_at?: number | string | null
  key_last_success_at?: number | string | null
  account_last_success_at?: number | string | null
  usage_last_success_at?: number | string | null
  connection_id?: string
}
