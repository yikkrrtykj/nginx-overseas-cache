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

## 缓存与实时数据

- `/templates/`：缓存图片、字体和模板资源；
- `/templates-auth/.../static/js/*.js`：缓存带哈希版本的 JavaScript；
- `/render`、其他页面、API、WebSocket：不缓存；
- `101 Switching Protocols` 长期处于挂起状态是正常的实时连接；
- `X-Cache-Status: HIT` 表示静态资源已由 Nginx 缓存返回。

清除 Nginx 静态缓存时，应先确认路径准确，再执行：

```bash
sudo find /var/cache/nginx/web_cache -mindepth 1 -delete
sudo systemctl reload nginx
```

## 防火墙建议

只允许内网访问 23333。以下示例允许整个 `192.168.0.0/16`，请按实际网段收紧：

```bash
sudo ufw allow from 192.168.0.0/16 to any port 23333 proto tcp
```

不要在公网路由器上转发 23333，因为 URL 和访问日志中可能出现 token。

