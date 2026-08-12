# Nginx Overseas Web Cache

用一台可访问海外源站的 Ubuntu 服务器，为内网电脑提供固定的 HTTP 入口：

```text
内网浏览器 -> http://SERVER_IP:23333 -> Ubuntu Nginx -> 海外网页/CDN
```

项目针对以下场景设计：

- 内网客户端不安装 VPN 或系统代理；
- 页面、API 和 WebSocket 保持实时，不缓存；
- 图片、字体和带版本号的 JavaScript 由 Nginx 缓存；
- 浏览器始终停留在内网 IP，不会跳回海外域名；
- token 单独保存在服务器上，不进入 Git 仓库。

> 本项目示例只适用于你有权访问和代理的站点。不要把带 token 的 HTTP 服务暴露到公网。

## 文件说明

```text
nginx/
├── conf.d/
│   └── proxy-cache.conf
├── settings.conf.example
└── sites-available/
    └── overseas-cache
```

## 安装

Ubuntu 安装 Nginx：

```bash
sudo apt update
sudo apt install -y nginx
```

复制配置：

```bash
sudo cp nginx/conf.d/proxy-cache.conf /etc/nginx/conf.d/proxy-cache.conf
sudo cp nginx/sites-available/overseas-cache /etc/nginx/sites-available/overseas-cache
sudo mkdir -p /etc/nginx/overseas-cache
sudo cp nginx/settings.conf.example /etc/nginx/overseas-cache/settings.conf
```

编辑服务器本地设置：

```bash
sudo nano /etc/nginx/overseas-cache/settings.conf
```

把 `REPLACE_WITH_REAL_TOKEN` 改成真实 token。该文件不要提交到 Git。

启用站点：

```bash
sudo ln -sfn /etc/nginx/sites-available/overseas-cache /etc/nginx/sites-enabled/overseas-cache
sudo nginx -t
sudo systemctl reload nginx
```

访问：

```text
http://SERVER_IP:23333/
```

## 修改 API 参数

当 channel、account 或 token 变化时，只编辑：

```text
/etc/nginx/overseas-cache/settings.conf
```

然后检查并重新加载：

```bash
sudo nginx -t
sudo systemctl reload nginx
```

## 更换上游域名

### 使用管理页面（推荐）

仓库包含一个可由可信内网直接访问的管理面板。它可以填写：

- 主站域名；
- 静态 CDN 域名；
- `channelId`；
- `accountId`；
- 新 token（留空则保持原 token）。

在 Ubuntu 的仓库目录中安装：

```bash
git pull
sudo bash scripts/install-admin.sh
```

安装程序会备份并部署仓库中的最新 Nginx 配置。如果当前 channel/account/token 仍直接写在旧的站点配置中，安装程序会尝试自动迁移到权限为 `0600` 的 `settings.conf`。

安装完成后，它会显示随机生成的管理密码。管理服务监听 `23000` 端口，但程序只接受私有内网网段，并且仍然要求用户名和密码。内网电脑直接访问：

```text
http://192.168.16.23:23000
```

使用安装程序显示的用户名和密码登录。管理页不会读取并显示原 token；token 输入框留空时会保持服务器上的当前值。每次保存都会自动备份、执行 `nginx -t` 并重新加载，失败会恢复旧配置。

管理面板的服务状态和日志：

```bash
sudo systemctl status overseas-cache-admin
sudo journalctl -u overseas-cache-admin -n 100 --no-pager
```

默认允许的管理网段为 `192.168.0.0/16`、`10.0.0.0/8`、`172.16.0.0/12` 和本机回环。需要收紧范围时，编辑 `/etc/nginx/overseas-cache/admin.env` 中的 `OVERSEAS_ADMIN_ALLOWED_NETWORKS`，多个网段使用英文逗号分隔，然后执行 `sudo systemctl restart overseas-cache-admin`。

> 管理页使用 HTTP，登录密码和新 token 在内网传输时不加密。它只适用于可信局域网，绝不能在路由器上映射或向公网开放 TCP 23000。

### 使用命令行脚本

主站域名变化时，不要在多个配置文件里手工查找替换。先把仓库中的脚本放到服务器，例如仓库已经克隆到 `/opt/nginx-overseas-cache`：

```bash
cd /opt/nginx-overseas-cache
sudo bash scripts/change-upstreams.sh 新的主站域名
```

如果主站和静态 CDN 域名都变了：

```bash
sudo bash scripts/change-upstreams.sh 新的主站域名 新的CDN域名
```

示例：

```bash
sudo bash scripts/change-upstreams.sh api.example.com cdn.example.com
```

只填域名，不要带 `https://`、端口、路径或结尾斜杠。脚本会：

1. 从现有 Nginx 配置识别旧域名；
2. 备份两个配置文件；
3. 更新统一的主站/CDN 变量和上游地址，Host、TLS SNI、跳转、Cookie、HTML/JS 地址及缓存键会自动引用新值；
4. 执行 `nginx -t`；
5. 验证成功后重新加载，失败则恢复备份。

域名会参与静态缓存键，因此换域名后不会把旧站缓存误返回给新站。旧缓存会在 `inactive=7d` 到期后由 Nginx 自动清理。

如果只是页面 URL 中的 `channelId`、`accountId` 或 `token` 变化，仍然只修改：

```text
/etc/nginx/overseas-cache/settings.conf
```

如果新的地址不仅换了域名，还改变了路径结构（例如 `/render`、`/templates/` 或 `/templates-auth/`），需要同步修改 `nginx/sites-available/overseas-cache` 中对应的 `location`，不能只运行换域名脚本。

## 缓存与实时数据

- `/templates/`：缓存图片、字体和模板资源；
- `/templates-auth/.../static/js/*.js`：缓存带哈希版本的 JavaScript；
- `/render`、其他页面、API、WebSocket：不缓存；
- `101 Switching Protocols` 长期处于挂起状态是正常的实时连接；
- `X-Cache-Status: HIT` 表示静态资源已由 Nginx 缓存返回。

专用访问日志位于：

```text
/var/log/nginx/overseas-cache.access.log
```

该日志只记录 `render`、`templates` 等请求类别，不记录具体路径、URL 查询参数或 Referer，避免把 channel token 写入普通访问日志。浏览器地址栏、历史记录及页面自身请求仍可能包含 token，因此 23333 端口必须只允许可信内网访问；Nginx 配置本身也会拒绝来自公共地址的连接。

如果服务器使用过旧版配置，旧访问日志中可能已经存在 `/templates-auth/.../<token>/...` 路径。升级并确认新配置生效后，可以清空旧日志：

```bash
sudo truncate -s 0 /var/log/nginx/overseas-cache.access.log /var/log/nginx/overseas-cache.error.log
```

清除 Nginx 静态缓存时，应先确认路径准确，再执行：

```bash
sudo find /var/cache/nginx/web_cache -mindepth 1 -delete
sudo systemctl reload nginx
```

## 防火墙建议

只允许内网访问 23333 和管理端口 23000。以下示例允许整个 `192.168.0.0/16`，请按实际网段收紧：

```bash
sudo ufw allow from 192.168.0.0/16 to any port 23333 proto tcp
sudo ufw allow from 192.168.0.0/16 to any port 23000 proto tcp
```

不要在公网路由器上转发 23333 或 23000，因为 URL、管理密码和 token 都是敏感信息。
