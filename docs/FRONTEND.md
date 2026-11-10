# 前端规格（React + shadcn/ui，history 路由）

## 1. history 路由与随机 basename（必须照此实现）

面板挂在 `/{web_path}/` 下，路径安装时随机生成，构建时并不知道。方案：**运行时注入**。

### 构建侧（Vite）

- `vite.config.ts`：`base: './'`（资源使用相对路径）。
- `index.html` 的 `<head>` 第一行放占位注释：`<!--BOXPANEL_INJECT-->`。

### 服务端（`internal/spa`）

处理 `/{web_path}/` 下的 GET 请求：

1. `/{web_path}`（无尾斜杠）→ 301 到 `/{web_path}/`。
2. `/{web_path}/api/...` → 交给 API 路由；API 未匹配返回 JSON 404，**不回退** index.html。
3. 静态文件存在（embed FS 中）→ 直接返回；`assets/*` 加 `Cache-Control: public, max-age=31536000, immutable`。
4. 路径最后一段含扩展名（如 `.js`、`.png`）但文件不存在 → 404。
5. 其他 → 返回 index.html（`Cache-Control: no-store`），并把占位注释替换为：

```html
<base href="/{web_path}/">
<script>window.__BOXPANEL__={"base":"/{web_path}","version":"<面板版本>"}</script>
```

`<base>` 保证深链接（如 `/{web_path}/users/3`）刷新时 `./assets/*.js` 仍解析到正确位置。

T0.2 补充边界约定：

- `web_path` 为不带斜杠的非空 `[a-z0-9]+`，或空字符串（挂在根目录）；12 位随机值在 T1.2 初始化时生成。
- 301 重定向保留查询参数。面板前缀必须匹配完整路径段，前缀外返回空白 404。
- 显式访问 `index.html` 也走注入与 `no-store`，不返回原始占位模板；注入先于相对资源标签，运行时 JSON 使用 HTML 安全转义。
- GET 与 HEAD 使用同一路由规则（HEAD 无响应体）；SPA 的其他方法返回 405。API 接收所有方法，`/api` 本身也保留给 API；T0.2 尚无业务 API，统一返回 API.md 中的 JSON 404。
- `spa.Options.API` 可接入后续 API handler，收到的路径已剥离面板前缀，保留 `/api/...`；接入的 API 路由须自行提供 JSON 404。
- 只直接提供文件，不展示目录列表；不存在的文件以最后一段扩展名判断，其他路径回退。SPA 请求中含 `.`、`..` 路径段或重复斜杠的非规范路径返回空白 404。
- 嵌入的 `index.html` 缺失或注入标记不是恰好一个时，启动失败并提示 `make build`，然后重启新二进制。

### 前端

```ts
// web/src/lib/runtime.ts
// 为 window.__BOXPANEL__ 声明可选的 { base: string; version: string } 类型。
export const BASE = window.__BOXPANEL__?.base ?? "";   // 开发模式为空
export const API_BASE = `${BASE}/api/v1`;

// web/src/router.tsx
createBrowserRouter(routes, { basename: BASE || "/" });
```

- 开发模式：`vite dev` 代理 `/api` 到 `http://127.0.0.1:2053`，后端以 `--dev` 启动（`web_path=""`）。
- 站内跳转一律用 `<Link>`/`navigate()`，禁止手拼 `window.location`（除了修改面板地址后的整页跳转）。

验收：生产构建下，直接在浏览器打开/刷新每个路由都能正常渲染；`/{web_path}/assets/不存在.js` 返回 404；`/其他路径` 返回空白 404。

## 2. 路由表

