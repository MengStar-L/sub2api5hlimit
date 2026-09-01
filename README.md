# Sub2API 配额中心

Sub2API 配额中心（服务名 `sub2api-limit-portal`）是一个独立的 Go sidecar：管理员把已经在 Sub2API 中配置好 5h 与 7d 限额的真实 Key 绑定给门户用户，用户登录后可以查看自己的 Key 用量、重置时间，以及管理员公开的全平台账号池状态。

> [!IMPORTANT]
> 本项目**不代理模型请求，也不自行扣减额度**。5h/7d 限制由 Sub2API 原生执行；门户负责身份管理、绑定、状态展示，以及由管理员明确触发的官方额度重置。上游必须是官方 `v0.1.183` 兼容接口，并且不能使用 `simple` 模式。原生并发计费在窗口临界点可能产生少量超额，这是已接受的上游语义。

## 工作方式

```text
用户浏览器 ──HTTP :2556─────────────────────────> sub2api-limit-portal
可选 HTTPS ──> Nginx ──HTTP :2556───────────────>      ├── SQLite（用户、会话、绑定、快照）
                                                        └── Sub2API Admin API（状态同步；管理员可触发额度重置）

用户的模型客户端 ─────────真实 Sub2API Key────────> Sub2API
```

- 一个普通用户最多绑定一个当前 Key，一个 Key 最多绑定一个门户用户。
- 可绑定 Key 必须同时具有正数 `rate_limit_5h` 和 `rate_limit_7d`。
- 门户只保存 Key ID、掩码与用量快照；上游返回的完整 Key 和最后使用 IP 仅在同步内存中短暂存在，随后丢弃。
- Sub2API Admin API Key 使用 AES-256-GCM 加密后保存；主密钥只来自进程环境，不写入数据库。
- Key 每 15 秒同步，45 秒无成功结果即标记陈旧；账号清单每 5 分钟同步；已公开账号用量每 60 秒以 `force:false` 刷新。
- 上游离线时保留最后快照并明确显示数据时间和“陈旧”状态，不把未知值伪装成零。

## 兼容性与前置条件

- Go 1.26.x。
- Node.js 与 npm（仅构建前端需要）。
- Sub2API 官方 `v0.1.183`，或经验证保持相同 Admin API schema 的版本。
- Sub2API 必须使用非 `simple` 模式，否则它不会执行所需的 5h/7d 原生限额。
- 默认可通过公网 HTTP 端口直接访问；如需 HTTPS，可使用附带的 Nginx 示例。
- 单实例部署，面向不超过 500 个门户用户和 100 个账号。SQLite 数据目录不能由多个实例同时共享。

上游连接只会调用以下 Admin API：

```text
GET  /api/v1/admin/system/version
GET  /api/v1/admin/users
GET  /api/v1/admin/users/:id/api-keys
PUT  /api/v1/admin/api-keys/:id
GET  /api/v1/admin/accounts
POST /api/v1/admin/accounts/usage/batch
```

`PUT /api/v1/admin/api-keys/:id` 仅在管理员明确执行单个或批量额度重置时调用，请求体只设置 `reset_rate_limit_usage=true`。这会清零上游 Key 的 5h、1d 与 7d 原生窗口，不是只读操作。

## 构建与测试

### 本机开发

```bash
npm --prefix web ci
npm --prefix web run build
go test ./...
go run ./cmd/sub2api-limit-portal keygen
```

`keygen` 输出一行可直接作为环境变量使用的 `SUB2API_LIMIT_MASTER_KEY=<base64>`。启动开发服务前设置四个环境变量：

```bash
export "$(go run ./cmd/sub2api-limit-portal keygen)"
export SUB2API_LIMIT_LISTEN=127.0.0.1:2556
export SUB2API_LIMIT_DB_PATH="$PWD/data/app.db"
export SUB2API_LIMIT_COOKIE_SECURE=false
go run ./cmd/sub2api-limit-portal serve
```

HTTP 访问使用 `SUB2API_LIMIT_COOKIE_SECURE=false`；如果自行配置 HTTPS，应将其改为 `true`。

### 完整质量门槛

```bash
go vet ./...
go test ./...
go test -race ./...
npm --prefix web run typecheck
npm --prefix web run test
npm --prefix web run build
npm --prefix web run test:e2e
npm --prefix web run test:e2e:production
```

