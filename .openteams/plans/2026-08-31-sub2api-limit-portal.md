# Sub2API Limit Portal Implementation Plan

**Goal:** 构建一个独立的 Sub2API 用户与配额门户，让每位用户查看自己的 5h/7d Key 用量和公开账号池状态，并让唯一管理员管理用户、绑定已有 Key 与发布账号。

**Architecture:** Go 单二进制提供标准库 HTTP API、后台同步器与内嵌 Vue 3 SPA；SQLite/WAL 保存门户身份、绑定、公开选择和最后快照。模型请求仍携带真实 Key 直达非 `simple` 模式 Sub2API，门户只调用 `v0.1.183` Admin API 获取状态，不代理请求或建立第二套计费账本。

**Tech Stack:** Go 1.26、`modernc.org/sqlite`、Argon2id、AES-256-GCM、Vue 3、TypeScript、Vite、Vitest、Playwright、systemd、Nginx。

---

## 固定契约

- CLI：无参数或 `serve` 启动；`keygen` 输出 `SUB2API_LIMIT_MASTER_KEY=<base64>`；`version` 与 `--version` 输出构建版本。
- 环境变量：`SUB2API_LIMIT_LISTEN`、`SUB2API_LIMIT_DB_PATH`、`SUB2API_LIMIT_MASTER_KEY`、`SUB2API_LIMIT_COOKIE_SECURE`；默认监听 `127.0.0.1:2560`。
- 成功 envelope：`{"data":...}`；错误 envelope：`{"error":{"code":"...","message":"..."}}`。
- 只有以下上游接口可被调用：

  ```text
  GET  /api/v1/admin/system/version
  GET  /api/v1/admin/users
  GET  /api/v1/admin/users/:id/api-keys
  GET  /api/v1/admin/accounts
  POST /api/v1/admin/accounts/usage/batch
  ```

- 一个普通用户只能有一个当前绑定；一个上游 Key 只能绑定一个门户用户；绑定要求两个限额都大于零。
- HTTP API 的时间字段统一为 UTC Unix 秒整数；Vue 格式化层在构造 JavaScript `Date` 前换算为毫秒。
- 普通用户只看到自己的 Key 和管理员公开的账号；邮箱必须脱敏，无邮箱账号使用持久化随机别名。
- Key 同步 15 秒、45 秒后陈旧；账号清单 5 分钟；公开账号用量 60 秒、`force:false`；最多 4 个并发任务，带 singleflight、抖动和最高 5 分钟退避。

## 文件边界

```text
cmd/sub2api-limit-portal/   CLI、进程生命周期、依赖装配
internal/config/            四个环境变量的解析与验证
internal/secure/            密码、会话 Token、AES-GCM、日志脱敏
internal/store/             SQLite migration、事务、用户/绑定/快照/审计
internal/sub2api/           唯一的上游 HTTP 访问边界与安全 DTO
internal/syncer/            定时、singleflight、退避和快照更新
internal/httpapi/           envelope、认证/RBAC/CSRF、门户路由
internal/webui/             go:embed 与构建后的 Vue 静态文件
web/                        Vue 3 SPA 源码、Vitest、Playwright
packaging/ + scripts/       systemd、Nginx、构建、安装、升级、卸载
```

## Task 1：配置、秘密与 SQLite 基座

**Files:**

- Create: `internal/store/migrations/001_initial.sql`
- Create: `internal/store/store.go`
- Create: `internal/store/models.go`
- Create: `internal/secure/{password,token,crypto,redact}.go`
- Modify: `internal/config/config.go`
- Test: `internal/secure/*_test.go`, `internal/store/*_test.go`, `internal/config/config_test.go`

