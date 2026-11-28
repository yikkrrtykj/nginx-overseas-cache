# Overseas Cache V2 现场验收待执行清单

适用工作：[真实 Renderer #4](https://github.com/yikkrrtykj/nginx-overseas-cache/issues/4)、[管理入口 #6](https://github.com/yikkrrtykj/nginx-overseas-cache/issues/6)、[升级回滚 #7](https://github.com/yikkrrtykj/nginx-overseas-cache/issues/7)。当前 **PRODUCTION HOLD**。现场验收须另行授权。

## 候选包与授权

**READY FOR SITE ACCEPTANCE / PRODUCTION HOLD。** [PR #8](https://github.com/yikkrrtykj/nginx-overseas-cache/pull/8) 已合并。现场验收候选包由精确 `main` `fb3d2820b18c6b95c4bb439ded7cac62a26bec30` 构建，Tree 为 `80f07ab40058ebf66c5a2c5c4fd3000c0a6462bb`，与已审查的 PR HEAD `381e1c1c396cdaa98fc53c1a3aa7abf9909bf306` 一致。现场验收和生产变更须各自授权。 [main CI 8/8](https://github.com/yikkrrtykj/nginx-overseas-cache/actions/runs/46159570297) · [恢复 CI 6/6](https://github.com/yikkrrtykj/nginx-overseas-cache/actions/runs/46158042212) · [双架构包验证](https://github.com/yikkrrtykj/nginx-overseas-cache/actions/runs/46159555739)

| 对象 | 版本或 SHA256 |
| --- | --- |
| [GitHub Actions Artifact 15269385931](https://github.com/yikkrrtykj/nginx-overseas-cache/actions/runs/46159570297/artifacts/15269385931) ZIP | `7e546e717e0c46f1bfe4822122a0ea8cca37c9be0358611c515664fc1372acac` |
| Linux amd64 tar.gz | `10c48071e4e5faa89ac5b23983479fa2ca6ae06da645b75a19694f73e1432a2e` |
| Linux arm64 tar.gz | `7d2543ab50be4881a2c792e2923f0bf98a665ae06b38ca67b80e7d9a4732c33a` |
| `overseas-admin`、`ws-fanout`、`config-helper` | 两种架构均为 `fb3d2820b18c` |

原 main 和各 Draft 包只作历史追溯。最终 PR [Artifact 15268392894](https://github.com/yikkrrtykj/nginx-overseas-cache/actions/runs/46158042197/artifacts/15268392894) 用于对应 HEAD 的隔离验证。现场批准后使用上述 main 候选包。

## 开始前填写

| 项目 | 现场负责人确认 |
|---|---|
| 对象与权限 | 预生产副本、独立 SSH 会话、允许中断的测试上游、值班人员、回滚负责人 |
| 候选包 | 精确 Git SHA、源码 Tree、Ubuntu 版本、CPU 架构、包 SHA256、三个二进制的版本和许可证 |
| Renderer 输入 | 专用测试账号与两个隔离测试频道、专用凭据、可人工核实持续更新的画面，测试频道与生产频道互不影响 |
| 管理输入 | 管理 VLAN、Admin 绑定 IP 和主机名、证书颁发链及有效期、业务 VLAN、拒绝验证网段 |
| V1 快照 | 记录有效的 V1 SHA 和快照 ID，验证原密码、班组 ACL、服务状态、配置及权限、已有缓存对象和缓存目录 |
| 回滚阈值 | 填写允许的画面冻结、握手恢复、管理不可达和回滚时间。用隔离实验耗时作为估算输入，按现场变更窗口独立批准阈值 |

## #4 Renderer 手动验收

1. 使用 OBS 浏览器源的实际 CEF 版本及现场使用的桌面浏览器版本。对每个实际部署的 Renderer Mode 执行，包括现有的 `onair`。每个模式至少验证一组两个健康客户端。另用不同频道或模式开对照组，保持画面持续更新。
2. 记录精确版本、浏览器版本和模式、匿名组 ID、每组上下游连接数、当前画面序号。先确认连续更新和同组单上游。
3. 对已授权的测试上游单独制造一次可计时断开，记录中断时刻、最后正常画面、下游断开、自动重连、`join`、`join_res`、首条新代实时消息、首个更新画面及稳定时间。
4. 恢复基线后另做一次 Fanout 服务重启。记录操作和首次可接受连接的时刻，以及相同的客户端事件。验证无需人工刷新、对照组符合该故障的预期影响范围、两客户端恢复到同一正确画面和单上游状态。对于 Fanout 整体重启，对照组也允许重新连线并须恢复。
5. 对照新旧连接与本次画面序号确认旧代快照和旧 `call_res` 未覆盖新代。握手往返仅记录方向、时间、连接代次、`action` 和合成事务关联符。录像、截图和网络事件脱敏，避免包含 Cookie、完整 URL、Token、请求头、凭据或原始帧。
6. 无法自动重连、`join_res` 关联不符、画面超阈值、恢复出旧频道或旧状态、单组出现重复上游、对照组异常或任一健康客户端需手动刷新均判失败。停止扩大测试，保留证据，按已授权方案切回上一经验证版本。合成 WebSocket 的连接和消息测试仅验证协议及代理链。

## #6 管理入口

- 从旧管理 VLAN 实际登录 V1，记录 ACL、监听和访问结果。另从业务网段及拒绝网段测试管理端口，并在业务端验证数据平面。
- 采用默认回环模式时，从已批准的独立 SSH 会话进行端口转发，使用原密码登录，访问状态及配置。确认直连管理端口无监听。
- 采用指定 IP 的 HTTPS 模式时，在管理 VLAN 从主机名访问，验证链、SAN、HSTS、Secure、HttpOnly、SameSite Cookie 和登录。确认业务及拒绝网段不能访问管理 API，并验证业务端实际渲染可用。
- 重启服务和预生产机后重复认证及否定测试。证书名、信任链、私钥权限或 ACL 错误导致访问失败时暂停切换，按已批准的回环或回滚路径恢复。
- 对每条路径分别记录 ACL、监听地址、实际认证访问和业务功能。不公开认证头、会话或敏感网络表。

## #7 升级和回滚

1. 在临时工作目录验证 `sha256sum -c SHA256SUMS`，或只下载一个架构包时用 `sha256sum -c --ignore-missing SHA256SUMS` 并确认输出包含目标架构的 `OK` 行。记录三个包内二进制 `--version`。读取精确版本的部署手册。
2. 备份 V1 运行状态和 VM、配置、证书及缓存。确认第二条 SSH 会话、反向路径及执行人。升级前验证 V1 认证、静态缓存和真实测试 Renderer。开始采集单调时钟探针和浏览器的画面变化。
3. 用审查通过的包运行 `sudo bash scripts/install-admin.sh`。验证 Nginx、Fanout、Admin、配置 Helper socket、原密码、ACL、配置内容、权限及缓存目录。确认旧缓存文件保留时，V2 的隔离缓存键仍可能使首次访问回源。
4. 分别以有效 HTTP 响应、`join_res` 与正确消息、真实可更新画面界定恢复。记录安装器耗时和业务恢复上下界。判断持续异常、管理失联、错误配置生效或超阈值，并根据事前授权回滚。
5. 运行获批候选包的 `sudo bash scripts/install-admin.sh rollback`，复验 V1 管理入口、ACL、最新有效密码、原配置、缓存与业务。记录恢复耗时。脚本恢复异常时调用事先验证的 VM 快照路径，保留日志。
6. 签署 #4、#6 和 #7 状态，附上选用版本、窗口、测试矩阵、脱敏证据、验证人及生产操作是否另获批准。当前没有真实 Renderer、现场网段或生产发布验证。

## 安装或配置变更中断后的恢复

1. 暂停后续配置写入、重复切换和服务启动。观察 `journalctl -u overseas-cache-recovery.service` 以及 `nginx.service`、`overseas-cache-admin.service`、`ws-fanout.service` 的状态。保留 `/var/lib/overseas-cache-installer/pending-install`、`/etc/nginx/overseas-cache/backups/pending`、`original` 链接及相关快照。证据仅记录日志和快照标识，按现场要求脱敏。
2. 如果同一开机周期内仍有受管进程运行，门禁可能阻断启动以避免 systemd 等待环。在已批准的恢复窗口，从已验证且获批的包目录执行 `sudo bash scripts/install-admin.sh recover`。处理磁盘不足、损坏快照、配置校验或 systemd 错误后，先保留证据，再按部署手册重试或走已验证的 VM 快照回退路径。
3. 恢复后确认待恢复记录清除，执行 `sudo nginx -t`，核对 Nginx、Admin、Fanout 及对应版本的 Helper socket，复验认证、ACL、缓存、WebSocket 和画面。V1 回滚及 V2 卸载保留门禁和独立的恢复 Helper，以便重放中断的事务。旧 V2 的运行 Helper 缺少新参数时，开机门禁使用已暂存的恢复 Helper。清理步骤见[部署手册](https://github.com/yikkrrtykj/nginx-overseas-cache/blob/fb3d2820b18c6b95c4bb439ded7cac62a26bec30/docs/DEPLOYMENT.md#recovery-gate-and-its-lifecycle)。
