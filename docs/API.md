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
| 500 | 内部错误（`internal_error`）或 sing-box 生效失败；恢复失败有独立错误码，不声称已恢复 |

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
- T2.1 已将 check/status 的 `singbox` 接入受管二进制和 systemd 实际状态：
  `{installed,running,version,uptime}`，version 为二进制版本，uptime 为当前服务运行整秒数。
  二进制不存在时返回 `{installed:false,running:false,version:"",uptime:0}`；
  二进制存在但服务未启动/单元不存在时 installed=true、running=false、uptime=0。
  命令/权限/解析错误在 check 中产生 `singbox` error 检查项（保留已知的 installed/version），
  status 返回 500，不冒充正常停止，也不暴露原始命令输出。非 Linux/`--dev` 仍返回占位值。
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

T2.3 实现 restart、logs、config；versions、upgrade 留在后续任务。

| 方法 | 路径 | 说明 |
|---|---|---|
| POST | `/singbox/restart` | 无参数，重新生成 → check → 生效；成功 204，无 body |
| GET | `/singbox/logs?lines=200` | `{lines: string[]}`（journalctl） |
| GET | `/singbox/config` | 当前生效配置（**脱敏**：私钥、密码、uuid 中段打码） |
| GET | `/singbox/versions` | `{current, pinned, latest?}` |
| POST | `/singbox/upgrade` | `{version}`：下载 → 校验 → 用新二进制 check 当前配置 → 替换 → 重启，失败回滚（后期任务） |

### T2.3 返回与脱敏

- 三个接口均需登录；restart 沿用 CSRF 校验；GET 支持 HEAD，全部 `Cache-Control: no-store`。
- restart 走 SPEC.md §7 完整流程，生成/check 失败为 `422 singbox_check_failed`；
  重启/存活失败为 `500 singbox_restart_failed`；防火墙失败为 `500 singbox_firewall_failed`；
  替换/事务收尾失败为 `500 singbox_apply_failed`。以上失败均不提交 DB，并尝试恢复文件、服务和规则。
  恢复本身失败统一为 `500 singbox_rollback_failed`，保留恢复记录并提示人工检查后再运行 apply。
  COMMIT 报错且无法重新读取 DB 核对时，明确提示数据库状态无法确认，不声称已回滚。
- `error.detail` 只含脱敏后的 check 输出或失败时 journal 最近 30 行；日志和错误原因不直接透传。
  DB/存储等内部错误不返回原始内容。生成错误无内核输出时可为空 detail，仍有修复建议。
- logs 的 lines 默认 200，必须为单个 1–1000 的整数，否则 400。使用固定受管 unit、
  `journalctl --no-pager --output=cat --quiet`，不执行 shell、不支持任意 unit/命令参数。
  返回 `{lines:[]}` 或字符串数组；脱敏后总文本最多 64 KiB，超长内容标记截断。
- config 返回当前磁盘配置 JSON（不是未生效 DB 的预览）；不存在返回 `404 singbox_config_missing`。
  私钥、密码（含 obfs）、Clash API secret、token 完全替换；UUID 仅保留首尾各 4 位。
  日志/check 输出替换当前/候选/备份凭证及转义形式，并屏蔽凭证字段行、PEM 和长随机凭证串。
- 非 Linux / `--dev` 返回 `409 system_unsupported`，不执行系统命令、不返回模拟成功。

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

所有持久化写操作通过 `singbox.Manager.Apply(ctx, mutation)` 走「生成 → check → 生效」流程。
创建返回 201，修改/启停返回 200 最新脱敏对象，删除成功返回 204；失败按错误表返回。
`/inbounds/suggest` 只提供推荐参数，不写 DB、不触发生效。

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

### T3.1 入站、用户与 Reality 的具体约定

本任务实现上述入站、用户 CRUD/启停/重置以及 Reality 接口。
`/users/{id}/links` 按 TASKS.md 留在 **T3.2**，此时仍返回 404；不实现订阅、规则、设置或引导接口。
所有接口需认证，POST/PUT/DELETE 校验 CSRF；GET 支持 HEAD，响应均为 `Cache-Control: no-store`。
JSON 请求沿用 8 KiB 限制，拒绝 null（含嵌套字段）、重复/未知字段、错误类型和多余 JSON。
删除、reset-token、reset-credentials 使用空请求体；toggle 必须给出布尔 `enabled`。
已知路径的方法错误返回 JSON 405 和完整 Allow，非法 ID 为 400，正整数 ID 不存在为 404。