1. 先写测试，锁定主密钥必须是 base64 编码的 32 字节、密码 12–128 字节、Argon2id 参数、AES-GCM AAD、Key 掩码 `sk-…abcd` 以及日志哨兵脱敏。
2. migration 一次性创建 `app_meta`、`users`、`sessions`、`settings`、`key_bindings`、`pool_accounts`、`audit_events`；使用唯一索引表达“一用户一绑定”和“一 Key 一用户”，使用状态值表达 active/disabled/deleted。Key 状态快照随绑定记录保存，不建立历史账本。
3. 打开 SQLite 时设置 WAL、foreign keys、busy timeout 和合理连接上限；每次启动运行嵌入式 migration，并提供 `PingContext` 给 `/readyz`。
4. 密码哈希固定为：

   ```go
   argon2.IDKey(password, salt, 3, 64*1024, 2, 32)
   ```

   会话与 CSRF 仅保存 SHA-256 摘要；Admin API Key 只以 AES-256-GCM 密文保存，AAD 绑定 settings 字段语义。
5. 运行：

   ```bash
   go test ./internal/config ./internal/secure ./internal/store
   ```

   预期：全部通过；临时 SQLite 中不存在测试使用的明文 Admin Key、密码、完整分发 Key或最后使用 IP。

## Task 2：安全的 Sub2API v0.1.183 客户端

**Files:**

- Create: `internal/sub2api/types.go`
- Create: `internal/sub2api/version.go`
- Create: `internal/sub2api/client.go`
- Test: `internal/sub2api/client_test.go`, `internal/sub2api/version_test.go`

1. 使用 `httptest.Server` 写表驱动测试，覆盖分页、`v0.1.183` 接受、旧版本拒绝、401、429、5xx、超时、超限响应、重定向拒绝、未知字段兼容和畸形 schema 拒绝。
2. `Config` 固定为：

   ```go
   type Config struct {
       BaseURL string
       APIKey string
       AllowPrivateHTTP bool
       Timeout time.Duration
       MaxResponseBytes int64
       PageSize int
       HTTPClient *http.Client
   }
   ```

   默认 10 秒、2 MiB、100 条/页；禁止 HTTP 重定向。HTTPS 默认允许；HTTP 只允许明确批准的回环、RFC 1918 或链路本地 IP 字面量，拒绝以 DNS 名称绕过私网检查。
3. DTO 只保留门户需要字段。原始 Key 和最后使用 IP只能存在于单次 decode 的私有结构中；返回的 `APIKey` 只能包含掩码、ID、名称、状态、两个限额/用量/窗口时间。
4. `BatchUsageResult` 允许部分成功，`nil` 窗口表示 Provider 未提供；utilization 采用上游百分比，不换算成美元。
5. 运行：

   ```bash
   go test ./internal/sub2api
   ```

   预期：全部通过，测试捕获的请求路径只属于固定 allowlist，重定向目标未收到 Admin Key。

## Task 3：事务化身份、绑定与公开账号池

**Files:**

- Create: `internal/store/setup.go`
- Create: `internal/store/users.go`
- Create: `internal/store/sessions.go`
- Create: `internal/store/bindings.go`
- Create: `internal/store/pool.go`
- Create: `internal/store/audit.go`
- Test: `internal/store/*_test.go`

1. Setup Token 只保存摘要和 30 分钟过期时间；完成 setup 的单个事务写入管理员、加密 settings、连接 UUID 和 `setup_complete=1`，并清除 Token 摘要。
2. `CreateUserWithBinding` 在一个事务中写用户和 Key 绑定；检查用户角色、两个正数限额、上游 Key 唯一性。换绑也必须事务化，失败后保留旧绑定。
3. 停用、软删除和管理员重置密码均在同一事务撤销目标用户全部会话；软删除不调用上游，也不删除上游 Key。
4. 池账号 upsert 保持随机 `public_alias` 稳定。普通 dashboard 查询只返回脱敏邮箱或 public alias，不返回原始 email；管理员查询可返回管理所需资料。
5. 绑定同步状态只有健康、缺失和限额不合规三类持久结果；缺失或不合规不自动按名称换绑。账号未在新目录出现时标记 missing，不擅自改变管理员的 published 选择。
6. 运行：

   ```bash
   go test ./internal/store -run 'Setup|Session|User|Binding|Pool|Audit'
   ```

   预期：并发重复绑定只有一个成功；事务错误后无半创建用户；停用/删用户/重置密码后旧会话均失效。

## Task 4：后台同步与陈旧快照

**Files:**

- Create: `internal/syncer/manager.go`
- Test: `internal/syncer/manager_test.go`

