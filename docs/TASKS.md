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
- [ ] 完整 make test：已运行，既有 internal/e2e.TestLoopback 在内核启动时因 macOS 沙箱
  DNS 通知注册 status 9 失败，需普通终端/CI 复跑；未修改启动行为或添加跳过条件。
- 待确认：旧 e2e 夹具未写 remark，但测试固定用 reality/hysteria2 查找订阅节点；
  新默认名会使沙箱外运行也找不到这些旧名称。因用户要求不修改该测试，目前未改 e2e；
  已请求允许仅将夹具 remark 显式设为原 tag，保持断言和执行流程不变。
- [x] 仅 T6.7，无新依赖/供应链策略变动，无服务端 Apply 改动，未执行 commit/push/切换分支。

## M7 后续增强（MVP 之后，按需排期）

- T7.1 每用户流量统计（评估 v2ray_api 统计是否在官方构建中可用；否则基于 clash_api 连接统计近似）、超额/到期自动禁用（定时任务 + 生效流程）
- T7.2 hy2 复用 ACME 证书（基础 ACME 已在 T6.3）；新增 Trojan/VLESS-WS+TLS（可套 CDN）
- T7.3 更多协议：TUIC v5、AnyTLS、ShadowTLS
- T7.4 TOTP 两步验证
- T7.5 备份/恢复
- T7.6 面板内升级 sing-box（新版本先 check 现有配置）
- T7.7 英文界面
- T7.8 多服务器（agent 模式）
