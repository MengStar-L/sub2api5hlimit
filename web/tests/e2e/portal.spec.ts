import { expect, test, type Page, type Route } from '@playwright/test'

const now = Date.now()
const isoAfter = (hours: number) => new Date(now + hours * 3_600_000).toISOString()

const user = { id: 2, username: 'alice', display_name: '林晓', role: 'user', status: 'active' }
const admin = { id: 1, username: 'admin', display_name: '平台管理员', role: 'admin', status: 'active' }
const pool = [
  { id: 11, masked_account: 'rao***@gmail.com', provider: 'OpenAI', status: 'normal', published: true, window_5h: { supported: true, utilization: 18, reset_at: isoAfter(4.5) }, window_7d: { supported: true, utilization: 46, reset_at: isoAfter(82) }, snapshot: { stale: false, as_of: new Date(now - 30_000).toISOString(), source_updated_at: new Date(now - 90_000).toISOString() } },
  { id: 12, masked_account: 'tea***@outlook.com', provider: 'Anthropic', status: 'normal', published: true, window_5h: { supported: true, utilization: 72, reset_at: isoAfter(2) }, window_7d: { supported: false }, snapshot: { stale: true, as_of: new Date(now - 70_000).toISOString() } },
]

const announcement = { id: 7, title: '周日维护窗口', body: '02:00-03:00 期间配额同步暂停，额度不受影响。', published_at: new Date(now - 3_600_000).toISOString(), dismissed: false }
const codexForecast = {
  score: 68, horizon_hours: 24, days_since_reset: 6, reset_announced: false,
  forecast_state: 'likely', evidence_tier: 'moderate', model_version: 'v3',
  breakdown: [{ label: '社区报告增多', points: 18 }],
  source_fetched_at: Math.floor((now - 600_000) / 1000),
  checked_at: Math.floor((now - 300_000) / 1000),
  last_success_at: Math.floor((now - 300_000) / 1000),
}

async function fulfill(route: Route, data: unknown, status = 200) {
  await route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(status >= 400 ? { error: data } : { data }) })
}

// 窄屏侧栏收成抽屉，公告入口与预测数值要先展开才可见
async function openSidebar(page: Page) {
  const trigger = page.getByRole('button', { name: '打开导航' })
  if (await trigger.isVisible()) await trigger.click()
}

