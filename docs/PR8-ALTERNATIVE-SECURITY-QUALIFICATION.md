# PR #8 替代安全验收

`CODEX SECURITY REVIEW UNAVAILABLE / OWNER APPROVED ALTERNATIVE SECURITY QUALIFICATION`

**PRODUCTION HOLD**。工作区的专用 Codex Security Review 未启用。Owner 授权使用独立安全专项审计和可复现检查。普通 Codex Code Review 回执单列。

| 对象 | 完整 SHA |
| --- | --- |
| 合并前 main | `8eaeb2aa8d8b6552fc5abe7aaf04581678814a88` |
| 初审 HEAD | `b1b8c448b47681732117f3e8069c3ae3f2fb35d9` |
| 最终审计 HEAD | `381e1c1c396cdaa98fc53c1a3aa7abf9909bf306` |
| 最终审计 Tree | `80f07ab40058ebf66c5a2c5c4fd3000c0a6462bb` |

## 发现及处置

| 级别 | 发现 | 处置与复验 |
| --- | --- | --- |
| 合并阻塞 | 当 Nginx 已运行，安装中断可使配置 Helper 在安装日志仍挂起时接收配置请求。 | 在共享配置锁内、任何配置读写或配置恢复前检查安装日志。冷启动和已运行的 Helper、RPC 读写和直接配置恢复均进入同一门禁。独立复审确认修复。 [锁及日志检查](https://github.com/yikkrrtykj/nginx-overseas-cache/blob/381e1c1c396cdaa98fc53c1a3aa7abf9909bf306/internal/configstore/store.go#L182) |
| 中风险 | root 回滚从服务账户可写的密码文件路径迁移记录，旧代码可能跟随快照中的符号链接。 | 仅在 root 私有快照中捕获单个文件条目，限制暂存大小，校验捕获对象是 16 KiB 以内的普通文件。拒绝符号链接、超大文件、目录和 FIFO，复验未复制目录内容。 [快照实现](https://github.com/yikkrrtykj/nginx-overseas-cache/blob/381e1c1c396cdaa98fc53c1a3aa7abf9909bf306/scripts/install-admin.sh#L95) |
| 回归阻塞 | 冷启动拒绝测试后，试运行的 Unix socket 可在恢复 V1 时残留，使再次切到 V2 时配置请求连接失败。 | 恢复旧服务定义前停止 socket，即使 systemd 已丢失其配置文件也核对运行状态。V1 恢复明确断言试运行 socket 已清理，V2 重装后用真实配置 RPC 复验。 [失败运行](https://github.com/yikkrrtykj/nginx-overseas-cache/actions/runs/46129146472) · [最终回归](https://github.com/yikkrrtykj/nginx-overseas-cache/actions/runs/46158042212) |
| 回归阻塞（解除 Draft 后 Codex P2） | 升级早期中断时，旧 V2 运行 Helper 缺少 `--recover-before-nginx`，开机配置恢复受阻。 | 在加载门禁前持久化兼容恢复 Helper。Ubuntu 24.04 与 26.04 真实来宾机使用原 main 的 Helper，在加载门禁后杀掉安装器，重启时恢复已确认配置且仍保留旧运行二进制。 [已解决线程](https://github.com/yikkrrtykj/nginx-overseas-cache/pull/8#discussion_r4726395051) · [24.04](https://github.com/yikkrrtykj/nginx-overseas-cache/actions/runs/46158042212/job/141019140960) · [26.04](https://github.com/yikkrrtykj/nginx-overseas-cache/actions/runs/46158042212/job/141019140983) |
| 回归阻塞（再次解除 Draft 后 Codex P2） | 历史原始快照可能把 Renderer 指令内嵌在站点中。旧检查会拒绝合法原始快照，阻断回滚与卸载。 | 无独立设置文件时验证三项内嵌指令齐全。迁移与校验共用解析器，已封存的快照仍使用原哈希校验。在两种 Ubuntu amd64 和原生 arm64 同次开机环境验证退回旧 V1、实际重定向和缺项拒绝。 [已解决线程](https://github.com/yikkrrtykj/nginx-overseas-cache/pull/8#discussion_r4727565713) · [恢复矩阵](https://github.com/yikkrrtykj/nginx-overseas-cache/actions/runs/46158042212) |

独立审计在原实现之外逐次审查初审和每一版修改，最终以 `381e1c1c396cdaa98fc53c1a3aa7abf9909bf306` 和上述 Tree 给出 **PASS，0 未解决高风险项或有效合并阻塞项**。审计覆盖 root 安装器、systemd 依赖、PID 启动时间和实际 flock、路径及符号链接、原子临时文件、记录完整性与权限、配置和安装恢复并发、失败时保留日志、凭据和供应链。普通 [Codex Code Review 回执](https://github.com/yikkrrtykj/nginx-overseas-cache/pull/8#issuecomment-7241233183) 单列，精确 HEAD 0 findings。

## 可复现检查

| 工具和版本 | 范围及结果 |
| --- | --- |
| ShellCheck 0.9.0 | 五个 PR 涉及的 Bash 文件。0 errors。唯一警告为既有的 SC2033，调用 `sudo install` 时 sudo 执行系统二进制，ShellCheck 基于脚本同名函数给出提示。 [扫描日志](https://github.com/yikkrrtykj/nginx-overseas-cache/actions/runs/46158120121/job/141019158678) · [原始证据](https://github.com/yikkrrtykj/nginx-overseas-cache/actions/runs/46158120121/artifacts/15268900793) |
| Bash 5.2.21 | 相同五个文件的 `bash -n` 全部通过。 [扫描日志](https://github.com/yikkrrtykj/nginx-overseas-cache/actions/runs/46158120121/job/141019158678) |
| Go 1.27.4 | `go mod verify`、`go vet ./...`、`go test -race -count=1 ./...` 通过。覆盖源码及测试触发的并发路径。 [Go job](https://github.com/yikkrrtykj/nginx-overseas-cache/actions/runs/46158042197/job/141018917347) |
| govulncheck v1.10.0 | `./...` 扫描未报告漏洞，结果取决于执行时漏洞库和该构建可达范围。 [Go job](https://github.com/yikkrrtykj/nginx-overseas-cache/actions/runs/46158042197/job/141018917347) |
| Gitleaks v8.28.0 | 精确 HEAD 工作树目录，100% 掩码，未报告泄露。原生 arm64 另对合成 Admin 密码、Renderer Token 与 WebSocket 探针 Token 做日志验证。 [Gitleaks](https://github.com/yikkrrtykj/nginx-overseas-cache/actions/runs/46158042197/job/141018917158) · [arm64 日志](https://github.com/yikkrrtykj/nginx-overseas-cache/actions/runs/46158201836/job/141019404745) |
| npm 11.19.1 | `web` 的锁文件依赖，`npm audit --audit-level=high` 报告 0 vulnerabilities。 [Browser job](https://github.com/yikkrrtykj/nginx-overseas-cache/actions/runs/46158042197/job/141018917350) |
| systemd-analyze verify | Ubuntu 24.04 systemd 255 及 26.04 实机矩阵验证恢复 unit。真实 systemd 验证 Requires/After、安装和配置恢复互锁、服务账户访问、失败保留记录、两次恢复及重启。 [恢复矩阵](https://github.com/yikkrrtykj/nginx-overseas-cache/actions/runs/46158042212) |
| 发布包 | 精确 HEAD CI 进行可复现构建和 SHA256 校验。两个架构归档，原生 arm64 对标准构建执行二进制版本核验、安装/回滚和共享 WebSocket 帧。 [PR Artifact 15268392894](https://github.com/yikkrrtykj/nginx-overseas-cache/actions/runs/46158042197/artifacts/15268392894) · [arm64 工作流](https://github.com/yikkrrtykj/nginx-overseas-cache/actions/runs/46158201836) |

## 剩余边界

- 旧版 Helper 在升级初段中断时可能继续服务既有文件。升级或恢复期间暂停配置操作。受信任 root 的手动 Nginx reload 走系统管理路径，操作手册要求先恢复挂起事务。
- systemd 在安装器仍有效时已接受的试运行作业存在从检查到执行的间隔。启动门禁保护新的激活请求，运行就绪由单独检查确认。
- 主工作流依赖 GitHub-hosted 镜像、上游 Actions 版本标签和扫描库。两项旧版本回归分别依赖历史 Artifact `15236030131` 和 `15254252857` 的保留期限。发布证据保留 checkout SHA、Artifact digest、包 SHA256、版本和可复现构建结果。
- 现场仍需授权验证实际管理 VLAN、ACL、HTTPS 证书和负向访问、Renderer 重连及画面恢复、升级窗口和回退阈值。#5 的小时级实验继续使用已验收结果。

## 候选包和合并记录

精确 PR 构建 `381e1c1c396c` 的 [Artifact 15268392894](https://github.com/yikkrrtykj/nginx-overseas-cache/actions/runs/46158042197/artifacts/15268392894) 用于隔离验收。ZIP SHA256 为 `fd7883086f09454dde82cab060001262b76beae7a1903850e1c57c76cae2128e`。amd64 和 arm64 包 SHA256 分别为 `3b5f5839edb0625371fb94896b38c1fb162e9de776bb5aa6ce151304a2113d3e`、`c4b2f3020f802613da328b6b61d4f60daff26b8f9f670899684ea8a767b1ee91`。

解除 Draft 后触发的 [最终普通 Code Review](https://github.com/yikkrrtykj/nginx-overseas-cache/pull/8#issuecomment-7236491544) 在精确候选 HEAD 上完成，0 findings，两个历史 P2 线程均已解决，未解决线程为 0。

PR #8 通过 merge commit `fb3d2820b18c6b95c4bb439ded7cac62a26bec30` 合入 `main`，保留 15 个原始提交。两个父提交依次为 `8eaeb2aa8d8b6552fc5abe7aaf04581678814a88` 和 `381e1c1c396cdaa98fc53c1a3aa7abf9909bf306`。合并 Tree `80f07ab40058ebf66c5a2c5c4fd3000c0a6462bb` 与审查候选完全一致。

合并后从隔离工作流触发精确 main 的标准 CI，**8/8 PASS**。恢复工作流的 6/6 PASS 来自等同 Tree 的 PR HEAD。原生 arm64 迁移、回滚与 WebSocket 帧通过。Fanout 入口、核心源码和 Go 依赖文件与合并前 main 的 Git 对象一致，#5 的完整小时级数据继续适用。 [main CI](https://github.com/yikkrrtykj/nginx-overseas-cache/actions/runs/46159570297) · [PR 恢复证据](https://github.com/yikkrrtykj/nginx-overseas-cache/actions/runs/46158042212) · [原生 arm64](https://github.com/yikkrrtykj/nginx-overseas-cache/actions/runs/46158201836)

| 现场候选包 | SHA256 |
| --- | --- |
| [Artifact 15269385931](https://github.com/yikkrrtykj/nginx-overseas-cache/actions/runs/46159570297/artifacts/15269385931) ZIP | `7e546e717e0c46f1bfe4822122a0ea8cca37c9be0358611c515664fc1372acac` |
| Linux amd64 tar.gz | `10c48071e4e5faa89ac5b23983479fa2ca6ae06da645b75a19694f73e1432a2e` |
| Linux arm64 tar.gz | `7d2543ab50be4881a2c792e2923f0bf98a665ae06b38ca67b80e7d9a4732c33a` |

主工作流两次构建校验和一致。双架构隔离验证分别在原生 amd64 和 arm64 校验 ZIP digest、`SHA256SUMS`、六份许可证及声明、源码随包文件、三个二进制版本 `fb3d2820b18c`。与精确 PR 包逐文件比较时，脚本、配置、单元、文档和许可证一致，三个二进制和归档带合并提交版本。对照原 main 包，包内部署手册和安装器已更新，并增加 systemd 恢复单元和依赖配置。 [双架构验证](https://github.com/yikkrrtykj/nginx-overseas-cache/actions/runs/46159555739)

**READY FOR SITE ACCEPTANCE / PRODUCTION HOLD。** 现场候选包通过 GitHub Actions Artifact 交付，等待现场验收授权。
