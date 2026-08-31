import { spawn, spawnSync, type ChildProcessWithoutNullStreams } from 'node:child_process'
import { randomBytes } from 'node:crypto'
import { mkdtemp, rm } from 'node:fs/promises'
import http, { type IncomingMessage, type ServerResponse } from 'node:http'
import net from 'node:net'
import os from 'node:os'
import path from 'node:path'
import { fileURLToPath } from 'node:url'
import type { FullConfig } from '@playwright/test'

const adminAPIKey = 'production-e2e-admin-key'
const rawDistributionKey = 'sk-production-e2e-distribution-1234'
const repoRoot = fileURLToPath(new URL('../../../', import.meta.url))

function envelope(response: ServerResponse, data: unknown, status = 200) {
  response.writeHead(status, { 'Content-Type': 'application/json; charset=utf-8' })
  response.end(JSON.stringify(status >= 400 ? data : { code: 0, message: 'success', data }))
}

function pageData(request: IncomingMessage, items: unknown[]) {
  const url = new URL(request.url || '/', 'http://127.0.0.1')
  const page = Number(url.searchParams.get('page') || '1')
  const pageSize = Number(url.searchParams.get('page_size') || '100')
  return { items, total: items.length, page, page_size: pageSize, pages: 1 }
}

async function readJSON(request: IncomingMessage): Promise<Record<string, unknown>> {
  const chunks: Buffer[] = []
  for await (const chunk of request) chunks.push(Buffer.from(chunk))
  return JSON.parse(Buffer.concat(chunks).toString('utf8')) as Record<string, unknown>
}

async function startFakeSub2API() {
  const server = http.createServer(async (request, response) => {
    if (request.headers['x-api-key'] !== adminAPIKey) {
      envelope(response, { code: 401, message: 'invalid admin key' }, 401)
      return
    }

    const url = new URL(request.url || '/', 'http://127.0.0.1')
    const now = new Date()
    const reset5h = new Date(now.getTime() + 4 * 60 * 60 * 1000).toISOString()
    const reset7d = new Date(now.getTime() + 5 * 24 * 60 * 60 * 1000).toISOString()
    const updatedAt = new Date(now.getTime() - 45_000).toISOString()

    if (request.method === 'GET' && url.pathname === '/api/v1/admin/system/version') {
      envelope(response, { version: 'v0.1.183' })
      return
    }
    if (request.method === 'GET' && url.pathname === '/api/v1/admin/users') {
      envelope(response, pageData(request, [{
        id: 7, email: 'owner@example.com', username: 'key-owner', role: 'admin', status: 'active',
        created_at: updatedAt, updated_at: updatedAt,
      }]))
      return
    }
    if (request.method === 'GET' && url.pathname === '/api/v1/admin/users/7/api-keys') {
      envelope(response, pageData(request, [{
        id: 301, user_id: 7, key: rawDistributionKey, last_used_ip: '203.0.113.99',
        name: '生产配额 Key', status: 'active', rate_limit_5h: 50, usage_5h: 12.5,
        reset_5h_at: reset5h, rate_limit_7d: 250, usage_7d: 75, reset_7d_at: reset7d,
        updated_at: updatedAt,
      }]))
      return
    }
    if (request.method === 'GET' && url.pathname === '/api/v1/admin/accounts') {
      envelope(response, pageData(request, [{
        id: 101, name: 'Production Pool', platform: 'openai', type: 'oauth', status: 'active',
        schedulable: true, credentials: {
          email: 'pool-real-e2e@example.com', plan_type: 'pro', access_token: 'never-persist-this-token',
        },
        updated_at: updatedAt,
      }]))
      return
    }
    if (request.method === 'POST' && url.pathname === '/api/v1/admin/accounts/usage/batch') {
      const body = await readJSON(request)
      const accountIDs = Array.isArray(body.account_ids) ? body.account_ids.map(Number) : []
      const usage: Record<string, unknown> = {}
      if (accountIDs.includes(101)) {
        usage['101'] = {
          source: 'passive', updated_at: updatedAt,
          five_hour: { utilization: 28, resets_at: reset5h, remaining_seconds: 14_400 },
          seven_day: { utilization: 43, resets_at: reset7d, remaining_seconds: 432_000 },
        }
      }
      envelope(response, { usage, errors: {} })
      return
    }
    envelope(response, { code: 404, message: `unhandled ${request.method} ${url.pathname}` }, 404)
  })

  await new Promise<void>((resolve, reject) => {
    server.once('error', reject)
    server.listen(0, '127.0.0.1', () => resolve())
  })
  const address = server.address()
  if (!address || typeof address === 'string') throw new Error('fake Sub2API did not bind a TCP port')
  return { server, url: `http://127.0.0.1:${address.port}` }
}