| 路径 | 页面 | 说明 |
|---|---|---|
| `/login` | 登录 | 未登录访问任何页都重定向到这里（带 `?next=`） |
| `/setup` | 引导 | 重定向到当前步骤 |
| `/setup/:step` | 引导步骤 | `welcome → check → scenario → node → user → firewall → finish` |
| `/` | 概览 | 系统状态、sing-box 状态、节点数/用户数、快捷入口 |
| `/inbounds` | 节点列表 | 卡片/表格，启停开关 |
| `/inbounds/new` | 新建节点 | 先选协议卡片，再填（大部分自动填好，「高级」折叠） |
| `/inbounds/:id` | 编辑节点 | |
| `/users` | 用户列表 | 流量进度条、到期时间、状态 |
| `/users/:id` | 用户详情 | **订阅中心**：二维码、复制链接、一键导入、单节点 URI |
| `/rules` | 分流规则 | 预设单选卡片、去广告开关、自定义域名、DNS、规则源 |
| `/settings` | 设置 | 面板地址/端口/路径、节点地址、订阅路径、账号密码 |
| `/logs` | 日志 | sing-box 日志，自动刷新开关 |
| `*` | 404 | |

路由守卫：

- 未登录 → `/login`。
- 已登录且 `setup.completed=false` → 强制进入 `/setup`（引导页右上角提供「跳过引导」）。

## 3. 布局与组件

- 桌面：左侧 Sidebar（shadcn `sidebar`）+ 顶栏（面板名、sing-box 运行状态点、主题切换、退出）。
- 移动端（<768px）：Sidebar 收为抽屉；表格改卡片列表。手机上用面板是常见场景，必须可用。
- 主题：亮/暗/跟随系统。
- 用到的 shadcn 组件：button, card, input, label, form, select, switch, tabs, dialog, alert-dialog, dropdown-menu, table, badge, progress, tooltip, sonner(toast), skeleton, sidebar, sheet, separator, radio-group, accordion, alert。
- 二维码：`qrcode.react`。
- 所有破坏性操作（删除节点/用户、重置 token）用 alert-dialog 二次确认，并说明后果。
- 写操作期间按钮 loading；配置生效失败时展示 `error.message` + `hint`，`detail` 放在可展开区域。

## 4. 引导流程（小白核心体验）

进度条显示 7 步，每步可返回；进度存后端 `/setup/status`，关闭浏览器后能续上。

1. **welcome**：一句话说明会做什么（大约 2 分钟）；根据实际证书类型说明自签警告或可信证书状态，开发 HTTP 不显示自签警告。按钮：开始。
2. **check**：调用 `/system/check`，渲染检查清单（✅/⚠️/❌）。BBR 未开时给「一键开启」按钮。存在 error 级项时禁止下一步并给出修复说明。
3. **scenario**：两张大卡片——
   - 「我没有域名（推荐）」→ VLESS + Reality
   - 「我有域名」→ 输入域名，校验全部 A/AAAA 记录指向本机公网 IP 后，MVP 仍用 Reality，并把域名设为 `public_host`（acme 模式自动重新签发面板证书）
   - 额外复选：「同时创建 Hysteria2（网络不稳定时更快，需 UDP）」默认勾选
4. **node**：调用 `/inbounds/suggest` 预填端口、伪装目标（显示检测出的推荐列表与延迟），用户可直接下一步；点「创建」时调用 `POST /inbounds`。
5. **user**：输入使用者名字（默认 `me`），可选流量上限/到期时间（默认不限）。
6. **firewall**：列出需要放行的端口与协议（如 `443/TCP`、`8443/UDP`、面板端口/TCP），说明「本机防火墙已自动放行；**云服务商的安全组需要你手动放行**」，并按常见厂商（阿里云、腾讯云、AWS、GCP、Oracle、Vultr…）给出折叠的官方入站规则文档链接。提供「我已放行，测试一下」按钮：后端从本机连接自身公网端口做 TCP 探测（UDP 只提示无法自动检测）。
7. **finish**：大二维码（自动格式订阅）+ 复制按钮 + 按平台的 Tabs（Windows / macOS / Android / iOS / Linux），每个 Tab：推荐客户端名、下载链接、「一键导入」按钮（若支持）、三步导入说明。最后标记 `completed=true`，按钮：进入面板。

