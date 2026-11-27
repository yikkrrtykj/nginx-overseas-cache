# Nginx Overseas Web Cache

在 Ubuntu 上为可信内网提供渲染页面入口。Nginx 处理 HTTPS 源站转发、静态资源缓存与域名改写，Go Fanout 按来源、Token、频道和渲染模式共享 WebSocket，Go Admin 提供管理控制台。

| 入口 | 默认绑定 | 用途 |
|---|---|---|
| Renderer | TCP 23333，按 Nginx 内网网段规则访问 | 实时页面、API、静态缓存和 WebSocket |
| Admin | `127.0.0.1:23000` | 连接、缓存、配置、识别和诊断 |
| Fanout | `127.0.0.1:3001` | Nginx 内部路由、状态文件、本机指标 |
| Nginx health | `127.0.0.1:23334` | 本机进程可达性 |
| Config helper | `/run/overseas-cache-config.sock` | 配置应用与回滚 |

## 部署

在隔离主机验证后，复制对应 Linux 架构的构建包，校验 `SHA256SUMS`，从解包目录运行 `sudo bash scripts/install-admin.sh`。安装前需要 Ubuntu Nginx 和 systemd。全新安装从终端读取自定义管理密码。现有安装会继续使用原有的 PBKDF2 密码记录、源站、CDN 和渲染参数。

使用 [安装、升级及回滚说明](docs/DEPLOYMENT.md) 配置 Admin 绑定地址和 TLS。渲染端仍运行兼容的内网 HTTP，链接可能携带 Token，应保持在受控局域网。管理操作迁移到单独的配置服务，控制台可恢复备份中的渲染字段。

## 本地开发

```sh
npm --prefix web ci --ignore-scripts
npm --prefix web run build
go test ./...
go vet ./...
python -m pip install -r ws-fanout/requirements.txt
python -m unittest discover -s tests -v
node --test tests/test_admin_ui.cjs
```

安装 Playwright Chromium 后运行 `npm --prefix web run browser`。四种视口和状态截图输出到 `artifacts/browser/`。运行 `bash scripts/build-release.sh` 生成 Linux amd64 / arm64 包及 SHA256 校验文件。

- [架构及兼容矩阵](docs/ARCHITECTURE.md)
- [HTTP 接口契约](docs/API.md)
- [权限与安全边界](docs/SECURITY.md)
- [界面规范](design.md)
- [前端选型 ADR](docs/ADR-001.md)
- [性能验证](docs/PERFORMANCE.md)
