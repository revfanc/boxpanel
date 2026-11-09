# 管理 API 契约

前缀：`/{web_path}/api/v1`。除 `/auth/login` 外均需登录会话。请求/响应均为 JSON。

## 通用

错误格式（所有非 2xx）：

```json
{ "error": { "code": "singbox_check_failed", "message": "人类可读的中文说明", "hint": "下一步建议", "detail": "原始输出(可选)" } }
```

| HTTP | 场景 |
|---|---|
| 400 | 参数校验失败（`code: invalid_argument`，`detail` 给字段错误） |
| 401 | 未登录 / 会话过期 |
| 403 | 跨站写请求（`code: csrf_rejected`） |
| 404 | API 路径不存在（`code: not_found`）；返回 JSON 错误，不回退到 SPA |
| 405 | 已知 API 的请求方法不支持（`code: method_not_allowed`，带 Allow） |
| 409 | 端口被占用、名称重复 |
| 422 | `sing-box check` 未通过（配置未生效，DB 已回滚） |
| 429 | 限流（`rate_limited`）或哈希计算繁忙（`auth_busy`），带 Retry-After 秒数 |
| 500 | 内部错误（`internal_error`）；后续包括 sing-box 重启失败（已自动回滚） |

时间统一为 unix 秒；流量统一为字节。

## 认证

| 方法 | 路径 | 说明 |
|---|---|---|
| POST | `/auth/login` | `{username, password}` → 设置 cookie，返回 `{username}` |
| POST | `/auth/logout` | 删除当前会话，过期同路径 cookie，204 无 body |
| GET | `/auth/me` | `{username}` |
| PUT | `/auth/password` | `{old_password, new_password}`（新密码 ≥10 个字符、≤1024 字节），成功 204 |
| PUT | `/auth/username` | `{username}`（`[a-zA-Z0-9_-]{1,32}`），成功 204 |

T1.2 边界：

- 登录成功返回 200，仅含 `{username}`；失败统一 `401 invalid_credentials`，不区分账号不存在和密码错误。
  同 IP 第 5 次失败返回 429 并锁定 15 分钟；锁定中正确凭证也无效。
- 密码/用户名修改成功会撤销该管理员全部会话并清空当前 cookie，调用方须重新登录。
  原密码错误返回 `401 invalid_credentials`；重名返回 `409 username_exists`。
- cookie、会话有效期、开发模式与 CSRF 细节见 SPEC.md §8。
  登录也要求通过来源检查；curl 等没有 Origin / Sec-Fetch-Site 的客户端可正常调用。
- JSON 请求上限 8 KiB，拒绝未知字段、多余 JSON 值、无效字段与错误 Content-Type，返回 400。
  `me` 支持 HEAD（同 GET 状态/响应头，无 body）。所有认证响应 `Cache-Control: no-store`。
- T1.2 只注册上表认证接口；未知 API 返回 JSON 404，不实现后续系统/用户/节点等资源。

