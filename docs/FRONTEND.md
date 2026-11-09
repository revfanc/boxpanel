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

### 前端

```ts
// web/src/lib/runtime.ts
export const BASE = (window as any).__BOXPANEL__?.base ?? "";   // 开发模式为空
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

1. **welcome**：一句话说明会做什么（大约 2 分钟），提示「浏览器提示不安全是因为自签证书，属正常」。按钮：开始。
2. **check**：调用 `/system/check`，渲染检查清单（✅/⚠️/❌）。BBR 未开时给「一键开启」按钮。存在 error 级项时禁止下一步并给出修复说明。
3. **scenario**：两张大卡片——
   - 「我没有域名（推荐）」→ VLESS + Reality
   - 「我有域名」→ 输入域名，校验 A 记录指向本机后，MVP 仍用 Reality，并把域名设为 `public_host`（ACME 证书后期再接）
   - 额外复选：「同时创建 Hysteria2（网络不稳定时更快，需 UDP）」默认勾选
4. **node**：调用 `/inbounds/suggest` 预填端口、伪装目标（显示检测出的推荐列表与延迟），用户可直接下一步；点「创建」时调用 `POST /inbounds`。
5. **user**：输入使用者名字（默认 `me`），可选流量上限/到期时间（默认不限）。
6. **firewall**：列出需要放行的端口与协议（如 `443/TCP`、`8443/UDP`、面板端口/TCP），说明「本机防火墙已自动放行；**云服务商的安全组需要你手动放行**」，并按常见厂商（阿里云、腾讯云、AWS、GCP、Oracle、Vultr…）给出折叠的图文步骤链接占位。提供「我已放行，测试一下」按钮：后端从本机连接自身公网端口做 TCP 探测（UDP 只提示无法自动检测）。
7. **finish**：大二维码（自动格式订阅）+ 复制按钮 + 按平台的 Tabs（Windows / macOS / Android / iOS / Linux），每个 Tab：推荐客户端名、下载链接、「一键导入」按钮（若支持）、三步导入说明。最后标记 `completed=true`，按钮：进入面板。

客户端推荐清单放 `web/src/data/clients.ts`（名称、平台、下载地址、支持的订阅格式、导入深链模板），方便后续维护。

## 5. 数据层

- `web/src/lib/api.ts`：fetch 封装（`credentials: "same-origin"`），统一解析错误格式，401 时跳转登录。
- TanStack Query：每个资源一个 `queryKey`；写操作成功后 invalidate 相关 key。
- 表单：react-hook-form + zod，schema 与 API 字段一一对应，放 `web/src/schemas/`。
- 类型：`web/src/types/api.ts` 手写与 `docs/API.md` 一致的类型。

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
    data/clients.ts
    components/ui/           # shadcn 生成
    components/              # 业务通用组件（QRCard、CopyField、StatusDot、ProtocolBadge…）
    layouts/{AppLayout.tsx, SetupLayout.tsx, AuthLayout.tsx}
    pages/{login, dashboard, inbounds, users, rules, settings, logs, setup, not-found}/
```