客户端推荐清单放 `web/src/data/clients.json`，由 `clients.ts` 校验并导出（名称、平台、下载地址、支持的订阅格式、导入深链模板），方便后续维护。

### T5.1 实现与边界

- 引导使用独立 SetupLayout、history 路由及原有 basename/认证守卫。未完成时强制进入后端记录
  的步骤；`/setup`、旧书签、非法步骤均回到当前步骤。取消 T4.1 的 404 放行兼容。
  每次下一步/上一步先 PUT 持久化，成功才导航；失败保留当前页并提供 hint/detail。
  welcome 的上一步不可用；右上角跳过只设置 completed=true 后进入面板，保留步骤以便重进。
  finish 点击“进入面板”时才标记完成，不因读取订阅失败阻止退出引导。
- scenario 的域名和 Hysteria2 选择通过 setup/status 保存；有域名时提交与 DNS 校验/设置
  public_host 在服务端完成，失败不推进。无域名默认 Reality，Hysteria2 默认勾选。
  A/AAAA 校验及公网探测在开发环境不能伪造成功，请选择无域名演示或在 Linux 上校验。
- node 复用 T4 的 SuggestedForm/InboundForm、schema、推荐与手动检测逻辑，逐个创建 Reality、
  可选 Hysteria2；推荐候选和延迟自动展示。Fake-IP 导致检测失败时仍显示可编辑表单，允许手填。
  user 复用 T4 UserForm、校验及流量/时间换算，默认 me、0 上限、永不过期。
  在实际资源写入中屏蔽返回/跳过；进度写入中屏蔽全部步骤操作。
- 节点步骤读取已有启用节点，只补缺少的协议；用户步骤复用 ID 最小的 active 用户。
  返回/刷新、第一节点成功第二节点失败、资源成功但进度保存失败都不会再次创建已存在资源。
  已保存节点显示端口摘要；取消 Hysteria2 勾选不会删除已有节点，需进入节点页管理。
  未提交的字段只在组件内，不跨刷新持久化；本阶段不引入草稿同步或多标签写入协调。
- firewall 展示所有启用节点、面板 TCP 端口及 UDP 跳跃范围；厂商折叠区提供控制台操作步骤和
  官方文档链接（T6.3），不编造教程 URL。生产说明自动放行及云安全组职责；dev 则明确没有改系统。
  按唯一 TCP 端口调用 port-probe，只报告自连结果与限制，不宣称安全组已验证；UDP 需外部客户端验证。
- finish 使用专用 links 接口，本地生成大二维码和 CopyField，五个平台使用可键盘操作的 Tabs。
  提供项目官方入口、来源说明、对应格式链接、一键导入及三步说明；沿用 sing-box 1.14+ 提示。
  订阅链接仅保留内存，离开页面回收查询；错误、空节点或无可用用户不显示可导入的二维码/按钮。
  dev 的 Hysteria2 缺少证书时显示 409 和下一步：Linux 生效后获取，或进入面板停用 Hysteria2
  再导出 Reality；不造证书、不降级验证。Reality-only 的开发订阅可导出，但明确节点未实际运行。