Linux 或 macOS 可以运行 `make verify` 和 `make linux`。Windows PowerShell 可构建门户和特权更新器的 Linux 双架构静态二进制，并生成 SHA-256 清单：

```powershell
pwsh -File scripts/build-linux.ps1
Get-Content dist/SHA256SUMS
```

输出为 `dist/sub2api-limit-{portal,updater}-linux-{amd64,arm64}` 四个文件。默认注入版本 `0.2.0`；发布其他版本时传入 `-Version 0.2.1`。构建采用 `CGO_ENABLED=0`，Vue SPA 已通过 `go:embed` 内嵌，不需要在服务器上安装 Node.js 或复制静态目录。

## 首次初始化

1. 设置主密钥、监听地址和数据库路径并启动进程。未初始化的实例会生成一个仅 30 分钟有效的 Setup Token；Token 只写到控制台或 systemd journal，不写入数据库明文。
2. 打开 `/setup`，输入 Setup Token，创建唯一管理员。
3. 填写 Sub2API Base URL、全权限 Admin API Key 和固定 Key 所有者。
4. 明确确认上游不是 `simple` 模式。向导会验证版本、管理员用户、该所有者的 Key 列表、账号列表及批量用量接口。
5. 初始化完成后退出向导并登录。管理员可发布账号池、创建普通用户并原子绑定合规 Key。

Base URL 默认必须是 HTTPS。只有 RFC 1918、回环或链路本地地址可以在明确确认后使用 HTTP；上游客户端不跟随重定向，并限制超时与响应大小。

Setup Token 过期时，在尚未完成初始化的前提下重启服务以生成新 Token。systemd 部署可这样查看：

```bash
sudo systemctl restart sub2api-limit-portal
sudo journalctl -u sub2api-limit-portal.service -n 50 --no-pager
```

## Linux 部署

GitHub Release 提供内嵌前端的 Linux `amd64` 和 `arm64` 包，每个包包含门户和独立更新器两个静态二进制。服务器不需要安装 Go 或 Node.js。

### 1. 下载并验证 Release

以下命令以 `v0.2.0` 为例，会自动选择当前 CPU 架构：

```bash
release_version=v0.2.0
case "$(uname -m)" in
  x86_64|amd64) machine_arch=amd64 ;;
  aarch64|arm64) machine_arch=arm64 ;;
  *) echo "不支持的架构: $(uname -m)" >&2; exit 1 ;;
esac

archive="sub2api5hlimit-${release_version}-linux-${machine_arch}.tar.gz"
release_url="https://github.com/MengStar-L/sub2api5hlimit/releases/download/${release_version}"
curl --fail --location --remote-name "${release_url}/${archive}"
curl --fail --location --remote-name "${release_url}/SHA256SUMS"
grep "  ${archive}$" SHA256SUMS | sha256sum --check -
tar -xzf "$archive"
cd "sub2api5hlimit-${release_version}-linux-${machine_arch}"
```

校验必须显示 `OK`。若失败，不要运行压缩包中的任何文件。

### 2. 安装

```bash
sudo bash ./scripts/install.sh
```

安装器会验证两个二进制的架构与版本、展示所有目标路径并要求确认。首次安装会创建低权限服务账号、生成 AES 主密钥，同时安装门户服务、root 更新 oneshot 和路径 watcher；再次运行安装器会在停服状态下备份两个二进制、三个 unit 及 SQLite/WAL/SHM，失败时自动回滚。

默认布局：

| 内容 | 路径 |
| --- | --- |
| 安装根目录 | `/opt/sub2api5hlimit` |
| 门户二进制 | `/opt/sub2api5hlimit/bin/sub2api-limit-portal` |
| 更新器二进制 | `/opt/sub2api5hlimit/bin/sub2api-limit-updater` |
| 环境文件 | `/opt/sub2api5hlimit/config/sub2api-limit-portal.env` |
| SQLite | `/opt/sub2api5hlimit/data/app.db` |
| 升级与手工备份 | `/opt/sub2api5hlimit/backups/` |
| 更新状态 | `/opt/sub2api5hlimit/update/status.json` |
| 更新恢复事务 | `/opt/sub2api5hlimit/update/transaction.json`（仅执行期间存在） |
| 卸载器 | `/opt/sub2api5hlimit/uninstall.sh` |
| systemd units | `/etc/systemd/system/sub2api-limit-portal{,-update}.{service,path}` |

