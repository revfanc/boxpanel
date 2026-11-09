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

### T1.3 已实现的系统接口与返回约定

本任务仅实现 `/system/check`、`/system/status`、`/system/public-ip`、`/system/bbr`。
四个接口均需登录，BBR 写操作沿用 CSRF 检查；GET 支持 HEAD，响应禁止缓存。
`POST /system/bbr` 无请求参数，成功返回 `200 {"enabled":true,"available":true}`。
不接入 `/setup/status`、端口公网连通性探测或任何 sing-box 管理接口。

- `/system/check` 总诊断窗口最多 15 秒；单项失败仍返回 200，将失败写到 `items`，
  不把原始命令输出暴露给客户端。每项均有 `id/level/title/hint`，只有可开启 BBR 时才有
  `action="enable_bbr"`。OS/虚拟化无法识别为 warn；不支持的 CPU 架构、公网 IP 探测失败、
  防火墙检测失败、端口无法检查为 error；推荐端口占用为 warn（可改用其他端口）。
- `ports` 固定为 `443/tcp`、`8443/udp`，`free` 为 `true/false/null`；null 表示未知，
  不把检测失败当成空闲。检测失败时 firewall 为 `{type:"unknown",active:false}`，
  正常 type 为 `none|ufw|firewalld|nftables|iptables`，active 由当前状态判断。
- 上方 sing-box 响应是最终目标示例。T1.3 不检查或操作现有 sing-box，check/status 的
  `singbox` 暂统一返回 `{installed:false,running:false,version:"",uptime:0}`，等待 T2.1 接入。
- `/system/public-ip` 按 CONFIG.md §6 分别探测两个地址族，缺失/冲突的地址族为空字符串；
  至少一个地址族成功即 200，两个都失败返回 `500 internal_error`，提示检查网络后重试。
  重新探测不会覆盖 DB 中的 `public_host`。
- 非 Linux 或 `--dev`：check 只包含 `platform` warn 与空探测值（ports 的 free 为 null）；
  status 为 `supported:false` 且指标为零，不能解读为真实的系统指标。
  public-ip 和 bbr 返回 `409 system_unsupported`。Linux 内核不提供 BBR 时返回
  `409 bbr_unavailable`；读取/写入/应用/校验失败返回 `500 internal_error`，附修复建议。

`/system/status` 的具体形状（Linux 生产模式）：

```json
{
  "supported": true,
  "cpu": 12.5,
  "mem": { "used": 1073741824, "total": 2147483648 },
  "disk": { "used": 10737418240, "total": 42949672960 },
  "uptime": 86400,
  "net": { "rx_rate": 1024, "tx_rate": 2048 },
  "singbox": { "installed": false, "running": false, "version": "", "uptime": 0 }
}
```

- cpu 为两次成功采样间的总体忙碌百分比（0–100）；`/proc/stat` 不重复累计 guest，
  idle 包含 iowait。mem.used 为 MemTotal − MemAvailable（字节）。
- disk 为 DB 所在文件系统的 `{used,total}`（字节）；used 按总块数减空闲块数计算。
  uptime 为系统启动后的整秒数，不是面板进程时长。
- net 为除 lo 外接口的字节/秒，按每个同名接口的两次计数差及实际时间间隔计算；
  首次采样的 cpu/net 返回 0，计数回退、接口新增、非递增时钟不产生负值或下溢尖峰。
  采样串行化，不启动后台轮询；Linux 必需指标读取失败返回 500，不伪造成功状态。
  计数定义参考 [Linux proc 文档](https://docs.kernel.org/filesystems/proc.html)。

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
