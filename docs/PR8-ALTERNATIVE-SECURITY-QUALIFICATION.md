# PR #8 替代安全验收

`CODEX SECURITY REVIEW UNAVAILABLE / OWNER APPROVED ALTERNATIVE SECURITY QUALIFICATION`

**PRODUCTION HOLD**。工作区的专用 Codex Security Review 未启用。Owner 授权使用独立安全专项审计和可复现检查。普通 Codex Code Review 回执单列。

| 对象 | 完整 SHA |
| --- | --- |
| 合并前 main | `8eaeb2aa8d8b6552fc5abe7aaf04581678814a88` |
| 初审 HEAD | `b1b8c448b47681732117f3e8069c3ae3f2fb35d9` |
| 最终审计 HEAD | `4d7bbe0084ab6adfa95d61d895e0ad57c4c4cfbe` |
| 最终审计 Tree | `978f23131ef79291cc2c983e70f554335b55b6bc` |

## 发现及处置

| 级别 | 发现 | 处置与复验 |
| --- | --- | --- |
| 合并阻塞 | 当 Nginx 已运行，安装中断可使配置 Helper 在安装日志仍挂起时接收配置请求。 | 在共享配置锁内、任何配置读写或配置恢复前检查安装日志。冷启动和已运行的 Helper、RPC 读写和直接配置恢复均进入同一门禁。独立复审确认修复。 [锁及日志检查](https://github.com/yikkrrtykj/nginx-overseas-cache/blob/4d7bbe0084ab6adfa95d61d895e0ad57c4c4cfbe/internal/configstore/store.go#L182) |
| 中风险 | root 回滚从服务账户可写的密码文件路径迁移记录，旧代码可能跟随快照中的符号链接。 | 仅在 root 私有快照中捕获单个文件条目，限制暂存大小，校验捕获对象是 16 KiB 以内的普通文件。拒绝符号链接、超大文件、目录和 FIFO，复验未复制目录内容。 [快照实现](https://github.com/yikkrrtykj/nginx-overseas-cache/blob/4d7bbe0084ab6adfa95d61d895e0ad57c4c4cfbe/scripts/install-admin.sh#L95) |
| 回归阻塞 | 冷启动拒绝测试后，试运行的 Unix socket 可在恢复 V1 时残留，使再次切到 V2 时配置请求连接失败。 | 恢复旧服务定义前停止 socket，即使 systemd 已丢失其配置文件也核对运行状态。V1 恢复明确断言试运行 socket 已清理，V2 重装后用真实配置 RPC 复验。 [失败运行](https://github.com/yikkrrtykj/nginx-overseas-cache/actions/runs/46129146472) · [最终回归](https://github.com/yikkrrtykj/nginx-overseas-cache/actions/runs/46129534682) |

独立审计在原实现之外逐次审查初审和每一版修改，最终以 `4d7bbe0084ab6adfa95d61d895e0ad57c4c4cfbe` 和上述 Tree 给出 **PASS，0 未解决高风险项或有效合并阻塞项**。审计覆盖 root 安装器、systemd 依赖、PID 启动时间和实际 flock、路径及符号链接、原子临时文件、记录完整性与权限、配置和安装恢复并发、失败时保留日志、凭据和供应链。普通 [Codex Code Review 回执](https://github.com/yikkrrtykj/nginx-overseas-cache/pull/8#issuecomment-7237137801) 单列，精确 HEAD 0 findings。

## 可复现检查

| 工具和版本 | 范围及结果 |
| --- | --- |
| ShellCheck 0.9.0 | 五个 PR 涉及的 Bash 文件。0 errors。唯一警告为既有的 SC2033，调用 `sudo install` 时 sudo 执行系统二进制，ShellCheck 基于脚本同名函数给出提示。 [扫描日志](https://github.com/yikkrrtykj/nginx-overseas-cache/actions/runs/46129576945/job/140931575343) · [原始证据](https://github.com/yikkrrtykj/nginx-overseas-cache/actions/runs/46129576945/artifacts/15256528698) |
| Bash 5.2.21 | 相同五个文件的 `bash -n` 全部通过。 [扫描日志](https://github.com/yikkrrtykj/nginx-overseas-cache/actions/runs/46129576945/job/140931575343) |
| Go 1.27.4 | `go mod verify`、`go vet ./...`、`go test -race -count=1 ./...` 通过。覆盖源码及测试触发的并发路径。 [Go job](https://github.com/yikkrrtykj/nginx-overseas-cache/actions/runs/46129534676/job/140931518813) |
| govulncheck v1.10.0 | `./...` 扫描未报告漏洞，结果取决于执行时漏洞库和该构建可达范围。 [Go job](https://github.com/yikkrrtykj/nginx-overseas-cache/actions/runs/46129534676/job/140931518813) |
| Gitleaks v8.28.0 | 精确 HEAD 工作树目录，100% 掩码，未报告泄露。原生 arm64 另对合成 Admin 密码、Renderer Token 与 WebSocket 探针 Token 做日志验证。 [Gitleaks](https://github.com/yikkrrtykj/nginx-overseas-cache/actions/runs/46129534676/job/140931518840) · [arm64 日志](https://github.com/yikkrrtykj/nginx-overseas-cache/actions/runs/46129719492/job/140931997633) |
| npm 11.19.1 | `web` 的锁文件依赖，`npm audit --audit-level=high` 报告 0 vulnerabilities。 [Browser job](https://github.com/yikkrrtykj/nginx-overseas-cache/actions/runs/46129534676/job/140931518819) |
| systemd-analyze verify | Ubuntu 24.04 systemd 255 及 26.04 实机矩阵验证恢复 unit。真实 systemd 验证 Requires/After、安装和配置恢复互锁、服务账户访问、失败保留记录、两次恢复及重启。 [恢复矩阵](https://github.com/yikkrrtykj/nginx-overseas-cache/actions/runs/46129534682) |
| 发布包 | 精确 HEAD CI 进行可复现构建和 SHA256 校验。两个架构归档，原生 arm64 对标准构建执行二进制版本核验、安装/回滚和共享 WebSocket 帧。 [PR Artifact 15257411186](https://github.com/yikkrrtykj/nginx-overseas-cache/actions/runs/46129534676/artifacts/15257411186) · [arm64 工作流](https://github.com/yikkrrtykj/nginx-overseas-cache/actions/runs/46129719492) |

## 剩余边界

- 旧版 Helper 在升级初段中断时可能继续服务既有文件。升级或恢复期间暂停配置操作。受信任 root 的手动 Nginx reload 走系统管理路径，操作手册要求先恢复挂起事务。
- systemd 在安装器仍有效时已接受的试运行作业存在从检查到执行的间隔。启动门禁保护新的激活请求，运行就绪由单独检查确认。
- 主工作流依赖 GitHub-hosted 镜像、上游 Actions 版本标签和扫描库。发布证据保留 checkout SHA、Artifact digest、包 SHA256、版本和可复现构建结果。
- 现场仍需授权验证实际管理 VLAN、ACL、HTTPS 证书和负向访问、Renderer 重连及画面恢复、升级窗口和回退阈值。#5 的小时级实验继续使用已验收结果。

## 候选包和合并记录

精确 PR 构建 `4d7bbe0084ab` 的 [Artifact 15257411186](https://github.com/yikkrrtykj/nginx-overseas-cache/actions/runs/46129534676/artifacts/15257411186) 用于隔离验收。ZIP SHA256 为 `6e22488e555b29e6a150633563c45e4a86c78dd4e11d63dbb05fd40ed574b40a`。amd64 和 arm64 包 SHA256 分别为 `56a75198138494028ab438eaf0047e25d71ca33774424b9390eb3cc5507266f8`、`1fe71feb33bddce98479e0a1a1e85d05cb68032bb0a03bfbd4f8081066df0551`。

当前等待最终合并门禁。合并后从精确 main 构建新的现场候选包并记录对应验证。
