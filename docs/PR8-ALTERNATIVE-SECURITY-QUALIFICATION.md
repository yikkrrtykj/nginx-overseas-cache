# PR #8 替代安全验收

`CODEX SECURITY REVIEW UNAVAILABLE / OWNER APPROVED ALTERNATIVE SECURITY QUALIFICATION`

**PRODUCTION HOLD**。工作区的专用 Codex Security Review 未启用。Owner 授权使用独立安全专项审计和可复现检查。普通 Codex Code Review 回执单列。

| 对象 | 完整 SHA |
| --- | --- |
| 合并前 main | `8eaeb2aa8d8b6552fc5abe7aaf04581678814a88` |
| 初审 HEAD | `b1b8c448b47681732117f3e8069c3ae3f2fb35d9` |
| 最终审计 HEAD | `01746cc861e1151fd8530ef0e8adb85fb5892525` |
| 最终审计 Tree | `fda8ff30da6d79a2fe6c629a4c641cc507ed905f` |

## 发现及处置

| 级别 | 发现 | 处置与复验 |
| --- | --- | --- |
| 合并阻塞 | 当 Nginx 已运行，安装中断可使配置 Helper 在安装日志仍挂起时接收配置请求。 | 在共享配置锁内、任何配置读写或配置恢复前检查安装日志。冷启动和已运行的 Helper、RPC 读写和直接配置恢复均进入同一门禁。独立复审确认修复。 [锁及日志检查](https://github.com/yikkrrtykj/nginx-overseas-cache/blob/01746cc861e1151fd8530ef0e8adb85fb5892525/internal/configstore/store.go#L182) |
| 中风险 | root 回滚从服务账户可写的密码文件路径迁移记录，旧代码可能跟随快照中的符号链接。 | 仅在 root 私有快照中捕获单个文件条目，限制暂存大小，校验捕获对象是 16 KiB 以内的普通文件。拒绝符号链接、超大文件、目录和 FIFO，复验未复制目录内容。 [快照实现](https://github.com/yikkrrtykj/nginx-overseas-cache/blob/01746cc861e1151fd8530ef0e8adb85fb5892525/scripts/install-admin.sh#L95) |
| 回归阻塞 | 冷启动拒绝测试后，试运行的 Unix socket 可在恢复 V1 时残留，使再次切到 V2 时配置请求连接失败。 | 恢复旧服务定义前停止 socket，即使 systemd 已丢失其配置文件也核对运行状态。V1 恢复明确断言试运行 socket 已清理，V2 重装后用真实配置 RPC 复验。 [失败运行](https://github.com/yikkrrtykj/nginx-overseas-cache/actions/runs/46129146472) · [最终回归](https://github.com/yikkrrtykj/nginx-overseas-cache/actions/runs/46130980292) |
| 回归阻塞（解除 Draft 后 Codex P2） | 升级早期中断时，旧 V2 运行 Helper 缺少 `--recover-before-nginx`，开机配置恢复受阻。 | 在加载门禁前持久化兼容恢复 Helper。Ubuntu 24.04 与 26.04 真实来宾机使用原 main 的 Helper，在加载门禁后杀掉安装器，重启时恢复已确认配置且仍保留旧运行二进制。 [已解决线程](https://github.com/yikkrrtykj/nginx-overseas-cache/pull/8#discussion_r4726395051) · [24.04](https://github.com/yikkrrtykj/nginx-overseas-cache/actions/runs/46130980292/job/140936038141) · [26.04](https://github.com/yikkrrtykj/nginx-overseas-cache/actions/runs/46130980292/job/140936038159) |

独立审计在原实现之外逐次审查初审和每一版修改，最终以 `01746cc861e1151fd8530ef0e8adb85fb5892525` 和上述 Tree 给出 **PASS，0 未解决高风险项或有效合并阻塞项**。审计覆盖 root 安装器、systemd 依赖、PID 启动时间和实际 flock、路径及符号链接、原子临时文件、记录完整性与权限、配置和安装恢复并发、失败时保留日志、凭据和供应链。普通 [Codex Code Review 回执](https://github.com/yikkrrtykj/nginx-overseas-cache/pull/8#issuecomment-7237335267) 单列，精确 HEAD 0 findings。

## 可复现检查

| 工具和版本 | 范围及结果 |
| --- | --- |
| ShellCheck 0.9.0 | 五个 PR 涉及的 Bash 文件。0 errors。唯一警告为既有的 SC2033，调用 `sudo install` 时 sudo 执行系统二进制，ShellCheck 基于脚本同名函数给出提示。 [扫描日志](https://github.com/yikkrrtykj/nginx-overseas-cache/actions/runs/46131019490/job/140935945226) · [原始证据](https://github.com/yikkrrtykj/nginx-overseas-cache/actions/runs/46131019490/artifacts/15257740866) |
| Bash 5.2.21 | 相同五个文件的 `bash -n` 全部通过。 [扫描日志](https://github.com/yikkrrtykj/nginx-overseas-cache/actions/runs/46131019490/job/140935945226) |
| Go 1.27.4 | `go mod verify`、`go vet ./...`、`go test -race -count=1 ./...` 通过。覆盖源码及测试触发的并发路径。 [Go job](https://github.com/yikkrrtykj/nginx-overseas-cache/actions/runs/46130980298/job/140935825383) |
| govulncheck v1.10.0 | `./...` 扫描未报告漏洞，结果取决于执行时漏洞库和该构建可达范围。 [Go job](https://github.com/yikkrrtykj/nginx-overseas-cache/actions/runs/46130980298/job/140935825383) |
| Gitleaks v8.28.0 | 精确 HEAD 工作树目录，100% 掩码，未报告泄露。原生 arm64 另对合成 Admin 密码、Renderer Token 与 WebSocket 探针 Token 做日志验证。 [Gitleaks](https://github.com/yikkrrtykj/nginx-overseas-cache/actions/runs/46130980298/job/140935825414) · [arm64 日志](https://github.com/yikkrrtykj/nginx-overseas-cache/actions/runs/46131077478/job/140936125073) |
| npm 11.19.1 | `web` 的锁文件依赖，`npm audit --audit-level=high` 报告 0 vulnerabilities。 [Browser job](https://github.com/yikkrrtykj/nginx-overseas-cache/actions/runs/46130980298/job/140935825451) |
| systemd-analyze verify | Ubuntu 24.04 systemd 255 及 26.04 实机矩阵验证恢复 unit。真实 systemd 验证 Requires/After、安装和配置恢复互锁、服务账户访问、失败保留记录、两次恢复及重启。 [恢复矩阵](https://github.com/yikkrrtykj/nginx-overseas-cache/actions/runs/46130980292) |
| 发布包 | 精确 HEAD CI 进行可复现构建和 SHA256 校验。两个架构归档，原生 arm64 对标准构建执行二进制版本核验、安装/回滚和共享 WebSocket 帧。 [PR Artifact 15257670944](https://github.com/yikkrrtykj/nginx-overseas-cache/actions/runs/46130980298/artifacts/15257670944) · [arm64 工作流](https://github.com/yikkrrtykj/nginx-overseas-cache/actions/runs/46131077478) |

## 剩余边界

- 旧版 Helper 在升级初段中断时可能继续服务既有文件。升级或恢复期间暂停配置操作。受信任 root 的手动 Nginx reload 走系统管理路径，操作手册要求先恢复挂起事务。
- systemd 在安装器仍有效时已接受的试运行作业存在从检查到执行的间隔。启动门禁保护新的激活请求，运行就绪由单独检查确认。
- 主工作流依赖 GitHub-hosted 镜像、上游 Actions 版本标签和扫描库。两项旧版本回归分别依赖历史 Artifact `15236030131` 和 `15254252857` 的保留期限。发布证据保留 checkout SHA、Artifact digest、包 SHA256、版本和可复现构建结果。
- 现场仍需授权验证实际管理 VLAN、ACL、HTTPS 证书和负向访问、Renderer 重连及画面恢复、升级窗口和回退阈值。#5 的小时级实验继续使用已验收结果。

## 候选包和合并记录

精确 PR 构建 `01746cc861e1` 的 [Artifact 15257670944](https://github.com/yikkrrtykj/nginx-overseas-cache/actions/runs/46130980298/artifacts/15257670944) 用于隔离验收。ZIP SHA256 为 `8b146927180183ab7533596b099a7947a11a9df6c677f156a11277a955d15eb3`。amd64 和 arm64 包 SHA256 分别为 `8e697c75e12dbd79e453b69beb263ddaec2f298d4ffc74f201de6473e046c7ad`、`47461b9174e1c20fc3995ad665444481cdb2ba98922bc2f708479a552558609b`。

当前等待最终合并门禁。合并后从精确 main 构建新的现场候选包并记录对应验证。