async function freePort(): Promise<number> {
  const server = net.createServer()
  await new Promise<void>((resolve, reject) => {
    server.once('error', reject)
    server.listen(0, '127.0.0.1', resolve)
  })
  const address = server.address()
  if (!address || typeof address === 'string') throw new Error('failed to reserve a portal port')
  const port = address.port
  await new Promise<void>((resolve, reject) => server.close(error => error ? reject(error) : resolve()))
  return port
}

async function waitForPortal(url: string, token: () => string, child: ChildProcessWithoutNullStreams, stderr: () => string) {
  const deadline = Date.now() + 20_000
  while (Date.now() < deadline) {
    if (child.exitCode !== null) throw new Error(`production portal exited early (${child.exitCode}): ${stderr()}`)
    try {
      const response = await fetch(`${url}/healthz`)
      if (response.ok && token()) return
    } catch {
      // The listener may not be ready yet.
    }
    await new Promise(resolve => setTimeout(resolve, 100))
  }
  throw new Error(`production portal did not become ready: ${stderr()}`)
}

async function stopChild(child: ChildProcessWithoutNullStreams) {
  if (child.exitCode !== null) return
  const exited = new Promise<void>(resolve => child.once('exit', () => resolve()))
  child.kill('SIGTERM')
  await Promise.race([exited, new Promise(resolve => setTimeout(resolve, 5_000))])
  if (child.exitCode === null) {
    child.kill('SIGKILL')
    await Promise.race([exited, new Promise(resolve => setTimeout(resolve, 2_000))])
  }
}

export default async function productionSetup(_config: FullConfig) {
  const tempDirectory = await mkdtemp(path.join(os.tmpdir(), 'sub2api-limit-portal-e2e-'))
  const executable = path.join(tempDirectory, process.platform === 'win32' ? 'sub2api-limit-portal.exe' : 'sub2api-limit-portal')
  const build = spawnSync('go', ['build', '-trimpath', '-o', executable, './cmd/sub2api-limit-portal'], {
    cwd: repoRoot, encoding: 'utf8', windowsHide: true,
  })
  if (build.status !== 0) {
    await rm(tempDirectory, { recursive: true, force: true })
    throw new Error(`failed to build production Go binary: ${build.stderr || build.stdout}`)
  }

  const fake = await startFakeSub2API()
  const port = await freePort()
  const portalURL = `http://127.0.0.1:${port}`
  const child = spawn(executable, ['serve'], {
    cwd: tempDirectory,
    env: {
      ...process.env,
      SUB2API_LIMIT_LISTEN: `127.0.0.1:${port}`,
      SUB2API_LIMIT_DB_PATH: path.join(tempDirectory, 'portal.db'),
      SUB2API_LIMIT_MASTER_KEY: randomBytes(32).toString('base64'),
      SUB2API_LIMIT_COOKIE_SECURE: 'false',
    },
    stdio: ['pipe', 'pipe', 'pipe'],
    windowsHide: true,
  })

  let setupToken = ''
  let stderr = ''
  child.stderr.on('data', chunk => {
    stderr = (stderr + chunk.toString()).slice(-20_000)
    const match = stderr.match(/"setup_token":"([^"]+)"/)
    if (match) setupToken = match[1]
  })

  try {
    await waitForPortal(portalURL, () => setupToken, child, () => stderr)
  } catch (error) {
    await stopChild(child)
    await new Promise<void>(resolve => fake.server.close(() => resolve()))
    await rm(tempDirectory, { recursive: true, force: true })
    throw error
  }

  process.env.PRODUCTION_PORTAL_URL = portalURL
  process.env.PRODUCTION_SETUP_TOKEN = setupToken
  process.env.PRODUCTION_UPSTREAM_URL = fake.url
  process.env.PRODUCTION_UPSTREAM_ADMIN_KEY = adminAPIKey

  return async () => {
    await stopChild(child)
    await new Promise<void>(resolve => fake.server.close(() => resolve()))
    await rm(tempDirectory, { recursive: true, force: true })
  }
}