## 引导 / 系统

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/setup/status` | `{completed: bool, step: string}` |
| PUT | `/setup/status` | `{completed?, step?}` 保存进度 |
| GET | `/system/check` | 环境自检，见下 |
| POST | `/system/bbr` | 开启 BBR（写 sysctl.d 并立即生效） |
| GET | `/system/status` | `{cpu, mem:{used,total}, disk, uptime, net:{rx_rate,tx_rate}, singbox:{running, version, uptime}}` |
| GET | `/system/public-ip` | 重新探测 `{ipv4, ipv6}` |

`/system/check` 响应：

```json
{
  "os": "debian 12", "arch": "amd64", "virt": "kvm",
  "ipv4": "1.2.3.4", "ipv6": "",
  "bbr": { "enabled": false, "available": true },
  "firewall": { "type": "ufw", "active": true },
  "singbox": { "installed": true, "version": "1.12.x", "running": true },
  "ports": [ { "port": 443, "proto": "tcp", "free": true } ],
  "items": [ { "id": "bbr", "level": "warn", "title": "未开启 BBR", "hint": "建议开启以提升速度", "action": "enable_bbr" } ]
}
```

`items` 是前端直接渲染的检查清单，`level`: `ok | warn | error`。

## sing-box

| 方法 | 路径 | 说明 |
|---|---|---|
| POST | `/singbox/restart` | 重启 |
| GET | `/singbox/logs?lines=200` | `{lines: string[]}`（journalctl） |
| GET | `/singbox/config` | 当前生效配置（**脱敏**：私钥、密码、uuid 中段打码） |
| GET | `/singbox/versions` | `{current, pinned, latest?}` |
| POST | `/singbox/upgrade` | `{version}`：下载 → 校验 → 用新二进制 check 当前配置 → 替换 → 重启，失败回滚（后期任务） |

## Reality 目标

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/reality/recommend` | `{targets: [{server, tls13, h2, latency_ms, ok}]}`，前 3 个推荐 |
| POST | `/reality/check` | `{server, port?}` → 单个检测结果 |

## 入站

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/inbounds` | 列表（options 中私钥字段不返回，返回 `public_key`） |
| POST | `/inbounds` | 创建；body 见下；未给的字段自动生成 |
| GET | `/inbounds/{id}` | 详情 |
| PUT | `/inbounds/{id}` | 修改 |
| DELETE | `/inbounds/{id}` | 删除（同步关闭防火墙端口、清理端口跳跃） |
| POST | `/inbounds/{id}/toggle` | `{enabled}` |
| POST | `/inbounds/suggest` | `{protocol}` → 返回一份推荐参数（端口、SNI、密钥…），不落库；引导流程用 |

创建 body：

```json
{ "protocol": "vless_reality", "port": 443, "remark": "香港-Reality", "options": { "server_name": "..." } }
```

所有写操作走「生成 → check → 生效」流程，成功返回最新对象，失败按错误表返回。

## 用户

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/users` | 列表（含流量、到期、状态） |
| POST | `/users` | `{name, remark?, traffic_limit?, expire_at?}`，uuid/password/token 自动生成 |
| GET/PUT/DELETE | `/users/{id}` | |
| POST | `/users/{id}/toggle` | `{enabled}` |
| POST | `/users/{id}/reset-token` | 重置订阅 token（旧链接立即失效） |
| POST | `/users/{id}/reset-credentials` | 重置 uuid/password（需重新导入） |
| GET | `/users/{id}/links` | 见下 |

`/users/{id}/links`：

```json
{
  "subscription": {
    "auto":    "https://host:port/{sub_path}/{token}",
    "singbox": "...?format=singbox",
    "clash":   "...?format=clash",
    "base64":  "...?format=base64"
  },
  "import": {
    "singbox": "sing-box://import-remote-profile?url=<enc>#<name>",
    "clash":   "clash://install-config?url=<enc>&name=<name>"
  },
  "nodes": [ { "inbound_id": 1, "remark": "香港-Reality", "uri": "vless://..." } ]
}
```

## 规则

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/rules` | `settings.rules`（结构见 CONFIG.md §3） |
| PUT | `/rules` | 保存（只影响订阅输出；服务端的 BT 屏蔽在 settings 中） |
| GET | `/rules/preview?format=singbox\|clash` | 用第一个用户渲染一份订阅预览（脱敏） |

## 设置

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/settings` | `{public_host, web_port, web_path, sub_path, panel_tls_mode, block_bittorrent, panel_name}` |
| PUT | `/settings` | 修改；改 `web_port`/`web_path` 时响应里返回新面板地址，后端 2 秒后重启监听 |

## 备份（后期）

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/backup` | 下载 `boxpanel-backup-<date>.json`（DB 全量 + 证书） |
| POST | `/restore` | 上传备份并重新生效 |
