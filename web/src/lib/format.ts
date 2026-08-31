import type { KeyWindowView, PoolWindowView } from '@/types'

const dateTime = new Intl.DateTimeFormat('zh-CN', {
  month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', hour12: false,
})

export function formatUSD(value?: number | null): string {
  if (value === null || value === undefined || !Number.isFinite(value)) return '—'
  return `$${value.toFixed(value >= 100 ? 0 : 2)}`
}

export function clampPercent(value?: number | null): number {
  if (value === null || value === undefined || !Number.isFinite(value)) return 0
  return Math.min(100, Math.max(0, value))
}

export function keyPercent(window?: KeyWindowView | null): number {
  if (!window) return 0
  if (Number.isFinite(window.percent)) return clampPercent(window.percent)
  return window.limit_usd > 0 ? clampPercent((window.used_usd / window.limit_usd) * 100) : 0
}

export function poolPercent(window?: PoolWindowView | null): number | null {
  if (!window?.supported || window.utilization === null || window.utilization === undefined) return null
  return clampPercent(window.utilization)
}

function parseTime(value: number | string): Date {
  const normalized = typeof value === 'number' && value < 10_000_000_000 ? value * 1000 : value
  return new Date(normalized)
}

export function formatDateTime(value?: number | string | null): string {
  if (!value) return '—'
  const date = parseTime(value)
  return Number.isNaN(date.getTime()) ? '—' : dateTime.format(date)
}

export function formatCountdown(value?: number | string | null, now = Date.now()): string {
  if (!value) return '尚未启动'
  const target = parseTime(value).getTime()
  if (!Number.isFinite(target)) return '时间未知'
  const delta = Math.max(0, target - now)
  if (delta === 0) return '即将重置'
  const minutes = Math.ceil(delta / 60_000)
  const days = Math.floor(minutes / 1_440)
  const hours = Math.floor((minutes % 1_440) / 60)
  const mins = minutes % 60
  if (days > 0) return `${days}天 ${hours}小时`
  if (hours > 0) return `${hours}小时 ${mins}分`
  return `${mins}分钟`
}

export function statusLabel(status?: string): string {
  const labels: Record<string, string> = {
    active: '正常', normal: '正常', available: '可用', disabled: '已停用', deleted: '已删除', healthy: '正常',
    stale: '数据陈旧', missing: '需要换绑', invalid_limits: '配置异常',
    upstream_inactive: '上游已停用', degraded: '状态降级', rate_limited: '已达限额',
    unbound: '未绑定', error: '异常', unavailable: '不可用', warning: '注意',
  }
  return labels[status || ''] || status || '未知'
}

export function isStale(snapshot?: { stale?: boolean } | null): boolean {
  return Boolean(snapshot?.stale)
}