生产默认配置为：

```dotenv
SUB2API_LIMIT_LISTEN=0.0.0.0:2556
SUB2API_LIMIT_DB_PATH=/opt/sub2api5hlimit/data/app.db
SUB2API_LIMIT_COOKIE_SECURE=false
```

`0.0.0.0:2556` 表示进程接受所有 IPv4 网卡的连接。安装完成后可以直接打开 `http://服务器公网IP:2556/setup`。默认 HTTP 没有传输加密；TLS、防火墙和访问控制由部署管理员自行决定。

### 3. 可选：配置 Nginx HTTPS

先让域名解析到服务器并准备有效证书，然后安装示例配置：

```bash
sudo install -m 0644 packaging/nginx-sub2api-limit-portal.conf /etc/nginx/conf.d/sub2api-limit-portal.conf
sudoedit /etc/nginx/conf.d/sub2api-limit-portal.conf
sudo nginx -t
sudo systemctl reload nginx
```

将示例中的 `quota.example.com` 和证书路径替换为真实值。Nginx 对外监听 443，并把请求转发到 `127.0.0.1:2556`。示例已经配置 HSTS、同源 CSP、禁止 iframe、1 MiB 请求体限制和必要的代理头。

启用 HTTPS 后，可把环境文件中的 Cookie 设置改为 `true`：

```bash
sudo sed -i 's/^SUB2API_LIMIT_COOKIE_SECURE=.*/SUB2API_LIMIT_COOKIE_SECURE=true/' /opt/sub2api5hlimit/config/sub2api-limit-portal.env
sudo systemctl restart sub2api-limit-portal.service
```

是否限制 TCP 2556 直连由部署管理员自行配置；Nginx 示例不会修改防火墙或云安全组。

### 4. 首次初始化

安装后从 journal 读取 30 分钟有效的 Setup Token：

```bash
sudo journalctl -u sub2api-limit-portal.service -n 50 --no-pager
```

直接部署时打开 `http://服务器公网IP:2556/setup`；使用 Nginx 时打开 `https://你的域名/setup`。依次创建管理员、填写 Sub2API Base URL、Admin API Key 和固定 Key 所有者，并确认上游不是 `simple` 模式。初始化前 `/readyz` 返回 503 属于正常现象。

### 5. 使用 systemd 管理

```bash
sudo systemctl status sub2api-limit-portal.service
sudo systemctl start sub2api-limit-portal.service
sudo systemctl stop sub2api-limit-portal.service
sudo systemctl restart sub2api-limit-portal.service
sudo systemctl enable sub2api-limit-portal.service
sudo journalctl -u sub2api-limit-portal.service -f
```

修改 `/opt/sub2api5hlimit/config/sub2api-limit-portal.env` 后需要重启服务。查看本机健康状态：

```bash
curl --fail --silent http://127.0.0.1:2556/healthz
curl --fail --silent http://127.0.0.1:2556/readyz
```

- `/healthz` 只表示进程 HTTP 服务存活。
- `/readyz` 检查初始化、数据库和主密钥；上游短暂离线不会使其失败。

### 6. 升级与自更新

`v0.2.0` 是自更新功能的首次引导版本。`v0.1.x` 没有更新器和 systemd watcher，因此从旧版升级到 `v0.2.0` 必须下载并校验 `v0.2.0` Release，然后人工运行一次包内安装器：

```bash
sudo bash ./scripts/install.sh
```

完成这次引导后，管理员可在“程序更新”页检查并申请更新。门户进程只会以服务账号原子写入 `/opt/sub2api5hlimit/data/update.request`，其中只有 `schema`、随机操作 ID、目标版本和请求时间；它不能写安装目录，也不能控制 systemd。该请求会一直保留到更新成功、确定失败或完成回滚，避免进程中断后丢失唤醒条件。`sub2api-limit-portal-update.path` 会同时监视请求和 root updater 的恢复事务，随后启动 root oneshot 独立执行以下检查和事务：

