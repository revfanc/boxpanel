# Codex 任务拆分

按顺序执行，每个任务一个 PR。每个任务的通用验收：`make lint test build` 通过；新增逻辑有单测；文档与实现一致。

**给 Codex 的提示词模板：**

> 阅读 AGENTS.md 和 docs/ 下全部规格，实现 docs/TASKS.md 中的任务 **Tx.y**。只做这一个任务，完成后逐条核对验收标准，在 PR 描述中列出勾选结果和未决问题。

---

## M0 骨架

### T0.1 仓库骨架
- Go module `github.com/revfanc/boxpanel`，`cmd/boxpanel/main.go`（子命令解析用标准库 `flag` 或轻量手写分发），`version` 命令。
- `web/`：Vite + React + TS + Tailwind v4 + shadcn/ui 初始化，`components.json`，路径别名 `@/`。
- `web/embed.go` + `web/dist/.gitkeep`；Makefile 目标见 AGENTS.md；`.github/workflows/ci.yml`。
- **验收**：`make build` 产出 `bin/boxpanel`；`bin/boxpanel version` 输出版本。

范围说明：此阶段保留 `make dev-server` 目标，但不实现 `serve`（T0.2）或完整
`--dev` 模式（T1.2）；CI 暂只运行现有代码的 lint/test/build 与 version 检查，
sing-box 校验和安装脚本检查在对应任务加入，见 INSTALL.md §5。

### T0.2 SPA 服务与 history 回退
- 实现 `internal/spa`（FRONTEND.md §1 全部规则）与最小 `serve` 命令（临时用命令行参数指定 web_path、端口，HTTP）。
- 前端放 3 个占位路由（`/`、`/users`、`/users/:id`），用 `createBrowserRouter` + 运行时 basename。
- **验收**：Go 单测覆盖 FRONTEND.md §1 的 5 条规则；手动：生产构建后直接访问 `/{path}/users/3` 并刷新，页面正常、资源加载 200。

## M1 后端基础

### T1.1 DB 与迁移
- `internal/db`：打开 SQLite（WAL、`foreign_keys=on`、`busy_timeout`），embed 的 `migrations/*.sql` 顺序执行并记录版本；SPEC.md §6 全部表。
- settings 的类型化读写封装。
- **验收**：迁移幂等；单测用临时文件 DB。

### T1.2 init / 认证 / 会话
- `boxpanel init`、`info`、`reset-admin`（INSTALL.md §3）；随机 web_path/sub_path/端口；自签面板证书（SAN 含公网 IP）；HTTPS 监听。
- `internal/auth`：argon2id、会话表、cookie 属性、登录限流、CSRF 校验（SPEC.md §8）。
- API：`/auth/*`；中间件：认证、请求日志、panic 恢复、统一错误格式。
- `serve --dev` 模式。
- **验收**：认证与限流单测；`init` 重复执行被拒绝。

### T1.3 系统自检
- `internal/sysinfo`：OS/架构/虚拟化、公网 IP 探测、端口占用、BBR 检测与开启、`/system/check` 的 `items` 生成；`/system/status`。
- `internal/firewall`：检测 ufw/firewalld/nftables/iptables，`Open(port, proto)`、`Close(...)`，dev 模式为 no-op。
- **验收**：对 `/proc`、命令输出做可注入的抽象，单测覆盖解析逻辑。

## M2 sing-box 管理

### T2.1 二进制与 systemd
- `internal/singbox/version.go` 锁定版本常量；`boxpanel singbox install|status`；下载、校验、解包到 `/usr/local/share/boxpanel/`。
- systemd 单元生成与 `systemctl` 封装（接口化，dev 模式 mock）。
- **验收**：下载 URL 拼接与架构映射单测。

### T2.2 配置生成（服务端）
- `internal/reality`：X25519 密钥、short_id；`internal/certs`：hy2 自签证书与指纹。
- `internal/confgen`：按 CONFIG.md §2 生成，支持 vless_reality、hysteria2；用户过滤规则；输出稳定。
- **验收**：golden test 覆盖：单入站、双入站、无用户、用户超额/过期被剔除；有 sing-box 时 `sing-box check` 通过。

