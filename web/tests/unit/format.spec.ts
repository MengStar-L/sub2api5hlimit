import { describe, expect, it } from 'vitest'
import { clampPercent, formatCountdown, formatDateTime, formatUSD, keyPercent, poolPercent, statusLabel } from '@/lib/format'

describe('quota formatting', () => {
  it('formats money without hiding small values', () => {
    expect(formatUSD(12.345)).toBe('$12.35')
    expect(formatUSD(125.4)).toBe('$125')
    expect(formatUSD(null)).toBe('—')
  })

  it('clamps all upstream percentages to the visible range', () => {
    expect(clampPercent(-5)).toBe(0)
    expect(clampPercent(42.5)).toBe(42.5)
    expect(clampPercent(128)).toBe(100)
    expect(keyPercent({ limit_usd: 20, used_usd: 5, remaining_usd: 15, percent: Number.NaN })).toBe(25)
  })

  it('uses the upstream provider percentage without ratio conversion', () => {
    expect(poolPercent({ supported: true, utilization: 1 })).toBe(1)
    expect(poolPercent({ supported: true, utilization: 42 })).toBe(42)
    expect(poolPercent({ supported: false, utilization: 0 })).toBeNull()
  })

  it('describes reset times and empty windows in Chinese', () => {
    const now = Date.parse('2026-08-31T00:00:00Z')
    expect(formatCountdown(null, now)).toBe('尚未启动')
    expect(formatCountdown('2026-08-31T05:30:00Z', now)).toBe('5小时 30分')
    expect(formatCountdown('2026-09-02T02:00:00Z', now)).toBe('2天 2小时')
    expect(formatCountdown(1788145200, now)).toBe('3小时 0分')
    expect(formatDateTime(1788134400)).not.toBe('—')
    expect(statusLabel('invalid_limits')).toBe('配置异常')
    expect(statusLabel('upstream_inactive')).toBe('上游已停用')
    expect(statusLabel('degraded')).toBe('状态降级')
    expect(statusLabel('rate_limited')).toBe('已达限额')
  })
})