1. 使用可注入 clock、随机源、timer 和 `sub2api.Client` 接口编写确定性测试；验证 15 秒、5 分钟、60 秒计划，手动触发合并，最大并发 4，连续失败最高退避 5 分钟，成功后重置退避。
2. Key 同步顺序为读取 settings、列固定所有者 Key、转为安全快照、单事务 upsert、把消失绑定标成 missing。最后成功时间只在完整成功时推进；45 秒没有成功的 view 设置 `stale=true`。
3. 账号目录同步 upsert 允许字段并标记消失账号；用量同步只发送 published 且未 missing 的 ID，固定 `force:false`，逐账号保留部分成功和独立错误码。
4. 设置变更要求当前绑定数和 published 数均为零。成功变更时生成新 connection UUID 并清空旧 Key/账号快照，防止不同上游复用数字 ID。
5. 运行：

   ```bash
   go test ./internal/syncer -count=20
   go test -race ./internal/syncer
   ```

   预期：全部通过；竞态检测无报告；在模拟 429/5xx 后仍能读取最后成功快照。

## Task 5：HTTP API、认证和健康检查

**Files:**

- Create: `internal/httpapi/contracts.go`
- Create: `internal/httpapi/response.go`
- Create: `internal/httpapi/server.go`
- Create: `internal/httpapi/setup.go`
- Create: `internal/httpapi/auth.go`
- Create: `internal/httpapi/dashboard.go`
- Create: `internal/httpapi/admin_users.go`
- Create: `internal/httpapi/admin_pool_settings.go`
- Test: `internal/httpapi/*_test.go`

1. 核心公开类型固定为：

   ```go
   type KeyWindowView struct {
       LimitUSD float64 `json:"limit_usd"`
       UsedUSD float64 `json:"used_usd"`
       RemainingUSD float64 `json:"remaining_usd"`
       Percent float64 `json:"percent"`
       ResetAt *int64 `json:"reset_at"`
   }
   type PoolWindowView struct {
       Supported bool `json:"supported"`
       Utilization *float64 `json:"utilization"`
       ResetAt *int64 `json:"reset_at"`
   }
   type SnapshotMeta struct {
       AsOf *int64 `json:"as_of"`
       SourceUpdatedAt *int64 `json:"source_updated_at"`
       LastSuccessAt *int64 `json:"last_success_at"`
       Stale bool `json:"stale"`
   }
   ```

   `remaining_usd=max(limit-used,0)`；`percent` 按使用量/限额计算并限制为可展示范围；空 reset 不猜测时间。
2. 实现 `/api/setup/status|probe|complete`、`/api/auth/session|login|logout|password`、`/api/me/dashboard`、用户/密码/绑定管理、`/api/admin/upstream-keys`、`/api/admin/pool`、`/api/admin/settings`、`POST /api/admin/sync`。
3. Cookie 设置 HttpOnly、SameSite=Lax、Path=/；生产环境 Secure 由配置开启。所有变更方法要求与会话绑定的 CSRF Token；所有 `/api/admin/*` 在服务端检查 admin 角色。
4. `/healthz` 只返回进程存活；`/readyz` 检查配置主密钥、数据库 ping 和 setup 已完成，不依赖 Sub2API 在线。
5. 添加安全响应头、JSON body 大小限制、严格方法检查、通用外部错误信息和经脱敏的内部日志；API 和 SPA fallback 互不吞路径。
6. 使用假 Sub2API 跑 setup → 管理员会话 → 建用户绑定 → 普通用户登录 → dashboard → 发布账号完整集成流程，并覆盖跨用户访问、disabled/deleted、CSRF 缺失、401/429/5xx。
7. 运行：

   ```bash
   go test ./internal/httpapi
   go test -race ./internal/httpapi ./internal/store
   ```

   预期：所有接口 envelope 一致；普通用户无法访问 admin 端点；哨兵 Admin Key、完整分发 Key、密码、最后使用 IP 不出现在响应或日志中。

## Task 6：CLI、生命周期和 SPA 嵌入

**Files:**

- Create: `cmd/sub2api-limit-portal/main.go`
- Modify: `internal/webui/embed.go`
- Modify: `Makefile`
- Test: `cmd/sub2api-limit-portal/main_test.go`, `internal/webui/embed_test.go`

