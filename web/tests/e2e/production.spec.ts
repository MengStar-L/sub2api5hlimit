import { expect, test } from '@playwright/test'

const adminUsername = 'portal-admin'
const adminPassword = 'admin-password-2026'
const userUsername = 'production-user'
const userPassword = 'user-password-2026'

function runtime(name: string): string {
  const value = process.env[name]
  if (!value) throw new Error(`${name} was not provided by production global setup`)
  return value
}

async function expectNoHorizontalOverflow(page: import('@playwright/test').Page) {
  const overflow = await page.locator('html').evaluate(element => element.scrollWidth > element.clientWidth)
  expect(overflow).toBe(false)
}

test.describe.configure({ mode: 'serial' })

test('real Go binary supports setup, administration, binding, publication, and user dashboard', async ({ page }, testInfo) => {
  const portalURL = runtime('PRODUCTION_PORTAL_URL')
  await page.goto(portalURL)
  await expect(page).toHaveURL(/\/setup$/)

  await page.getByLabel('Setup Token').fill(runtime('PRODUCTION_SETUP_TOKEN'))
  await page.getByLabel('用户名').fill(adminUsername)
  await page.getByLabel('显示名称').fill('生产管理员')
  await page.getByLabel('密码', { exact: true }).fill(adminPassword)
  await page.getByLabel('确认密码').fill(adminPassword)
  await page.getByRole('button', { name: '继续' }).click()

  await page.getByLabel('Sub2API Base URL').fill(runtime('PRODUCTION_UPSTREAM_URL'))
  await page.getByLabel('Admin API Key').fill(runtime('PRODUCTION_UPSTREAM_ADMIN_KEY'))
  await page.getByLabel('固定 Key 所有者 ID').fill('7')
  await page.getByRole('checkbox').nth(0).check()
  await page.getByRole('checkbox').nth(1).check()
  await page.getByRole('button', { name: '继续' }).click()
  await page.getByRole('button', { name: '验证并启用' }).click()
  await expect(page).toHaveURL(/\/login\?setup=complete$/)

  await page.waitForTimeout(500)
  await page.getByLabel('用户名').fill(adminUsername)
  await page.locator('input[name="password"]').fill(adminPassword)
  await page.getByRole('button', { name: '登录配额中心' }).click()
  await expect(page).toHaveURL(/\/admin\/users$/)

  await page.getByRole('link', { name: '连接设置' }).click()
  const syncResponse = page.waitForResponse(response => response.url().endsWith('/api/admin/sync') && response.status() === 202)
  await page.getByRole('button', { name: '全部同步' }).click()
  await syncResponse
  await page.waitForTimeout(800)
  await page.getByRole('link', { name: '用户管理' }).click()

  await page.getByRole('button', { name: '添加用户' }).first().click()
  await page.getByLabel('用户名').fill(userUsername)
  await page.getByLabel('显示名称').fill('生产集成用户')
  await page.getByLabel('密码', { exact: true }).fill(userPassword)
  await expect(page.getByLabel('上游 Key').locator('option', { hasText: '生产配额 Key' })).toHaveCount(1)
  await page.getByLabel('上游 Key').selectOption('301')
  await page.getByRole('button', { name: '保存用户' }).click()
  await expect(page.getByText('生产集成用户')).toBeVisible()
  await expect(page.locator('tbody').getByText('sk-…1234')).toBeVisible()

  await page.getByRole('link', { name: '账号池' }).click()
  await expect(page.getByText('pool-real-e2e@example.com')).toBeVisible()
  await page.getByTitle('公开账号').click()
  await expect(page.getByText('账号已公开', { exact: true })).toBeVisible()
  await page.waitForTimeout(500)

  await page.getByRole('button', { name: '退出登录' }).click()
  await page.getByLabel('用户名').fill(userUsername)
  await page.locator('input[name="password"]').fill(userPassword)
  await page.getByRole('button', { name: '登录配额中心' }).click()
  await expect(page).toHaveURL(/\/dashboard$/)
  await expect(page.getByText('sk-…1234')).toBeVisible()
  await expect(page.getByText('已用 $12.50 / $50.00')).toBeVisible()
  await expect(page.getByText('已用 $75.00 / $250')).toBeVisible()
  await expect(page.getByText('p***@example.com')).toBeVisible()
  await expect(page.getByLabel('5h 已使用 28%')).toBeVisible()
  await expect(page.getByLabel('7d 已使用 43%')).toBeVisible()
  await expect(page.locator('body')).not.toContainText('pool-real-e2e@example.com')
  await expect(page.locator('body')).not.toContainText('sk-production-e2e-distribution-1234')
  await expectNoHorizontalOverflow(page)
  await page.waitForTimeout(900)
  await page.screenshot({ path: testInfo.outputPath('production-dashboard-desktop.png'), fullPage: true })
})

test('real Go binary user dashboard remains overflow-free at a mobile viewport', async ({ page }, testInfo) => {
  await page.setViewportSize({ width: 390, height: 844 })
  await page.goto(`${runtime('PRODUCTION_PORTAL_URL')}/login`)
  await page.getByLabel('用户名').fill(userUsername)
  await page.locator('input[name="password"]').fill(userPassword)
  await page.getByRole('button', { name: '登录配额中心' }).click()
  await expect(page).toHaveURL(/\/dashboard$/)
  await expect(page.getByText('5 小时额度')).toBeVisible()
  await expect(page.getByText('7 天额度')).toBeVisible()
  await expect(page.getByText('p***@example.com')).toBeVisible()
  await expectNoHorizontalOverflow(page)
  await page.waitForTimeout(900)
  await page.screenshot({ path: testInfo.outputPath('production-dashboard-mobile.png'), fullPage: true })
})
