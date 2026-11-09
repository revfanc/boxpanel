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

范围补充：临时 CLI 参数与默认值见 INSTALL.md §3；`--dev` 在本任务仅实现
HTTP、`127.0.0.1:2053`、空 `web_path`，使 `make dev-server` 可运行。
DB、认证、HTTPS 等完整开发模式属于 T1.2；systemctl 接口与 dev mock 随 T2.1 的封装加入。

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

范围补充：路径覆盖、无 root 开发、初始化默认值、会话撤销、CSRF 和超时探测约定见
INSTALL.md §3、SPEC.md §6/§8、API.md 认证部分、CONFIG.md §6。
本任务只做初始化所需的最小 IP 探测，不加入 T1.3 自检/API、防火墙、T2.1 systemd、sing-box 管理，
也不实现 M4 的登录页面。`init` 不生成 systemd 单元；`serve --dev` 不执行任何系统管理命令。

### T1.3 系统自检
- `internal/sysinfo`：OS/架构/虚拟化、公网 IP 探测、端口占用、BBR 检测与开启、`/system/check` 的 `items` 生成；`/system/status`。
- `internal/firewall`：检测 ufw/firewalld/nftables/iptables，`Open(port, proto)`、`Close(...)`，dev 模式为 no-op。
- **验收**：对 `/proc`、命令输出做可注入的抽象，单测覆盖解析逻辑。

范围补充：平台限制、采样单位、BBR 恢复策略、四种防火墙的操作边界见 SPEC.md §9 和
API.md 的 T1.3 约定；双栈 IP 探测见 CONFIG.md §6。不新增依赖，不修改前端。
sing-box 状态保留未安装占位值；启动同步、端口跳跃、setup/port-probe API 仍留在后续任务。

## M2 sing-box 管理

### T2.1 二进制与 systemd
- `internal/singbox/version.go` 锁定版本常量；`boxpanel singbox install|status`；下载、校验、解包到 `/usr/local/share/boxpanel/`。
- systemd 单元生成与 `systemctl` 封装（接口化，dev 模式 mock）。
- **验收**：下载 URL 拼接与架构映射单测。

范围补充：锁定版本 1.14.4（2026-10-24T01:04:25Z，稳定版且已发布超过 7 天）；
官方未提供独立校验文件，核对资产 API 的 SHA-256，并检查 tar 结构与版本输出。
详细校验规则、路径覆盖、安装重试和已有配置时的版本切换限制见 INSTALL.md §4。
新增 `make tools`，将宿主平台二进制安装到忽略的 `.tools/bin/`，`make test` 按存在情况加入 PATH；
CI 在测试前运行 `make tools`。所有单测用注入的下载/命令，无网络/systemctl。
接入已有 `/system/check|status` 的内核状态，不新增 `/singbox/*` API；
不生成配置、不启动服务、不做 T2.2/T2.3 或 T6 安装脚本。

### T2.2 配置生成（服务端）
- `internal/reality`：X25519 密钥、short_id；`internal/certs`：hy2 自签证书与指纹。
- `internal/confgen`：按 CONFIG.md §2 生成，支持 vless_reality、hysteria2；用户过滤规则；输出稳定。
- **验收**：golden test 覆盖：单入站、双入站、无用户、用户超额/过期被剔除；有 sing-box 时 `sing-box check` 通过。

范围补充：服务端字段按 1.14.4 核对，接口、证书指纹编码、过滤边界、DNS/私网拦截修正、
Hysteria2 BBR 语义及测试命令见 CONFIG.md §1/§2。生成器只消费一致快照与显式运行参数，
不接 DB/HTTP/CLI，不落地服务端配置；所有 golden 用临时证书执行锁定内核 check。
Reality 测试实际调用 `sing-box generate reality-keypair` 比较格式；无工具时 Skip 外部命令检查。

### T2.3 生效流程
- SPEC.md §7 完整实现（事务内生成+校验、原子替换、重启、存活检查、回滚、互斥锁、启动时重新生效、防火墙同步）。
- Hysteria2 端口跳跃规则下发/清理。
- `/singbox/restart|logs|config`（脱敏）、`boxpanel apply`。
- **验收**：用 fake runner 覆盖：check 失败 → DB 回滚；重启失败 → 文件与 DB 回滚并再次重启；成功路径。