**入站**：

- 列表是按 ID 升序的 JSON 数组（空列表为 `[]`）；详情与写入成功响应均为
  `{id,tag,protocol,port,enabled,remark,options,created_at,updated_at}`。
  options 使用 CONFIG.md §1 的协议字段，永不返回 `private_key` 或 `obfs_password`；
  Reality 保留 `public_key`。其他未知存储字段也不透传。时间为 Unix 秒。
- POST 必须提供 `protocol`（仅 vless_reality / hysteria2）；可选 `port/tag/enabled/remark/options`。
  默认 enabled=true、remark=""；端口优先 443/TCP、8443/UDP，已占用时在 10000–60000
  中选择空闲端口；tag 默认 `vless-reality-<port>` / `hysteria2-<port>`。
  tag 限 1–64 位 `[a-zA-Z0-9_-]` 且唯一，remark 最多 1024 字节。
- Reality 未给 server_name 时使用本机 TLS 检测后的首个推荐目标；没有合格目标返回
  `409 reality_target_unavailable`，提示检查网络或手动填写目标，不把未检测域名当作可用推荐。
  handshake_server 默认跟随创建时的 server_name、handshake_port=443、fingerprint=chrome；
  自动生成 X25519 密钥对和一个 16 位 short ID。short_ids 接受 1–16 个偶数长度的 8–16 位 hex。
  显式 private_key 未配 public_key 时自动推导，二者都给出时检查对应关系。
- Hysteria2 默认 sni=www.bing.com、cert_mode=self_signed、带宽均 0、混淆关闭，
  port_hopping={enabled:false,start:20000,end:30000}。带宽非负，混淆密码最多 1024 字节。
  实际证书仅由生产 Apply 生成。ACME、其他协议不接受。
- PUT 采用**部分更新**：省略的顶层及 options 字段保持原值（包括私钥和混淆密码），
  port_hopping 也逐字段合并；数组整体替换。不允许切换 protocol，需新建入站。
  更新 port 不自动更改 tag；显式空混淆密码表示关闭混淆。禁用记录同样校验参数。
- 端口须为 1–65535；事务内检查所有入站的唯一端口（含禁用记录）、面板 DB/实际监听端口、
  Clash TCP 控制端口及启用 Hysteria2 的跳跃冲突，返回 `409 port_in_use`；tag 重复为 `409 tag_exists`。
  Linux 生产在进入 Apply 前用可注入的 `ss -H -lntu` 快照检查对应协议的系统监听，
  保持原端口的已启用入站视为自身监听。检查失败返回 500 并提示安装 iproute2/检查权限。
  快照不预留端口；之后被抢占仍由 Apply 重启/存活检查发现并回滚。
- suggest 接收 `{protocol}`，返回可直接作为 POST body 的 `{protocol,port,options}`。
  原文“密钥…”与凭证不出 API 的规则冲突：最小修正为 suggest **不返回私钥，也不返回
  无法复用的临时公钥**；创建时在服务端生成新密钥并返回公钥。suggest 不落库、不预留端口。

**用户**：

- POST 的 name 必填；PUT 可部分更新 name/remark/traffic_limit/expire_at，其余字段不能直接修改。
  name 为 `[a-zA-Z0-9_-]{1,32}`、区分大小写且唯一，重名为 `409 name_exists`；
  remark 最多 1024 字节；流量上限、到期时间为非负 int64，0 分别表示不限/永不过期。
- 默认启用、流量计数为零；UUID v4、24 字节 base64url 密码与 token 均用 crypto/rand 生成。
  列表按 ID 升序，返回数组；对象为
  `{id,name,uuid,enabled,traffic_limit,traffic_up,traffic_down,expire_at,remark,created_at,status}`。
  uuid 只显示首尾各四位，中间 `****`；不返回 password/sub_token。
- status 按优先级为 disabled / expired / exhausted / active；到期和超额边界同 CONFIG.md，
  加总流量避免 int64 溢出。这里只计算展示状态和随 Apply 生成配置，不实现 T7.1 的采样/定时禁用。