1. CLI 行为严格固定：

   ```text
   sub2api-limit-portal              # 等同 serve
   sub2api-limit-portal serve        # 启动服务
   sub2api-limit-portal keygen       # 输出一行环境变量赋值
   sub2api-limit-portal version      # 输出版本
   sub2api-limit-portal --version    # 输出版本
   ```

   未知命令返回非零退出码，错误不回显环境秘密。
2. `serve` 创建状态目录、打开 store、初始化 setup Token、装配 upstream factory、sync manager、HTTP server 与 embed handler；监听失败立即退出。SIGINT/SIGTERM 停止接收请求，给 HTTP 与同步器有限关闭时间，再关闭 SQLite。
3. 未初始化时只输出一次明文 Setup Token 和 UTC 到期时间；完成初始化后不再生成或输出新 Token。
4. `go:embed all:dist` 提供哈希静态资源长期 immutable cache、HTML no-cache 和非 API 路径 SPA fallback；缺少构建产物时返回明确 503。
5. 运行：

   ```bash
   go test ./cmd/sub2api-limit-portal ./internal/webui
   go run ./cmd/sub2api-limit-portal keygen
   go run ./cmd/sub2api-limit-portal --version
   ```

   预期：keygen 只输出 `SUB2API_LIMIT_MASTER_KEY=` 加合法 32 字节 base64；version 输出构建版本；测试通过。

## Task 7：Vue 3 状态界面

**Files:**

- Create: `web/src/lib/{api,format}.ts`
- Create: `web/src/state/{session,toast}.ts`
- Create: `web/src/router.ts`, `web/src/App.vue`, `web/src/styles.css`
- Create: `web/src/components/{QuotaBand,PoolTable,StatusPill,SideDrawer,SkeletonBlock,EmptyState,BrandMark}.vue`
- Create: `web/src/views/{Setup,Login,Dashboard,Users,Pool,Settings}View.vue`
- Create: `web/tests/*.spec.ts`, `web/e2e/portal.spec.ts`

1. API client 只解析统一 envelope；将 CSRF Token 保存在内存并随变更请求发送，401 清空本地会话状态。路由守卫用 setup 状态、登录状态和角色决定可见页面，但权限仍以服务端为准。
2. Setup 页面包含管理员资料、Base URL、Admin API Key、固定所有者、私网 HTTP 明确确认和非 `simple` 确认；连接失败保留非秘密输入，不回显 Admin Key。
3. Dashboard 顶部显示 Key 掩码、健康/缺失/不合规/陈旧状态，两个额度带显示 limit、used、remaining、percent、reset；随后显示公开账号池的账号别名、状态、5h/7d utilization 和 reset。空 reset 显示“尚未启动”，unsupported 显示“未提供”。
4. 管理后台完成用户创建、停用、软删除、重置密码、换绑/解绑；账号池完成筛选与批量发布；设置页在存在绑定或公开账号时禁止切换上游，并允许触发四种同步 scope。
5. 视觉使用暖白、墨色、钴蓝、青绿、紫色、琥珀与珊瑚，圆角 6–8px，内嵌 Noto Sans SC、Sora、JetBrains Mono。动画限定为 Vue Transition/TransitionGroup、CSS 和 Web Animations，并在 reduced motion 下关闭非必要运动。
6. Vitest 覆盖额度计算显示、空 reset、unsupported、权限导航、表单错误、账号列表响应式重排；Playwright 以生产 Go 二进制验证 setup、管理员、普通用户、桌面与移动截图、焦点、溢出与重叠。
7. 运行：

   ```bash
   npm --prefix web run typecheck
   npm --prefix web run test
   npm --prefix web run build
   npm --prefix web run test:e2e
   ```

   预期：全部通过；`internal/webui/dist` 生成；构建产物不包含哨兵密钥、Admin Key、密码或最后使用 IP。

## Task 8：Linux 打包、安装与运维文档

**Files:**