1. 从固定的官方 GitHub `releases/latest` API 重新取得最新稳定版本，并要求它与请求目标一致。
2. 严格解析只有 `schema/version/min_updater_version/mode` 四字段的 `update-manifest.json`；只接受 `schema=1` 且 `mode=binary`。
3. 按本机 `amd64` 或 `arm64` 选择固定命名的归档，分别核对 GitHub 资产 `sha256` digest、`SHA256SUMS` 和实际下载摘要，并拒绝超限、路径穿越、链接、设备文件或缺少二进制的 tar。
4. 停止原门户前先原子写入 `/opt/sub2api5hlimit/update/transaction.json`；把两个旧二进制及同一停服时刻的 SQLite/WAL/SHM 复制到 `/opt/sub2api5hlimit/backups/update-*`，持久化完整备份清单后，再以同目录 `rename` 原子替换两个二进制。
5. 重启并同时检查 systemd active、`/readyz` 与门户版本；任一步失败都会停掉新进程、恢复两个二进制和数据库三件套，并重新验证旧服务。

回滚与断点恢复分别为停服、文件恢复、旧服务启动及健康检查保留独立超时预算；大数据库复制不会消耗掉后续启动旧服务的时间窗口。

如果 updater 被 `SIGKILL`、主机重启或意外断电打断，请求或恢复事务仍会让 path watcher 再次启动 updater。它会在处理任何新下载前验证事务清单，停止可能处于中间状态的门户，恢复旧二进制和离线数据库快照，并核对旧版本及 `/readyz`；只有恢复完成后才删除事务与请求标记。不要手工删除 `transaction.json`。

涉及目录布局、安装脚本或 systemd unit 变化的 Release 必须把 manifest 标为 `mode=manual`。页面会提示人工下载 Release 并再次运行安装器，root updater 不会自行改写 `/etc/systemd/system`。查看 watcher、执行日志和状态文件：

Release workflow 从仓库内的 `packaging/update-policy.json` 生成 manifest。该策略文件只允许 `min_updater_version` 与 `mode` 两个字段，并随代码评审；当前 `v0.2.0` 因首次引入 updater、目录和 systemd units，策略为 `manual`。发布后续纯二进制版本前，维护者才可把 `mode` 明确改为 `binary`。

```bash
sudo systemctl status sub2api-limit-portal-update.path
sudo journalctl -u sub2api-limit-portal-update.service -n 100 --no-pager
sudo cat /opt/sub2api5hlimit/update/status.json
```

`status.json` 只包含操作 ID、目标版本、状态阶段、稳定错误码和 UTC Unix 秒时间，不写下载 URL、路径或底层错误详情。若更新器报告 `rollback_failed`，保持服务隔离并使用对应 `backups/update-*` 目录人工恢复；不要连续重复更新。

GitHub 资产 digest 与 `SHA256SUMS` 可以发现传输或资产不一致，但二者处于同一个 GitHub Release 信任边界，并不等同于独立的签名验证。高保证环境应继续人工固定 Release tag 与摘要。

### 7. 手工升级与卸载

manifest 要求人工升级，或自更新 helper 不可用时，下载并校验新版本 Release，解压后再次运行包内安装器：

```bash
sudo bash ./scripts/install.sh
```

自动化环境只有在外部流程已核对路径时才应使用 `--yes`。默认卸载保留环境、数据库、备份和服务账号：

```bash
sudo /opt/sub2api5hlimit/uninstall.sh
```

确认已经另行备份后才可永久清除全部数据和服务账号：

```bash
sudo /opt/sub2api5hlimit/uninstall.sh --purge
```

### 8. 旧路径迁移

若检测到旧版 `/usr/local`、`/etc/sub2api-limit-portal` 或 `/var/lib/sub2api-limit-portal` 布局且新安装根不存在，安装器会中止。它不会猜测主密钥或自动创建空数据库。

迁移前停止服务并完整备份旧环境文件、二进制、SQLite、WAL 和 SHM。然后把**副本**放入新布局，保留原主密钥，并修改副本中的监听和数据库路径：

