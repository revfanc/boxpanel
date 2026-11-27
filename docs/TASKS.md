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
- [x] （审查者已在沙箱外复跑 / 浏览器实测通过）两协议真实连通及错误公钥反例通过：当前 macOS 沙箱内核启动报 DNS 通知注册 `status 9`，尚未执行代理请求，需普通终端/CI 复跑。
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
- [x] （审查者已在沙箱外复跑 / 浏览器实测通过）`make test` 全部通过：当前 macOS 沙箱的既有 T3.3 TestLoopback 被内核 DNS 通知注册 `status 9` 阻断；其余 Go 包通过，需沙箱外复跑，不修改/跳过该测试。
- [x] （审查者已在沙箱外复跑 / 浏览器实测通过）生产构建浏览器实测 375px 无横向滚动及随机路径深链刷新：jsdom 不提供布局测量，留待审查者用嵌入的生产构建复核。

未实现 T4.2–T4.5 业务 UI 或 T5.1 引导/后端 API；未执行 git commit/push/分支切换。

### T4.2 概览 / 日志 / 设置
- 概览卡片（5 秒刷新）、日志页、设置页（改端口/路径后整页跳转到新地址）。

- [x] 系统/资源卡片 5 秒刷新、快捷入口、空资源提示；开发环境不可用状态及重启确认。
- [x] 最近 200 行日志、自动刷新开关、空状态、409 提示与其他错误重试。
- [x] 设置及账号表单；访问地址变更确认、按后端 panel_url/restart_after 整页跳转；账号变更后重新登录。
- [x] 逐页组件测试，包含轮询/停止、加载、成功、错误、空输入与破坏性操作确认。

### T4.3 节点
- 列表、新建（协议卡片 → 自动预填 → 高级折叠）、编辑、启停、删除确认；Reality 目标推荐与自定义检测。

- [x] 节点卡片列表、协议选择、suggest 预填、高级参数、编辑、启停与删除确认。
- [x] Reality 推荐/手动目标/自定义检测；Fake-IP 导致检测失败时仍可手填并提交。
- [x] 编辑保持私钥和未修改的混淆密码；不把脱敏凭证回写到后端。
- [x] 列表与新建/编辑组件测试，包含加载、空列表、缺失记录、保存/删除失败及取消确认。

### T4.4 用户与订阅中心
- 用户列表（流量进度、到期、状态）、新建/编辑、订阅中心（二维码、复制、一键导入、各格式链接、单节点 URI、重置 token 确认）。

- [x] 用户列表、流量进度/到期/状态、新建/编辑、GiB 与 Unix 秒换算，启停/删除确认。
- [x] 本地二维码、复制与失败降级、两种导入深链、四种订阅地址及单节点 URI。
- [x] token/连接凭证重置确认，成功后清除旧链接重新获取；空节点/不可用用户/缺证书均有提示。
- [x] 列表、编辑对话框、订阅详情组件测试，包含写入 loading/失败与旧 token 缓存清理。

### T4.5 分流规则
- 预设单选卡片（附一句话说明各自适合谁）、去广告、自定义域名（标签输入）、DNS、规则源与镜像、预览抽屉。

- [x] 三预设及适用说明、去广告、域名标签、DNS/FakeIP/IPv6、规则源/镜像/更新间隔。
- [x] 保存完整规则，纳入未提交的域名草稿；只预览已保存规则，JSON/YAML 抽屉与空用户/证书错误提示。
- [x] 页面组件测试覆盖加载、空标签/空预览、保存成功/失败、输入校验、预览切换和关闭焦点恢复。

T4.2–T4.5 本次统一验收（按用户要求一并交付四项）：
- [x] 前端 `pnpm typecheck`、`pnpm lint`、`pnpm test` 通过（15 个测试文件、78 个测试）。
- [x] `make lint` 通过；Go 使用 `/opt/homebrew/bin/go`，GOCACHE 放在可写 `/tmp` 下，CGO 保持关闭。
- [x] `make build` 通过，产出嵌入前端的 `bin/boxpanel`。
- [x] （审查者已在沙箱外复跑 / 浏览器实测通过）`make test` 全部通过：既有 T3.3 TestLoopback 在当前 macOS 沙箱中启动内核时，
  DNS 通知注册报 `status 9`；其余 Go 包通过。未跳过/修改该测试，须在普通终端或 CI 复跑。
- [x] （审查者已在沙箱外复跑 / 浏览器实测通过）375px 与桌面无横向滚动、嵌入生产构建深链刷新实测：已按窄屏布局实现，需审查者浏览器复核。
- [x] 未实现 T5.1 或后续任务；未运行 Vite dev server；未执行 commit/push/切换分支。

规格缺口的最小处理、依赖理由及具体交互见 FRONTEND.md 的 T4.2–T4.5 约定；
修改密码 401 的区分补充在 API.md。未修改 Go 后端、配置生成或生效流程。

**M4 验收**：`pnpm typecheck lint` 通过；375px 宽度下所有页面无横向滚动；所有写操作有 loading 与错误提示。

## M5 引导流程

### T5.1 引导
- FRONTEND.md §4 全部 7 步；进度持久化；强制引导与跳过；`clients.ts` 数据；安全组端口自测接口（后端 TCP 自连探测，新增 `POST /system/port-probe {port}`，同步更新 API.md）。
- **验收（修正开发环境冲突）**：全新 dev DB 从登录可走完全部 7 步；Reality 检测失败可手填目标。
  默认双协议在 dev 因没有 Hysteria2 证书，finish 显示明确的暂不可用和后续操作，仍可完成引导。
  仅 Reality 可得到可导出的订阅（不宣称 dev 节点可连接）；生产订阅需成功 Apply 后验证。

T5.1 核对结果：
- [x] 七步引导、独立布局、history/basename、后端进度/场景持久化、强制进入/跳过/每步返回。
- [x] setup GET/PUT、TCP port-probe；认证/CSRF/严格 JSON/统一错误、3 秒超时、只连本机公网地址。
- [x] 域名 A/AAAA 全部匹配本机公网 IP；可注入 DNS/TCP/公网探测，网络单测离线。
- [x] 复用 T4 节点/用户表单及原有 Apply；手动 Reality、双入站部分失败及进度失败可恢复。
- [x] 防火墙端口/协议/UDP 跳跃清单、厂商折叠说明占位、自连限制；finish QR/复制/五平台导入。
- [x] 官方来源 clients.ts、dev 缺证书说明；无新依赖或供应链例外，不提前实现后续任务。
- [x] 组件测试覆盖强制引导、跳过、刷新续接与每步返回，另覆盖写入失败/loading、域名和订阅错误。
- [x] `make lint`、`make build` 通过；前端 typecheck/lint/vitest 通过（16 文件、94 测试）。
- [x] 真实初始化的全新 dev DB handler 集成测试通过：七步、手填 Reality、双协议缺证书 409、
  完成状态持久化、仅 Reality 可导出；全部新网络单测使用注入替身。
- [x] 构建后二进制 + 全新临时 dev DB HTTP 实测通过：登录、七步保存、预期 409、完成，
  停用 Hysteria2 后公开 sing-box JSON 可导出；嵌入 `/setup/finish` 深链与资源返回 200。
- [x] （审查者已在沙箱外复跑 / 浏览器实测通过）`make test` 全部通过：既有 T3.3 TestLoopback 仍被当前 macOS 沙箱的
  `watch DNS configuration: register DNS configuration notification: status 9` 阻断；
  其余 Go 包均通过。未跳过或修改该测试，须普通终端/CI 复跑。
- [x] （审查者已在沙箱外复跑 / 浏览器实测通过）生产构建浏览器可视化实测：当前浏览器工具返回 `The browser access host is unavailable`，
  尚未实测布局和浏览器刷新；HTTP 深链和组件 history 导航验证不能代替这项。

本次仅 T5.1；未执行 git commit/push/切换分支。验收原文的 dev“立即可用订阅”与
不生成 Hysteria2 证书的既有约定冲突，按用户补充修正；客户端与 DNS 接口补齐说明见
FRONTEND.md/API.md。无新增依赖、未修改配置生成或 Apply 实现。


## M6 安装与发布

### T6.1 install.sh
- INSTALL.md §2 全部行为；`shellcheck` 通过。
- **验收**：在 Debian 12、Ubuntu 24.04 的干净 VPS/容器（systemd）上安装 → 打开面板 → 走完引导 → 客户端导入可连通；`update`、`uninstall` 可用。