- Create: `packaging/sub2api-limit-portal.service`
- Create: `packaging/sub2api-limit-portal.env.example`
- Create: `packaging/nginx-sub2api-limit-portal.conf`
- Create: `scripts/install.sh`
- Create: `scripts/uninstall.sh`
- Create: `scripts/build-linux.ps1`
- Create: `README.md`, `SECURITY.md`

1. systemd 以专用无登录用户运行，使用 `StateDirectory` 创建 0750 数据目录，启用 NoNewPrivileges、ProtectSystem=strict、ProtectHome、私有 `/tmp`、空 capability set、地址族和 syscall allowlist；环境文件权限 0640。
2. Nginx 只代理到 `127.0.0.1:2560`，HTTP 308 跳转 HTTPS，并设置 HSTS、CSP、frame deny、nosniff、referrer policy、permissions policy、1 MiB body limit 和正确 forwarded headers。
3. 安装器只接受 Linux amd64/arm64 正规文件，运行候选 `keygen` 验证架构与 CLI；首次安装生成主密钥，升级保留 env 与数据。停服后复制旧二进制和同一时刻存在的 DB/WAL/SHM，新服务不稳定时回退旧 unit、二进制、升级前数据库快照和原 active/enabled 状态。
4. 默认卸载只移除程序和 unit；`--purge` 固定校验 `/etc/sub2api-limit-portal`、`/var/lib/sub2api-limit-portal`、`/var/backups/sub2api-limit-portal` 不是链接或宽路径，并要求输入完整确认词后才递归删除。
5. README 中文说明架构、v0.1.183/非 simple 前置条件、构建测试、setup、部署、API、WAL/SHM 备份、恢复与威胁模型；SECURITY 说明私密报告路径、Admin Key 全权限风险与部署方责任。
6. 运行：

   ```bash
   bash -n scripts/install.sh scripts/uninstall.sh
   shellcheck scripts/install.sh scripts/uninstall.sh
   systemd-analyze verify packaging/sub2api-limit-portal.service
   nginx -t -c "$PWD/packaging/nginx-sub2api-limit-portal.conf"
   ```

   预期：语法和静态检查无 error；Nginx 独立验证时可用包含 `events {}` 与 `http { include ...; }` 的临时主配置包装示例片段。

## Task 9：全量验收与发布阻断条件

**Files:**

- Test: all Go tests, frontend unit/e2e tests, `dist/` Linux artifacts

1. 使用假的 Sub2API Admin API 跑一次全新初始化与普通用户完整链路；再分别注入 Key 删除、限额清零、账号窗口缺失、401、429、5xx 和超时，确认最后快照、陈旧标识与错误展示。
2. 在临时数据库、HTTP 响应、日志和 `internal/webui/dist` 中搜索一组唯一哨兵 Admin Key、完整分发 Key、密码与 IP；任何匹配均阻断发布。
3. 运行完整门槛：

   ```bash
   go vet ./...
   go test ./...
   go test -race ./...
   npm --prefix web run typecheck
   npm --prefix web run test
   npm --prefix web run build
   npm --prefix web run test:e2e
   make linux
   sha256sum dist/sub2api-limit-portal-linux-amd64 dist/sub2api-limit-portal-linux-arm64
   ```

   预期：所有命令退出 0，两个 Linux artifact 非空且 checksum 不同。
4. 在 amd64 与 arm64 Linux 测试机分别执行安装、初始化、升级失败回退、默认卸载、重装复用数据、带 `--purge` 卸载；确认 unit 加固项生效、环境文件不是 world-readable、服务只监听 loopback。
5. 只有以下条件同时满足才发布：没有秘密泄漏、RBAC/CSRF/会话撤销测试通过、上游失败不清空快照、两个架构可启动、安装回退成功、README 的命令与实际 CLI 一致。

## 明确不包含

- 不代理模型请求，不创建或删除上游 Key，不显示完整 Key。
- 不提供多管理员、多 Key、邮件找回、MFA、趋势历史、Docker 或多实例一致性。
- 不根据 Provider 缺失数据推算 5h/7d，不把未知值显示成 0。
- 不承诺消除 Sub2API 原生并发窗口检查带来的少量超额。
- 不在应用内自动备份或轮换 Admin API Key；这些属于部署运维边界。