```bash
sudo systemctl stop sub2api-limit-portal.service
sudo install -d -m 0755 /opt/sub2api5hlimit/bin
sudo install -d -m 0750 /opt/sub2api5hlimit/config
sudo install -d -m 0750 -o sub2api-limit-portal -g sub2api-limit-portal /opt/sub2api5hlimit/data
sudo install -d -m 0700 /opt/sub2api5hlimit/backups
sudo install -m 0755 /usr/local/bin/sub2api-limit-portal /opt/sub2api5hlimit/bin/sub2api-limit-portal
sudo install -m 0640 -o root -g sub2api-limit-portal /etc/sub2api-limit-portal/sub2api-limit-portal.env /opt/sub2api5hlimit/config/sub2api-limit-portal.env
for source in /var/lib/sub2api-limit-portal/app.db /var/lib/sub2api-limit-portal/app.db-wal /var/lib/sub2api-limit-portal/app.db-shm; do
  if sudo test -f "$source"; then
    destination="/opt/sub2api5hlimit/data/$(basename "$source")"
    sudo cp --preserve=mode,timestamps "$source" "$destination"
    sudo chown sub2api-limit-portal:sub2api-limit-portal "$destination"
  fi
done
sudo sed -i 's|^SUB2API_LIMIT_LISTEN=.*|SUB2API_LIMIT_LISTEN=0.0.0.0:2556|' /opt/sub2api5hlimit/config/sub2api-limit-portal.env
sudo sed -i 's|^SUB2API_LIMIT_DB_PATH=.*|SUB2API_LIMIT_DB_PATH=/opt/sub2api5hlimit/data/app.db|' /opt/sub2api5hlimit/config/sub2api-limit-portal.env
```

随后运行新 Release 的 `scripts/install.sh`，检查 journal 和页面数据无误后再归档旧路径。不要删除旧环境文件，直到确认新服务能解密原 Admin API Key 并完成一次恢复演练。

## SQLite 备份

数据库使用 WAL 模式。**运行中不能只复制 `app.db`**，否则最近事务可能只存在于 WAL。推荐使用 `sqlite3 .backup` 获取一致快照：

```bash
backup_dir=/opt/sub2api5hlimit/backups/manual-$(date -u +%Y%m%dT%H%M%SZ)
sudo install -d -m 0700 "$backup_dir"
sudo sqlite3 /opt/sub2api5hlimit/data/app.db ".backup '$backup_dir/app.db'"
sudo sha256sum "$backup_dir/app.db" | sudo tee "$backup_dir/SHA256SUMS" >/dev/null
```

没有 `sqlite3` CLI 时，先停服，再复制同一时刻存在的数据库文件：

```bash
backup_dir=/opt/sub2api5hlimit/backups/manual-$(date -u +%Y%m%dT%H%M%SZ)
sudo systemctl stop sub2api-limit-portal.service
portal_restart_required=true
trap 'if [ "$portal_restart_required" = true ]; then sudo systemctl start sub2api-limit-portal.service; fi' EXIT INT TERM
sudo install -d -m 0700 "$backup_dir"
for source in /opt/sub2api5hlimit/data/app.db /opt/sub2api5hlimit/data/app.db-wal /opt/sub2api5hlimit/data/app.db-shm; do
  if sudo test -f "$source"; then sudo cp --preserve=mode,timestamps "$source" "$backup_dir/"; fi
done
sudo systemctl start sub2api-limit-portal.service
portal_restart_required=false
trap - EXIT INT TERM
```

备份必须连同 `/opt/sub2api5hlimit/config/sub2api-limit-portal.env` 中的主密钥安全保存；没有原主密钥将无法解密数据库中的 Admin API Key。数据库和主密钥应分开加密保存，并定期进行恢复演练。

## 门户 API

成功响应统一为 `{"data": ...}`，错误响应统一为 `{"error":{"code":"...","message":"..."}}`。setup 接口只在未初始化阶段开放，登录和健康检查无需会话；所有已认证的变更接口还要求 CSRF 令牌，`/api/admin/*` 只允许管理员。