- 创建 201；更新、启停、重置均 200 返回最新脱敏对象；删除 204。
  reset-token 只更新 token，reset-credentials 只更新 UUID/密码，均触发 Apply。
  新 token 不由此接口返回，T3.2 的 links 接口负责提供新的订阅地址。

**事务与开发环境**：

- 所有以上持久化写操作（包括删除、启停及两种重置）只进入 `Manager.Apply(ctx, mutation)`。
  mutation 的所有查询、唯一性检查、写入和响应对象读取只使用传入的 `*sql.Tx`，
  不使用单连接池 `*sql.DB`，不自行提交或执行系统副作用。Apply 成功返回后才发送响应。
  生成/check 失败 422、重启/防火墙/恢复失败沿用 T2.3 契约；失败不提交资源变更。
- **仅开发用的例外**：`serve --dev` 显式装配 `ApplyOptions{Dev:true,DevDBOnly:true}`，
  仍从同一个 Manager.Apply 入口提交非 nil mutation，但只写 DB，**不生成/check/写配置、
  不生成证书、不重启服务、不修改防火墙**。保持认证、CSRF、事务和参数/DB 冲突校验，
  不探测宿主监听端口。成功的资源写响应头为 `X-Boxpanel-Apply: dev-db-only`，启动日志也明确提示。
  使用独立开发 DB，不应把此响应解释为节点可连接；TLS 推荐/检测仍执行真实、可注入的探测。
- Linux 生产成功写响应头为 `X-Boxpanel-Apply: applied`。
  非 Linux 且未开 --dev 的资源写入仍为 `409 system_unsupported`。
  未显式启用 DevDBOnly 的普通 Manager 在非 Linux / --dev 下仍拒绝 Apply。
  `/singbox/restart|logs|config`、CLI apply、启动行为保持 T2.3 约定，开发替身不提供模拟生效成功。

**Reality 探测**：

- 候选域名集中在 `internal/reality/probe.go` 的常量 `CandidateDomains`，不在 T3.1 增加设置覆盖接口。
  recommend 并发检测候选，共享最多 3 秒窗口，只返回证书有效、TLS 1.3、ALPN=h2 的结果，
  按握手耗时升序取前三，同耗时保持候选顺序；全失败时 `200 {targets:[]}`。
- check 的 server 必填，仅接受 ASCII 公网域名/公网 IP（不接受 URL、host:port、私网/回环地址）；
  port 默认 443，范围 1–65535。参数错误 400；网络、证书、协议不合格为 200 且 `ok=false`，
  附 `hint` 告知检查网络/证书或更换目标；不返回原始网络错误。latency_ms 为成功 TLS 握手耗时，
  不含 DNS 时间，失败为 0。