### T2.3 生效流程
- SPEC.md §7 完整实现（事务内生成+校验、原子替换、重启、存活检查、回滚、互斥锁、启动时重新生效、防火墙同步）。
- Hysteria2 端口跳跃规则下发/清理。
- `/singbox/restart|logs|config`（脱敏）、`boxpanel apply`。
- **验收**：用 fake runner 覆盖：check 失败 → DB 回滚；重启失败 → 文件回滚；成功路径。

## M3 节点、用户、订阅

### T3.1 入站与用户 API
- API.md 中入站、用户、`/inbounds/suggest`、`/reality/*` 全部接口；端口冲突 409；名称校验。
- **验收**：handler 单测（httptest）；每个写操作都触发生效流程（fake）。

### T3.2 分享链接与订阅
- `internal/share`：vless/hysteria2 URI（CONFIG.md §4.5）。
- `internal/rules`：规则源与预设（§3）。
- `internal/subscription`：格式识别、sing-box JSON、mihomo YAML、base64、响应头、浏览器 HTML 页；订阅路由、限速、404 伪装。
- `/users/{id}/links`、`/rules`、`/rules/preview`、`/settings`。
- **验收**：每种格式 × 每种预设的 golden test；UA 识别表驱动测试；sing-box 客户端配置在有 sing-box 时 `check` 通过。

## M4 前端面板

### T4.1 基础设施
- api 封装、类型、Query、i18n、主题、AppLayout（含移动端抽屉）、路由守卫、登录页、404。

### T4.2 概览 / 日志 / 设置
- 概览卡片（5 秒刷新）、日志页、设置页（改端口/路径后整页跳转到新地址）。

### T4.3 节点
- 列表、新建（协议卡片 → 自动预填 → 高级折叠）、编辑、启停、删除确认；Reality 目标推荐与自定义检测。

### T4.4 用户与订阅中心
- 用户列表（流量进度、到期、状态）、新建/编辑、订阅中心（二维码、复制、一键导入、各格式链接、单节点 URI、重置 token 确认）。

### T4.5 分流规则
- 预设单选卡片（附一句话说明各自适合谁）、去广告、自定义域名（标签输入）、DNS、规则源与镜像、预览抽屉。

**M4 验收**：`pnpm typecheck lint` 通过；375px 宽度下所有页面无横向滚动；所有写操作有 loading 与错误提示。

## M5 引导流程

### T5.1 引导
- FRONTEND.md §4 全部 7 步；进度持久化；强制引导与跳过；`clients.ts` 数据；安全组端口自测接口（后端 TCP 自连探测，新增 `POST /system/port-probe {port}`，同步更新 API.md）。
- **验收**：全新 dev DB 下从登录到 finish 一路「下一步」即可得到可用订阅。

## M6 安装与发布

### T6.1 install.sh
- INSTALL.md §2 全部行为；`shellcheck` 通过。
- **验收**：在 Debian 12、Ubuntu 24.04 的干净 VPS/容器（systemd）上安装 → 打开面板 → 走完引导 → 客户端导入可连通；`update`、`uninstall` 可用。

### T6.2 发布流水线
- `release.yml`（INSTALL.md §5）。
- **验收**：推 `v0.1.0-rc.1` tag 产出 Release，install.sh 可用其安装。

## M7 后续增强（MVP 之后，按需排期）

- T7.1 每用户流量统计（评估 v2ray_api 统计是否在官方构建中可用；否则基于 clash_api 连接统计近似）、超额/到期自动禁用（定时任务 + 生效流程）
- T7.2 ACME 证书（域名场景），面板与 hy2 共用；新增 Trojan/VLESS-WS+TLS（可套 CDN）
- T7.3 更多协议：TUIC v5、AnyTLS、ShadowTLS
- T7.4 TOTP 两步验证
- T7.5 备份/恢复
- T7.6 面板内升级 sing-box（新版本先 check 现有配置）
- T7.7 英文界面
- T7.8 多服务器（agent 模式）
