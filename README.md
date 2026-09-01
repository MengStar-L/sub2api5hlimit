<div align="center">

# Sub2API 配额中心

**给 Sub2API 加一个额度门户。** 用户自己看用量，管理员统一发号。

</div>

---

## 这是什么

Sub2API 本身能按 5 小时和 7 天限制每个 Key 的用量，但用户看不到自己还剩多少。

配额中心补上这一块：管理员把 Sub2API 里已配好限额的 Key 分给门户用户，用户登录网页就能看到自己的额度、已用多少、什么时候重置，以及管理员公开出来的账号池状态。

> [!IMPORTANT]
> 门户不代理模型请求，也不自己扣额度。限额始终由 Sub2API 执行，门户只负责发号、展示和由管理员触发的官方重置。用户拿到的是真实 Sub2API Key，照常填进自己的客户端使用。

## 你能用到什么

**普通用户**

- 自己 Key 的 5h / 7d 额度：限额、已用、剩余、百分比、重置时间
- 管理员公开的账号池状态（邮箱自动脱敏）
- 首次登录自动弹出最新公告，看过就不再打扰，之后可在侧栏回看
- 侧栏的 Codex 额度重置预测（来自第三方，仅供参考）
- 自助修改密码

**管理员**

- 创建用户并一键绑定合规 Key，随时换绑、解绑、停用、重置密码
- 选择哪些账号对用户可见
- 对单个用户或批量清零上游额度窗口
- 发布公告
- 在网页上检查并安装新版本

## 开始之前

准备好三样东西：

| | 要求 |
| --- | --- |
| 一台 Linux 服务器 | amd64 或 arm64，能开放一个端口。不需要装 Go、Node.js 或数据库 |
| 一个 Sub2API 实例 | 官方 `v0.1.183`，且**不能是 `simple` 模式**，否则它不会执行 5h/7d 限额 |
| 一个 Sub2API 管理员 API Key | 用于同步状态和执行重置。这是全权限凭据，请妥善保管 |

打算绑定给用户的 Key，需要在 Sub2API 中同时设置好正数的 5h 与 7d 限额。

## 安装

下载对应架构的安装包，校验后运行安装脚本。以下命令会自动识别 CPU 架构：

```bash
release_version=v0.5.1
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

校验结果必须是 `OK`。如果不是，不要运行包里的任何文件，重新下载。

然后安装：

```bash
sudo bash ./scripts/install.sh
```

脚本会列出所有要写入的路径并等你确认，随后创建低权限服务账号、生成加密主密钥、注册开机自启。程序装在 `/opt/sub2api5hlimit`，数据库在 `/opt/sub2api5hlimit/data/app.db`，默认监听 `0.0.0.0:2556`。

## 首次设置

安装完成后，从日志里取出 Setup Token，它只有 30 分钟有效：

```bash
sudo journalctl -u sub2api-limit-portal.service -n 50 --no-pager
```

浏览器打开 `http://服务器IP:2556/setup`，按向导四步走完：

1. 填入 Setup Token
2. 创建管理员账号
3. 填写 Sub2API 地址、管理员 API Key、以及存放待分配 Key 的那个 Sub2API 用户
4. 确认上游不是 `simple` 模式

向导会实地验证一遍上游版本、Key 列表和账号列表，通过后即完成初始化。之后登录管理员账号就可以开始发号了。

Token 过期了就重启一次服务，会生成新的：

```bash
sudo systemctl restart sub2api-limit-portal.service
```

Sub2API 地址默认必须是 HTTPS。内网、回环地址在明确确认后可以用 HTTP。

## 配 HTTPS（推荐）

默认的 HTTP 访问没有传输加密。如果门户要暴露在公网上，建议用包内的 Nginx 示例配置套一层 HTTPS。先让域名解析到这台服务器并准备好证书，然后：

```bash
sudo install -m 0644 packaging/nginx-sub2api-limit-portal.conf /etc/nginx/conf.d/sub2api-limit-portal.conf
sudoedit /etc/nginx/conf.d/sub2api-limit-portal.conf
sudo nginx -t
sudo systemctl reload nginx
```

把配置里的 `quota.example.com` 和证书路径换成你的。Nginx 监听 443 并转发到本机 2556，安全头都已经配好。

