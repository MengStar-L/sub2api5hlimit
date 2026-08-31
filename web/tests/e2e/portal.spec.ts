import { expect, test, type Page, type Route } from '@playwright/test'

const now = Date.now()
const isoAfter = (hours: number) => new Date(now + hours * 3_600_000).toISOString()

const user = { id: 2, username: 'alice', display_name: '林晓', role: 'user', status: 'active' }
const admin = { id: 1, username: 'admin', display_name: '平台管理员', role: 'admin', status: 'active' }
const pool = [
  { id: 11, masked_account: 'rao***@gmail.com', provider: 'OpenAI', status: 'normal', published: true, window_5h: { supported: true, utilization: 18, reset_at: isoAfter(4.5) }, window_7d: { supported: true, utilization: 46, reset_at: isoAfter(82) }, snapshot: { stale: false, as_of: new Date(now - 30_000).toISOString(), source_updated_at: new Date(now - 90_000).toISOString() } },
  { id: 12, masked_account: 'tea***@outlook.com', provider: 'Anthropic', status: 'normal', published: true, window_5h: { supported: true, utilization: 72, reset_at: isoAfter(2) }, window_7d: { supported: false }, snapshot: { stale: true, as_of: new Date(now - 70_000).toISOString() } },
]

async function fulfill(route: Route, data: unknown, status = 200) {
  await route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(status >= 400 ? { error: data } : { data }) })
}

async function fakeAPI(page: Page, role: 'user' | 'admin', setupComplete = true) {
  await page.route('**/api/**', async route => {
    const request = route.request()
    const path = new URL(request.url()).pathname
    const method = request.method()
    if (path === '/api/setup/status') return fulfill(route, { complete: setupComplete, expires_at: isoAfter(.5) })
    if (path === '/api/setup/complete') return fulfill(route, { complete: true, version: '0.1.183' })
    if (path === '/api/auth/session') return fulfill(route, { user: role === 'admin' ? admin : user, csrf_token: 'csrf-test-token' })
    if (path === '/api/auth/login') return fulfill(route, { user: role === 'admin' ? admin : user, csrf_token: 'csrf-test-token' })
    if (path === '/api/auth/logout' || path === '/api/auth/password') return fulfill(route, {})
    if (path === '/api/me/dashboard') return fulfill(route, { user: role === 'admin' ? admin : user, key: { id: 41, name: '团队主 Key', masked_key: 'sk-…7f9a', status: 'healthy', window_5h: { limit_usd: 50, used_usd: 12.4, remaining_usd: 37.6, percent: 24.8, reset_at: isoAfter(4.7) }, window_7d: { limit_usd: 240, used_usd: 88, remaining_usd: 152, percent: 36.7, reset_at: isoAfter(93) }, snapshot: { stale: false, as_of: new Date().toISOString(), source_updated_at: new Date(now - 60_000).toISOString() } }, pool, snapshot: { stale: false, as_of: new Date().toISOString() } })
    if (path === '/api/admin/users' && method === 'GET') return fulfill(route, { users: [admin, { ...user, created_at: new Date(now - 86400000).toISOString(), binding: { upstream_key_id: 41, key_name: '团队主 Key', masked_key: 'sk-…7f9a', status: 'healthy' } }] })
    if (path === '/api/admin/users' && method === 'POST') return fulfill(route, { ...user, id: 3 })
    if (path.startsWith('/api/admin/users/')) return fulfill(route, {})
    if (path === '/api/admin/upstream-keys') return fulfill(route, { keys: [{ id: 41, name: '团队主 Key', masked_key: 'sk-…7f9a', rate_limit_5h: 50, rate_limit_7d: 240, bound_user_id: 2, bound_username: 'alice', eligible: true }, { id: 42, name: '新用户 Key', masked_key: 'sk-…3cd1', rate_limit_5h: 30, rate_limit_7d: 150, eligible: true }] })
    if (path === '/api/admin/pool' && method === 'GET') return fulfill(route, { accounts: pool })
    if (path === '/api/admin/pool' && method === 'PUT') return fulfill(route, {})
    if (path === '/api/admin/settings' && method === 'GET') return fulfill(route, { base_url: 'https://sub2api.example.com', owner_user_id: 9, owner_username: 'key-owner', upstream_version: '0.1.183', last_success_at: new Date(now - 20_000).toISOString(), key_last_success_at: new Date(now - 20_000).toISOString(), account_last_success_at: new Date(now - 120_000).toISOString(), usage_last_success_at: new Date(now - 50_000).toISOString() })
    if (path === '/api/admin/settings' || path === '/api/admin/sync') return fulfill(route, {})
    return fulfill(route, { code: 'not_found', message: `Unhandled ${method} ${path}` }, 404)
  })
}

test('user dashboard shows both quota windows and the shared pool', async ({ page }, testInfo) => {
  await fakeAPI(page, 'user')
  await page.goto('/dashboard')
  await expect(page.getByRole('heading', { name: '你好，林晓' })).toBeVisible()
  await expect(page.getByText('sk-…7f9a')).toBeVisible()
  await expect(page.getByText('5 小时额度')).toBeVisible()
  await expect(page.getByText('7 天额度')).toBeVisible()
  await expect(page.getByText('rao***@gmail.com')).toBeVisible()
  await expect(page.getByText('未提供')).toBeVisible()
  const overflow = await page.locator('html').evaluate(element => element.scrollWidth > element.clientWidth)
  expect(overflow).toBe(false)
  await page.screenshot({ path: `output/playwright/dashboard-${testInfo.project.name}.png`, fullPage: true })
})

test('admin can open the atomic user and key form', async ({ page }) => {
  await fakeAPI(page, 'admin')
  await page.goto('/admin/users')
  await expect(page.getByRole('heading', { name: '用户管理' })).toBeVisible()
  await page.getByRole('button', { name: '添加用户' }).click()
  await expect(page.getByRole('dialog', { name: '添加用户' })).toBeVisible()
  await page.getByLabel('用户名').fill('new-user')
  await page.getByLabel('显示名称').fill('新用户')
  await page.getByLabel('密码', { exact: true }).fill('strong-password-2026')
  await page.getByLabel('上游 Key').selectOption('42')
  await expect(page.getByRole('button', { name: '保存用户' })).toBeEnabled()
})

test('admin dashboard requests are redirected to user administration', async ({ page }) => {
  await fakeAPI(page, 'admin')
  await page.goto('/dashboard')
  await expect(page).toHaveURL(/\/admin\/users$/)
  await expect(page.getByRole('heading', { name: '用户管理' })).toBeVisible()
  await expect(page.getByRole('link', { name: '配额概览' })).toHaveCount(0)
})

test('incomplete installations are routed into the setup flow', async ({ page }) => {
  await fakeAPI(page, 'admin', false)
  await page.goto('/dashboard')
  await expect(page).toHaveURL(/\/setup$/)
  await expect(page.getByRole('heading', { name: '创建管理员账户' })).toBeVisible()
  await expect(page.getByText('步骤 1 / 3')).toBeVisible()
})