T2.3 补充验收（实现范围不扩展到 T3）：
- [x] 同一 SQLite 事务内生成/check，保持事务直到生效完成；进程内互斥锁与跨进程 flock。
- [x] 重启/存活失败恢复 .bak 并再次重启；首次失败停止服务；DB 不提交。
- [x] 恢复也失败、文件/防火墙失败、请求取消、并发两次生效、启动重新生效与中断恢复单测。
- [x] 可注入 nft/iptables runner 的 Hysteria2 端口跳跃下发、重放与清理；单测不改宿主规则。
- [x] restart/logs/config API 的认证、CSRF、错误契约与脱敏；CLI apply；不实现 versions/upgrade 或 T3 API。
- [x] .tools/bin/sing-box 真实 check 集成测试（双入站、临时自签证书；无二进制 Skip）。
- 事务选择、启动安装前边界、恢复记录见 SPEC.md §7；运行参数与证书布局见 CONFIG.md §2。
  不新增依赖，不修改前端包清单或锁文件。

## M3 节点、用户、订阅

### T3.1 入站与用户 API
- API.md 中入站、用户、`/inbounds/suggest`、`/reality/*` 全部接口；端口冲突 409；名称校验。
- **验收**：handler 单测（httptest）；每个写操作都触发生效流程（fake）。

T3.1 补充验收与边界：
- [x] 入站/用户 CRUD、启停、用户凭证/token 重置；名称、端口、协议参数校验及 409 冲突。
- [x] 每个持久化写接口调用 Manager.Apply；mutation 只使用传入事务，成功后才返回对象。
- [x] httptest 使用真实 Manager + fake 命令/防火墙验证全部写操作；check 失败逐接口回滚，重启失败恢复 DB/文件。
- [x] 单连接 SQLite 下创建/修改入站和用户、重置凭证有超时约束测试，证明不会等待自身连接而死锁。
- [x] Reality 候选常量、可注入 DNS/TLS/时钟和 handler Probe；离线测试过滤、排序、证书/TLS/ALPN、私网地址限制。
- [x] 非 Linux 正常模式保持 409；serve --dev 显式启用仅 DB 开发替身，响应头与日志标注，不伪装真实生效。
- [x] 认证/CSRF、400/404/405/409/422/500、HEAD、敏感字段及日志脱敏覆盖。
- [x] make lint、make test、make build 通过；make test 使用已有 .tools/bin/sing-box 执行真实配置 check。

规格补齐见 API.md 的 T3.1 约定：PUT 为部分更新，成功/删除状态码明确；suggest 不传私钥，
创建时生成并只返回公钥；开发例外同步 SPEC.md/INSTALL.md。不新增依赖、不改前端包清单或锁文件。
`/users/{id}/links`、订阅、rules/settings 和 setup 均留给后续任务。

### T3.2 分享链接与订阅
- `internal/share`：vless/hysteria2 URI（CONFIG.md §4.5）。
- `internal/rules`：规则源与预设（§3）。
- `internal/subscription`：格式识别、sing-box JSON、mihomo YAML、base64、响应头、浏览器 HTML 页；订阅路由、限速、404 伪装。
- `/users/{id}/links`、`/rules`、`/rules/preview`、`/settings`。
- **验收**：每种格式 × 每种预设的 golden test；UA 识别表驱动测试；sing-box 客户端配置在有 sing-box 时 `check` 通过。

T3.2 补充验收与边界：
- [x] VLESS/Reality、Hysteria2 URI，IPv6、URL 转义、混淆及端口跳跃；所有节点采用 public_host。
- [x] 三预设、双规则源、广告/自定义域名/DNS/FakeIP/IPv6/镜像；mihomo 始终 MetaCubeX mrs。
- [x] 3 格式 × 3 预设 × 3 组合共 27 份 golden；UA/format 优先级、稳定输出、空节点测试。
- [x] 9 份 sing-box golden 逐一通过 .tools/bin/sing-box 1.14.4 check，无网络下载或配置替换。
- [x] 按 1.14 核对 tun、新 DNS、rule-set/HTTP client、route actions、selector/urltest；mihomo 按官方文档使用 yaml.v3 编码。
- [x] 规则 URL 实际 HEAD：14 个不同最终 URL 全部 200；原 SagerNet GFW 404 已按 CONFIG.md 记录并单项回退 MetaCubeX。
- [x] 公开订阅响应头、浏览器本地二维码/导入页面；30 次/IP/分钟、常量时间 token 比较、404 伪装及测试。
- [x] links/rules/preview/settings API，认证/CSRF/HEAD、脱敏、token 重置；settings BT 变更通过 Apply 并测试失败回滚。
- [x] 监听预绑定、失败释放、延迟切换及本地真实 TCP 验证；不改前端依赖、不执行 git 提交/推送/分支切换。
- [x] `make lint`、`make test`、`make build` 全部通过（macOS arm64、Go 1.27.3、CGO_ENABLED=0）；Go 缓存放在可写的 /tmp 下。