| 方法与路径 | 用途 |
| --- | --- |
| `GET /api/setup/status` | 查询是否已初始化和 Setup Token 到期时间 |
| `POST /api/setup/probe` | 在写入设置前验证上游版本、所有者、Key、账号与批量用量能力 |
| `POST /api/setup/complete` | 校验 Token、上游和管理员资料并原子完成初始化 |
| `GET /api/auth/session` | 查询当前会话 |
| `POST /api/auth/login` | 登录 |
| `POST /api/auth/logout` | 注销并撤销当前会话 |
| `PUT /api/auth/password` | 普通用户或管理员修改自己的密码 |
| `GET /api/me/dashboard` | 当前用户的 Key 额度与公开账号池快照 |
| `GET, POST /api/admin/users` | 列出用户；创建用户并绑定 Key |
| `PUT, DELETE /api/admin/users/:id` | 停用/启用或软删除用户 |
| `PUT /api/admin/users/:id/password` | 管理员重置密码并撤销该用户全部会话 |
| `PUT, DELETE /api/admin/users/:id/binding` | 换绑或解绑 Key |
| `GET /api/admin/upstream-keys` | 列出固定所有者的可绑定 Key 掩码与合规状态 |
| `GET, PUT /api/admin/pool` | 查看账号池；批量发布或取消发布账号 |
| `GET, PUT /api/admin/settings` | 查看或更新上游连接设置 |
| `POST /api/admin/sync` | 手动触发 `all`、`keys`、`accounts` 或 `usage` 同步 |
| `POST /api/admin/users/:id/quota-reset` | 重置一个绑定用户的上游 Key 原生额度窗口 |
| `POST /api/admin/quota-resets` | 创建批量额度重置任务 |
| `GET /api/admin/quota-resets/current` | 查询当前批量重置任务 |
| `GET /api/admin/quota-resets/:id` | 查询指定批量重置任务 |
| `GET /api/admin/update` | 查看当前版本、最新稳定版和更新执行状态 |
| `POST /api/admin/update/check` | 立即重新检查官方稳定 Release |
| `POST /api/admin/update/apply` | 写入一次版本绑定的自更新请求 |

核心视图类型：

```text
KeyWindowView  { limit_usd, used_usd, remaining_usd, percent, reset_at }
PoolWindowView { supported, utilization, reset_at }
SnapshotMeta   { as_of, source_updated_at, last_success_at, stale }
```

所有时间字段当前均为 UTC Unix 秒整数；前端会先乘以 1000 再交给 JavaScript `Date`。`reset_at` 为空表示窗口尚未启动；账号 Provider 不提供某个窗口时 `supported=false`，界面显示“未提供”。批量账号接口可能返回较旧的 passive 快照，因此 `source_updated_at` 是上游数据时间，不能把 60 秒轮询误解成 60 秒更新保证。

## 安全模型与限制

### 信任边界

- 主机 root、systemd 环境文件、SQLite 文件、Nginx 和 Sub2API 管理端都属于可信计算基。
- root 更新器属于可信计算基，但不加载门户 EnvironmentFile 或 Admin API Key。门户服务账号只能写请求标记；只有 root oneshot 能写二进制、离线备份与更新状态。
- 普通门户用户互不信任，只能读取自己的 Key 状态；所有已登录用户都能读取管理员明确发布的脱敏账号池状态。
- 程序默认通过 `0.0.0.0:2556` 提供公网 HTTP。部署管理员可自行增加 Nginx HTTPS、防火墙或其他访问控制；使用 HTTPS 时应同步启用 Secure Cookie。

### 重要风险

- **Admin API Key 是全权限凭据。** Sub2API `v0.1.183` 没有满足本服务所需接口的细粒度只读令牌。数据库中虽为 AES-256-GCM 密文，但同时取得数据库和环境主密钥的攻击者仍可恢复它。必须限制主机、配置文件、备份与 journal 的访问。
- 管理员额度重置会调用全权限上游接口，并同时清零目标 Key 的 5h、1d 和 7d 原生窗口。批量操作不可撤销，执行前应核对目标集合。
- 门户不参与请求转发或扣费。攻击者绕过门户仍只能使用真实 Sub2API Key，限额成败完全取决于非 `simple` 模式的 Sub2API。原生并发检查在临界点允许少量超额。
- 上游被攻陷或返回伪造状态时，门户无法独立验证计费事实；它会把成功解析的上游响应作为快照展示。
- 本项目不抵御已取得 root、管理员浏览器会话或 Sub2API 管理权限的攻击者，也不提供 DDoS 防护、MFA、邮件找回、密钥轮换编排或多实例一致性。
- 更换 Base URL 或固定 Key 所有者前必须先解绑全部用户并取消所有账号发布。设置更新会轮换连接标识并清空旧快照，避免把另一实例中复用的数字 ID 错认成原对象。

密码使用 Argon2id 保存；会话使用不可逆 Token 摘要；停用用户、重置密码或软删除用户会撤销其全部会话。安全问题处理流程见 [`SECURITY.md`](SECURITY.md)。