- `clients.ts` 来源核对（2026-11-09）：Windows/macOS/Linux 为
  [Clash Verge Rev 官方 Releases](https://github.com/clash-verge-rev/clash-verge-rev/releases)，
  [导入格式](https://www.clashverge.dev/guide/url_schemes.html)；Android 的
  [官方客户端页](https://sing-box.sagernet.org/clients/android/) 指向
  [SagerNet/sing-box Releases](https://github.com/SagerNet/sing-box/releases)；iOS 的
  [官方 Apple 页](https://sing-box.sagernet.org/clients/apple/) 指向
  [sing-box MT App Store](https://apps.apple.com/us/app/sing-box-mt/id6785326793)。
  sing-box 深链来自 [官方通用规范](https://sing-box.sagernet.org/clients/general/)。
  下载地址只用官方 Releases/商店，不固定未经验证的资产文件名；来源和深链模板随数据一起维护。
- 无新增依赖：复用已安装的 radix-ui Tabs、qrcode.react 和 T4 表单/组件；未修改 shadcn 生成文件，
  未改 pnpm 冷静期、package.json 或锁文件。补充强制进入、跳过、刷新续接、每步返回、DNS 拒绝、
  BBR/错误阻断、手动 Reality、双入站重试、dev 订阅降级提示、平台 Tabs 和端口检查组件测试。

## 5. 数据层

- `web/src/lib/api.ts`：fetch 封装（`credentials: "same-origin"`），统一解析错误格式，401 时跳转登录。
- TanStack Query：每个资源一个 `queryKey`；写操作成功后 invalidate 相关 key。
- 表单：react-hook-form + zod，schema 与 API 字段一一对应，放 `web/src/schemas/`。
- 类型：`web/src/types/api.ts` 手写与 `docs/API.md` 一致的类型。

### T4.1 基础设施边界与实现约定

- 仅登录页、公共布局、404 和数据层可用。路由表中的业务页面均为明确占位，不提供概览统计、
  日志、设置表单、节点/用户编辑、订阅中心或引导步骤；这些依次属于 T4.2–T5.1。
  顶栏只读取 `/settings` 的面板名与 `/system/status` 的内核状态，不加入概览的 5 秒轮询。
  `supported=false` 明确显示当前环境不提供状态，错误显示未知与重试，不冒充内核停止。
- `api.ts` 使用运行时 `API_BASE`、同源 cookie、no-store 与 JSON；支持空 204、AbortSignal、
  统一错误及 Retry-After 秒数。网络/非 JSON 错误提供下一步建议，不展示原始 HTML 或网络异常。
  受保护请求 401 触发 Router 导航与业务缓存清理；登录接口 401 留在表单中，不丢失 next。
  同一会话的并发 401 只处理一次，旧会话迟到的 401 不干扰新登录。
- `/auth/me` 是会话唯一依据；加载时不渲染受保护内容，非 401 失败显示可重试错误。
  登录成功、退出成功/会话失效时取消查询，清除资源查询及 mutation 缓存，再更新会话查询；
  保留会话查询对象，避免当前页的 observer 留在被移除的 pending 查询上。
  查询按资源命名，读取默认 30 秒 fresh、窗口聚焦时刷新，网络/5xx 最多重试一次；写操作不自动重试。
- `next` 保存不含 basename 的站内路径、query、hash；登录后由 Router 加前缀。
  拒绝外部 URL、双斜杠、反斜杠、控制字符、无效转义与登录页自身，非法 next 回到 `/`。
  表单按真实后端校验用户名及密码 **1024 字节**上限，不把修改密码的最短长度用于登录；
  不 trim 密码、不将登录密码放入 Query/mutation 缓存或持久化存储。
- **引导契约缺口**：当前 `internal/httpapi` 尚未注册 `/setup/status`，API.md 也将其保留到 T5.1。
  T4.1 仅对该 GET 的 `404 + error.code=not_found` 返回“接口不可用”状态并允许进入面板，
  不伪造 `completed=true`、不写 DB；其他错误或非法响应阻止进入并允许重试。
  接口一旦提供 `completed=false`，守卫强制 `/setup`，setup 路由自身不循环跳转。
  当前 setup 仅占位；跳到实际步骤、跳过与持久化全部留给 T5.1。
- 桌面使用 CLI 生成的 shadcn Sidebar，<768px 使用单独封装的 Sheet，以提供中文标题/说明/关闭按钮，
  不修改生成组件。抽屉支持焦点约束、Escape、关闭后恢复焦点、导航/切换桌面后关闭。
  内容区使用 min-width:0、可换行错误详情及受视口限制的抽屉，面板名称截断以适配 375px。
- 主题默认为跟随系统，支持浅色/深色，保存在 `boxpanel-theme`；同步系统及跨标签页变化，
  localStorage 不可用时仍可切换。挂载前应用主题，不存储账号密码或任何会话 token。
  所有业务文案集中在 `zh-CN.ts`，保持订阅页面已有标记 JSON 不变。

依赖理由：新增 `@tanstack/react-query`（请求缓存/状态）、`react-hook-form`、`zod`、
`@hookform/resolvers`（登录表单及校验）；开发依赖 `@testing-library/react`、
`@testing-library/user-event`、`@testing-library/jest-dom`、`jsdom`（无服务器的 DOM/交互测试）。
使用项目已有 shadcn 4.23.0 CLI 添加 sidebar/input/label/dropdown-menu/alert 及其所需组件，
没有手改生成文件。ESLint 仅对生成 sidebar 的 useSidebar 导出、生成 use-mobile 的 effect 初始化
做定点兼容，业务代码仍使用完整规则。
安装沿用 pnpm 12.18.0 默认 minimumReleaseAge（1440 分钟），未添加任何豁免或降低既有策略；
依赖版本及完整性由 lockfile 固定。参考 [pnpm 供应链保护](https://pnpm.io/supply-chain-security)、
[shadcn Sidebar](https://ui.shadcn.com/docs/components/radix/sidebar)、
[TanStack QueryClient](https://tanstack.com/query/latest/docs/framework/react/reference/QueryClient)、
[react-hook-form](https://react-hook-form.com/docs/useform)。

测试使用 Vitest + jsdom，覆盖随机 basename 深链/登录回跳、401 缓存清理、引导守卫与缺口、
登录/退出 loading 和错误重试、404、抽屉键盘与导航、主题、API 错误与取消、密码字节边界。
jsdom 不计算实际布局，因此 375px 无横向滚动和生产 history 刷新需审查者以嵌入构建浏览器复核；
本任务不启动 Vite dev server。

### T4.2–T4.5 业务页面与对接约定

以上 T4.1 段落记录该阶段的范围。本次按顺序一起完成 T4.2–T4.5，替换概览、日志、设置、
节点、用户/订阅中心与规则页的占位；setup 仍按 T4.1 兼容规则保留，未实现 T5.1。

- 继续使用原有运行时 basename、history 路由、API 封装、QueryClient、布局与主题。
  业务页面按路由懒加载；表单使用 react-hook-form + zod，校验文案与页面文案集中在 zh-CN.ts。
  响应形状按 internal/httpapi 核对，列表直接读取数组，204 不解析 JSON。
- 概览的系统指标、节点数和用户数每 5 秒刷新，卸载后停止轮询；没有资源时给创建入口。
  `supported=false` 或 `409 system_unsupported` 显示环境提示，不把占位零值当成真实指标；
  顶栏同样将此 409 显示为状态不可用。已安装内核可经确认重启，并展示失败诊断。
  日志读取最近 200 行，默认每 5 秒刷新，可关闭自动刷新；409 时停止自动轮询，保留手动刷新。
- T6.3 在设置页独立证书卡片中提供申请与改回自签。修改面板端口/路径或订阅路径前确认并列出拟保存值，
  提示旧订阅链接失效、安全组与重新登录的影响。保存成功仅在 `restart_after>0` 时，等待
  返回的秒数加 500ms 缓冲，再整页跳转到后端的 `panel_url`，并提供可手动打开的新地址。
  `restart_after=0` 留在设置页刷新缓存。开发模式的固定端口/路径限制由真实 API 返回可操作提示。
- 管理员用户名/密码修改成功后清理会话与业务缓存，回登录页。原密码错误的
  `401 invalid_credentials` 只在修改密码表单显示；真实 `401 unauthorized` 仍触发会话失效。
  请求体（含密码/混淆密码）只存于组件表单及请求闭包，不写 Query mutation 缓存或持久存储。
- 节点新建先选协议，再调用 suggest 自动预填。推荐失败保留手动表单；端口可留空由后端分配，
  Reality 必须填写伪装目标。可请求候选列表或单独检测握手目标；空候选和 `ok=false` 均提供下一步。
  Fake-IP 代理可能使本机探测失败，明确允许手动填写后保存，不伪造推荐成功或绕过后端校验。
  编辑不切换协议、不提供私钥输入或回写公钥；混淆密码留空保持原值，显式关闭才发送空字符串。
  高级字段错误会展开高级区域。停用和删除前确认其连接/订阅影响，失败保留确认框与输入。
- 用户新建/编辑使用对话框；不增加未定义的 `/users/new` 路由。界面使用 GiB，提交换算为整数字节，
  日期使用本地时区输入、Unix 秒提交，空日期为 0；编辑保留已有秒精度。展示后端计算的四种状态，
  进度限制在 0–100%，不限额单独标明。这里只展示已记录流量，不提前实现 T7.1 的采样/定时禁用。
- 订阅中心从专用 links 接口读取二维码、自动/JSON/YAML/base64 地址、导入深链和单节点 URI。
  二维码在浏览器本地生成，无第三方 QR 请求；链接只存在内存，离开详情页立即回收 links 查询缓存。
  剪贴板失败时提供可选中的只读文本与手动复制提示。空节点、用户不可用、开发 Hysteria2 缺少证书
  分别给出说明；不生成虚假的 URI、不以 insecure 绕过证书约束。
  删除用户、停用、重置 token/连接凭证均需确认；重置成功取消旧请求并清空 links 数据，重新获取，
  刷新中或刷新失败时不展示旧二维码和旧链接。
- 规则页提供三预设及适用说明、广告过滤、域名标签、DNS/FakeIP/IPv6、规则源/镜像/更新间隔。
  T6.5：FakeIP/IPv6 开关注明只影响 sing-box；DNS 提示说明 Clash 固定 Fake-IP/关闭 IPv6、
  本地 DoH 的用途及远程 DNS 经节点选择中的代理。仅改集中式文案/提示，不改变表单/API 数据结构。
  标签支持 Enter、添加和移除；保存时同时校验并纳入尚未按 Enter 的域名，避免悄悄丢失输入。
  PUT 发送完整规则结构，不含界面草稿字段，不调用重启。预览按钮明确标为“预览已保存规则”：
  当前 API 只支持 GET 已保存规则，抽屉不伪装预览未保存内容；按需请求 JSON/YAML，无用户显示创建建议，
  证书缺失和其他错误保留 hint/detail。后台刷新不会自动覆盖用户正在编辑的表单。
- 每个写操作防重复提交、有 loading 与可展开诊断；成功后取消旧资源读请求并失效相关 Query key。
  业务确认框和编辑对话框限制视口高度、可滚动，支持键盘操作与关闭后的焦点恢复。
  节点/用户采用可换行卡片，宽屏双列、手机单列；长 URL、日志与预览可换行，避免撑宽页面。

依赖：新增固定 `qrcode.react@4.2.0`（2024-12-11 发布）用于本地 SVG 二维码，
参见 [官方说明](https://github.com/zpao/qrcode.react)。pnpm 12.18.0 安装时通过供应链检查，
未降低 minimumReleaseAge 或增加任何豁免。使用现有 shadcn CLI 添加 alert-dialog、dialog、
switch、badge、progress；未手改生成文件，仅对 badgeVariants 导出增加定点 ESLint 兼容。

组件测试逐页覆盖加载、成功、错误与下一步提示，集合页/日志/链接/预览覆盖空状态；设置与编辑表单
覆盖空输入校验和不存在记录。所有提供破坏性操作的页面覆盖确认/取消；只读日志及规则页没有删除确认。
补充轮询开关/卸载、手动 Reality 目标、写失败保留输入、token 重置清缓存、复制失败、日期/流量换算、
JSON/YAML 抽屉和随机 basename 登录深链测试。jsdom 不提供真实布局测量，375px/桌面和生产刷新
仍交由审查者以 `serve --dev` 的嵌入生产构建实测；本次未启动 Vite dev server。

## 6. 目录

```
web/
  index.html
  vite.config.ts
  components.json            # shadcn
  embed.go                   # package web; //go:embed all:dist
  dist/.gitkeep
  src/
    main.tsx
    router.tsx
    lib/{runtime.ts, api.ts, utils.ts, format.ts}
    i18n/zh-CN.ts
    types/api.ts
    schemas/
    data/{clients.ts,clients.json}
    components/ui/           # shadcn 生成
    components/              # 业务通用组件（QRCard、CopyField、StatusDot、ProtocolBadge…）
    layouts/{AppLayout.tsx, SetupLayout.tsx, AuthLayout.tsx}
    pages/{login, dashboard, inbounds, users, rules, settings, logs, setup, not-found}/
```


## 7. T6.3 证书与引导修复

- 设置页证书卡片读取 cert，展示实际类型、证书地址、签发者、到期时间、上次尝试/成功及失败提示。
  支持申请/重新申请、改回自签，写入有 loading/错误；申请返回 202 后显示已受理，不冒充签发成功。
  主布局与设置页共享查询，正常 30 秒刷新、申请中 2 秒刷新，离开受保护页面后停止；开发模式说明不签发。
- cert.warning 在主布局顶部显示醒目提醒和设置入口；失败 hint 给出放行 TCP 80、DNS/CDN、权限等下一步。
  welcome/finish 根据实际 type 显示自签警告或 ACME 说明，HTTP 开发模式不展示自签警告。
  staging/custom CA 不被描述为必然受客户端信任；Hysteria2 的自签证书提示与指纹行为保持。
- suggest 无 Reality 候选仍用返回端口预填，目标留空并显示一次 hint。创建失败时替换推荐提示，
  节点表单统一展示当前操作的一个错误框，不重复展示同一失败。
  “同时创建 Hysteria2”用显式 label/htmlFor 和 aria-labelledby 绑定可见文案。
- acme 模式的 firewall 步骤列 TCP 80，说明云安全组必须放行、验证监听是临时的；
  普通 TCP 自连不包含 ACME 80，避免空闲期间被误判为安全组配置失败。
- 官方文档逐页打开核实（2026-11-09），下表采用重定向后的官方 URL；链接数据和文案集中在 zh-CN.ts。

| 厂商 | 官方页面 |
|---|---|
| 阿里云 | [安全组及入站规则](https://help.aliyun.com/zh/ecs/user-guide/start-using-security-groups) |
| 腾讯云 | [添加安全组规则](https://cloud.tencent.com/document/product/213/112614) |
| AWS | [Configure security group rules](https://docs.aws.amazon.com/vpc/latest/userguide/working-with-security-group-rules.html) |
| Google Cloud | [Use VPC firewall rules](https://docs.cloud.google.com/firewall/docs/using-firewalls) |
| Azure | [Manage network security groups](https://learn.microsoft.com/en-us/azure/virtual-network/manage-network-security-group) |
| Oracle Cloud | [Security list ingress rules](https://docs.oracle.com/en-us/iaas/Content/Network/Concepts/creating-securitylist.htm) |
| Vultr | [Firewall rules](https://docs.vultr.com/products/network/firewall-groups/management/rules) |
| DigitalOcean | [Configure firewall rules](https://docs.digitalocean.com/products/networking/firewalls/how-to/configure-rules/) |
| Linode / Akamai | [Manage firewall rules](https://techdocs.akamai.com/cloud-computing/docs/manage-firewall-rules) |
| 搬瓦工 | [官方 iptables 排障](https://bandwagonhost.com/kb.php?action=displayarticle&id=24) |

规格缺口：搬瓦工未找到单独的云安全组入站教程；采用其官方网络连通性/iptables 规则说明，
在 UI 明确这是 VPS 内防火墙排障、保留已有规则，不把它标成云安全组教程，不链接第三方文章。
其余九家页面均包含安全组/防火墙入站规则配置。
无前端依赖增加，未修改 shadcn 生成组件、pnpm 锁文件或供应链冷静期。

## 8. T6.4 公网地址展示

引导环境检查展示本次探测到的 IPv4、IPv6，IPv4 排在前并标注优先；缺失的地址族不显示，
两者均无结果时保留原检查项/错误提示，不伪造地址。展示不覆盖已保存的 `public_host`。
地址使用可换行文本适配窄屏，新增文案集中在 zh-CN.ts；组件测试覆盖双栈、单栈和无地址。


## 9. T6.6 订阅分享页与地址变更提示

- 浏览器订阅页由 Go `html/template` 渲染 `internal/subscription/page.html`，CSS/复制脚本均内联，
  不加载 SPA、字体、图标或第三方资源；二维码为本地 PNG。内容宽度 32rem，左右 16px 留白，
  手机按钮触控高度至少 44px。亮/暗色通过 prefers-color-scheme 跟随系统，neutral token 原样取自
  index.css 并测试核对；当前 index.css 没有阴影 token，使用现有边框及圆角，不另造阴影变量。
- 展示面板名、用户名、状态、上传下载合计、总限额、进度及 UTC 到期日期。单位按 1024 进位到
  B/KB/MB/GB/TB，保留两位小数；无上限进度为 0 并标「不限」，有限进度限制为 0–100%。
  剩余天数按剩余秒数向上取整，过期为 0，永不过期为「长期有效」；加总不发生 int64 溢出。
- 状态优先级仍为停用 → 过期 → 流量用尽 → 正常。与原安全契约的冲突按最小方案处理：
  渲染器支持四种状态并有测试，公开 handler 对停用/过期/超额用户仍返回相同空白 404，
  不暴露用户是否存在或提供失效订阅页面。
- 各格式放原生 details；地址为只读输入框，无 JS 仍可手动选择，导入和官方下载链接仍可点击。
  Clipboard 成功显示「已复制」两秒，失败选中输入并提示手动复制。每个 HTML GET/HEAD 都以
  crypto/rand 生成独立 32 字节 nonce，响应 CSP 与唯一脚本一致，保留 no-store/no-referrer 等保护。
- 客户端名称、平台、下载入口、来源、格式和深链模板唯一保存在 clients.json；React 的 clients.ts
  使用已有 zod 校验/导出，Go embed 读取同一份 JSON。沿用 T5.1 已核实清单，不添加客户端或资源请求。
  所有页面文案仍在 zh-CN.ts 的 SubscriptionText 标记 JSON；页面 golden 固定 nonce/时间。
- 引导 domain 实际改变 public_host 时，PUT 响应提供新 panel_url 和 panel_tls_mode（API.md）；
  设置页复用既有响应。主机实际修改且目标 origin 与当前访问地址不同时，成功后展示共同对话框，
  可复制地址并提示收藏、证书切换及可能重新登录。对话框挂在路由/会话守卫之外，
  提示不因引导进入下一步或旧证书导致后台认证请求失败而消失。
  生产点击「前往新地址」才整页跳转；开发 http 只提示并提供继续按钮，不跳转。
  无变化、已通过新 origin 访问或保存失败不弹出此框。域名改回 IP 同样处理。
- 同时修改主机和端口/路径时保持对话框，等待 restart_after 秒加 500ms 后启用跳转按钮；
  单独端口/路径变更继续沿用 T4.2 的确认和自动延迟跳转。新地址由服务端生成，
  主机单独改变时保留实际监听端口及运行时路径，包括 CLI 覆盖，不从浏览器猜测。
- 无新增依赖，未修改 shadcn 生成组件、供应链保护或 sing-box 配置生效流程；不实现 M7。