async function fakeAPI(page: Page, role: 'user' | 'admin', setupComplete = true) {
	let batchCreated = false
	let updateRequested = false
	let updatePolls = 0
	const quotaJob = { id: 9, status: 'completed', total_count: 1, pending_count: 0, running_count: 0, succeeded_count: 1, failed_count: 0, unknown_count: 0, skipped_count: 0, items: [{ id: 91, job_id: 9, user_id: 2, username: 'alice', display_name: '林晓', status: 'succeeded' }] }
	const adminUser = { ...user, created_at: new Date(now - 86400000).toISOString(), resettable: true, binding: { upstream_key_id: 41, key_name: '团队主 Key', masked_key: 'sk-…7f9a', binding_state: 'healthy' }, window_5h: { limit_usd: 50, used_usd: 12.4, remaining_usd: 37.6, percent: 24.8, reset_at: isoAfter(4.7) }, window_7d: { limit_usd: 240, used_usd: 88, remaining_usd: 152, percent: 36.7, reset_at: isoAfter(93) }, snapshot: { stale: false, as_of: new Date().toISOString() } }
	const update = { current: { version: 'v0.2.0', os: 'linux', arch: 'amd64' }, latest: { version: 'v0.2.1', release_url: 'https://github.com/MengStar-L/sub2api5hlimit/releases/tag/v0.2.1', mode: 'binary', min_updater_version: 'v0.2.0' }, status: 'update_available', update_available: true, compatible: true, updater_available: true }
  await page.route('**/api/**', async route => {
    const request = route.request()
    const path = new URL(request.url()).pathname
    const method = request.method()
    if (path === '/api/setup/status') return fulfill(route, { complete: setupComplete, expires_at: isoAfter(.5) })
    if (path === '/api/setup/complete') return fulfill(route, { complete: true, version: '0.1.183' })
    if (path === '/api/auth/session') return fulfill(route, { user: role === 'admin' ? admin : user, csrf_token: 'csrf-test-token' })
    if (path === '/api/auth/login') return fulfill(route, { user: role === 'admin' ? admin : user, csrf_token: 'csrf-test-token' })
    if (path === '/api/auth/logout' || path === '/api/auth/password') return fulfill(route, {})
    // 默认空公告，避免自动弹窗遮住其他用例；需要弹窗的用例自行覆盖这条路由
    if (path === '/api/me/announcements') return fulfill(route, { announcements: [], popup: null, unread_count: 0 })
    if (path.startsWith('/api/me/announcements/')) return fulfill(route, { dismissed: true })
    if (path === '/api/codex-forecast') return fulfill(route, { forecast: codexForecast, source_url: 'https://www.willcodexquotareset.com/', disclaimer: '该数值由第三方站点依据公开信号推算，属于预测而非官方公告，仅供参考。' })
    if (path === '/api/admin/announcements' && method === 'GET') return fulfill(route, { announcements: [announcement] })
    if (path === '/api/admin/announcements' && method === 'POST') return fulfill(route, { ...announcement, id: 8 })
    if (path.startsWith('/api/admin/announcements/')) return fulfill(route, method === 'DELETE' ? { deleted: true } : announcement)
    if (path === '/api/me/dashboard') return fulfill(route, { user: role === 'admin' ? admin : user, key: { id: 41, name: '团队主 Key', masked_key: 'sk-…7f9a', status: 'healthy', window_5h: { limit_usd: 50, used_usd: 12.4, remaining_usd: 37.6, percent: 24.8, reset_at: isoAfter(4.7) }, window_7d: { limit_usd: 240, used_usd: 88, remaining_usd: 152, percent: 36.7, reset_at: isoAfter(93) }, snapshot: { stale: false, as_of: new Date().toISOString(), source_updated_at: new Date(now - 60_000).toISOString() } }, pool, snapshot: { stale: false, as_of: new Date().toISOString() } })
	if (path === '/api/admin/users' && method === 'GET') return fulfill(route, { users: [adminUser] })
    if (path === '/api/admin/users' && method === 'POST') return fulfill(route, { ...user, id: 3 })
	if (path === '/api/admin/users/2/quota-reset' && method === 'POST') return fulfill(route, { user_id: 2, upstream_key_id: 41, status: 'succeeded', snapshot_updated: true })
    if (path.startsWith('/api/admin/users/')) return fulfill(route, {})
	if (path === '/api/admin/quota-resets' && method === 'POST') { batchCreated = true; return fulfill(route, { ...quotaJob, status: 'queued', pending_count: 1, succeeded_count: 0, items: undefined }) }
	if (path === '/api/admin/quota-resets/current') return batchCreated ? fulfill(route, { ...quotaJob, items: undefined }) : fulfill(route, { code: 'NOT_FOUND', message: '暂无批量任务' }, 404)
	if (path === '/api/admin/quota-resets/9') return fulfill(route, quotaJob)
    if (path === '/api/admin/upstream-keys') return fulfill(route, { keys: [{ id: 41, name: '团队主 Key', masked_key: 'sk-…7f9a', rate_limit_5h: 50, rate_limit_7d: 240, bound_user_id: 2, bound_username: 'alice', eligible: true }, { id: 42, name: '新用户 Key', masked_key: 'sk-…3cd1', rate_limit_5h: 30, rate_limit_7d: 150, eligible: true }] })
    if (path === '/api/admin/pool' && method === 'GET') return fulfill(route, { accounts: pool })
    if (path === '/api/admin/pool' && method === 'PUT') return fulfill(route, {})
    if (path === '/api/admin/settings' && method === 'GET') return fulfill(route, { base_url: 'https://sub2api.example.com', owner_user_id: 9, owner_username: 'key-owner', upstream_version: '0.1.183', last_success_at: new Date(now - 20_000).toISOString(), key_last_success_at: new Date(now - 20_000).toISOString(), account_last_success_at: new Date(now - 120_000).toISOString(), usage_last_success_at: new Date(now - 50_000).toISOString() })
    if (path === '/api/admin/settings' || path === '/api/admin/sync') return fulfill(route, {})
	if (path === '/api/admin/update' && method === 'GET') {
		if (updateRequested) {
			updatePolls++
			if (updatePolls === 1) return route.abort('connectionfailed')
			return fulfill(route, {
				...update,
				current: { ...update.current, version: 'v0.2.1' },
				status: 'up_to_date', update_available: false,
				operation: { operation_id: 'operation-e2e', target_version: 'v0.2.1', state: 'succeeded', phase: 'completed', rolled_back: false },
			})
		}
		return fulfill(route, update)
	}
	if (path === '/api/admin/update/check' && method === 'POST') return fulfill(route, update)
	if (path === '/api/admin/update/apply' && method === 'POST') { updateRequested = true; return fulfill(route, { operation_id: 'operation-e2e', target_version: 'v0.2.1', state: 'queued' }) }
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
	await expect(page.getByText('$12.40')).toBeVisible()
	await expect(page.getByText('$88.00')).toBeVisible()
  await page.getByRole('button', { name: '添加用户' }).click()
  await expect(page.getByRole('dialog', { name: '添加用户' })).toBeVisible()
  await page.getByLabel('用户名').fill('new-user')
  await page.getByLabel('显示名称').fill('新用户')
  await page.getByLabel('密码', { exact: true }).fill('strong-password-2026')
  await page.getByLabel('上游 Key').selectOption('42')
  await expect(page.getByRole('button', { name: '保存用户' })).toBeEnabled()
})

test('admin history preserves disabled status and explains skipped resets', async ({ page }) => {
	await fakeAPI(page, 'admin')
	await page.route('**/api/admin/users', async route => {
		if (route.request().method() !== 'GET') return route.fallback()
		return fulfill(route, {
			users: [{
				...user,
				status: 'disabled',
				created_at: new Date(now - 86_400_000).toISOString(),
				resettable: true,
				binding: { upstream_key_id: 41, key_name: '团队主 Key', masked_key: 'sk-…7f9a', binding_state: 'healthy' },
			}],
		})
	})
	await page.route('**/api/admin/quota-resets/current', route => fulfill(route, {
		id: 10,
		status: 'completed',
		total_count: 1,
		pending_count: 0,
		running_count: 0,
		succeeded_count: 0,
		failed_count: 0,
		unknown_count: 0,
		skipped_count: 1,
		items: [{ id: 101, job_id: 10, user_id: 2, username: 'alice', display_name: '林晓', status: 'skipped', error_code: 'BINDING_CHANGED' }],
	}))

	await page.goto('/admin/users')
	await expect(page.locator('.user-status .status-pill')).toHaveText(/已停用/)
	await expect(page.getByText('批量重置已完成', { exact: true })).toHaveCount(0)
	await page.getByRole('button', { name: '展开明细' }).click()
	await expect(page.getByText(/执行前 Key 已换绑（BINDING_CHANGED）/)).toBeVisible()
})

test('admin can reset one user, start a batch, and request the checked update', async ({ page }, testInfo) => {
	await fakeAPI(page, 'admin')
	page.on('dialog', dialog => dialog.accept())
	await page.goto('/admin/users')
	await page.screenshot({ path: `output/playwright/admin-users-${testInfo.project.name}.png`, fullPage: true })

	const singleRequest = page.waitForRequest(request => request.method() === 'POST' && request.url().endsWith('/api/admin/users/2/quota-reset'))
	await page.getByTitle('重置 5h、1d、7d 额度').click()
	const singleConfirm = page.getByRole('dialog', { name: '重置该用户额度' })
	await expect(singleConfirm).toBeVisible()
	await singleConfirm.getByRole('button', { name: '重置额度' }).click()
	await singleRequest
	await expect(page.getByText('额度已重置')).toBeVisible()

	const batchRequest = page.waitForRequest(request => request.method() === 'POST' && request.url().endsWith('/api/admin/quota-resets'))
	await page.getByRole('button', { name: '重置全部额度' }).click()
	const batchConfirm = page.getByRole('dialog', { name: '重置全部用户额度' })
	await expect(batchConfirm).toBeVisible()
	await batchConfirm.getByRole('button', { name: '重置全部额度' }).click()
	const request = await batchRequest
	expect(request.postDataJSON()).toEqual({ scope: 'all_non_deleted' })
	await expect(page.getByText('最近一次批量重置')).toBeVisible()

	await page.goto('/admin/update')
	await expect(page.locator('.update-overview article').filter({ hasText: '当前版本' }).getByText('v0.2.0', { exact: true })).toBeVisible()
	await expect(page.locator('.update-overview article').filter({ hasText: '最新稳定版' }).getByText('v0.2.1', { exact: true })).toBeVisible()
	const applyRequest = page.waitForRequest(request => request.method() === 'POST' && request.url().endsWith('/api/admin/update/apply'))
	await page.getByRole('button', { name: '安装 v0.2.1' }).click()
	expect((await applyRequest).postDataJSON()).toEqual({ target_version: 'v0.2.1' })
	await expect(page.getByText('更新请求已提交')).toBeVisible()
	await expect(page.getByRole('heading', { name: '正在等待服务恢复' })).toBeVisible({ timeout: 5_000 })
	await expect(page.getByText('已安装 v0.2.1。')).toBeVisible({ timeout: 7_000 })
	await expect(page.getByRole('button', { name: '安装 v0.2.1' })).toHaveCount(0)
	const overflow = await page.locator('html').evaluate(element => element.scrollWidth > element.clientWidth)
	expect(overflow).toBe(false)
	await page.screenshot({ path: `output/playwright/admin-update-${testInfo.project.name}.png`, fullPage: true })
})

test('admin dashboard requests are redirected to user administration', async ({ page }) => {
  await fakeAPI(page, 'admin')
  await page.goto('/dashboard')
  await expect(page).toHaveURL(/\/admin\/users$/)
  await expect(page.getByRole('heading', { name: '用户管理' })).toBeVisible()
  await expect(page.getByRole('link', { name: '配额概览' })).toHaveCount(0)
})

test('the newest announcement pops on first login and stops after confirmation', async ({ page }) => {
  await fakeAPI(page, 'user')
  await page.route('**/api/me/announcements', route => {
    if (route.request().method() !== 'GET') return route.fallback()
    return fulfill(route, { announcements: [announcement], popup: announcement, unread_count: 1 })
  })
  await page.goto('/dashboard')

  const popup = page.getByRole('dialog', { name: '周日维护窗口' })
  await expect(popup).toBeVisible()
  await expect(popup.getByText('02:00-03:00 期间配额同步暂停，额度不受影响。')).toBeVisible()
  await expect(popup.getByText(/发布时间 \d{2}\/\d{2} \d{2}:\d{2}/)).toBeVisible()

  const dismissal = page.waitForRequest(request => request.method() === 'POST' && request.url().endsWith('/api/me/announcements/7/dismiss'))
  await popup.getByRole('button', { name: '已了解，不再弹出此公告' }).click()
  await dismissal
  await expect(popup).toBeHidden()

  // 关闭后仍可从侧栏回看，且带发布时间
  await openSidebar(page)
  await page.getByRole('button', { name: /公告/ }).click()
  const drawer = page.getByRole('dialog', { name: '平台公告' })
  await expect(drawer.getByRole('heading', { name: '周日维护窗口' })).toBeVisible()
  await expect(drawer.getByText('已了解', { exact: true })).toBeVisible()
})

test('the codex forecast score is visible without opening it and explains itself when opened', async ({ page }, testInfo) => {
  await fakeAPI(page, 'user')
  await page.goto('/dashboard')
  await openSidebar(page)

  const chip = page.getByRole('button', { name: /Codex 重置预测 68 分/ })
  await expect(chip).toBeVisible()
  await expect(chip).toContainText('68')

  await chip.click()
  const modal = page.getByRole('dialog', { name: 'Codex 配额重置预测' })
  await expect(modal.locator('.codex-gauge strong')).toHaveText('68')
  await expect(modal.getByText('这是预测值，不可全信')).toBeVisible()
  await expect(modal.getByRole('link', { name: /willcodexquotareset\.com/ })).toHaveAttribute('href', 'https://www.willcodexquotareset.com/')
  await expect(modal.getByText('数据获取时间')).toBeVisible()
  await expect(modal.getByText('社区报告增多')).toBeVisible()
  await page.screenshot({ path: `output/playwright/codex-forecast-${testInfo.project.name}.png`, fullPage: true })
})

test('admin can publish an announcement from its own menu entry', async ({ page }, testInfo) => {
  await fakeAPI(page, 'admin')
  await page.goto('/admin/users')
  await openSidebar(page)
  await page.getByLabel('主导航').getByRole('link', { name: '公告发布' }).click()
  await expect(page.getByRole('heading', { name: '公告发布' })).toBeVisible()
  // 底部导航也要保留这个入口（桌面态它被 display:none 隐藏，用 CSS 选择器数 DOM）
  await expect(page.locator('.mobile-nav a[href="/admin/announcements"]')).toHaveCount(1)
  await expect(page.getByRole('heading', { name: '周日维护窗口' })).toBeVisible()

  await page.getByRole('button', { name: '发布公告' }).first().click()
  const drawer = page.getByRole('dialog', { name: '发布公告' })
  await expect(drawer).toBeVisible()
  await drawer.getByLabel('标题').fill('新版本已上线')
  await drawer.getByLabel('正文').fill('本次更新新增公告与预测数值展示。')

  const publish = page.waitForRequest(request => request.method() === 'POST' && request.url().endsWith('/api/admin/announcements'))
  await drawer.getByRole('button', { name: '发布' }).click()
  expect((await publish).postDataJSON()).toEqual({ title: '新版本已上线', body: '本次更新新增公告与预测数值展示。' })
  await expect(page.getByText('公告已发布')).toBeVisible()
  const overflow = await page.locator('html').evaluate(element => element.scrollWidth > element.clientWidth)
  expect(overflow).toBe(false)
  await page.screenshot({ path: `output/playwright/admin-announcements-${testInfo.project.name}.png`, fullPage: true })
})

test('incomplete installations are routed into the setup flow', async ({ page }) => {
  await fakeAPI(page, 'admin', false)
  await page.goto('/dashboard')
  await expect(page).toHaveURL(/\/setup$/)
  await expect(page.getByRole('heading', { name: '创建管理员账户' })).toBeVisible()
  await expect(page.getByText('步骤 1 / 3')).toBeVisible()
})