字段核对、源可用性缺口、依赖理由、设置契约补齐见 CONFIG.md/API.md；不推进 M4 或后续任务。

### T3.3 端到端回环测试
- 新建 `internal/e2e` Go 测试，使用 `.tools/bin/sing-box`；工具缺失或 `go test -short` 时 Skip，`make test` 默认执行。
- 同一份 DB 快照/模型分别走生产 `confgen` 和 `subscription` 路径，生成服务端与 sing-box 客户端配置。
- 完全离线：在 `127.0.0.1` 启动 TLS 1.3 + h2 的 Reality handshake 目标及返回固定内容的 HTTP 目标。
- 服务端仅在测试中绑定回环随机端口、去掉 `ip_is_private` 拒绝规则；客户端去掉 TUN，保留随机端口 mixed，全部请求路由到被测节点，并禁用外网测速/规则下载。
- **验收**：VLESS Reality、Hysteria2 经真实服务端/客户端和 mixed 代理取回固定内容；Reality 错误 public_key 必须失败。
- **进程验收**：随机端口、等待就绪、启动/请求有超时、所有退出路径清理并回收子进程，失败打印脱敏日志。
- **范围**：只增加测试与相应规格，不改生产配置行为，不涉及 systemd/防火墙、M4 或后续任务，不改 `web/package.json` / `pnpm-lock.yaml`。
- **通用验收**：`make lint`、`make test`、`make build` 全部通过。

T3.3 当前核对结果：
- [x] 同源模型/临时 DB、生产生成器、离线 TLS 1.3 + h2 / HTTP 目标与测试专用配置调整。
- [x] 两协议成功用例、错误 Reality 公钥反例、随机端口/超时/进程回收/失败日志均已实现；无工具及 short 的 Skip 边界明确。
- [x] `make lint`、`make build` 通过；其余 Go 包与前端 typecheck/lint/vitest 通过。
- [ ] 两协议真实连通及错误公钥反例通过：当前 macOS 沙箱内核启动报 DNS 通知注册 `status 9`，尚未执行代理请求，需普通终端/CI 复跑。
- [x] `make test` 全部通过（审查者在沙箱外本机复跑：三个子测试均 PASS）。

## M4 前端面板

### T4.1 基础设施
- api 封装、类型、Query、i18n、主题、AppLayout（含移动端抽屉）、路由守卫、登录页、404。

T4.1 验收与边界：
- [x] API fetch 使用随机 basename、同源 cookie、统一错误/下一步提示、空 204、请求取消及 401 会话失效处理。
- [x] TypeScript 类型对齐真实 handler；TanStack Query 按资源缓存，登录/退出/会话失效清理业务数据。
- [x] 登录使用 react-hook-form + zod，密码按字节校验；loading、失败重试、429 提示、安全 next 回跳。
- [x] history 路由守卫、受保护 404、错误重试；引导接口缺口按 FRONTEND.md 的 T4.1 约定最小兼容。
- [x] 中文文案集中；浅色/深色/跟随系统及持久化、跨标签同步。
- [x] 桌面 Sidebar + 顶栏；移动 Sheet 抽屉、中文无障碍标签、Escape/焦点恢复/导航后关闭。
- [x] shadcn 由 CLI 生成，未手改生成文件；新增依赖理由见 FRONTEND.md，pnpm 冷静期策略保持原值，无豁免。
- [x] 前端 typecheck/lint/vitest 通过；`make lint`、`make build` 通过。
- [ ] `make test` 全部通过：当前 macOS 沙箱的既有 T3.3 TestLoopback 被内核 DNS 通知注册 `status 9` 阻断；其余 Go 包通过，需沙箱外复跑，不修改/跳过该测试。
- [ ] 生产构建浏览器实测 375px 无横向滚动及随机路径深链刷新：jsdom 不提供布局测量，留待审查者用嵌入的生产构建复核。

未实现 T4.2–T4.5 业务 UI 或 T5.1 引导/后端 API；未执行 git commit/push/分支切换。

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