T6.1 本次实现与核对（按用户要求与 T6.2 一并交付）：
- [x] install/update/uninstall、支持系统/架构/root/systemd 检查、依赖安装、非交互参数和明确的卸载确认。
- [x] 固定 tag 下载、checksums.txt SHA-256、单普通文件 tar/版本检查、原子替换及更新失败恢复旧二进制。
- [x] 重复执行、安装中断续接、卸载默认保留/显式清除、保留数据后恢复同版本内核。
- [x] shell 仅编排 CLI；新增 system firewall-open/firewall-cleanup/bbr/ready 与 singbox install --preserve-version，复用既有 Apply/防火墙/BBR，实现与失败分支有 Go 测试。
- [x] 生产启动重放已登记面板端口；卸载清理当前及 pending 中的规则，不绕过校验写配置，不提前做内核升级。
- [x] macOS Bash 离线测试覆盖安装/升级/卸载、重试、坏下载/摘要/包、平台/参数、就绪失败和 BBR/互斥锁；已接入 make test。
- [x] 本机官方 ShellCheck 0.11.0（下载经官方资产 SHA-256 校验）检查全部 scripts/*.sh 无警告；Linux CI 强制检查。
- [x] （审查者真机验收）Ubuntu 22.04 双栈 VPS（207.57.124.128）：一键安装、systemd、Let's Encrypt IP/域名证书、浏览器引导、iPhone mihomo 1.19.33 连通（Google/YouTube）与 ipleak 无 DNS 泄漏均通过；发现的问题已在 T6.4/T6.5 修复。

### T6.2 发布流水线
- `release.yml`（INSTALL.md §5）。
- **验收**：推 `v0.1.0-rc.1` tag 产出 Release，install.sh 可用其安装。

- [x] v* tag 触发；构建前端与 CGO_ENABLED=0 的 linux/amd64、linux/arm64，注入 tag，生成两个 tar.gz 与 checksums.txt。
- [x] draft → 上传完整资产 → 发布；rc 标记 prerelease；权限限定、同 tag 串行、失败 draft 可续接。
- [x] Action 使用发布超过 7 天的稳定版本并锁定完整 SHA；原 pnpm/action-setup v6.1.1 未满 7 天，改用 v6.1.0，来源/日期见 INSTALL.md。
- [x] 本机执行发布打包脚本（v0.1.0-rc.1），两架构静态 ELF、tar 清单及摘要核对通过；ci.yml/release.yml 通过 actionlint。
- [x] CI/release 增加 Ubuntu 24.04 容器 dry-run、离线脚本测试和真实 Linux CLI init/info；本机未执行容器步骤，不将其当作 systemd 实机验收。
- [x] （审查者）v0.1.0-rc.2～rc.4 由 tag 构建 Release，并在 VPS 上用一键命令安装/升级成功；rc.1 因 ShellCheck 版本失败，已修复。

T6.1/T6.2 通用验证：
- [x] make lint、make build 通过；新增 Go 测试及其余 Go 包通过，前端 typecheck/lint/vitest 通过（16 文件、94 测试）。
- [x] （审查者沙箱外复跑通过）make test 全部通过：已执行，既有 internal/e2e TestLoopback 被当前 macOS 沙箱的
  `watch DNS configuration: register DNS configuration notification: status 9` 阻断；未跳过/修改该测试或替换内核，需普通终端/CI 复跑。
- [x] 未修改 web/package.json、锁文件或 pnpm 冷静期；无新增项目依赖，未实现 M7 或执行 git 写操作。

规格补齐：非交互卸载 --yes/--purge、dry-run 边界、重试/保留数据重装、面板端口持久重放、
下载强制 SHA-256、update 的正常启动生效与二进制回退边界，均见 INSTALL.md；新增设置键见 SPEC.md §12。

### T6.3 可信证书（Let's Encrypt）+ 引导体验修复

**背景**：面板与订阅目前只有自签 HTTPS，sing-box / mihomo 客户端拉取订阅会因证书不受信而失败——这是 MVP 阻断问题。
Let's Encrypt 自 2026-01-15 起正式签发 IP 地址证书（必须使用 `shortlived` profile，有效期约 160 小时），
见 <https://letsencrypt.org/2026/01/15/6day-and-ip-general-availability>。

**证书（必须）**
1. 新增 `internal/acme`：为 public_host 申请证书。public_host 为 IP → 请求 IP 标识符 + `shortlived` profile；
   为域名 → 默认 profile（`classic`）。仅用 **HTTP-01**（80 端口；443 常被 Reality 占用，不用 TLS-ALPN-01）。
   挑战监听器只在签发/续期期间临时监听 :80，结束即关闭；80 被占用时直接失败并给出下一步提示。
2. 依赖选择：先查 `golang.org/x/crypto/acme` 当前版本是否支持 ACME Profiles 与 IP 标识符；支持则用它（已是依赖），
   不支持再引入 `github.com/go-acme/lego/v4`（遵守冷静期，PR 描述写明理由）。不得手写 JWS。
3. `panel_tls_mode` 取值扩展为 `self_signed | acme`。新安装默认尝试 `acme`；签发失败**自动回退自签**并记录原因，
   不阻断安装/启动。ACME 账号密钥与证书保存在 `<data-dir>/certs/`（0600），切换不覆盖原自签证书文件。
4. 续期：后台定时任务，按实际证书有效期计算已用寿命 ≥ 1/2（160 小时证书为 80 小时，90 天证书为 45 天）时续期，失败指数退避重试
   （上限 6 小时），连续失败且证书剩余 < 24h 时在面板显著提示。证书通过 `tls.Config.GetCertificate` 热替换，**不重启进程**。
5. 面板设置页：显示当前证书类型、签发者、到期时间、上次续期结果；提供「申请/重新申请可信证书」与「改回自签」操作；
   API 见 API.md（新增 `GET /api/cert`、`POST /api/cert/issue`、更新 settings 的 panel_tls_mode 校验），全部走现有认证/CSRF。
6. 防火墙/引导：acme 模式下本机防火墙放行 TCP 80，引导防火墙步骤与云安全组提示列出 80；完成页根据证书类型
   决定是否显示「浏览器警告」说明。public_host 变更后自动重新签发。
7. 安装脚本：`boxpanel init` 之后（服务启动前或启动后由服务自行）尝试签发；安装结束输出里说明证书类型；
   `system ready` 本机检查必须同时适配 LE 证书与自签证书（不得用 insecure）。
8. 测试：ACME 客户端可注入 directory URL 与时钟；单测用假 ACME 服务器/接口注入，不访问外网；
   若 PATH 中有 `pebble` 可加集成测试，否则 `t.Skip`。生产默认 directory 为 LE 正式环境，支持
   `--acme-directory` / `BOXPANEL_ACME_DIRECTORY` 覆盖为 staging。`serve --dev` 不签发。
9. Hysteria2 证书保持自签+指纹（本任务不改）。

**体验修复（一起做）**
10. `/api/inbounds/suggest`：Reality 目标检测全部失败时不要整体 409——仍返回推荐端口等其余字段，
    `server_name` 置空并带 `hint`，前端预填端口、让用户手填 server_name。
11. 引导「节点」步骤同一错误显示了两个错误框，去重为一个。
12. 「同时创建 Hysteria2」复选框的可访问名称是 `on`，修正为与可见文案一致（用 label/htmlFor 或 aria-labelledby）。
13. 防火墙步骤的云厂商指引：把占位文案「图文教程链接待补充」换成常见厂商**官方文档**中安全组/防火墙入站规则页面链接
    （至少：阿里云、腾讯云、AWS、Google Cloud、Azure、Oracle Cloud、Vultr、DigitalOcean、Linode/Akamai、搬瓦工），
    链接必须逐个核实可访问且为官方域名；无法核实的厂商不要列。

- **验收**：
  - [x] make lint test build 通过（审查者沙箱外复跑，含 e2e）；新增单测覆盖签发成功、80 被占用、签发失败回退自签、续期时机、热替换、域名/IP 两种 profile。
  - [x] 面板设置页可见证书状态与操作；acme 失败时有可执行的下一步提示。
  - [x] suggest 在检测失败时返回端口；节点步骤单一错误框；复选框可访问名正确；厂商链接均为官方且可访问。
  - [x] SPEC/API/INSTALL/FRONTEND 文档同步更新。

**T6.3 实现与验证记录（2026-11-09）**：
- 复用 x/crypto/acme，升级至 v0.58.0 获得 profiles 支持，最低 Go 版本随上游升至 1.26；
  未引入 lego、前端新依赖或改变 pnpm 冷静期。IP shortlived、域名默认 profile，仅 HTTP-01。
- 实现异步首次签发、自签回退、实际半寿命续期、持久化指数退避、临期告警与 TLS 热替换；
  认证/CSRF API、设置/引导、CLI/systemd、TCP 80 管理及本机验证 readiness 已接入。
  主机/策略改变取消旧订单，激活前复核；手动请求与后台共享互斥，避免重复签发。
- 假 CA/注入测试覆盖签发、DNS/IPv4/IPv6 profile/SAN、80 占用、失败回退、半寿命、退避上限、
  重启恢复、设置变更与取消、证书热替换、TLS 验证、权限与敏感信息保护；未访问 LE 正式环境。
- `make lint`、`make build` 通过；`make test` 已实际执行，安装脚本测试和其余 Go 包通过，
  仅 `internal/e2e.TestLoopback` 因 macOS 沙箱 DNS 通知 `status 9` 失败，未修改或跳过该测试。
  因该失败导致 make 提前停止，已单独执行前端 typecheck、lint、vitest：17 文件 / 99 测试通过。
  第一项总验收保留未勾选，需在允许运行 sing-box 的环境复验完整 `make test`。
- 十家官方链接已逐页核实；搬瓦工没有找到独立云安全组教程，使用官方 iptables 排障并明确标注，
  具体链接及规格缺口见 FRONTEND.md §7。更正原“classic 约 30 天”半寿命示例，统一按实际有效期计算。
- 本次仅 T6.3，Hysteria2 仍自签+指纹；未提前实现 M7。

### T6.4 真机验收修复（207.57.124.128，Ubuntu 22.04 双栈）

真机安装 v0.1.0-rc.2 结果：安装、sing-box 生效、**Let's Encrypt 正式 IP 证书签发均成功**（YE2，约 160h）。发现问题：

1. **公网 IP 优先选了 IPv6**：`api64.ipify.org` 在双栈机器返回 IPv6，面板地址/节点地址/证书都变成
   `2001:df1:7880:6::18e`，国内多数家宽没有 IPv6，小白第一步就打不开面板。
   - 修复：所有公网 IP 探测（init、setup 环境检查、`/system/public-ip`、端口探测本机 IP 列表等）**优先 IPv4**：
     先用强制 IPv4 的来源（例如 `https://api.ipify.org`、`https://ipv4.icanhazip.com`，HTTP 客户端拨号限定 `tcp4`），
     全部失败再回退 IPv6 来源。环境检查同时展示 v4/v6（如已有）。
   - 单测覆盖：双栈返回 v4；仅 v6 时回退 v6；v4 来源超时时的回退。
2. **`boxpanel info` 证书文案误导**：ACME 证书生效时固定输出「staging 证书不受公开信任」。
   - 修复：显示签发者与到期时间（本地时区无关，用 UTC 或 RFC3339），只有当前 ACME directory 不是 LE 正式环境时才提示 staging。
3. **缺少不登录面板修改节点地址的途径**：探测错了用户无法进面板。
   - 新增 CLI `boxpanel set-host <IP|域名>`（支持 `--data-dir/--db-path` 等现有路径参数）：复用 settings API 的校验
     （rules.ValidHost 等），写入 public_host；ACME 模式下证书 worker 会在下一轮检测到变化并重新签发
     （若进程内无法通知，则输出「执行 systemctl restart boxpanel 立即生效」并在 systemd 下自动重启 boxpanel 服务，
     失败时只提示不报错退出）。完成后打印新的面板地址。INSTALL.md 记录用法；install.sh 结尾提示里说明地址不对时可用该命令。

- **验收**：
  - [x] make lint test build 通过（审查者沙箱外复跑，含 e2e）；新增单测覆盖上述三项。
  - [x] 文档同步（INSTALL.md CLI 一节、API.md 如涉及）。

**T6.4 实现与验证记录（2026-11-09）**：
- [x] init 与系统检查/重新探测/域名校验/端口探测共享 IPv4 优先路径；专用来源、`tcp4/tcp6`、
  无代理/重定向、每族多数、3 秒总窗口内保留 IPv6 回退预算；环境检查展示已探测到的双栈地址。
- [x] 离线 HTTP 客户端/拨号器测试覆盖双栈 IPv4 优先、IPv6-only、IPv4 超时/冲突回退、
  错误地址族、取消与短父 deadline；系统检查保留双栈，端口探测列表 IPv4 在前。
- [x] info 输出签发者和 UTC RFC3339 到期时间；仅已记录的非正式 ACME 来源提示 staging/自定义 CA，
  不受 CLI shell 的 directory 环境变量影响；覆盖正式/staging/自定义/来源缺失/自签回退。
- [x] set-host 复用 rules.ValidHost 与既有路径参数/环境变量，原子更新 public_host、打印新 URL；
  提交后才尝试重启 boxpanel，失败仅提示、保留成功保存；开发/非 Linux 不执行系统命令。
  测试覆盖 IPv4/IPv6/域名、参数前后位置/覆盖、非法输入、缺失 DB、写入失败及重启失败；
  证书 worker 另用独立 DB 连接验证无需进程内通知即可在下一轮为新主机签发。
- [x] 安装结束及重复安装提供 set-host 提示；INSTALL/API/CONFIG/FRONTEND 同步。
  超时预算、签发来源缺失与跨进程通知缺口的最小处理已在对应规格中说明。
- [x] make lint（含 ShellCheck）、make build 通过；前端 typecheck/lint/vitest 通过（17 文件 / 103 测试）。
  构建后二进制用全新临时 DB 离线验证 init → set-host（IPv4/域名）→ info，地址持久化及参数位置正常。
- [x] （审查者沙箱外复跑通过）完整 make test：已执行，安装脚本及其余 Go 包通过；仅既有 internal/e2e.TestLoopback
  被 macOS 沙箱的 `watch DNS configuration: register DNS configuration notification: status 9` 阻断。
  未修改或跳过该测试，总验收保持未勾选，需普通终端/CI 复验；本机未做 Linux/systemd VPS 实测。
- [x] 仅 T6.4，无新依赖，无配置生成/Apply 流程改动；未修改内核回环测试、pnpm 冷静期或锁文件，
  未执行 git commit/push/切换分支。

### T6.5 客户端 DNS 防泄漏 + Clash 兼容（真机反馈）

真机反馈：iPhone 上名为「Clash」的 App 导入订阅后「连不上」。服务端抓包确认 VLESS Reality/Hysteria2 握手成功且有数据往返，
服务器上用同一订阅跑 sing-box 客户端两个节点均可访问 Google。判断问题在 mihomo 订阅的 DNS：
`redir-host` + `nameserver: https://1.1.1.1/dns-query` **直连**，国内网络常被干扰，表现为测速通、网页打不开；
同时外网域名的 DNS 查询从用户真实 IP 发出，属于 DNS 泄漏。

**mihomo / Clash YAML（必须）**
1. `dns.enhanced-mode: fake-ip`，`fake-ip-range: 198.18.0.1/16`，`fake-ip-filter` 至少含 `*.lan`、`*.local`、`localhost.ptlink`、
   常见 NTP/系统连通性检测域名（如 `time.*.com`、`+.msftconnecttest.com`、`+.msftncsi.com`、`captive.apple.com`），
   以及 `rule-set:geosite-cn`（仅在客户端支持时；不支持就不加，以兼容性为准，写明依据）。
2. 默认 `nameserver` 改为国内 DoH（`https://223.5.5.5/dns-query`、`https://doh.pub/dns-query`）只用于直连域名；
   **外网域名不在本地解析**：fake-ip 下按域名交给代理，由服务端 resolve。
   `nameserver-policy` 保留 geosite-cn → 国内 DoH；如需远端 DNS（例如 `geolocation-!cn`），必须走代理
   （`https://1.1.1.1/dns-query#节点选择` 语法，或 `respect-rules: true` + `proxy-server-nameserver`，按 mihomo 官方文档核实并引用）。
3. 所有 IP 类规则（`IP-CIDR`、`RULE-SET,geoip-cn`）加 `no-resolve`，避免为匹配规则在本地解析外网域名。
4. `rule-providers` 改用通用格式：`format: yaml`（`.yaml` 文件，MetaCubeX meta-rules-dat 同路径已存在，已核实 200），
   不再输出 mihomo 专有的 `mrs`，以兼容 Stash 等非 mihomo 客户端；仍通过 `proxy: 节点选择` 下载。
5. `dns.ipv6: false` 保持；`ipv6: false` 保持。
6. 用户选择的规则预设（全局/绕过大陆/仅 GFW）都要满足上述防泄漏规则；gfw_only 预设下非 GFW 域名直连属于预期。

**sing-box JSON（核对 + 测试）**
7. 现有设计（hijack-dns；外网 dns-remote 经 proxy；geosite-cn 走 dns-local；AAAA 回空）保持；补测试断言：
   dns-remote 的 detour 必须是代理出站、final 是 dns-remote（gfw_only 除外）、存在 hijack-dns 规则。

**诊断**
8. 公开订阅请求成功时记录一条 INFO 日志：格式（singbox/clash/uri）、User-Agent（截断 128 字符，去控制字符）、
   用户 id；**不得记录 token、完整路径或客户端 IP 以外的敏感信息**（客户端 IP 也不要记）。

**测试与文档**
9. golden 全部更新；新增「防泄漏」单测：解析生成的 YAML/JSON，断言：无直连的境外 DNS 服务器；IP 规则都有 no-resolve；
   fake-ip 开启；rule-provider 不是 mrs。若 PATH 有 sing-box 仍跑 check。
10. CONFIG.md §4.4 示例与说明同步，并附 mihomo DNS 文档链接。

- **验收**：
  - [x] make lint test build 通过（审查者沙箱外复跑，含 e2e）；golden 与新单测覆盖上述各项。
  - [x] 文档同步。

**T6.5 实现与验证记录（2026-11-09）**：
- [x] 1：Clash 固定 Fake-IP、198.18.0.1/16，包含 LAN/local/NTP/连通性检测例外；
  bypass_cn 含 rule-set:geosite-cn。mihomo 与当前 Stash 官方文档均确认集合 filter 支持，依据见 CONFIG.md §4.4。
- [x] 2：默认阿里/腾讯 DoH；国内域名和节点地址使用本地 DNS；远端 DNS 配合 respect-rules、
  proxy-server-nameserver 及置顶端点路由走代理。补 Stash 对应 follow-rule，不依赖未确认的 URL 后缀语法。
  自定义本地 DNS、同主机 local/remote 冲突、Fake-IP 与非地址查询边界在 CONFIG.md 明确。
- [x] 3：所有 IP-CIDR/IP-CIDR6/geoip-cn 规则均含 no-resolve，包括新增 DNS 端点 IP 规则。
- [x] 4：Clash 五种规则源全部为 MetaCubeX YAML，实际 HEAD 均为 200；保持代理下载与镜像前缀。
- [x] 5：Clash 顶层和 DNS IPv6 固定 false；原开关继续控制 sing-box，规则页文案注明区别。
- [x] 6：三种预设保持语义，gfw_only 非 GFW 默认直连；空节点远端 DNS/代理流量拒绝，无自动直连兜底。
- [x] 7：sing-box 生成逻辑未修改；测试断言远端代理 detour、final、hijack-dns、国内 DNS 与 AAAA 回空。
- [x] 8：成功 GET/HEAD 一条 INFO，含实际格式、128 字符以内的清理后 UA、用户 ID；
  测试覆盖格式覆盖、Unicode/控制字符、凭证/路径/IP 反射脱敏及失败不记成功。HTML 格式缺口补为 html。
- [x] 9：重新生成全部 27 份 golden（9 份 Clash 内容变化，JSON/URI 保持）；9 份 sing-box 实际 check 通过。
  解析测试覆盖三预设、默认/选项/自定义 DNS/IPv6 DNS/共享 DNS/空节点，以及 IP no-resolve、YAML provider 和 DNS 出站。
- [x] 10：CONFIG §4.4 示例、规则源表和官方依据同步；API 日志契约、SPEC 规则格式、FRONTEND 开关说明同步。
- [x] make lint、make build 通过；安装脚本及除 e2e 外全部 Go 包通过；前端 typecheck/lint/vitest 通过（17 文件 / 103 测试）。
- [x] （审查者沙箱外复跑通过）完整 make test 已运行，唯一失败为既有 internal/e2e.TestLoopback 的
  `watch DNS configuration: register DNS configuration notification: status 9`；未修改或跳过该测试。
  需普通终端/CI 复验，并在反馈问题的 iOS 客户端更新订阅后复验；本机没有该 App 的实机验证结果。
- [x] 仅 T6.5，无新依赖、无供应链设置变动、无服务端 Apply 改动，未执行 commit/push/切换分支。

### T6.6 订阅分享页美化 + 地址变更提示（v0.1.0 发布前）

真机验收已通过（iPhone mihomo 1.19.33：Google/YouTube 可用，ipleak 无 DNS 泄漏）。发布正式版前最后两项。

**A. 订阅分享页（浏览器打开订阅链接时的 HTML，`internal/subscription/page.go`）**
目标：视觉与面板一致（shadcn/ui 风格，neutral 色板），手机优先（多数用户在手机上打开），亮/暗色跟随系统。
1. 仍由 Go `html/template` 服务端渲染，**不加载 SPA、外部字体、外部脚本或任何第三方资源**（token 在 URL 中）。
   CSS 内联，设计变量（颜色、圆角、边框、阴影）取自 `web/src/index.css` 的 `:root` 与 `.dark` token（oklch 值原样复制），
   `@media (prefers-color-scheme: dark)` 切换；系统字体栈；最大宽度约 28–32rem 居中，16px 左右留白，375px 无横向滚动。
2. 布局（自上而下，卡片式，样式对应 shadcn Card/Button/Badge/Progress/Separator）：
   - 头部：面板名称 + 用户名；状态 Badge（正常 / 已停用 / 已过期 / 流量用尽，按现有数据判断）。
   - 用量卡：已用流量（上传+下载，自动换算 B/KB/MB/GB/TB，保留 1–2 位小数）/ 总量（无限显示「不限」），进度条；
     到期日期（`YYYY-MM-DD`，UTC 标注）与剩余天数；永不过期显示「长期有效」。不再显示原始字节数。
   - 订阅卡：二维码（白底、圆角、留白，暗色下仍为白底保证可扫）；订阅地址只读输入框 + 「复制」按钮。
   - 一键导入：主按钮「导入到 sing-box」「导入到 Clash / mihomo」；下方各平台推荐客户端及官方下载链接，
     复用面板订阅中心已核实的客户端列表数据（同一份数据源，不要复制两份，必要时抽到可被 Go 嵌入的 JSON 或在 i18n 中集中）。
   - 「其他格式」折叠区（`<details>`）：sing-box / Clash / 通用 base64 三个地址，各带复制按钮。
   - 页脚：兼容性与隐私提示（现有文案），小号 muted 文字。
3. 复制按钮需要少量 JS：内联 `<script>` 使用每次响应随机的 CSP nonce（crypto/rand），CSP 改为
   `script-src 'nonce-…'`，其余指令保持现状（default-src 'none'、img-src data:、style-src 'unsafe-inline'…）。
   无 JS 时页面仍完整可用（地址可长按选择、链接可点）。复制成功后按钮文字短暂变为「已复制」。
4. 文案全部放 i18n（现有 SubscriptionText 机制），组件内不写死中文。
5. 测试：页面 golden（固定 nonce 注入）；断言无外部 URL 资源引用（除客户端下载链接 `<a href>`）、CSP 含 nonce 且与脚本一致、
   流量换算与到期显示、停用/过期状态。

**B. 节点地址改为域名后的提示**
6. 引导「使用场景」选择域名、或设置页修改 public_host 后，若面板访问地址随之变化（证书将切换到新地址），
   保存成功后弹出对话框：显示新的面板地址（可复制），说明「旧地址将因证书不匹配而无法访问，请收藏新地址」，
   按钮「前往新地址」直接跳转。开发模式下不跳转仅提示。前端测试覆盖。

- **验收**：
  - [x] make lint test build 通过（审查者沙箱外复跑，含 e2e）；页面 golden 与新增测试通过。
  - [x] 375px 与桌面宽度、亮/暗色下页面美观无溢出（审查者浏览器验证；另补复制的 execCommand 兜底与「0 B」显示）。


**T6.6 实现与验证记录（2026-11-09）**：
- [x] A1：Go html/template + 内联 CSS/脚本，系统字体、32rem 居中及手机留白；
  neutral oklch/圆角/边框 token 与 index.css 一致并有测试。index.css 无阴影 token，按最小方案仅用边框。
- [x] A2：面板名/用户名/状态、换算用量/限额/进度、UTC 日期及剩余天数、白底二维码、
  只读地址/复制、两种导入按钮、官方客户端入口和原生 details 格式区；客户端唯一数据源 clients.json。
- [x] A3：每个 HTML GET/HEAD 独立 crypto/rand 32 字节 nonce，CSP 与脚本匹配；
  复制成功两秒反馈、失败选中地址并提示手动复制；无 JS 仍保留内容/手动选择/导入及下载。
- [x] A4：所有新增中文在 zh-CN.ts，分享页沿用 SubscriptionText；未手改 shadcn 生成组件。
- [x] A5：固定 nonce/时间的 page.golden；外部资源、转义、nonce 随机性/响应头、流量单位/溢出、
  UTC 日期/剩余天数、四状态与优先级测试；浏览器失效订阅仍空白 404，冲突处理详见 FRONTEND §9。
- [x] B6：引导/设置主机变更后显示可复制的新地址和证书提示，生产点击跳转、dev 仅提示。
  覆盖无变化/失败/当前 origin、同时切端口路径的等待，以及引导换步/旧证书导致认证刷新失败时保留对话框。
  引导 PUT 的最小响应补充与有效端口/路径约定已同步 API.md，完整交互见 FRONTEND §9。
- [x] make lint（含 ShellCheck）、make build 通过；页面 golden 和新增测试通过。
  安装脚本及其余 Go 包通过；前端 typecheck/lint/vitest 通过（18 文件 / 115 测试）。
- [x] （审查者沙箱外复跑通过）完整 make test 已执行，唯一失败为既有 internal/e2e.TestLoopback 的
  `watch DNS configuration: register DNS configuration notification: status 9`；
  未修改或跳过该测试，需普通终端/CI 复跑，总验收第一项仍保持未勾选。
- [x] （审查者已在浏览器验收）375px/桌面及亮暗色视觉验收：浏览器工具返回 host unavailable，未取得真实截图/布局测量，
  jsdom 测试不能替代此项，仍需审查者浏览器验证。
- [x] 仅 T6.6，无新依赖/锁文件/供应链策略变动，无内核配置/Apply 改动，未执行 commit/push/切换分支。

### T6.7 代理组结构调整（v0.1.1，用户反馈）

用户反馈：Clash 中「节点选择」内含「自动选择」+各节点，同时「自动选择」组也单独可见，节点和「自动」在两层重复出现，容易混淆。
新结构：**第一层只选方式，第二层才放节点**，每样东西只出现在一层。

**Clash / mihomo YAML**
1. 节点数 ≥ 2：
   - `节点选择`（select，规则 MATCH、rule-provider 下载、远端 DNS 等现有引用全部指向它）：`[自动选择, 手动选择, DIRECT]`，默认第一项。
   - `自动选择`（url-test）：全部节点；`url: https://www.gstatic.com/generate_204`、`interval: 300`、**`tolerance: 50`**、`lazy: true`。
   - `手动选择`（select）：全部节点。
   - 节点不得直接出现在 `节点选择` 中。
2. 节点数 = 1：只有 `节点选择`：`[该节点, DIRECT]`，不生成 `自动选择`/`手动选择`。
3. 组名文案放 i18n（现有 select/auto 键，新增 manual 键；DIRECT 保持 mihomo 内置名，显示文案不改）。

**sing-box JSON**
4. 同样结构：selector `proxy`（显示用 tag 维持现有 i18n 规则）`outbounds: [auto, manual, direct]`，`default: auto`；
   urltest `auto`：全部节点，`interval: 3m`、**`tolerance: 50`**；selector `manual`：全部节点。
   单节点时 selector `proxy` = `[该节点, direct]`，不生成 auto/manual。
   路由 final、DNS detour、rule_set download_detour 等现有对 `proxy` 的引用保持不变。字段以 sing-box 1.14 官方文档为准
   （selector / urltest 的 tolerance、interrupt_exist_connections 等不确定字段先查文档）。

**节点显示名**
5. 订阅中节点名：优先使用面板中该入站的备注（remark，去首尾空白）；为空时用「Reality · <端口>」「Hysteria2 · <端口>」
   （文案在 i18n）。同一订阅内名称冲突时追加「 (2)」「 (3)」去重；名称不得包含逗号、换行等破坏 YAML/URI 的字符（转义或替换）。
   URI 格式（base64）的 `#` 片段名同步使用该显示名。

**测试与文档**
6. golden 全部更新；新增测试：≥2 节点三组结构及「节点不在第一层」、单节点无第二层、tolerance、名称去重/默认名；
   PATH 有 sing-box 时 check 通过。CONFIG.md §4.3/§4.4 同步示例。

- **验收**：
  - [x] make lint test build 通过（审查者沙箱外复跑，含 e2e）；golden 与新增测试覆盖上述各项。

**T6.7 实现与验证记录（2026-11-09）**：
- [x] 1：Clash 多节点第一层为自动选择/手动选择/DIRECT；两个第二层均包含全部节点，
  自动组显式输出 gstatic URL、interval=300、tolerance=50、lazy=true。
- [x] 2：单节点只有节点选择 `[该节点, DIRECT]`，默认该节点；两种协议均有测试。
- [x] 3：新增 manual 和两协议默认名模板到 zh-CN.ts；策略组保留名从同一 i18n 数据读取。
- [x] 4：sing-box 多节点 proxy → auto/manual/direct，auto 间隔 3m、容差 50；
  单节点只有 proxy → 该节点/direct。三预设的路由、DNS、规则下载引用保持。
  官方字段依据与空节点的最小补齐方案见 CONFIG.md §4.3/§4.4 及命名约定。
- [x] 5：Store.Load 不再回退内部 tag；备注清理、友好默认名、顺序后缀去重及保留名避让；
  测试覆盖空白/控制字符/逗号/Unicode 换行/自然后缀，JSON/YAML/base64/links 同名且不改数据库。
- [x] 6：重新生成原 27 份 golden，新增单节点/默认名及重名共 18 份，共 45 份；
  15 份 sing-box JSON 实际通过锁定 1.14.4 check。CONFIG 示例、字段/边界说明与 API 测试清单同步。
- [x] make lint、make build 通过；安装脚本及其余 Go 包通过；前端 typecheck/lint/vitest 通过（18 文件 / 115 测试）。
- [x] （审查者沙箱外复跑通过）完整 make test：已运行，既有 internal/e2e.TestLoopback 在内核启动时因 macOS 沙箱
  DNS 通知注册 status 9 失败，需普通终端/CI 复跑；未修改启动行为或添加跳过条件。
- 待确认：旧 e2e 夹具未写 remark，但测试固定用 reality/hysteria2 查找订阅节点；
  新默认名会使沙箱外运行也找不到这些旧名称。因用户要求不修改该测试，目前未改 e2e；
  已请求允许仅将夹具 remark 显式设为原 tag，保持断言和执行流程不变。
- [x] 仅 T6.7，无新依赖/供应链策略变动，无服务端 Apply 改动，未执行 commit/push/切换分支。

### T6.8 第一层只保留「自动 / 手动」（v0.1.2，用户反馈）

用户反馈 T6.7 的菜单仍显杂乱。调整：`节点选择` = `[自动选择, 手动选择]`（不再含 DIRECT，直连用客户端自带的直连模式）；
`自动选择` url-test 加 `hidden: true`（mihomo 文档：需界面支持，Clash Verge Rev 支持），节点只在 `手动选择` 中可见；
单节点时 `节点选择` = `[该节点]`；sing-box 同步（`proxy` = `[auto, manual]`，sing-box 无隐藏能力）。gfw_only 的 `MATCH,DIRECT` 不受影响。

- [x] 审查者直接实现；golden 更新，groups_test 断言新结构与 hidden；make lint test build（含 e2e）通过。

### T6.9 只保留一个手动分组（v0.1.3，用户反馈）

真机反馈：iPhone 上的 Clash 客户端不支持 `hidden`，T6.8 的自动组仍可见；两个平级分组又会让其中一个不被规则引用。
定稿：Clash 与 sing-box 都只有一个分组 `节点选择` / `proxy`（select / selector），列出全部节点，默认第一个；
不生成自动测速组，不含 DIRECT（直连用客户端自带模式）。无节点时 Clash 为 `[REJECT]`、sing-box 为 `[direct]`（沿用既有空订阅语义）。
`自动选择`/`手动选择` 文案键删除，节点名不再为其保留。

- [x] 审查者直接实现；golden、groups_test、names_test 更新；make lint test build（含 e2e）通过。

### T6.10 调研：创建限流量用户报错且已有订阅暂时失效（v0.1.4）

**用户报告（真机，v0.1.3）**：在面板创建一个设了流量上限的用户时页面报错；与此同时**已有用户的订阅/代理也不可用**，
过一段时间自行恢复。具体报错文案未知。前端 GiB→字节换算已核实取整无误（`web/src/schemas/user.ts`）。

**先调研、复现，再修复。** 审查者的假设（需逐条用代码与测试证实或否定，不要直接按假设改）：
1. **生效事务阻塞所有读请求**：`Manager.Apply` 按 SPEC §7 在 DB 事务内完成换文件→重启→健康检查→防火墙同步；
   DB 为单连接池（`SetMaxOpenConns(1)`）+ `BEGIN IMMEDIATE`，期间公开订阅、面板 API 的读请求全部排队或超时，
   表现为「已有订阅不可用，过一段时间恢复」。
2. **请求超时**：HTTP Server `WriteTimeout` 30s；若生效耗时（重启 + 健康检查 + 防火墙）接近或超过，前端拿到网络错误，
   而生效仍在后台继续或回滚。
3. **用户变更导致 sing-box 整体重启**：任何用户增删改都会重启内核，所有在线连接被断开。
4. 其他：创建流量受限用户时生成配置/健康检查/回滚路径上的真实错误（例如某分支返回 500），请查看
   `internal/singbox/apply.go`、`internal/httpapi/users.go`（applyResource）、`internal/confgen/build.go`（eligibleUsers）。

**要求**
- 写出能复现问题的测试（注入慢重启/慢健康检查，并发请求订阅与面板读接口；以及创建带 traffic_limit 的用户），
  在 PR 描述中给出根因结论与证据。
- 修复方向（按根因选择，保持 SPEC §7 的「先校验后生效、失败回滚、崩溃恢复」语义不被削弱）：
  - 订阅与面板只读接口在生效期间不得被阻塞超过 1 秒：例如读路径改用独立的只读连接（WAL 下读写可并发），
    或缩短持锁范围（以 apply_generation/pending 日志保证一致性）。方案写入 SPEC §7 补充。
  - 生效总时长有上限并小于 HTTP 写超时；超时返回明确的 503 + 下一步提示，前端显示「正在生效，请稍后刷新」而非网络错误。
  - **用户变更不中断现有连接**：查 sing-box 1.14 官方文档是否支持不重启进程的配置重载（如 SIGHUP / reload），
    支持则对仅用户变化的生效走重载，仍须 check 通过、失败回滚；不支持则在 PR 中说明并保持重启。
- 若根因与以上假设都不同，按实际根因修复并更新文档。

**顺带完善**
- 安装脚本：apt/dnf 锁被占用（新 VPS 开机自动更新常见）时不要立即失败：最多等待 5 分钟（apt 用
  `-o DPkg::Lock::Timeout=300` 或轮询 `fuser`），等待时提示「系统正在自动更新软件，请稍候」；超时再报错并给出处理建议
  （不得建议删除锁文件或 kill 进程）。install_test 覆盖。

- **验收**：
  - [x] 根因结论与复现测试；修复后测试通过。
  - [x] 生效期间订阅/只读接口延迟 < 1s 的测试；超时路径测试。
  - [x] apt 锁等待测试。
  - [x] make lint test build 通过（审查者沙箱外复跑，含 e2e；install_test 15 项、shellcheck 通过）；文档同步。

**T6.10 实现与验证记录（2026-11-16）**：
- 根因与逐条假设证据、复现测试、修复及 PR 验收记录见 [T6.10.md](T6.10.md)。
- 独立只读 WAL 池解除订阅/会话/面板读取阻塞；写事务及 pending/generation 恢复语义保持。
  诊断日志/磁盘配置在生效中快速 503；Apply 12 秒工作 + 12 秒恢复，24 秒总预算，超时给明确 503。
- sing-box 1.14.4 SIGHUP 会关闭并重建实例，不能保持连接；按本任务例外继续 restart，依据见 SPEC §7。
- apt update/install、dnf 锁等待最多 300 秒；锁释放、超时、普通错误/权限错误均有离线测试。
- make lint、make build 通过；其余 Go 包、安装脚本、前端 116 项测试通过。
  完整 make test 仍仅因既有 e2e 的 macOS DNS 通知 `status 9` 失败，未修改/跳过，最后一项保留未勾选。
- 仅 T6.10，无新增依赖/供应链策略变动，未执行 git commit/push/切换分支。

### T7.1 到期自动停用 + 每用户流量统计（v0.2.0）

全盘审查发现：`traffic_up/traffic_down` 从未被写入，流量上限形同虚设；到期用户只在下一次无关的生效时才被移出
sing-box 配置，已导入的设备可继续使用。本任务补齐两者，共用一个后台调度器。

**已核实的约束**：锁定的官方 sing-box 1.14.4 构建标签为
`with_gvisor,with_quic,with_dhcp,with_wireguard,with_utls,with_acme,with_clash_api,…`，**不含 `with_v2ray_api`**，
不能用 v2ray_api 的用户统计；不得改用自编译内核。现有配置已启用 Clash API（127.0.0.1 + 随机 secret）。

**A. 流量采集（先调研后实现）**
1. 查 sing-box v1.14.4 源码（`experimental/clashapi`）确认 `/connections` 每条连接的 metadata 是否带认证用户
   （如 inbound user / `auth_user` 对应字段）。在 PR 描述中引用源码行。
2. 若带用户字段：直接按该字段归属。若不带：服务端配置为每个启用用户生成独立直连出站（tag 如 `direct-u<id>`，
   仍为 `direct` 类型），并在路由规则中按 `auth_user` 路由到对应出站（字段以 1.14 官方文档为准），连接的 `chains`
   即可确定用户。保持现有 sniff/BT 屏蔽/resolve/私网拒绝规则的语义与顺序不变，golden 更新并 `sing-box check`。
3. 采集器：每 5s 轮询 `/connections`，按连接 id 计算 upload/download 增量累加到对应用户的内存计数；
   连接在两次轮询之间关闭会丢失最后一段增量——属已知近似误差，在 SPEC 与面板提示中如实说明（不得宣称精确）。
   sing-box 重启（连接 id 重置）要正确处理，不得重复计数或出现负增量。
4. 每 60s（以及进程正常退出时）把内存增量批量写入 DB（单个短事务，`traffic_up += ?`），写失败保留增量下次重试；
   必须与 T6.10 的生效事务、只读池兼容，不得长时间持有写锁。

**B. 到期 / 超额自动停用**
5. 调度器每 30s 检查：已到期、或用量达到上限（沿用 `eligibleUsers` 的判定）且仍在当前生效配置中的用户；
   有变化时**合并为一次** `Manager.Apply` 重新生效（失败按现有回滚；下轮重试，指数退避上限 10 分钟），
   使其连接被断开且不能再连。日志记录被停用的用户 id 与原因（到期/超额），不记录凭证。
6. 用户在面板延长到期时间、提高上限或重置流量后，按现有生效流程恢复，无需额外操作。

**C. 流量重置**
7. 新增 `POST /users/{id}/reset-traffic`（认证 + CSRF + 走生效流程），把 up/down 清零；面板用户详情加「重置流量」
   按钮（二次确认）。
8. 用户可选「每月重置日」（1–28，0 表示不自动重置；新增列，带迁移）。调度器在 UTC 该日 00:00 后首次检查时清零，
   用 `traffic_reset_at` 防重复。表单、详情和 API 文档同步。

**D. 展示**
9. 面板用户列表/详情、订阅分享页、`Subscription-Userinfo` 显示真实用量；去掉「实时统计将在后续版本提供」文案，
   改为说明「统计每 5 秒采集，可能略少于实际用量」。
10. 保存用户时提示「保存会使所有在线连接中断约 1 秒」（sing-box 1.14.4 重载即重建实例，见 T6.10）。

**测试**
- 采集器：注入假 Clash API 响应，覆盖增量、连接关闭、sing-box 重启 id 重置、未知用户、批量落库失败重试。
- 调度器：注入时钟，覆盖到期、超额、合并生效、生效失败退避、每月重置不重复。
- e2e 回环测试中新增断言：经代理传输已知字节后，用户用量在一次采集+落库后增加且量级正确（允许近似）。
- make lint test build 通过；SPEC/API/CONFIG/FRONTEND 文档同步。

- **验收**：
  - [x] 调研结论（用户字段是否存在）与采用方案写入 PR 描述/SPEC。
  - [x] 到期、超额自动停用并断开；恢复路径可用（真实 Manager + 假系统边界测试通过，内核回环待复验）。
  - [x] 用量真实增长；重置与每月重置可用（采集/落库/API/页面集成通过，真实内核量级断言待复验）。
  - [x] make lint test build 通过（审查者沙箱外复跑，含真实内核流量 e2e）；文档同步。

**T7.1 实现与验证记录（2026-11-16）**：
- 调研、源码链接、最小规格补齐及逐项测试见 [T7.1.md](T7.1.md)；没有认证用户 metadata，采用 auth_user → direct-u<ID> → chains。
- 新迁移保留旧计数，增加月度重置日/时间及内部 epoch；5 秒近似采样、60 秒短事务落库、30 秒合并检查，
  与既有 Apply 和独立只读 WAL 池兼容；失败保留增量/回滚，生效退避至多 10 分钟。
- 重置 API 与确认按钮、每月 UTC 计划、真实用量和误差/保存中断提示完成；SPEC/API/CONFIG/FRONTEND 同步。
- make lint、make build 通过；安装脚本、其余 Go 包、前端 typecheck/lint/118 项测试通过；golden 通过锁定 1.14.4 check。
- 完整 make test 已运行，原 TestLoopback 和新增 TestTrafficLoopback 均仅因沙箱 DNS 通知 status 9 启动失败，
  须普通终端/CI 复跑；未修改原 TestLoopback 文件或跳过条件，总验收最后一项保留未勾选。
- 仅 T7.1，无新增依赖或供应链策略变动；未执行 commit/push/切换分支。

### T7.1b 配置未变化时不重启内核（审查者追加，随 v0.2.0）

用户原则：**新增/编辑一个用户不应影响其他用户**。sing-box 1.14.4 任何生效都会重启并断开全部连接（T6.10 已证实无热更新）。
但很多写操作根本不改变生成的配置：修改流量上限/到期时间/备注（用户仍有效）、手动或每月重置流量、改订阅 token 等。

审查者已在 `internal/singbox/apply.go` 加入短路（请审阅并完善，不要回退）：当 `mutate != nil`、已有配置、
新生成配置与当前文件**逐字节相同**、防火墙计划相同、且内核 `is-active` 时，只在同一事务里写
`AppliedFirewall`/`AppliedUsers`/`ApplyGeneration` 并提交，不写临时文件、不 check、不重启、不动防火墙。
`mutate == nil`（显式重启）、内核不在运行、或任一不同，走原完整流程。

审查者另修复：`internal/traffic/collector.go` 新增 `born` 基线——面板进程重启后首次见到、且开始时间早于采集器启动的连接
只记基线不计量，避免长连接重复计数；已加 `TestCollectorPanelRestartDoesNotRecount`。

**要求**
1. 现有失败测试（`TestUserHandlersEveryWriteAppliesAndRedacts`、`TestInboundHandlersEveryWriteAppliesAndFirewallFollows`、
   `TestEveryResourceWriteRollsBackOnCheckFailure`、`TestSubscriptionManagement` 等）按新语义修正：
   - 会改变配置的写操作仍必须 check→替换→重启→健康→防火墙，失败回滚（用能改变配置的夹具验证）；
   - 不改变配置的写操作：不调用 check/restart/firewall，DB 修改与 applied 记账提交，响应成功。
   不得用“测试专用开关”绕过；断言要区分两条路径。
2. 新增 apply 单测：未变化短路；`mutate==nil` 仍重启；内核未运行时走完整流程并启动；配置字节不同/防火墙不同走完整流程；
   短路提交失败时的回滚；与 pending 恢复兼容。
3. 核实配置生成是**确定性**的（同一快照两次 Build 字节相同，含用户顺序、map 序列化、出站顺序），否则短路无效；补测试。
4. 文档：AGENTS.md 硬性约定 1 改为「写入配置必须先校验后生效…；生成配置与运行中完全相同时不写文件、不重启」；
   SPEC §7 补充；FRONTEND/i18n 中「保存会使所有在线连接中断约 1 秒」提示改为仅在会改变配置的操作上出现
   （新增/删除/启停用户、节点变更等；编辑上限/到期/备注、重置流量不提示）。
5. 回答并在 PR 描述写明：哪些用户操作现在不再中断他人连接，哪些仍会。

- **验收**：
  - [x] 旧测试按配置改变/不变分开断言；未用测试开关绕过。
  - [x] apply 短路、nil、未运行、字节/防火墙不同、提交失败、pending 恢复测试。
  - [x] 配置生成确定性测试（重复 Build、用户/入站顺序、options/map、出站顺序）。
  - [x] AGENTS/SPEC/FRONTEND/i18n 及 API/CONFIG 同步；操作影响写入评审说明。
  - [x] make lint、make build；安装脚本、其余 Go 包及前端 121 项测试通过。
  - [x] （审查者沙箱外复跑通过）完整 make test：已运行，仅 internal/e2e 两个回环因已知沙箱 DNS 通知 status 9 失败，未修改/跳过。

**T7.1b 实现与验证记录（2026-11-16）**：
- 具体行为、哪些操作仍中断他人连接、测试证据和未决问题见 [T7.1b.md](T7.1b.md)。
- 保留审查者的短路与 born 基线修复，完善短路 COMMIT 失败时的连接丢弃/generation 核对；
  两条路径共享记账，pending 必须先处理。配置真实变更仍走完整 check/原子替换/重启/回滚。
- 普通有效用户元数据、token、手动/月度清零不再重启；恢复/移除用户或改名/有效凭证仍可能重启。
- 无新增依赖/迁移/供应链策略变动；未推进后续任务，未执行 commit/push/切换分支。

### T7.1c 流量统计改为内核计数（真机发现严重少计）

**真机结果（香港 VPS，v0.2.0-test）**：用户 me 经 Reality 下载 20,000,000 字节，记账只增加 5,227,421 字节（约 26%）。
原因：Clash API 轮询（5s）只能看到仍活跃的连接；连接在两次轮询间结束，剩余字节全部丢失；
寿命 < 5s 的短连接（网页浏览的主体）可能完全不被看到。T7.1 的「略少于实际」与事实不符，方案不可用。
到期自动停用已在真机验证通过（到期后 27s 内停用并移出配置）。

**新方案：nftables 内核计数，按用户 fwmark 归属**
1. 每个用户的直连出站 `direct-u<id>` 增加 `routing_mark`（sing-box 1.14 Dial Fields，Linux；字段格式以官方文档为准），
   mark = 固定基址 + 用户 id。基址选一个不易冲突的高位范围并写入 SPEC；校验用户 id 不溢出该范围。
2. 新建独立表 `table inet boxpanel_traffic`（不影响现有防火墙表与端口跳跃表）：
   - output 链：对带用户 mark 的包 `ct mark set meta mark`，并计入该用户「上传」命名计数器；
   - input 链：`ct mark` 为用户 mark 的回包计入该用户「下载」命名计数器；
   - 用 `nft -j list counters table inet boxpanel_traffic` 读取。链优先级、hook 选择要能正确统计 TCP 与 UDP（Hysteria2 转发的 UDP、QUIC）。
3. 表与计数器随生效流程同步（启用用户集合变化时增删计数器，未变化的计数器保留，不重置）；纳入 T2.3 的
   pending/回滚语义；`firewall` 包现有的 nft 执行与测试注入方式复用。仅 Linux 生产启用；开发模式与非 Linux 跳过。
4. 采集器改为每 10s 读取计数器，按用户算增量（计数器减小视为被重建/重启，以当前值为增量起点，不得出负数），
   落库与 epoch/重置/批量重试逻辑保留；移除 Clash API 连接轮询（若保留为无 nft 时的退路，必须在面板标注「近似」）。
   面板进程重启不得重复计数：首次读取只建立基线。
5. 安装脚本确保 `nftables` 已安装（Debian/Ubuntu `nftables` 包，RHEL 系同名），install_test 覆盖。
6. 文档：SPEC 说明统计口径——统计的是服务器与目标站点之间的字节（上传=服务器发出、下载=服务器收到），
   不含客户端到服务器之间的加密开销，与客户端显示可能有几个百分点差异；服务器自身 DNS 解析不计入。
   面板与订阅页的文案同步修改（去掉「每 5 秒采集，可能略少」）。

**测试**
- 单测：生成的 nft 规则文本 golden；计数器读取解析；增量、重建（减小）、面板重启基线、用户删除；同步增删计数器不重置。
- Linux 集成测试：若以 root 运行且有 nft，做真实计数测试，否则 `t.Skip`（不得在 macOS 上失败）。
- e2e：原 TestTrafficLoopback 依赖 Clash API，按新方案调整为在无 root/nft 时跳过流量断言，连通性断言保留。
- make lint test build 通过；审查者会在真机复测「下载 20 MB 应记账约 20 MB」。

- **验收**：
  - [x] nft 规则、计数器同步与采集器实现及测试（Linux 实际计数待具备条件的环境复跑）。
  - [x] 安装脚本与文档同步。
  - [ ] make lint test build 通过：lint/build、安装脚本 17 项、除 e2e 外全部 Go 包及前端 121 项通过；
    完整 make test 已运行，两个回环仍因已知沙箱 DNS 通知 status 9 在内核启动时失败，未改动/跳过 TestLoopback。

**T7.1c 实现与验证记录（2026-11-16）**：
- 具体实现、官方 routing_mark/nftables 依据、规格补齐及验收结果见 [T7.1c.md](T7.1c.md)。
- 独立 fwmark 范围与 nft 表，保留已有用户计数器、增删规则纳入原 Apply/pending/回滚/清理；
  改为每 10 秒读取累计内核字节，保留 epoch/批次/调度及 T7.1b 不变短路。
- macOS check 对 Linux marks 的限制明确验证；Linux 实测用例在隔离 network namespace 执行，
  当前仅交叉编译通过，需 Linux root+nft 实测及审查者 20 MB 复验。
- 无新依赖/迁移/供应链变更，未推进后续任务，未执行 commit/push/切换分支。

**审查者真机验收（2026-11-16/17，香港 VPS，v0.2.0-test2，Ubuntu 22.04）**
- [x] 经 Reality 下载 20,000,000 字节 → 记账 20,078,075；经 Hysteria2 → 20,078,355（约 +0.4% 包头开销）。
- [x] 到期自动停用：到期后 27s 内停用并移出配置（T7.1，test 版本）。
- [x] 面板修改用户流量上限：PUT 200（14.6ms），sing-box PID/启动时间不变，未中断他人连接（T7.1b）。

## M8 多服务器（主控 + 节点）路线图（2026-11-18 确定）

用户确认实施「主控 + 节点」：一个订阅包含所有服务器节点，用户、流量上限、到期在主控统一管理。顺序：

1. **T7.5 备份与恢复**（前置：主控 DB 将成为唯一事实来源）——详细规格见下。
2. **T7.9 通知与告警**：用户流量 80%/100%、到期提醒；服务器网卡流量按计费日与计费口径（出站/双向/取大）对比月额度；
   通道 Bark / Server 酱 / Telegram Bot，测试发送、失败重试队列、同一事件每周期只发一次、面板首页兜底展示。
   设计预留多服务器维度。说明：用户流量（服务器↔目标）≠ VPS 账单（约 2× 用户流量 + 加密开销 + 扫描/系统流量）。
3. **T8.1** 角色（master/agent，同一二进制）、servers 表、入站归属服务器、单机平滑升级为主控。
4. **T8.2** 一次性加入令牌 + `install.sh --join`；节点本地生成密钥对注册，之后双向认证；**节点主动连主控**，节点不暴露管理端口、不跑面板。
5. **T8.3** 期望状态同步（入站、有效用户、规则、generation），节点用现有 Apply（校验/回滚/不变不重启）生效并回报健康；离线判定；
   节点自生成 Reality/Hysteria2 私钥，只上报公钥/指纹。
6. **T8.4** 节点上报每用户累计值（单调 epoch 与各方向高水位保证幂等），主控汇总并统一判定到期/超额，下一轮同步全网停用。
7. **T8.5** 在 T8.1 的跨服务器订阅和默认命名基础上完善订阅体验；≥3 节点时可选自动测速组。
8. **T8.6** 面板「服务器」页：添加向导（复制加入命令）、状态/版本/最近联系/流量、移除。
9. **T8.7** 版本兼容检查与提示；端到端测试（本机 1 主控 + 2 节点）；告警扩展到全部服务器 + 服务器离线告警；
   可选：主控把加密备份复制到节点，实现异机备份。
真机验收需要第二台 VPS。

### T7.5 备份与恢复

**目标**：服务器损坏或迁移时，在新 VPS 上一键恢复，**已导入的客户端无需重新导入订阅**
（UUID/密码/订阅 token/Reality 密钥/Hysteria2 证书指纹全部保持；若使用域名，只需改 DNS）。

**备份内容**（单个 `.tar.gz`，内含 `manifest.json`）：
- SQLite 一致性快照（`VACUUM INTO` 或在线 backup，不得直接复制正在写的 db/wal 文件）；
- `certs/` 中 Hysteria2 证书与私钥（**必须**，客户端固定其指纹）、ACME 账号密钥、当前生效的面板证书包；
- 不备份 `sing-box.json`（恢复后由 DB 重新生成）、锁文件、`.bak/.pending`。
- `manifest.json`：boxpanel 版本、schema 版本（迁移号）、创建时间、源 public_host、每个文件的 SHA-256。

**加密**：从面板下载的备份**必须**用口令加密（≥ 12 字符；scrypt 派生 + XChaCha20-Poly1305，使用已有依赖
`golang.org/x/crypto`，不手写密码学原语，不引入新依赖）；口令不保存。服务器本地自动备份可不加密，但文件与目录 0600/0700。

**生成**
1. CLI：`boxpanel backup [--output 文件] [--passphrase-file 文件]`（口令从文件或交互读取，不出现在进程参数/日志）。
2. 面板：设置页「备份与恢复」，输入口令 → 下载加密备份（`GET /backup` 改为 `POST /backup`，因需提交口令，CSRF + 认证）。
3. 自动备份：每天一次写入 `<data-dir>/backups/`，保留最近 7 份；面板显示最近一次自动备份时间与结果；
   面板提示「自动备份与服务器在同一台机器上，请定期下载加密备份到别处」。

**恢复**
4. CLI（主路径，面板坏了也能用）：`boxpanel restore <文件> [--passphrase-file]`：
   校验口令/完整性（SHA-256）/manifest；schema 比当前程序新则拒绝并提示先升级 boxpanel；旧则恢复后执行迁移；
   停止面板服务 → 原数据目录整体留存为 `<data-dir>.pre-restore-<时间>` → 写入恢复数据 → 启动面板 → 完整生效（重新生成配置、防火墙）；
   任一步失败自动回到恢复前状态。恢复后所有会话失效，管理员凭证为备份中的凭证。
   若当前公网 IP 与备份中的 public_host（IP 形式）不同，输出提示：可用 `boxpanel set-host` 更新，或改域名解析。
5. 面板：上传加密备份 + 口令 → 二次确认（说明会覆盖当前全部数据）→ 后台执行与 CLI 相同的恢复流程（可用 `systemd-run` 或自重启机制，
   不得在持有数据库连接时替换 DB 文件）→ 页面提示「恢复中，约 30 秒后重新登录」。
6. 安装脚本：`install.sh --restore <文件>`：全新机器安装后直接恢复（跳过生成新管理员与新密钥）。

**顺带**：清理 `certs/` 中未被引用的旧 `panel-*.pem`（保留当前生效与上一份），在证书切换成功后执行。

**测试**
- 往返：备份 → 新数据目录恢复 → DB 内容、证书指纹、Reality 密钥、订阅 token 一致；生成的 sing-box 配置与原配置一致。
- 错误口令、篡改（任一字节）、截断文件、manifest 缺失、schema 更新、路径穿越条目（`../`、绝对路径、符号链接）均被拒绝且不改动现有数据。
- 恢复中途失败回滚；自动备份轮转保留 7 份；口令不出现在日志/进程参数。
- make lint test build 通过；文档（INSTALL/API/SPEC/README）同步。

- **验收**：
  - [x] CLI/面板/安装脚本三种恢复路径与测试（注入 systemd/防火墙，Linux 实机待复核）。
  - [x] 自动备份与轮转（每日 UTC、最近 7 份、状态和权限测试）。
  - [ ] 完整 make lint test build 通过；文档已同步。lint/build、其余 Go 包、安装脚本 19 项及前端 124 项通过；
    make test 的两个既有 e2e 仍因 macOS 沙箱 DNS 通知 status 9 失败，未修改或跳过。

**T7.5 实现与验证记录（2026-11-18）**：
- 详细格式、规格缺口、实现及验收记录见 [T7.5.md](T7.5.md)、SPEC §15、INSTALL §8。
- 一致性快照、完整证书归档、scrypt + XChaCha20-Poly1305、manifest/SHA/schema 校验与受限解包。
- 三条恢复入口共用候选验证和目录回滚；面板使用独立 systemd-run，不在打开 DB 的进程中替换文件。
  恢复时完整 Manager.Apply，保留 pre-restore、撤销会话，身份/固定指纹不变；新 schema 拒绝、旧库迁移。
- 每日自动备份与保留 7 份、状态展示；成功证书切换后仅保留当前/上一份 bundle，归档与清理跨进程互斥。
- 无新依赖/迁移/锁文件/供应链变更，未修改 internal/e2e，未执行 commit/push/切换分支，未推进后续任务。

**审查者真机演练（2026-11-19，香港 VPS，v0.2.2-test）**
- [x] CLI 加密备份 11.6 KB；错误口令恢复被拒且数据不变。
- [x] 备份后修改数据 → 恢复（约 3 秒）→ 修改被撤销；两个服务 active；恢复前数据留存于 `/etc/boxpanel.pre-restore-*`。
- [x] 3 个用户 × 2 种订阅格式、`sing-box.json`、Hysteria2 证书的指纹与备份前**完全一致**；恢复后 Reality/Hysteria2 代理均 204。
- [x] 发现并修复：`restore` 只接受选项在文件之前（`--data-dir X 文件 --passphrase-file Y` 被拒），已改为与 set-host 一致的交错解析并加测试。
- [x] 每日自动备份在启动时生成。

### T7.9 通知与告警

**两类流量，口径不同（必须在面板与文档写清）**
- 用户流量：T7.1c 的内核按用户计数（服务器↔目标站点）。
- 服务器流量：网卡计数，对应 VPS 账单。代理流量会经过网卡两次（进出），≈ 2 × 用户流量 + 加密开销 + 扫描/系统流量。

**A. 服务器流量统计**
1. 读取 `/proc/net/dev` 中默认路由所在网卡（`ip route show default` 的 dev；可在设置中改选网卡），每 60s 记录 rx/tx 增量入库
   （新表，带迁移；计数器变小视为重启/重置，以当前值为增量，不得出负数；面板进程重启首次读取只建基线）。
2. 设置项：月流量额度（GB，0 = 不告警）、计费日（1–28）、计费口径：`out`（仅出站）/ `both`（入+出）/ `max`（取大）/ `in`；
   时区按 UTC（界面注明，并允许在说明中提示与服务商对照）。概览页显示本周期已用、额度、百分比、周期起止。
3. 预留 `server_id`（当前固定为本机 1），供 M8 多服务器使用。

**B. 告警事件**
- 用户：流量达到上限的 80%、100%；到期前 3 天、已到期；被自动停用（T7.1 调度器事件）。
- 服务器：本周期流量达到额度 80%、90%、100%；面板证书续期连续失败且剩余 < 24h（复用 acme Warning）；
  sing-box 未运行（连续 2 次检测 `is-active` 失败）及恢复。
- 阈值可在设置中开关；每个事件有唯一键（如 `user:3:quota:80:<周期>`），**同一键只发送一次**；恢复类事件只在先前告警过时发送。

**C. 通知通道**（可同时启用多个，面板配置，带「发送测试」）
- **Bark**：服务器地址（默认 `https://api.day.app`，可自建）+ Device Key。
- **Server 酱**（Turbo 版）：SendKey。
- **Telegram Bot**：Bot Token + Chat ID。
- 各通道 HTTP API 以官方文档为准并在 PR 描述引用；只允许 HTTPS；自建 Bark 地址须解析为公网 IP（复用 `reality.PublicIP` 校验，防 SSRF）。
- 凭证存 DB，API 只返回掩码（如 `****abcd`），日志不得出现；消息内容不得包含订阅 token、订阅链接、UUID、密码。
  消息含：服务器名/地址、事件、用户名、用量/额度/百分比、时间（UTC）。

**D. 可靠投递**
- 发件箱表：事件入队即持久化；投递失败按指数退避重试（30s 起，上限 1h），24h 后放弃并标记失败；
  进程重启后继续投递；每通道每小时最多 30 条，超出合并为一条摘要。
- 面板概览显示未处理告警与最近通知（含失败原因的脱敏摘要），可标记已读；面板首页即使通知全部失败也能看到告警（兜底）。

**测试**
- 网卡计数：增量、重置、基线、周期边界（计费日、跨月、2 月）、四种口径；告警阈值去重（同周期不重复、跨周期重新告警）。
- 三个通道用假 HTTPS 服务验证请求格式、超时、重试与掩码；SSRF 拒绝私网地址；消息不含敏感字段。
- 不访问外网；make lint test build 通过；SPEC/API/FRONTEND/README 同步（README 常见问题补充两类流量口径）。

- **验收**：
  - [x] 服务器流量统计与周期、口径设置。覆盖增量、重启基线、计数回退、UTC 周期和四种计费口径。
  - [x] 告警事件与去重、三通道及测试发送、发件箱重试与合并、概览兜底。用本地 HTTPS 假服务验证请求和脱敏。
  - [ ] 完整 make lint test build 通过。make lint 和 make build 通过，文档已同步。安装脚本 19 项、
    除 `internal/e2e` 外的 Go 测试、前端 typecheck/lint 和 127 项测试均通过。两个原回环用例执行时均遇到 macOS
    沙箱 DNS 通知注册 `status 9`，待沙箱外复跑。

官方请求格式、规格补齐和详细验收见 [T7.9.md](T7.9.md)。

**审查者真机（2026-11-19，香港 VPS，v0.3.0-test）**
- [x] 迁移 0003 建表成功；自动识别默认网卡 eth0；直连下载 20 MB 后该分钟样本 rx = 20,790,615。
- [ ] 待用户配置 Bark 后验证：测试发送送达、额度告警送达与周期去重。

### T8.1 多服务器数据模型与角色（M8 第一步，无网络通信）

本任务只做数据模型、迁移与单机内的多服务器渲染能力；**不实现**节点加入、通信与同步（T8.2/T8.3）。
升级后单机行为必须与 v0.3 完全一致（用户、订阅、配置、指纹不变）。

1. **角色**：设置 `node_role`，取值 `master`（默认；现有与新安装都是 master）/ `agent`（预留，T8.2 由加入流程设置）。
   `boxpanel info` 显示角色。agent 角色在本任务中仅做存储与展示，不改变行为。
2. **servers 表**（迁移 0004）：`id`、`name`、`public_host`、`is_local`（仅一行为 1）、`status`（local/online/offline/pending）、
   `last_seen_at`、`version`、`created_at`、`updated_at`，以及为 T8.2 预留的 `agent_public_key`（可空）。
   迁移时插入本机：`id=1`、`is_local=1`、`public_host` 取现有设置、`name` 默认「本机」（可改名）。
   本机的 `public_host` 与 settings 中的 `public_host` 保持同一事实来源（推荐：settings 继续存，servers 行读写时同步；
   或改为以 servers 为准并迁移引用——选一种并在 SPEC 说明，`set-host` 与设置页行为不变）。
3. **入站归属服务器**：`inbounds` 增加 `server_id INTEGER NOT NULL REFERENCES servers(id)`，现有行归属本机。
   **唯一约束改为按服务器**：`UNIQUE(server_id, port)`、`UNIQUE(server_id, tag)`（SQLite 需重建表，保持数据、外键与触发器；
   迁移在事务内完成，失败不留半成品）。
4. **生成**：sing-box 服务端配置、防火墙计划、流量计数（T7.1c）只包含**本机**（`is_local=1`）的入站——为 T8.3 节点按 server_id
   生成各自配置留好接口（`confgen.Build` 按 server 过滤）。用户集合仍是全局的。
5. **订阅**：渲染所有服务器的已启用入站，每个节点的 `server` 地址取其所属服务器的 `public_host`。
   节点显示名：只有 1 台服务器时与现状完全相同；≥2 台时默认名加服务器名前缀「<服务器名> · Reality · 443」，
   有备注时仍只用备注；去重规则不变。非本机服务器若 `status != online` 仍列出（客户端可自行切换），不做隐藏。
   Reality 公钥取自入站 options。本机 Hysteria2 沿用已经应用的指纹及证书；其他服务器的 Hysteria2 从 options 读取公开证书和指纹（T8.3 起由节点上报）。
6. **API / 面板**：`GET /servers`、`PUT /servers/{id}`（改名）；入站创建/编辑接受 `server_id`（本任务只允许本机，
   其他 id 返回 422 并提示「多服务器加入功能尚未开放」）；面板新增「服务器」页（列表：名称、地址、状态、版本；可改名），
   入站列表显示所属服务器；只有 1 台服务器时界面不出现多余选择。
7. **备份/恢复**（T7.5）自动包含新表；从 v0.3 备份恢复到新版本时走迁移。
8. 告警（T7.9）事件中的 `server_id` 使用 servers 表；服务器流量设置挂到服务器维度（本机）。

**测试**
- 迁移：用 v0.3 结构与数据的库升级，校验数据、外键、约束、迁移后生成的 sing-box 配置与订阅**字节级不变**（golden）。
- 两台服务器的订阅渲染：测试中直接插入第二台服务器与其入站，验证地址、命名前缀、去重、同端口不同服务器允许、同服务器同端口拒绝。
- 本机配置/防火墙/流量计数不包含其他服务器的入站。
- make lint test build 通过；SPEC（数据模型）、API、FRONTEND 同步。

- **验收**：
  - [x] 迁移与字节级不变测试。使用真实 v0.3 表结构与归档验证，保留外键和触发器，并覆盖失败回滚。
    服务端配置及三种订阅逐字节比对，Reality 连接凭证与 Hysteria2 指纹保留。
  - [x] 多服务器订阅渲染与本机过滤测试。同端口跨服务器可用，单服务器内冲突被拒。
    离线服务器节点仍可导出，配置、防火墙和流量计数按本机入站限定。
  - [x] 服务器页与 API。覆盖登录、CSRF、名称校验、改名、归属显示及非本机写入返回 422。
  - [x] 完整 make lint test build 通过（审查者沙箱外复跑含 `internal/e2e`；CI 通过）。规格已同步。

**审查者真机（2026-11-19，香港 VPS，v0.3.0-test → v0.4.0-test）**
- [x] 升级前后 3 用户 × 3 种订阅格式、`sing-box.json`、nft 计数规则**逐字节一致**；角色 master。
- [x] `integrity_check` ok、外键违规 0；本机服务器行与两个入站 server_id=1；迁移版本 1–4。

### T8.2 节点加入与安全通道（心跳，不含配置同步）

**原则**：节点永远**主动**连主控（出站 HTTPS），主控不连节点；节点不跑面板、不开管理端口、不申请面板证书（不需要 TCP 80）。
本任务完成：加入、身份认证、心跳与在线状态、移除。配置同步在 T8.3。

**1. 加入令牌**
- 主控面板「服务器 → 添加服务器」：输入名称，生成一次性加入令牌（32 字节随机，**只存 SHA-256**，1 小时过期，单次使用），
  界面显示可复制的加入命令：
  `bash <(curl -fsSL …/install.sh) --join <主控面板 URL> --token <令牌>`
- T8.2 初版只为自签证书下发 pin。当前签发和校验规则见 T8.2b。
- 令牌可在面板撤销；过期/已用令牌一律拒绝，返回统一的「加入令牌无效或已过期，请在主控重新生成」。

**2. 注册**
- 节点本地生成 Ed25519 密钥对（私钥 0600，存数据目录，**不离开节点**）。
- `POST <主控>/{web_path}/api/agent/v1/join`：`{token, public_key, hostname, public_ip, version}`。主控在事务内校验并消费令牌，
  创建 servers 行（名称取令牌上的名称，status=pending→首次心跳后 online，保存 agent_public_key），返回 `{server_id}`。
- 注册接口单独限流（每 IP 每分钟 10 次），不走面板会话/CSRF（无 cookie），只接受 JSON。

**3. 请求认证（注册之后的所有节点请求）**
- 请求头：`X-Boxpanel-Server`（server_id）、`X-Boxpanel-Timestamp`（Unix 秒）、`X-Boxpanel-Nonce`（16 字节随机 base64url）、
  `X-Boxpanel-Signature` = Ed25519(私钥, `method\npath\ntimestamp\nnonce\nsha256(body)`)。
- 主控校验：服务器存在且未移除、公钥匹配、时间差 ≤ 120s、nonce 5 分钟内不重复（内存 LRU，有上限）、签名有效；
  失败统一 401 不区分原因。使用标准库 `crypto/ed25519`，不手写密码学。
- 节点访问主控：TLS 1.2+；按第 1 条校验证书；不跟随跨主机重定向。

**4. 节点运行模式**
- `boxpanel join --master <URL> --token <令牌> [--master-pin ...]`：注册并写入 `node_role=agent`、主控 URL、server_id、pin。
- 节点运行 `boxpanel serve` 时若 `node_role=agent`：**不启动面板监听**，只运行 sing-box 管理、流量计数（T7.1c）、网卡统计，
  以及每 30 秒心跳 `POST /api/agent/v1/heartbeat`（`{version, singbox_active, uptime}`）。主控记录 `last_seen_at`、版本，
  status=online；超过 90 秒无心跳标记 offline（主控后台每 30s 检查）。
- 主控离线时节点只重试（指数退避上限 60s），不改变本地运行状态。
- 安装脚本 `--join`：安装依赖、boxpanel、sing-box 后执行 join；不打开面板端口与 TCP 80，不申请 ACME；
  `boxpanel info` 在节点上显示「节点模式，主控：<URL>，server_id，最近心跳结果」。

**5. 移除**
- 主控面板「移除服务器」：删除公钥（之后该节点所有请求 401），servers 行标记 removed（保留历史流量，不再出现在订阅）；
  节点连续收到 401 后在日志与 `boxpanel info` 中提示「已被主控移除，可执行 boxpanel leave 清理或重新加入」。
- `boxpanel leave`：节点清除主控信息与密钥，回到未初始化状态（不卸载）。

**测试**
- 令牌：过期、重复使用、撤销、哈希存储；注册限流。
- 认证：正确签名、篡改 body/path/时间、时间偏差、nonce 重放、移除后 401、错误 server_id。
- 进程内端到端：启动主控 handler + 节点客户端，完成 join → 心跳 → online → 停止心跳 → offline → 移除 → 401。
- 安装脚本 `--join` 参数与非交互、dry-run；install_test 覆盖。
- 主控为自签证书时 pin 生效、pin 不匹配拒绝。
- make lint test build 通过；SPEC（通信协议章节）、API（agent 接口）、INSTALL、FRONTEND 同步。

- **验收**：
  - [x] 加入、认证、心跳/离线、移除与测试。
  - [x] 安装脚本 --join、join/leave 命令。
  - [x] 面板添加/移除服务器界面。
  - [x] make lint test build 通过；文档同步。真机验收需要第二台 VPS。
    `make lint` 和 `make build` 通过，SPEC、API、INSTALL、FRONTEND 和 CONFIG 已同步。
    21 项安装脚本用例、其余 Go 测试、前端类型检查和 lint、131 项 Vitest 用例通过。
    `make test` 执行了两个原有回环用例，均在 macOS 沙箱内调用 sing-box 注册 DNS 通知时返回 `status 9`。

### T8.2b 审查修复：主控证书固定的稳定性、时钟偏差提示

审查者发现（必须修复）：
1. **固定指纹会永久锁死节点**：当前仅在主控为自签证书时下发 pin，且节点只接受 pin。主控后续拿到 ACME 证书（密钥改变）→
   所有带 pin 的节点永久无法连接；反之 IP 证书 6 天有效，续期失败回退自签 → 依赖系统 CA 的节点全部断开。
2. **自签回退密钥不稳定**：`acme.reconcileLocal` 在原始 panel.crt 与当前 host 不匹配时调用 `certs.PanelHost` **每次生成新密钥**
   （香港 VPS 正是此情况：原证书为旧 IP，现 host 为域名），任何 pin 都不稳定。

**要求**
- 主控维护一个**长期固定的面板回退密钥**（首次使用时生成，0600，纳入 T7.5 备份；已有安装从现有自签私钥迁移或首次生成）。
  host 变化时只用同一密钥重新签发自签证书，密钥不变。
- 加入命令**总是**携带该固定密钥的 SPKI SHA-256 pin（不论当前是否 ACME）。
- 节点 TLS 校验改为：**pin 匹配（且证书在有效期内）** 或 **系统 CA 校验通过且主机名匹配**，二者其一即可；
  不得用 InsecureSkipVerify 后只做 pin 之外的宽松判断（系统 CA 分支必须调用标准 `x509.Verify` 并校验主机名）。
- 已加入的节点（存了旧 pin 或空 pin）升级后继续可用：空 pin 只走系统 CA；旧 pin 不匹配时仍可走系统 CA。
- 节点收到 401 时比较主控响应 `Date` 头与本地时间，偏差 > 100s 时报错并在 `boxpanel info`/日志提示
  「系统时间与主控相差 N 秒，请启用 NTP（timedatectl set-ntp true）」；否则保持原提示。
- 注册时节点上报的 `public_ip` 若为私网/回环/保留地址，主控拒绝（422）并提示「节点需要公网 IP；位于 NAT 后请用 --public-ip 指定」，
  `install.sh --join` 支持 `--public-ip`。

**测试**：固定 pin 节点跨 ACME 与自签回退双向切换。旧 pin 不匹配及空 pin 时验证系统 CA 与主机名，
系统已信任回退证书时两者也可跨切换；系统未信任的自签回退须匹配 pin，空 pin 的旧节点需用新加入命令注册。
host 变化后回退密钥 SPKI 不变；系统 CA 分支拒绝主机名不匹配；时钟偏差提示；私网 public_ip 拒绝；
备份包含回退密钥。make lint test build 通过；文档同步。

- **验收**：
  - [x] 回退密钥 0600 持久化，保留旧自签密钥，host 变化和 ACME 到期回退时 SPKI 稳定，新加入命令始终带 pin。
  - [x] 双路径 TLS 校验及旧节点兼容性测试，拒绝主机名不匹配、未受信任的证书及过期 pin。
  - [x] 401 时钟偏差记录及日志、info 的 NTP 提示，普通认证失败仍沿用原逻辑。
  - [x] 非公网地址返回 422，保留有效令牌供重试，安装脚本和 join 命令支持 `--public-ip`。
  - [x] 备份恢复保留回退密钥，拒绝缺失或不匹配的密钥。
  - [x] SPEC、API、INSTALL、FRONTEND 同步。
  - [x] `make lint`、`make build` 通过。21 项安装脚本用例、其余 Go 包、前端类型检查和 lint、131 项 Vitest 用例通过。
  - [x] 完整 `make test` 已执行。现有 `TestLoopback` 和 `TestTrafficLoopback` 在 macOS 沙箱启动 sing-box 时，
    注册 DNS 通知返回 `status 9`。待普通终端或 CI 复跑。

**审查者真机验收（2026-11-19，香港 VPS 主控 + LXD 容器 bp-node1 作为节点，原版 install.sh 仅替换 Release 下载为本地构建）**
- [x] `boxpanel server add` 生成命令 → 容器内 `install.sh --join` 8 步完成，server_id=2，首次心跳成功；令牌消费后剩 0。
- [x] 节点无面板/管理端口监听；`agent.key` 0600 留在节点。
- [x] 停止节点服务 102s 后主控标记 offline；重启后 2s 内恢复 online。
- [x] 攻击测试全部 401：重放已用令牌、随机令牌、无签名心跳、伪造签名；未产生多余服务器记录。
- [x] `boxpanel server remove 2` → 节点心跳 401，`info`/日志提示已被移除；`leave` 后用新令牌重新加入（server_id=3）在线。
- [x] 新增 CLI：`boxpanel server add/list/remove`（SSH 管理员与测试用）。
- [x] 真机发现并修复 2 个缺陷：① 主机装有 LXD/Docker 等软件时，其仅放行的 nft input 链被误判为系统防火墙，导致生效失败、面板重启循环；
  ② LXC/LXD 容器（大量廉价 VPS）内 `/proc/uptime` 被 LXCFS 虚拟化，与 systemd 单调时间戳相减为负，`singbox install` 失败。均已加回归测试。

### T8.3 期望状态同步：在主控为远端服务器建节点并自动生效

**目标**：在主控面板为任一服务器创建/修改/停用入站，或增删改用户，**30 秒内**在该服务器生效；订阅自动包含远端节点。
节点私钥不离开节点。主控离线时节点按最后状态继续服务。

**1. 版本号**：主控为每台远端服务器维护 `desired_generation`（任何影响该服务器的变更——其入站、全局用户集合、
服务端路由规则如 BT 屏蔽——都递增；单事务内完成）。心跳响应携带当前 `desired_generation`。

**2. 拉取期望状态**：节点发现 `applied_generation != desired_generation` 时，签名请求
`GET /api/agent/v1/desired` → `{generation, inbounds:[该服务器的入站], users:[有效用户: id,name,uuid,password], route:{block_bittorrent,…}}`。
- 只返回该服务器自己的入站；用户凭证通过已认证的 TLS 通道下发（必要），响应 `Cache-Control: no-store`，日志不得记录内容。
- 「有效用户」由**主控**判定（到期/超额/停用由主控决定，T8.4 汇总流量后生效）；节点模式下禁用本地的到期/超额调度器，
  避免与主控结论冲突。

**3. 节点本地生效**：节点在本地 DB 镜像入站/用户/规则（同一套表），在一次 `Manager.Apply` 的 mutation 中整体替换后生效：
复用先校验后生效、失败回滚、配置不变不重启、防火墙同步、nft 流量计数。成功后在下一次心跳上报
`applied_generation` 与结果；失败上报脱敏错误码与提示（如端口被占用），主控在服务器页显示「已同步 / 同步中 / 同步失败：原因」。

**4. 节点侧密钥**：主控为远端服务器创建 Reality / Hysteria2 入站时**不生成私钥**（options 中私钥字段为空，状态 pending）。
节点同步到 pending 入站后本地生成 Reality 密钥对 / Hysteria2 自签证书（私钥只存节点），通过签名请求
`POST /api/agent/v1/inbound-keys` 上报 `{inbound_id, reality_public_key, short_id, hy2_cert_sha256, hy2_cert_pem}`；
主控保存公开部分并将入站标为 ready。**订阅只包含 ready 的远端入站**。节点重新加入/恢复后若本地缺私钥，重新生成并上报（客户端需更新订阅，面板提示）。

**5. 面板**：入站创建/编辑可选择服务器（去掉 T8.1 的 422 限制，仅 online/offline 的远端服务器可选，removed 不可选）；
端口占用检测与 Reality 目标检测对远端服务器暂不执行（界面注明「在节点上生效时检测，失败会在服务器页显示原因」）；
服务器页显示同步状态、applied/desired 版本、最近错误；远端服务器的 public_host 可在服务器页修改（影响订阅地址）。

**6. 兼容**：单机（无远端服务器）行为与配置/订阅字节级不变（golden 回归）。主控本机入站仍由本机直接生效。

**测试**
- 进程内端到端：主控 handler + 节点（假 Apply/真实 confgen）完成：建远端入站 → 节点生成密钥上报 → ready → 订阅包含远端节点地址与公钥；
  增删用户 → generation 递增 → 节点同步；节点生效失败 → 主控显示原因；主控不可达 → 节点保持原状态。
- 鉴权：desired / inbound-keys 只能取/改**自己**服务器的数据（越权 server_id、他服务器 inbound_id 一律拒绝）。
- 节点模式禁用本地停用调度器。
- make lint test build 通过；SPEC/API/FRONTEND 同步。审查者将在 LXD 容器节点上真机验收（端口映射到主控公网 IP）。

- **验收**：
  - [x] 同步、节点侧密钥与鉴权。进程内 HTTPS 测试覆盖两个节点的归属隔离、入站与用户变化、密钥恢复、
    订阅、失败回滚、无变化免重启及离线保留。节点保留流量采样，跳过本地停用调度。
  - [x] 面板支持创建和编辑时选服务器、显示同步状态与版本、编辑远端公网地址。新增前端组件测试。
  - [x] `make lint`、`make build` 和单机配置/订阅 golden 通过。SPEC、API、FRONTEND、CONFIG、INSTALL 已同步。
  - [x] 完整 `make lint test build`（审查者在沙箱外复跑）：含 `internal/e2e`、134 项 Vitest，CI ubuntu/macos 均通过。
  - [x] LXD 实机（2026-11-19，主控 HK VPS v0.5.0-test6，节点 bp-node1 容器，LXD proxy 映射 20443/tcp、28443/udp）：
    - 为「测试节点2」建 Reality 20443 与 Hysteria2 28443（与面板创建等价的入库 + generation 递增）；
      节点在一个心跳周期内 applied=desired=2，sing-box 监听两端口。
    - 主控库中 Reality 仅有公钥与 short_id（private_key 为空），Hysteria2 仅有证书与指纹；私钥只在节点。
    - 订阅同时包含本机两节点与 85.189.54.156:20443/28443，公钥/指纹与节点上报一致。
    - 用订阅参数起 sing-box 客户端：Reality、Hysteria2 均出口 85.189.54.156，10 MB 下载约 18 MB/s。
    - 停用用户 14 s 后节点移除该用户、连接失败；重新启用 22 s 后恢复（gen 4/4），均在 30 s 内。

### T8.4 远端流量汇总：主控统一判定超额/到期并全网停用

**目标**：用户流量 = 本机 + 所有远端服务器之和；超额/到期由主控判定，**60 秒量级**内全网停用；节点离线期间的用量在恢复联系后补报，
不丢、不重复计。单机行为不变。

**1. 上报语义（幂等）**：节点上报**累计值**而非增量：`POST /api/agent/v1/traffic`
`{counters:[{user_id, epoch, up, down}]}`。`epoch` 为节点镜像用户的 `traffic_epoch`（T8.3 镜像已在 uuid 变化/新用户时换新 epoch），
同一 epoch 内 up/down 只增不减。主控按 `(server_id, user_id)` 保存确认的 `(epoch, up, down)`，增量 = 本次 − 上次；
单调递增的 epoch 换代时从 0 计，较旧 epoch 忽略。同 epoch 下某方向回退，该方向增量为 0 并保留最高确认值，记录不含用户信息的警告。
若接受回退值为基线，后续重放会重复记账，因此真正的累计重建由新 epoch 表示。
- 新迁移 `server_user_traffic(server_id, user_id, epoch, up, down, total_up, total_down, updated_at, PRIMARY KEY(server_id,user_id))`，
  外键/删除：用户删除时一并删除其行；服务器移除时保留（历史流量保留，见 T8.2）。
- 同一事务内更新基线，以饱和加法更新跨 epoch 累计和 `users.traffic_up/down`。未知 user_id（已删除）忽略且不建基线，
  不返回错误；单请求 ≤ 10000 条，重复 user_id → 400。该路由 body 上限放宽到 1 MiB（其他 agent 路由保持 8 KiB）。
- 鉴权同 T8.3：身份来自签名认证，只写该 server_id 的基线；removed 服务器 401。
- 已知取舍（写入文档）：主控月度重置/手动清零时，节点上次上报后、重置前的少量用量会计入新周期；节点只上报主控下发过的用户 id
  由节点侧保证，主控不额外校验「是否下发给该服务器」。

**2. 节点侧**：在心跳循环（或独立 30 s 定时器，与 Flush 对齐）读取本地**已落库**的镜像用户累计值，与上次被主控确认（2xx）的
快照比较，有变化才上报；失败下次重试（累计语义保证补报）。主控离线期间节点继续计数，恢复后一次补齐。节点仍不执行本地停用调度。
节点每 10 s 采样、30 s 落库，随正常 20 s 心跳上报。同步前尽力采样，在镜像应用事务中保存被替换或移除用户的最终累计值，
直到主控确认。节点保存内核采样基线，boxpanel 服务重启后可从仍存续的 nft 计数器补差值。

**3. 主控判定**：用户 traffic 增加越过上限时，现有 `desired_user_update` 触发器递增所有远端 desired_generation，节点下轮同步停用；
本机由现有 Scheduler 停用。需保证：
- 只有远端流量使用户超额（本机无该用户流量、甚至本机没有任何入站）时，停用事件/告警（T7.9 OnStopped、80%/100% 通知）
  与本机超额时**完全一致且每周期只发一次**；补测试。
- 到期判定沿用主控（T8.3 refreshDesired 已覆盖），无需改动。
- 月度重置后用户重新有效 → 触发器递增 generation → 节点恢复该用户。

**4. API / 面板**：`GET /servers` 为每台远端服务器返回 `traffic_up/traffic_down`（该服务器全部未删除用户的跨 epoch 累计之和，
来自 `server_user_traffic`）。用户详情按服务器拆分留给 T8.6。API.md 同步。
面板服务器列表显示该服务器累计流量（简单文本即可，完整服务器页在 T8.6）。

**测试**
- 主控：累计→增量正确；同一请求重放两次只计一次；epoch 变化重新基线；回退不倒扣；未知用户忽略；越权（用他服务器签名/伪造 server_id）拒绝；
  body 超限、重复 user_id 拒绝；饱和不溢出。
- 进程内端到端（沿用 T8.3 的 HTTPS 测试框架）：节点产生用量 → 上报 → 主控用户流量增加 → 超额 → generation 递增 → 节点同步后该用户被移除；
  主控不可达期间节点累计，恢复后补报一次且不重复。
- 只远端流量导致超额时，停用通知恰好一次。
- 单机 golden 与现有 traffic 测试不变。

- **验收**：
  - [x] 主控累计、重放、乱序、epoch 换代、回退、批次回滚、未知用户、鉴权、请求上限及饱和测试通过。
  - [x] HTTPS 集成验证离线补报、丢失确认重试、节点进程重启、两节点停用和月度恢复。远端独立导致超额时，每周期只记录一次停用及阈值事件。
  - [x] 单机 golden、既有流量测试、服务器列表和面板检查通过。SPEC、API、CONFIG、FRONTEND、INSTALL 已同步。
  - [x] `make lint`、`make build`、21 项安装脚本用例、除 `internal/e2e` 外全部 Go 测试、前端类型检查与 134 项 Vitest 通过。
  - [x] 完整 `make lint test build`（审查者沙箱外复跑）：全部 Go 包含 `internal/e2e`、134 项 Vitest 通过。
  - [x] LXD 实机（2026-11-19，主控 HK VPS 与节点 bp-node1 均为 v0.5.0-test7，客户端经节点 Reality 20443）：
    - 经节点下载 20 MB：21 s 后主控用户下行 +20,079,628 B，与节点 nft 计数逐字节一致；服务器列表累计同步增加。
    - 上限设为已用 +5 MB 后经节点下载 20 MB：80%/100% 事件各记录一次；主控本机调度器停用该用户，
      节点 25 s 后移除用户、连接失败（gen 5/5）。上限清零后 21 s 节点恢复，用户换新 epoch 从 0 计。
      同周期内已有一次 `user_stopped`（T8.3 停用测试产生），按 T7.9「同一事件每周期一次」未重复记录。
    - 阻断节点→主控 70 s 期间经节点下载 15 MB：主控 +0、节点本地照常累计；恢复后 3 s 补齐 +15,125,049 B，
      与 nft 一致，65 s 后仍一致（不重复）。
    - 下载 10 MB 后 11 s（落库前）重启节点 boxpanel：同一 boot 检查点恢复，主控 +10,098,151 B，与 nft 一致。

### T8.5 多服务器订阅体验

**目标**：一个订阅里多台服务器的节点清楚、稳定、好选；客户端能直接看到本用户的全网已用流量和到期时间。单机输出（golden）除新增响应头外不变。

**1. 排序**：订阅（所有格式、分享页、管理员 links）节点按服务器分组：本机在前，其余按 `servers.id`；同服务器内按入站 id。
重名去重（`(2)` 后缀）按此显示顺序进行。单机时顺序与现状一致。

**2. 命名（保持现规则，补边界）**：有备注用备注。无备注时多台未移除服务器为「服务器名 · 协议 · 端口」，单机为「协议 · 端口」。
基础名取决于该入站与其服务器。增删无命名碰撞的其他入站不改变已有节点名。重名后缀按显示顺序分配，碰撞顺序变化时可重排。
服务器改名会改变其无备注节点名，改名处提示「客户端更新订阅后需重新选择节点」。

**3. 订阅响应头**（Clash / sing-box / 通用 base64 等所有格式）：
- `subscription-userinfo: upload=<traffic_up>; download=<traffic_down>; total=<traffic_limit>; expire=<expire_at>`。
  流量为主控汇总值（含 T8.4 远端）；不限流量时 `total=0`，不限期时省略 `expire`。
- `profile-update-interval: 12`（小时）。
- `content-disposition: attachment; filename*=UTF-8''<面板名称 percent-encode>`（Clash 用 `.yaml`），面板名称经过 RFC 5987 编码，
  不允许任何 CR/LF 或引号注入；面板名称为空时用 `boxpanel`。
- 头部只用于公开订阅，HEAD 同样返回。HTML 分享页继续内联显示，省略下载头。

**4. 可选自动测速组**（默认关闭，尊重已确定的「只有一个手动分组」）：设置项 `subscription_auto_group`（bool，默认 false），
面板「设置」页开关，说明「节点 ≥ 3 个时，在『节点选择』中增加『自动选择』（按延迟自动挑选），默认仍为手动」。
- 开启且该用户订阅节点数 ≥ 3：Clash 增加 `url-test` 组「自动选择」（url `https://www.gstatic.com/generate_204`，interval 300，tolerance 50，
  lazy true），sing-box 增加 `urltest` 出站（同 url/间隔/容差）；放在「节点选择」选项的**最后**，默认选中项仍是第一个节点。
- 「自动选择」始终是保留名，使用该备注的节点加后缀。未开启或 < 3 节点时省略自动组。
  关闭后恢复开启前的配置字节，升级时已有同名备注仍需按保留名规则分配后缀。
- 设置写入 API（PUT settings）与 API.md 同步；不进入节点期望状态（订阅专用设置，不递增 desired_generation）。

**5. 分享页**：多服务器时按服务器分组显示实际导出的节点，分组标题为服务器名。单机沿用原分享页。

**测试**
- 既有单机 golden 逐字节不变；多服务器排序与去重；增删无命名碰撞的入站时命名稳定。
- 响应头：值正确（含远端汇总流量）、不限额/不限期、面板名含中文/空格/引号/换行时的编码与防注入。
- 自动组：关闭、开启但 < 3 节点、开启且 ≥ 3 节点三种输出；Clash 与 sing-box 结构校验；保留名冲突。
- 分享页分组的前端测试。

- **验收**：
  - [x] 以上测试；`make lint test build`（审查者沙箱外复跑，含 e2e、136 项 Vitest）通过；SPEC/API/CONFIG/FRONTEND 同步。
  - [x] 实机（2026-11-19，主控与节点 v0.5.0-test8，用户 test）：
    - 响应头 `subscription-userinfo: upload=232112; download=65390212; total=0`，与主控汇总值（含 T8.4 远端）一致，不限期时无 expire；
      `profile-update-interval: 12`；Clash `filename*=UTF-8''boxpanel.yaml`，sing-box 无扩展名；HEAD 返回同样的头。
    - 节点顺序：本机 Reality 443 → 本机 Hysteria2 8443 → 测试节点2 Reality 20443 → 测试节点2 Hysteria2 28443。
    - 开启自动组：「自动选择」位于「节点选择」末尾，默认仍为首个节点；Clash url-test 参数正确；订阅 sing-box 配置 `sing-box check` 通过；
      desired_generation 不变（6→6）。关闭后 Clash 订阅与开启前逐字节一致。

### T8.6 服务器页完善与按服务器的流量明细

T8.2–T8.4 已有：生成加入命令、有效令牌与撤销、状态/版本/同步/内核/最近联系/累计流量、改名改地址、移除。本任务补齐体验：

**1. 添加向导**（替换现「添加服务器」面板，保留现有 API）：
- 步骤 1 填名称 → 步骤 2 显示命令（CopyField）+ 要点：以 root 在新 VPS 执行；需放行节点端口；NAT/多 IP 机器在命令末尾加
  `--public-ip <公网IP>`；令牌 1 小时内有效、只能用一次。
- 步骤 3「等待节点连接…」：每 3 s 轮询 `GET /servers` 与 `GET /servers/join-tokens/{id}`，按令牌返回的 `server_id` 匹配，
  待 `last_seen_at` 非空时显示「已连接：名称 / 地址 / 版本」和按钮「为它创建节点」（跳转 `/inbounds/new?server=<id>` 并预选该服务器）。
  令牌过期或被撤销 → 提示重新生成。关闭向导停止轮询。

**2. 列表概览与卡片**：
- 顶部概览：服务器总数、在线（含本机）、离线、同步失败数量（只计未移除，联系状态和同步结果独立统计）。
- 状态用徽标（在线/本机 绿，离线 琥珀，同步失败 红，待连接 灰）；最近联系显示相对时间（「12 秒前」），悬停/标题显示绝对时间。
- 卡片增加：节点数（已就绪 / 等待密钥），链接「查看节点」→ `/inbounds?server=<id>`；远端 uptime（来自心跳，若已存储；没有则不显示）。
- 版本与主控不同的远端显示提示「版本与主控不同（主控 vX）」——仅提示，兼容性判定在 T8.7。

**3. 节点页按服务器筛选**：多服务器时节点页顶部出现服务器筛选（全部 / 各服务器），与 `?server=` 查询参数同步；单机不显示。

**4. 用户详情的按服务器流量明细**（T8.4 留下的项）：
- `server_user_traffic` 增加 `user_epoch` 与 `cycle_up/cycle_down`：记录远端增量时若 `users.traffic_epoch` 与存的 `user_epoch` 不同，
  先把 cycle 清零并更新 user_epoch（月度/手动清零后从新周期计）；`total_*` 保持累计不变（服务器卡片仍显示累计）。迁移给已有行
  user_epoch=当前用户 epoch、cycle=0（升级前的周期内远端用量计入「本机及未归属」，在文档注明）。
- `GET /users/{id}` 增加 `traffic_by_server: [{server_id, name, is_local, up, down}]`：未移除的远端项为本周期 cycle 值（epoch 不一致视为 0），
  本机项 = 用户当前周期总量 − 已列出的远端之和（下限 0），因此包含本周期已移除的节点用量。仅一台未移除的服务器时省略该字段。
- 用户详情页显示明细表（服务器名、上传、下载、占比条），单机不显示。

**5. 兼容**：单机界面除概览外不变（单机可不显示概览）；API 新增字段向后兼容；API.md、FRONTEND.md、SPEC 同步。

**测试**
- 后端：cycle 在用户 epoch 变化后清零、total 不变；traffic_by_server 计算（含本机差值下限、单机省略）；迁移。
- 前端：向导三步（含轮询成功、令牌过期、关闭停止轮询，使用 fake timers）；概览计数；徽标；相对时间；节点页筛选与 URL 同步；
  用户详情明细。

- **验收**：
  - [x] T8.6 后端和前端测试、`make lint`、`make build` 通过，API、FRONTEND 和 SPEC 已同步。
  - [x] 完整 `make lint test build`（审查者沙箱外复跑，含 e2e；Vitest 145 项）。
  - [x] Chrome 浏览器实测（本地测试实例和模拟远端服务器数据）：向导、概览、卡片、节点筛选、用户明细在 1200px 和 375px 宽度下均无横向滚动。
  - [x] 审查者本地测试实例复核（`serve --dev` + 4 台模拟远端）：概览、徽标、卡片、节点筛选 `?server=`、用户按服务器明细
    （本机 = 总量 − 远端，17.2% / 65.6% / 17.2%）在桌面与 375px 下正常、无横向滚动。审查时修正两处显示：
    离线/待连接服务器的同步状态显示「等待节点上线后同步」而非「同步中」；不足 1 小时的时长显示「20 分钟」而非「0 小时 20 分钟」（补测试）。
    加入向导需 HTTPS，dev 模式无法生成令牌，由组件测试覆盖。

### T8.7 版本兼容、全服务器告警与离线告警、多服务器端到端

**1. 协议版本与升级提示**
- 新常量 `agent.ProtocolVersion`（本任务定为 2；T8.2–T8.3 视为 1，T8.4 起的流量上报属于 2）与 `MinNodeProtocol`/`MinMasterProtocol`。
  节点在注册与每次心跳中带 `protocol`；主控存 `servers.protocol`，心跳回复带主控的 `protocol` 与 `version`。
- 节点协议 < 主控最低要求：主控对 desired/traffic 等接口返回 426 `agent_upgrade_required`（心跳仍接受，以便显示状态），
  面板卡片显示「节点版本过旧，需升级」+ 升级命令；节点保持最后配置继续服务。
- 主控协议 < 节点最低要求（或新接口 404）：节点把 `sync_error=master_outdated` 报给主控（面板提示「请先升级主控」），继续用最后配置。
- 面板：任何版本与主控不同的远端卡片显示复制升级命令
  `bash <(curl -fsSL https://raw.githubusercontent.com/revfanc/boxpanel/<tag>/scripts/install.sh) update --version <主控tag>`
  （主控为非发布版时用 main 且不带 --version，同 T8.2 加入命令规则）。
- **确认 `install.sh update` 在节点（node_role=agent）上可用**：保留节点身份、密钥、镜像库与 sing-box 配置，只替换二进制并重启；
  不得运行面板初始化/引导。若当前不支持则实现，并补 install.sh 测试。

**2. 全部服务器的网卡流量告警**
- 节点现已在本地采样网卡（`server_traffic_samples`）。新增签名接口 `POST /api/agent/v1/interface-samples`
  `{samples:[{sampled_at, interface, rx, tx}]}`（rx/tx 为该样本区间增量，≤ 1000 条/批，body ≤ 256 KiB）。主控按
  `(server_id, sampled_at)` `ON CONFLICT DO NOTHING` 写入（天然幂等），清理 62 天前样本同本机。节点记录已确认的最大 sampled_at，
  断线期间累积、恢复后按批补报。
- 告警设置按服务器：现有 `servers.alert_preferences`（interface、quota_gb、billing_day、billing_mode）对远端服务器生效。
  节点需要知道用哪个网卡：心跳回复带该服务器的 `interface` 偏好（空 = 自动检测）；节点心跳上报可选网卡列表 `interfaces`（≤ 32 个合法名称）
  供面板选择。修改远端告警设置不递增 desired_generation、不重启节点内核。
- 主控对每台未移除服务器（本机 + 远端）按其计费日与口径评估 80%/100% 用量告警，事件 server_id 为对应服务器、address 为其公网地址；
  同一服务器同一周期每档只发一次（沿用 serverKey）。
- API：告警设置读写接口增加 `server_id`（缺省=本机，向后兼容）；`GET` 返回该服务器的可选网卡与当前周期用量。面板：服务器卡片「流量告警」
  入口打开该服务器的设置（复用设置页组件），设置页本机部分保持不变。

**3. 服务器离线/恢复通知**
- 远端服务器由在线变为离线（现有离线判定，90 s 无心跳）时记录事件 `server_offline`（键 `server:<id>:offline:<last_seen_at>`，
  每次离线一次）；之后恢复心跳时记录 `server_recovered`（同一离线期一次），附离线时长。removed/待连接服务器不告警。
- 设置项「服务器离线通知」（默认开启），走现有通道与 outbox；面板首页事件列表显示。主控重启后不要对已离线的服务器重复告警。

**4. 多服务器端到端测试**（进程内，真实 HTTPS handler、真实 confgen + `sing-box check`（有二进制时）/假 Apply）：
主控 + 2 节点：加入 → 每台各建 Reality 与 Hysteria2 → 节点生成密钥 → 订阅含 3 台服务器 6 个节点且顺序正确 → 两节点上报用户流量与
网卡样本 → 用户超额 → 两节点同步停用 → 一个节点停止心跳 → 离线事件一次 → 恢复 → 恢复事件一次；节点协议过旧 → 426 与面板字段。

**5. 不做**：主控备份复制到节点（路线图「可选」）留到后续，文档注明。

**测试**：上述各项的单元/接口测试；install.sh update 在 agent 角色下的测试；单机 golden 与现有告警测试不变。

- **验收**：
  - [x] 以上测试；`make lint test build` 通过；SPEC/API/FRONTEND/INSTALL 同步（见下方记录）。

**实现与验证记录**

- 协议 2 协商、升级命令、分服务器网卡样本补报与告警、离线和恢复事件均已实现。缺少协议字段的前序版本按 1 处理，先升级主控，再升级节点。
- [x] 主控加两节点的进程内 HTTPS 生命周期、接口与迁移测试、22 项安装脚本用例和 149 项前端测试通过。SPEC、API、FRONTEND、INSTALL 已同步。
- [x] `make lint` 和 `make build` 通过。
  - [x] 完整 `make lint test build`（审查者沙箱外复跑，含 e2e、22 项 install.sh、149 项 Vitest）。
  - [x] 实机（2026-11-20，主控 HK VPS v0.5.0-test10，节点 bp-node1）：
    - 仅升级主控：旧节点（协议 1）心跳 200，拉取期望状态 426（applied 6 / desired 7），按最后配置继续服务。
    - 节点 `install.sh update`（本地构建替换下载）：主控连接、入站密钥、sing-box 配置与 hy2 证书摘要前后一致；升级后协议 2、
      生效 7/7，上报网卡 `eth0`，一次补报 1055 条历史分钟样本。
    - 节点额度 1 GB（入站口径）：经节点下载 600 MB 后 72% 无告警；再下载 600 MB 后 1.32 GB，80%/100% 事件各一次，地址为节点公网地址。
    - 停止节点 boxpanel：约 100 s 后 `server_offline` 一次；离线期间重启主控不重复；启动节点后 `server_recovered` 一次（离线 133 s），
      70 s 后无重复。未配置通道时不入 outbox（与既有设计一致）。

### T9.1 表单控件统一使用 UI 库组件

**问题**：部分表单控件是原生元素手写样式，未用 shadcn 组件：下拉箭头贴右边、单选/复选为浏览器默认样式，与其余界面不一致。

**范围**（`web/src` 中 components/ui 以外的全部原生控件）：
- `<select>`：节点页服务器筛选（inbounds/index）、节点编辑器的服务器选择（inbounds/editor）、分流规则预览格式与规则源（rules/index）、
  告警设置计费口径（AlertSettings）。
- `<input type="radio">`：引导场景选择（setup/steps）、节点协议选择（inbounds/editor）、分流预设（rules/index）。
- `<input type="checkbox">`：引导 Hysteria2 选项（setup/steps）。
- `<textarea>`：CopyField。

**做法**：
- 在 `components/ui` 按 shadcn（new-york，使用已安装的 `radix-ui` 统一包）新增 `select.tsx`（Radix Select：Trigger 带 ChevronDown 图标与右内边距，
  Content/Item/Value，键盘可用）、`radio-group.tsx`、`checkbox.tsx`、`textarea.tsx`，样式与现有 input/button 一致（高度 h-9、圆角、focus ring、
  disabled、aria-invalid 红框、暗色）。
- 替换上述全部用法。react-hook-form 字段用 `Controller`/`setValue` 接入 Select/RadioGroup/Checkbox，保持现有校验与提交行为、id/label 关联
  （`<Label htmlFor>` 指向 Trigger 或 RadioGroupItem）与无障碍名称。卡片式单选（协议/预设/场景）保留整卡可点击与选中高亮，内部用 RadioGroupItem。
- 「自动检测网卡」的 `Input + datalist` 可保留（属于可输入建议），但确认样式与 Input 一致。
- 不改变任何文案、接口与业务逻辑。

**测试**：更新受影响的组件测试改用组件的角色交互（combobox/option、radio、checkbox）；jsdom 中 Radix Select 需要的 `hasPointerCapture`/
`scrollIntoView` 等在测试 setup 中补 polyfill。新增断言：Select Trigger 含图标且有右内边距类（防止箭头贴边回归）；
`grep` 级检查：`web/src` 中 `components/ui` 以外不再出现 `<select`、`type="radio"`、`type="checkbox"`、`<textarea`（可写成一个 vitest 测试扫描源码）。

- **验收**：
  - [x] `make lint test build` 通过（审查者沙箱外复跑，含 e2e；Vitest 150 项，含原生控件残留扫描与箭头内边距断言）；FRONTEND.md 组件清单同步。
  - [x] 浏览器（本地测试实例）桌面与 375px、暗色：节点页服务器筛选、节点编辑器服务器选择与协议卡片、分流预设卡片外观统一，下拉箭头有内边距、选中项带勾，无横向滚动。
    审查时顺带修正：节点编辑器服务器选项对本机只显示名称（原为「本机 · 本机」）。

### T9.2 按用户限定可用服务器

**问题**：启用的用户一律加入所有服务器的所有入站，无法让某些用户只用部分服务器。

**做法**：
- 迁移 0010：`users.all_servers`（默认 1，升级前的用户行为不变）与 `user_servers(user_id, server_id)`；删除用户级联清理。
  触发器：`all_servers` 变化使全部远端期望版本递增，`user_servers` 增删只使对应远端递增。
- 主控本机 snapshot、远端 `desiredUsers`、订阅 `Store.Load` 都按「`all_servers=1` 或在 `user_servers` 中」过滤（`db.UserServerAccess`）。
  节点协议不变：节点镜像收到的就是过滤后的用户，旧节点无需升级；被移出的用户由既有镜像退役流程补报流量。
- API：用户响应带 `all_servers`、`server_ids`；POST/PUT 接受二者并校验（见 API.md）。只改动变化的行，未受影响的节点不重启。
- 面板：用户表单「可用服务器」复选组；文案在 zh-CN.ts。

**测试**：迁移与触发器（期望版本递增范围、级联删除）、`desiredUsers` 按服务器过滤及新增服务器对全部用户可见、
订阅按可用服务器过滤、用户 API 校验与读写、前端表单提交与校验。

- **验收**：
  - [x] `make lint test build` 通过（Linux 容器内：Go 全部包、install.sh 用例、Vitest 151 项）；SPEC/API/CONFIG/FRONTEND 同步。
  - [x] 实机（2026-11-22，HK 测试主控 + LXD 节点 bp-node1，二者为本分支构建）：全部服务器的用户同时出现在两侧内核配置与订阅（4 个节点）；
    仅本机的用户只在主控配置、订阅 2 个节点；只改备注并重存同一选择时节点期望版本不变、sing-box 未重启；加入节点后期望版本递增、
    节点配置出现该用户并重启一次、订阅变为 4 个节点；仅选节点时主控配置移除该用户；空选择与已移除服务器返回 400。

### T9.3 节点自动跟随主控升级

**问题**：主控升级后，每台节点都要手动复制升级命令执行。

**做法**：
- 迁移 0011：`servers.auto_upgrade`（默认 0）。`PUT /servers/{id}` 可修改（本机 400），不递增期望版本。
- 心跳回复增加可选 `auto_upgrade`；主控版本已在回复中。旧节点忽略未知字段，节点到主控的消息不变，协议版本仍为 2。
- 节点：`agent.NewerRelease(master, self)` 为真时，下载同 tag 的 install.sh，用 `systemd-run` 在独立单元中执行
  `update --version <tag>`（校验 checksums、失败恢复原二进制均由脚本负责）。仅限 `/usr/local/bin/boxpanel` 布局；
  `agent_upgrade_attempt` 记录尝试，同一 tag 6 小时内不重试；开发构建与降级从不触发。`boxpanel info` 显示最近尝试。
- 面板：远端编辑表单开关、卡片状态与等待提示。

**测试**：版本比较表、心跳回复字段、升级器（关闭/相同/较旧/开发版不触发；命令参数；退避与重启不重复；新目标立即尝试；
下载失败与非安装布局不执行）、服务器 API 读写与本机拒绝、前端开关。

- **验收**：
  - [x] `make lint test build` 通过（Linux 容器内）；API/INSTALL/FRONTEND 同步。
  - [x] 实机失败回滚（2026-11-22，HK 测试主控标为 v0.3.1、LXD 节点 bp-node1 标为 v0.3.0，均为本分支构建）：对本机开启返回 400；
    对节点开启后约 17 s 节点下载官方 v0.3.1 的 install.sh 并启动 `boxpanel-upgrade-v0.3.1`；官方 v0.3.1 不识别迁移 11，
    脚本恢复原二进制，节点回到 v0.3.0，boxpanel 与 sing-box active、心跳正常；`boxpanel info` 显示该次尝试；之后节点重启也只尝试 1 次。
  - [x] 实机升级成功：需发布两个含 T9.3 的预发布版本（如 rc.1 → rc.2），节点装 rc.1 并开启后，主控升级到 rc.2 时节点自动升级到 rc.2。
    2026-11-26 实机：LXD 节点 bp-old（rc.1）开启后，主控当时为 rc.2，节点 04:53:19 自动升到 rc.2；主控随后用发布版脚本升级到 rc.4，
    节点 04:53:41 再次自动升到 rc.4，各次由一次性 `boxpanel-upgrade-<版本>` 单元执行并退出，服务未反复重启。
    未开启的 bp-node 保持 rc.1。经节点的负载全程未中断。

### T9.4 主控迁移命令

**问题**：更换主控 VPS 只能手动备份、复制文件、恢复。用 IP 加入的节点仍连旧地址，只能 `leave` 后重新加入，
得到新服务器 ID、重建入站、换 Reality 密钥，用户授权需重选，所有客户端都要更新订阅。备份与切换之间的变更也会丢失。

**目标**：旧主控生成一条迁移命令，在新 VPS 执行后自动完成数据转移、节点切换和旧主控退役；节点的服务器 ID、入站、
密钥、用户授权不变，节点线路不中断。旧主控已失效时有手动兜底。

**做法**：
- 信任依据：节点用 `panel-fallback.key` 的 SPKI 指纹验证地址列表签名。新主控沿用同一把回退密钥，
  各地址的 TLS 仍按 T8.2b 使用 pin 或系统 CA 与主机名校验，节点用原身份签名心跳。
- 迁移令牌：设置页「备份与迁移 → 迁移主控」生成一次性令牌（30 分钟有效，同一时刻最多一个），命令形如
  `install.sh --migrate-from '<旧主控 URL>' --token '<令牌>' --master-pin '<指纹>' [--public-ip <IP>]`，
  脚本取旧主控同一 tag 并指定 `--version`（与 T9.3 相同规则）。`--migrate-from` 不能与 `--join`/`--restore`/`update`/`--port` 混用。
- 新机流程：安装同版本 → 以 pin 连接旧主控并凭令牌认领 → 旧主控进入**冻结**（面板只读；节点流量上报返回暂不确认，
  节点继续本地累积，沿用补报机制）→ 旧主控当场生成快照（与 T7.5 备份同一内容与校验）经 TLS 流式传输 →
  新机走 T7.5 的恢复与生效流程，沿用管理员、UUID、token 和密钥 → 按 TCP 对端或 `--public-ip` 确定新机公网地址，
  IP 模式更新 `public_host`，域名模式保留原域名供切 DNS → 按恢复的策略维护证书、放行端口、启动面板与本机入站 →
  本机 HTTPS 自检后报告新 URL，由旧主控从外部验证就绪。
- 节点切换：心跳回复增加可选 `master_urls`（按优先级），由主控回退密钥签名。节点验证签名后试连新地址，
  pin 校验与签名心跳都成功才设为首选，旧地址保留为备用；节点支持保存多个主控地址并按顺序回退。旧节点忽略该字段，协议版本不变。
- 旧主控退役：收到就绪后停本机入站，在心跳中下发新地址。机器订阅请求 301 到新地址，301 响应及跟随后返回的订阅都插入
  「⚠ 订阅地址已变更」提示节点，直接访问新订阅时省略。分享页保持可读并显示新链接。面板显示「已迁移 x/y 台节点」，
  全部切换后提示可关机。退役状态持久化，重启后保持。
- 失败与回退：新机在就绪前失败或超时（令牌有效期内）时旧主控自动解冻、令牌作废，可重新生成；新机清理已安装内容。
  就绪后不自动回退。未切换的节点（如离线）在新主控面板给出 `boxpanel set-master` 命令。
- `boxpanel set-master <URL>`（节点）：保存前试连，pin 校验与签名心跳成功才写入，失败保持原地址；保留身份、服务器 ID 和密钥。
  旧主控已失效时：每日备份 + `install.sh --restore` + 各节点 `set-master`。
- 面板：IP 模式下在引导与设置页提示「更换主控时客户端需手动更新订阅地址，建议使用域名」。迁移前确认框说明冻结、
  中断时长与 IP 模式的订阅影响。

**测试**：令牌签发/过期/单次使用；冻结期间写接口拒绝与流量不确认；快照传输中断与校验失败；新机失败后旧主控解冻；
`master_urls` 签名校验（伪造、错误密钥、旧节点忽略）、多地址回退、`set-master` 成功与失败不写入；
退役状态的 301、提示节点、进度计数与重启保持；补报流量在新主控入账且不重复；安装脚本参数互斥。

- **验收**：
  - [ ] 完整 `make lint test build` 通过。macOS 沙箱内 `make test` 的两个既有 sing-box 回环用例需要在 Linux 或允许注册 DNS 通知的环境复跑。
  - [x] 实机（测试主控 + 新 VPS + LXD 节点，主控用 IP）：执行迁移命令后节点自动切到新主控，服务器 ID、入站与密钥不变，
    迁移期间节点线路可用，冻结期间节点流量补报到新主控且总量一致；旧主控 301 与提示节点生效。
  - [x] 实机失败路径：新机就绪前中断，旧主控解冻并恢复正常；离线节点上线后用 `set-master` 切换成功。
  - [x] 实机域名模式：迁移后改 DNS，客户端无需任何操作。

**T9.4 实现与验证记录（2026-11-25）**

- [x] 令牌签发、替换、过期与并发单次认领，旧主控写入冻结、用户流量与网卡样本暂缓确认。覆盖传输截断、SHA-256 校验失败、新机自检失败和解冻。
- [x] 进程内 HTTPS 双主控用例覆盖快照恢复、签名地址切换、优先地址故障时回退、`set-master`、补报去重、节点进度与退役持久化。
  订阅测试覆盖三种格式的 301、警告节点及旧分享页。安装器覆盖参数互斥、标准安装、失败清理，前端覆盖确认与进度。
- [x] SPEC、API、INSTALL 和 FRONTEND 已同步。协议仍为 2，迁移控制记录与进度从恢复快照中剔除，保留节点身份与流量账目。
- [x] `make lint`、`make build`、Linux arm64 CLI 测试二进制交叉编译通过。`make test` 中 25 项安装脚本和除 `internal/e2e` 外的 Go 包通过，
  已使用锁定版 sing-box 校验配置。`TestLoopback` 和 `TestTrafficLoopback` 启动 sing-box 时在 macOS 沙箱内注册 DNS 配置通知返回 `status 9`。
  另行运行前端类型检查、lint 和 154 项 Vitest，均通过。
- [ ] 人工验收需旧主控、新 VPS 和 LXD 节点运行同一个包含 T9.4 的版本。先记录 `/servers`、用户授权、入站密钥指纹、节点内核状态和流量基线，
  再在 IP 模式运行迁移。冻结时持续经 LXD 节点产生流量，确认线路可用，交接后核对补报总量及重试不重复。检查旧机 GET/HEAD 返回 301、
  三种订阅提示节点、分享页新链接和进度，重启旧机后确认内核继续停用。另在新机就绪前中止安装，验证旧主控恢复写入和新机清理，
  并为离线节点运行 `boxpanel set-master`。最后使用同域名做一轮，改 A/AAAA 记录后使用已导入的原订阅测试更新和连通。
- [x] 实机 IP 模式（2026-11-26，`v0.5.0-rc.1`，Ubuntu 22.04 旧主控 + 全新 Ubuntu 22.04 VPS + 旧主控上的 LXD 节点）：
  迁移命令 8 步完成，节点下一轮心跳即连到新主控，`server_id`、入站和 Reality 公钥不变，新主控迁移进度记为完成。
  迁移全程经节点每 5 秒下载 1 MB，250 次全部成功；新主控用户总量与基线加负载量吻合，无丢失或重复。
  旧机三种订阅 GET/HEAD 均 301 到新主控并带 `bp_migrated=1`，旧响应和新主控带标记的响应含「订阅地址已变更」提示节点，
  不带标记时不含；面板页面仍为 200（符合 API.md，只对机器订阅 301）。重启旧机 boxpanel 后 `boxpanel-singbox` 保持停用且未启用。
  首次尝试时新机连不上旧主控面板端口：云厂商入站过滤（非本机防火墙），安装器在第 7 步报错并清理新机，旧主控停在 issued 未冻结；
  放行 TCP 面板端口后重跑成功。提示：两台机器都要在云安全组放行面板端口。
- [x] 实机域名模式（2026-11-26）：主控 `set-host` 到域名后自动签发 Let's Encrypt 证书，迁移到新机后沿用域名与证书。
  A 记录改到新机后，原域名订阅三种格式直接返回 200，证书校验通过且不含提示节点，客户端无需操作。
  注意：新机与运维机所在解析器可能在 TTL 内缓存旧 IP，旧记录未过期前新机认领会连回自己而失败；可等 TTL 或临时 hosts。
- [x] 实机失败路径（2026-11-26）：认领并收到快照后 `kill -9` 新机的 `boxpanel migrate`，安装器清理新机，旧主控进入 frozen：
  写接口返回 409 `migration_read_only`，节点线路与负载不中断；令牌到期后接口即恢复 active、写入恢复。
  新机在认领后自身失败时（下述残留锁导致）会主动撤销，旧主控立即恢复 active。
- [x] 实机离线节点（2026-11-26）：迁移期间停止节点，迁移后启动，它向退役旧主控发一次心跳即获签名地址并自动跟随，无需操作；
  另对其运行 `boxpanel set-master <新主控 URL>`，下一轮心跳切换成功，原地址保留为备用。
- [x] 实机分享页（2026-11-26）：退役旧主控对浏览器返回 200 分享页，提示地址已变更，订阅地址与三种导入链接指向新主控；无效令牌 404。
- [x] 已修复（2026-11-26）：`kill -9` 中断 `boxpanel migrate` 后，新机残留 `/etc/boxpanel.restore-lock` 目录和
  `/usr/local/share/boxpanel/.boxpanel-singbox-install.lock`，下次迁移报 `restore busy` 或 `lock installation`，需手动删除。
  两处改用 `filelock.TryAcquire`（flock，进程退出即由内核释放，正常结束时持锁删除文件），残留文件不再阻塞；
  旧版本留下的空 `.restore-lock` 目录自动清除；仅在另一进程仍持锁时报中文「请等待其结束后重试」。
  测试覆盖残留文件、并发占用及 SIGKILL 持锁子进程，并在 Linux 实机运行 filelock 测试通过；修复随下一个版本发布。
- [x] 已修复（2026-11-26）其余实机发现：
  1. 进度：`scripts/install.sh` 不再预先打印 [5/8]–[7/8]，由 `boxpanel migrate` 在认领、校验恢复、启动就绪时各自输出。
  2. 手动解冻：新增 `POST /api/v1/migration/cancel` 与设置页「中止迁移」（issued/frozen 可用，冻结期间不受只读拦截），
     立即解冻并作废交换标识，仍在运行的新机确认就绪时被拒并回滚；已退役返回 409。维护任务把到期的 issued/frozen 记录持久清空。
  3. 中断：安装器以受跟踪子进程运行 `boxpanel migrate`，INT/TERM/HUP 时先 SIGTERM 子进程（撤销认领、回滚）再清理；
     `boxpanel migrate` 处理 SIGHUP，并在 Linux 设置父进程退出信号，安装器被 SIGKILL 时同样撤销认领；残留安装时提示先 uninstall --purge。
  4. 登录会话：保持迁移撤销全部会话（与恢复一致，令牌不跨机传输），在确认框、退役提示和完成输出中说明需用原账号密码重新登录。
  测试：迁移服务覆盖中止 issued/frozen、作废后 ready/claim 被拒、退役不可中止、到期持久清空；HTTPS 生命周期覆盖 cancel 鉴权、确认和冻结可达；
  安装器新增中断用例（TERM 安装器时子进程在数据仍在时收到 SIGTERM，随后清理）；前端覆盖中止确认流程；
  父进程退出信号测试在 Linux 实机通过。`make lint test build` 通过（25 个 Go 包、26 项安装脚本、162 项 Vitest）。修复随下一个版本发布。
- [x] rc.2 实机复测（2026-11-26，`v0.5.0-rc.2` 主控 → 全新 VPS，域名模式）：
  1. 认领后 `kill -9` 新机 `boxpanel migrate`，旧主控 frozen，点「中止迁移」后立即 active，写接口恢复（创建/删除用户 201/204）；
     新机残留的 `.restore-lock` 为 flock 文件，下一次迁移未被阻塞。
  2. 新机已启动恢复面板、即将确认就绪时暂停，旧主控中止后恢复新机：确认就绪被拒（令牌无效），新机回滚，安装器清理全部文件，旧主控保持 active。
  3. 认领后 SIGTERM 安装器：`boxpanel migrate` 收到信号撤销认领，旧主控立即 active，新机清理完毕。
  4. 认领后 SIGKILL 安装器：`boxpanel migrate` 经父进程退出信号撤销认领，旧主控立即 active；安装器未能清理，残留二进制与 rc.2 的
     `/run/boxpanel-installer.lock` 目录，下一次安装被该锁拦截。
  5. 第 5–7 步进度由 `boxpanel migrate` 按实际阶段输出。
- [x] 已修复（2026-11-26，rc.2 之后）：安装器自身锁改用 `flock(1)`（`/run/boxpanel-installer.flock`，fd 9，`boxpanel migrate` 继承），
  被强杀后不残留阻塞；无 flock 时退回 mkdir。Linux 实机验证：仅存旧目录锁时卸载正常完成，另一进程持锁时报「另一个安装或迁移进程正在运行」。
  rc.3 构建因安装脚本测试在 Linux CI 上误用真实 flock 失败（只有 tag，无 release），测试改为可强制走 mkdir 退回后发布 rc.4。
  rc.4 冒烟（2026-11-26）：主控用发布版脚本 `update --version v0.5.0-rc.4` 八步完成，更新期间 flock 被持有、结束后释放，未创建目录锁，
  面板与内核服务 active，两个节点在线；main CI（macOS/Ubuntu）通过。

### T9.5 以服务器为中心的面板重构与统一流量展示

**问题**：多服务器能力叠加在单机面板上，界面割裂。概览的 CPU/内存/磁盘/网速、内核状态与重启、计费卡片只反映主控；
主控卡片没有流量和内核状态，远端计费要展开「流量告警」才可见；日志只有主控；告警设置分散在设置页和服务器卡片；
「节点」一词同时指入站和远端服务器；设置页堆叠过多。节点心跳也没有系统指标与日志，前端无法统一展示。

**信息架构**：导航为 概览 / 服务器 / 用户 / 分流规则 / 设置。「节点」导航移除，入站统一称「入站」，「节点」只指远端服务器。
- 概览（全局）：本周期全部服务器用户总流量及各服务器占比；每台服务器一行（在线/同步状态、CPU、内存、实时网速、
  计费用量与额度百分比）；用量最高、即将到期或超额的用户；未读告警。移除重复导航的快捷入口。
- 服务器详情 `/servers/:id`：本机与远端同一界面（本机仅是 `is_local`），标签为
  状态（CPU/内存/磁盘/网速、sing-box 状态与重启、版本与升级、运行时长）、入站（本服务器入站列表，在此新建/编辑）、
  流量（网卡计费用量与额度、各用户在本服务器的周期用量，两种口径并列说明）、告警（额度、计费日、口径、阈值，本机与远端同一表单）、日志。
  `/inbounds` 保留为全部入站的筛选视图，旧链接 `/inbounds?server=<id>` 重定向到对应服务器的入站标签。
- 用户详情：订阅（二维码、链接、单节点 URI）/ 流量（总量 + 按服务器明细）/ 设置（限额、到期、重置日、可用服务器）。
- 设置（仅全局）：面板（地址、证书、账号）、订阅（自动选择）、通知通道、备份与迁移（含 T9.4 入口）。

**后端**：
- 节点**每次心跳**携带系统指标：CPU（1 位小数）、内存与磁盘的已用/总量（MB）、网卡上下行速率；可选字段，旧节点省略时界面显示「需升级节点」。
  主控只保存最新一份，不落历史。增量约 200 字节/次，每台节点每月约 20 MB 以内。
- 远端日志按需上报（与每次心跳都带的系统指标不同，平时不传日志）：节点不开放入站端口，主控只能在心跳回复里提出请求。
  打开服务器日志标签时，主控在下一次心跳回复中请求日志，节点上传最近 200 行（脱敏规则与本机日志一致），并进入快速模式，
  每 3 秒只上传上次之后的新增行；主控在页面持续查看时续期请求，离开页面或 60 秒未续期后节点回到普通心跳。
  首屏最长等待一个心跳间隔，界面显示「正在向节点请求日志」。
- 远端重启内核：通过期望状态下发一次性重启指令，结果经心跳回报。
- 统一按服务器的接口：系统状态、计费用量、本周期用户流量、告警设置均以 `server_id` 访问，本机与远端返回同一结构；
  概览接口返回全部服务器汇总。

**测试**：心跳指标字段（含旧节点缺省）、日志请求、快速模式增量上传与 60 秒过期（含脱敏、离页停止）、远端重启指令幂等、按服务器接口对本机与远端一致、
概览汇总与用户合并用量一致；前端路由与旧链接重定向、服务器详情各标签、本机/远端同一表单、移动端布局。

- **验收**：
  - [ ] `make lint test build` 通过；SPEC/API/FRONTEND 同步，文案在 zh-CN.ts。
  - [ ] 实机（主控 + LXD 节点）：概览显示两台服务器的指标与计费，数值与各自 `boxpanel info`/系统工具一致；
    远端日志约 30 秒内可见；远端重启内核成功；各页面不再出现只含主控数据却未标注的流量数字。
  - [x] UI 底层改用 Base UI 并移除 Radix：shadcn 切到 Base UI 风格重新生成 `components/ui/`，`asChild` 改为 `render`，
    核对回调签名，`package.json` 不再依赖 `radix-ui`；同步 AGENTS.md 技术栈、`components.json` 与 FRONTEND.md 组件清单。

**T9.5 实现与验证记录（2026-11-25）**

- [x] 协议继续使用 2。进程内 HTTPS 测试覆盖新旧心跳、仅保留最新采样、租约续期、离页停止、60 秒过期、按游标增量、批次重试去重、
  日志归属和本机同规则脱敏。远端重启复用完整生效流程，覆盖结果丢失、节点进程重启、校验失败、旧结果重放和中断。
- [x] 本机和远端共用状态、流量及告警接口与详情页。测试覆盖两个流量口径、清零 epoch 归属、移除服务器后的用量以及概览与用户总量对账。
  前端覆盖 history 深链、旧入站链接重定向、五个服务器标签、同一告警表单、用户标签、窄屏导航及可换行布局约束。SPEC、API、FRONTEND 已同步，文案集中于 zh-CN.ts。
- [x] `make lint`、`make build` 通过。pnpm 12.18.0 使用原供应链保护，`@base-ui/react@1.9.0` 发布时间为 2026-10-09。
- [ ] 完整 `make test`。已用锁定版 sing-box 1.14.4 运行，25 项 install.sh 用例及其余 Go 包通过，包含服务端配置与订阅回归。
  两个既有回环用例 `TestLoopback`、`TestTrafficLoopback` 在 macOS 沙箱内启动内核时，注册 DNS 配置通知返回 `status 9`。
  另行运行前端类型检查、lint 和 161 项 Vitest，均通过。回环用例需在 Linux 或允许该通知的环境复跑。
- [ ] 人工验收步骤（真实 Linux 主控与 LXD 节点）
  1. 先升级主控，保留一台旧节点，确认其仍在线、原入站可用，状态显示「需升级节点」。随后升级节点，确认下一轮心跳显示 CPU、内存、磁盘、网速和版本。
  2. 在概览和两台服务器的流量标签核对各自网卡计费。用 `boxpanel info` 核对服务器身份及内核版本，用 `top`、`free -b`、
     `df -B1`、`ip -s link` 核对指标和接口计数。令测试用户在两台服务器产生流量，等待落库后核对用户总量与各服务器分项。
  3. 打开远端日志，核对约 30 秒内返回最近 200 行，与节点的 `journalctl -u boxpanel-singbox -n 200` 对照。
     产生新的日志后核对约 3 秒内追加。分别切换标签和中断页面连接，核对节点在最后一位查看者离开或 60 秒未续期后恢复普通心跳。
  4. 从远端状态标签重启 sing-box，在日志及 `systemctl status boxpanel-singbox` 核对只重启一次，面板显示完成且连接恢复。
  5. 以 375px 和桌面宽度打开概览、五个服务器标签及用户流量。检查无横向页面溢出、旧入站链接重定向及本机流量口径标注。
     当前环境的浏览器访问宿主不可用，实际布局留待上述复核。
- [x] 实机部分验收（2026-11-26，`v0.5.0-rc.1`，Ubuntu 22.04 主控 + LXD 节点）：节点详情显示 CPU、内存、磁盘、网速、内核版本与运行时间。
  打开日志标签后下一轮心跳返回日志；在节点重启 sing-box 后 4 秒内追加新行；离页约 60 秒后节点回到 20 秒普通心跳。
  面板远端重启后节点 `boxpanel-singbox` 只重启一次，页面显示「sing-box 已重启」。
  已知小问题：重启结果回报的同一轮心跳里内核运行时间仍是重启前采样，最多滞后一轮心跳。
- [x] 实机旧节点兼容（2026-11-26）：v0.4.0 节点可加入 rc.1 主控并保持在线，无指标时显示「需升级节点」；
  `install.sh update --version v0.5.0-rc.1` 后下一轮心跳即上报指标。
- [x] 实机指标核对（2026-11-26）：节点上报内存、磁盘、运行时间与 `free -b`、`df -B1`、`/proc/uptime` 一致（差异仅为采样时刻），
  网速与经节点的负载吻合；概览三台服务器显示指标、用户流量占比与网卡计费。
- [ ] 仍待实机：第 2 步用户在两台服务器分别产生流量后的分项对账，第 5 步实机 375px 窄屏（Chrome 窗口无法缩放，本地开发面板已测无溢出）。

### 安全加固：扫描指纹与 Reality 中转（2026-11-26）

- [x] 面板自签证书主题由 `CN=boxpanel` 改为主机本身，旧证书以同一备用密钥重签（固定指纹不变）。实测：旧版对无 SNI 的 IP 访问返回 `CN=boxpanel`。
- [x] Reality：候选去掉 `www.cloudflare.com`、`www.amazon.com`；检测/推荐/新建/修改拒绝 Cloudflare 托管目标，入站列表提醒既有此类目标。
  实测测试节点目标 `www.microsoft.com`（Akamai）对 apple/bing/cloudflare SNI 返回 403/400，不能被当中转。
- [x] Hysteria2 未启用混淆时默认 masquerade 反代 `https://www.bing.com/`，golden 经 sing-box 1.14.4 check。
- [x] 实机（2026-11-26，`v0.5.1-rc.1`）：主控升级后无 SNI 的 IP 访问证书由 `CN = boxpanel` 变为 `CN = 85.189.54.156`，
  域名访问仍为 Let's Encrypt，两节点保持在线（固定指纹未变）；bp-old 自动跟随升级。节点新建未混淆的 Hysteria2 入站后，
  配置含 `masquerade{proxy, https://www.bing.com/}`，HTTP/3 探测 `/` 返回 200「Search - Microsoft Bing」，`/auth` 返回 Bing 的 404。
  推荐仅返回 apple/microsoft/bing；检测 `www.cloudflare.com` 为 `ok:false` 并给出中转风险提示；把既有入站改为 `discord.com`
  或新建 `www.cloudflare.com` 目标均返回 400。
- 未做（按需）：登录限速按 IPv6 /64 聚合并避免满表时误伤管理员；Reality 前置 SNI 过滤（会削弱抗探测，不默认）。

### T9.6 出口服务器

**问题**：线路好的 VPS 和 IP 质量好的 VPS 往往是两台机器。用户希望保留现有入口和订阅，同时让 AI、流媒体等服务使用另一台机器的公网 IP。

**目标**：客户端继续连接入口的用户入站。入口按服务器端规则把命中的流量经加密链路送到出口，其余由入口直连。也可选择让全部符合安全规则的用户代理流量走出口。

**方案**：

- 主控和已加入、支持本功能的节点均可担任入口或出口。每个入口可选一个不同的出口，同一出口可服务多个入口。内部入站抵达出口后只允许本机出网，每条用户流量最多转发一跳。
- 链路选用 Shadowsocks-2022 `2022-blake3-aes-256-gcm`，同时支持 TCP、UDP，不需要 Reality 的握手站点、域名或证书。每对入口和出口使用 `crypto/rand` 生成独立的 32 字节预共享密钥。出口自动分配端口，分别检查数据库预留、Hysteria2 跳跃范围、面板与控制端口及最新的监听端口信息。入口和出口使用同地址族的公网 IP。公网 IP 默认沿用登记地址，可在出口标签修正 NAT 等场景的地址。
- 出口在 nftables 建立专用 TCP/UDP 源地址过滤，并为已启用的宿主防火墙放行受保护端口。过滤在内核监听前生效，sing-box 对内部入站再检查源地址。修改、停用和更换时，两端通过原有校验、原子替换、存活检查和回滚流程生效。远端由期望状态收敛。删除服务器时撤销其常规身份并保留仅用于确认移除的公钥，节点重连后清除旧链路。无法恢复通信的节点可在停止服务后手动清理，见 INSTALL。
- 默认为「分流」，可选「全部」。分流使用内置 AI（OpenAI/ChatGPT、Anthropic/Claude、Gemini 等）和流媒体（Netflix、Disney+ 等）域名后缀，以及管理员填写的精确域名、域名后缀。域名规则以 sing-box 1.14 `domain` / `domain_suffix` 生成，不依赖远程规则服务。规则独立于 `/rules` 的客户端订阅规则和存储。
- 默认故障策略为「阻断」，选配「回退直连」。入口每 30 秒用内部专用出站发起 HTTPS 探测；探测不随回退切换。连续两次失败后按策略变更路由，成功后恢复。面板显示探测结果、时间及是否已回退；告警通过既有面板事件和通知渠道发送，并按故障与恢复去重。
- 按用户的流量仍在入口用相同的内核计数方式记账，走链路时计入口连接的链路字节。出口用独立标记统计实际向目标发送和收到的累计字节（含探测），同时保有用户流量和网卡计费两个原口径。
- 外部手填 SOCKS5 / Shadowsocks 代理留作后续；L4 端口转发另立 T9.7。

**数据模型**：迁移 `0014` 保存每个入口的出口关系、生成的内部端口和服务凭证、规则、故障策略及最近一次探测。地址变更更新关系修订号，旧探测失效。服务器行另存链路公网地址、远端端口采样和出口累计流量。节点本地 SQLite 镜像只持有与自己有关的链路及本机探测、计数状态。主控迁移和备份保存关系、端口与凭证；T9.4 新主控按交接地址更新本机链路 IP，触发相关节点重新同步。

**API**：`GET /api/v1/servers/{id}/exit` 返回能力、可选服务器、设置、状态与出口累计流量；`PUT` 保存关系，`DELETE` 停用；`PUT /api/v1/servers/{id}/exit-address` 更新该服务器的链路公网 IP。面板 API、预览、用户订阅、用户入站列表都不返回内部凭证，凭证仅经签名认证的节点期望状态交付给两端。节点通信协议报 3，普通同步的最低要求仍为 2。只有协议 3+ 节点可新建链路，主控仅向它们下发独立的 `exit` 字段；心跳新报表同样按协议能力发送。

**配置生成**：先保护内部入站的源地址，再执行 sniff 和可选 BT 拒绝。入口先拒绝用户指定的私有 IP，在本机解析前按用户入站和 `auth_user` 匹配出口规则。命中的域名交给出口解析，未命中或回退直连的域名由入口解析。两端都在本机出网前检查解析结果并拒绝私有地址。内部入站使用出口计数标记，用户出站保留原用户标记，最终路由仍为 direct。

**界面**：服务器详情增加「出口」标签，显示可用候选及需升级的节点、链路地址、分流与规则编辑、故障策略、立即可见的保存和停用状态、探测结果及下一步提示。「流量」显示作为出口转发的累计流量。

**测试**：配置 golden 覆盖分流、全部、阻断、回退、源地址和安全规则顺序，用锁定版 sing-box 校验。覆盖 API、凭证隔离与脱敏、端口预留、更换/停用/删除、旧节点兼容、探测和告警、累计流量去重、迁移快照；前端覆盖表单及出口状态。有可运行内核时验证双实例链路回环。

- **验收**：
  - [x] 规格、实现、自动化测试和 `make lint` / `make test` / `make build` 验证完成。Codex 的 macOS 沙箱跳过了回环用例；沙箱外 `TestExitLoopback`（直连、自定义出口域名、AI 经出口）与 `TestLoopback` 用锁定 sing-box 1.14.4 实跑通过。
  - [x] 实机两台 VPS：分流与全部模式的 TCP/UDP 流量从预期公网 IP 出网，其他公网地址连不到内部端口，客户端原订阅可用。
    rc.2 公网实测：原订阅地址返回 200，重新获取的 VLESS 节点参数与升级前保存的完全相同。分流时，命中规则的 HTTPS 和 UDP 查询均显示出口 IP，未命中的显示入口 IP；全部模式均显示出口 IP。UDP 通过同一台权威 DNS 的来源地址回显验证。出口外网接口从非入口公网源收到 TCP SYN 和 UDP 报文，未发回 SYN-ACK 或 UDP 应答。测试后已恢复原模式及完整规则。
  - [x] 实机停止/恢复出口：确认阻断、直连回退、告警及恢复，核对入口用户计数与出口转发计数。
    rc.2 隔离实例复测：出口不可用时产生 `exit_down`，阻断时订阅客户端没有收到数据；切换直连后成功下载 64 KiB，出口转发计数未增加。恢复后产生 `exit_recovered`，再次下载 64 KiB 经出口，入口用户用量及出口累计转发量均已入账。另一次 64 KiB 下载后，两项数据库计数均至少增加 65,536 字节。
  - [x] 出口域名解析修复：分流和全部模式先匹配出口，直连回退使用入口 DNS。配置 golden 和 sing-box 回环回归覆盖两端 DNS 返回不同答案的情形。
  - [x] 两台 VPS 的 AI 预设 DNS 复测通过（2026-11-27，`v0.6.0-rc.2`）。`v0.6.0-rc.1` 的入口先执行 `resolve`，交给出口的是入口 DNS 的解析结果。
    两台机器升级后，经原 VLESS 入口在「分流」及「全部」模式各连续复测两轮。`chatgpt.com`、`claude.ai` 的 `/cdn-cgi/trace` 均返回 HTTP 200、TLS 证书校验通过、来源 IP 为出口。
    对照域名 `www.cloudflare.com` 在分流时显示入口 IP，在全部模式显示出口 IP。两种模式下 ChatGPT 首页均为 200，Claude 首页均为 403 且有 `cf-mitigated: challenge`。升级后的首次 ChatGPT 代理请求失败一次，后续两轮通过。复测后已恢复原「全部」模式和「回退直连」策略。
  - [x] 实机更换、停用、移除节点及 T9.4 迁移：确认两端配置和防火墙收敛，服务凭证与内部端口随迁移保留。
    rc.2 在出口 VPS 上的四个独立 Linux/systemd 容器中验证。更换出口后旧节点清除了配置、TCP/UDP 监听和 nftables 来源规则，新节点使用新密钥正常转发。停用后两端清理完成。离线移除另一测试节点后，其常规身份被撤销，重连后旧监听和防火墙规则自动清理。
    使用 T9.4 安装器把隔离主控迁到全新实例。入口与出口 ID、端口、密钥、模式及规则一致；出口只放行新主控地址，节点自动切到新主控，旧主控内核已停止。重新获取的订阅经 Hysteria2 下载 65,536 字节，同时指定出口的内核转发下载计数增加 72,093 字节。

## M7 后续增强（MVP 之后，按需排期）

- T7.1 见上方详细任务（已排期）
- T7.2 hy2 复用 ACME 证书（基础 ACME 已在 T6.3）；新增 Trojan/VLESS-WS+TLS（可套 CDN）
- T7.3 更多协议：TUIC v5、AnyTLS、ShadowTLS
- T7.4 TOTP 两步验证
- T7.5 备份/恢复（已排期，见 M8 路线图）
- T7.6 面板内升级 sing-box（新版本先 check 现有配置）
- T7.7 英文界面
- T7.8 多服务器（已排期为 M8，见路线图）