- 默认探测验证系统信任链、证书主机名和有效期，检查 TLS/ALPN；DNS 答案含内部地址则拒绝，
  连接固定到已验证 IP 并保留原主机名 SNI，防止再次解析绕过限制；每次最多 3 秒。
  HTTP handler 可注入整个 Probe，探测器还可注入 DNS、TLS 交换和时钟，单测不访问网络。
  TLS 行为依据 [Go crypto/tls 文档](https://pkg.go.dev/crypto/tls#Dialer)。

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

### T3.2 订阅、规则与设置约定

- 上述 links/rules/preview/settings 均已实现；需登录，PUT 保持 CSRF 与严格 JSON 校验，
  GET 支持 HEAD，响应 no-store，错误沿用统一 JSON 契约。setup、备份和 M4 UI 不在此任务。
- `/users/{id}/links` 是有意提供可导入凭证的专用接口：仅管理员可取完整 token/用户凭证 URI，
  不返回服务端私钥；普通用户 CRUD 继续脱敏。URL 只使用保存的 public_host、sub_path、TLS 模式
  和实际监听端口（包括 CLI 初始覆盖），不信任 Host、X-Forwarded-*。端口切换后需重新获取订阅链接。
- `/rules` PUT 为整体替换，200 返回保存后的规则；不重启 sing-box 服务。字段范围见 CONFIG.md。
  `/rules/preview` 默认 singbox，只接受单个 `format=singbox|clash`，选 ID 最小的用户，
  无用户为 404；200 返回 `{format,config}`，singbox 的 config 为 JSON 对象，clash 为 YAML 字符串。
  UUID、密码、混淆密码全部替换为 `[REDACTED]`，token 不包含在配置中；含转义的凭证同样脱敏。
- Hysteria2 缺失已生效证书/指纹时，links/preview 返回 `409 subscription_unavailable`，
  提示先在 Linux 生效配置；开发模式不生成临时证书、不用 insecure 兜底。
- `/settings` PUT 为部分更新，省略字段保持。成功 200 返回原 GET 字段加
  `{panel_url,restart_after}`，restart_after 为 0 或 2（秒）。
  public_host 接受 ASCII 域名或裸 IPv4/IPv6，不接受 URL/端口；panel_name 为 1–128 字节、无换行。
  web_path 为空或 1–64 位小写字母数字，sub_path 为 1–64 位小写字母数字，二者不能相同；
  sub_path 保留 api/assets/index。端口 1–65535，不得与任何入站或 Clash 控制端口冲突（409）。
  panel_tls_mode 当前只能保持原值，ACME 留在 T7.2，不允许 API 降级 HTTPS。
- 客户端/面板元数据仅在 SQLite 事务中保存；block_bittorrent **实际改变**时，整个 settings
  更新必须通过 Manager.Apply，失败全部回滚（包括同时提交的地址和名称）。开发替身保持 T3.1 标记。
  更新前后的比较防止并发覆盖；冲突为 `409 settings_changed`，要求刷新后重试。
- 改端口/面板路径时，先构造新 handler、预绑定新 TCP 监听，再持久化；占用/切换未结束时返回 409。
  事务失败释放预留监听；成功后延迟 2 秒启用新监听/路由，旧监听优雅关闭，同端口仅替换 handler。
  新 handler 同步更新 cookie Path、SPA basename 与 API 前缀。跨路径需重新登录，不重启整个进程。
  不自动更新云安全组或现有面板证书 SAN；更换 public_host 时请使用证书覆盖的主机名/地址。
  `--dev` 固定 2053/根路径，拒绝修改这两项；其他设置可正常编辑。
- sub_path 更新立即用于路由判断，旧路径失效；规则、名称、public_host 修改立即影响下一次订阅。
  正在处理的请求仍使用开始时的一致快照。此次不增加流量采样或定时封禁。

公开订阅 `/{sub_path}/{token}`：

- GET/HEAD，无登录 cookie；SHA-256 将两边 token 归一为固定长度，再用 crypto/subtle 常量时间比较。
  遍历所有存储 token，不使用 SQL `WHERE sub_token=?` 或找到匹配即提前退出。
  错误 token、未知路径、非法路径、禁用/到期/超额用户都是相同的空白 404（无订阅响应头）。
- 使用真实 TCP 对端 IP，不信任代理头；同 IP 每个 60 秒固定窗口允许 30 次，第 31 次起 429，
  带 Retry-After。计数包括错误 token、HEAD 和该订阅命名空间内的非法请求，合法 token 也不能绕过。
  内存限流表最多 10000 项，过期清理，满时拒绝新来源；重启清空，切换监听不重置。
- 显式 format 参数优先于 UA；不支持的值、空值或重复参数在 token 有效后返回 400；UA 表见 CONFIG.md。
  成功配置类型为 application/json、application/yaml 或 text/plain，通用三项响应头见 CONFIG.md。
  无法生成时返回无敏感细节的 503 与下一步建议，不透传 DB/证书错误。
- 浏览器 HTML 以 text/html 内联显示（不发 attachment），仍带流量/更新头；二维码在本地生成 PNG，
  页面使用 no-store、no-referrer、CSP、HTML 转义，无外部脚本、字体、QR 服务或请求日志中的 token。
  显示各格式链接、sing-box/Clash 导入按钮、流量与 UTC 到期时间，提示客户端版本与 URI 规则限制。

自动化验证：三种格式 × 三个预设 × 默认/完整选项/空节点共 27 份 golden；9 份 sing-box golden
逐一执行锁定内核 check。另覆盖 UA、URI 特殊字符/IPv6、规则顺序/源、有效期/流量边界、
限流并发/窗口/代理头、404 伪装、token 重置、认证/CSRF/HEAD、预览脱敏、settings Apply 回滚及真实本地监听切换。

## 备份（后期）

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/backup` | 下载 `boxpanel-backup-<date>.json`（DB 全量 + 证书） |
| POST | `/restore` | 上传备份并重新生效 |