启用 HTTPS 后打开 Secure Cookie：

```bash
sudo sed -i 's/^SUB2API_LIMIT_COOKIE_SECURE=.*/SUB2API_LIMIT_COOKIE_SECURE=true/' /opt/sub2api5hlimit/config/sub2api-limit-portal.env
sudo systemctl restart sub2api-limit-portal.service
```

防火墙和是否封掉 2556 直连由你决定，Nginx 示例不会改动这些。

## 日常使用

**发一个号**：用户管理页新建用户，从下拉里选一个合规 Key，创建即绑定。把用户名、初始密码和 Key 交给用户。

**公开账号池**：账号池页勾选要让用户看到的账号，点公开。用户端只能看到脱敏邮箱和用量窗口。

**清额度**：用户管理页可以单独重置某个用户，也可以批量重置。这会调用 Sub2API 官方接口，清零该 Key 的 5h、1d、7d 三个窗口，**不可撤销**，批量前请核对目标。

**发公告**：公告页写标题和正文，发布后用户下次登录会自动弹一次。

数据多久刷一次：Key 用量每 15 秒，账号清单每 5 分钟，公开账号用量每 60 秒。上游临时离线时，界面会保留最后一次快照并明确标注数据时间和「陈旧」，不会把未知当成零。页面上的数据时间是上游那边的时间，不等于门户的刷新间隔。

## 维护

日常操作：

```bash
sudo systemctl status sub2api-limit-portal.service
sudo systemctl restart sub2api-limit-portal.service
sudo journalctl -u sub2api-limit-portal.service -f
```

**升级**：管理员登录后打开「程序更新」页，检查更新并安装即可，服务会自行完成下载校验、替换和重启，失败会自动回滚。少数涉及目录或服务配置变动的版本，页面会提示需要手工下载 Release 并重新运行 `sudo bash ./scripts/install.sh`。

**备份**：数据库用 WAL 模式，运行中不能只复制 `app.db`。用官方方式取一致快照：

```bash
backup_dir=/opt/sub2api5hlimit/backups/manual-$(date -u +%Y%m%dT%H%M%SZ)
sudo install -d -m 0700 "$backup_dir"
sudo sqlite3 /opt/sub2api5hlimit/data/app.db ".backup '$backup_dir/app.db'"
```

> [!WARNING]
> 备份数据库的同时，必须一并保存 `/opt/sub2api5hlimit/config/sub2api-limit-portal.env` 里的主密钥。没有原主密钥，数据库里的 Sub2API API Key 无法解密，恢复出来的实例连不上上游。两者请分开加密存放，并定期演练恢复。

**卸载**：默认保留数据、备份和配置。

```bash
sudo /opt/sub2api5hlimit/uninstall.sh
```

确认已另行备份后，才用 `--purge` 彻底清除全部数据。

## 需要知道的几件事

- **管理员 API Key 是全权限凭据。** Sub2API 目前没有满足门户所需接口的只读令牌。它在数据库里是加密存放的，但同时拿到数据库和主密钥就能解开，所以主机、配置文件、备份和日志的访问都要收紧。
- **额度重置不可撤销**，且会同时清零 5h、1d、7d 三个窗口。
- **限额由 Sub2API 执行**。绕过门户的人拿到的仍是真实 Key，能不能超额完全取决于 Sub2API 处于非 `simple` 模式。窗口临界点的并发请求可能有少量超额，这是上游的既有行为。
- **Codex 重置预测来自第三方站点**，是根据公开信号推算的预测，不是官方公告，别拿它做运维决策。
- **一个门户用户对应一个 Key**，反之亦然。
- **单实例部署**，规模设计到 500 个用户和 100 个账号。数据目录不能被多个实例同时使用。
- 更换 Sub2API 地址或 Key 所有者之前，必须先解绑全部用户并取消所有账号公开，否则旧快照里的数字 ID 会和新实例撞车。
- 门户不提供 DDoS 防护、二次验证、邮件找回和密钥轮换。密码用 Argon2id 存储；停用用户、重置密码或删除用户会立即让其所有会话失效。

安全问题请按 [`SECURITY.md`](SECURITY.md) 的流程反馈。
