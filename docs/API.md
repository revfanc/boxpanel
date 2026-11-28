# 管理 API 契约

前缀：`/{web_path}/api/v1`。除 `/auth/login` 外均需登录会话。除加密备份下载和恢复上传外，请求/响应均为 JSON。

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
| 503 | 生效总预算耗尽（`singbox_apply_timeout`）或诊断忙（`singbox_applying`），带 Retry-After: 1；刷新核对后重试 |
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
  T4.2 前端仅将修改密码接口的 `401 invalid_credentials` 作为本地表单错误，保留有效会话；
  中间件的 `401 unauthorized` 仍清理会话并跳转登录，不将两种 401 混为一谈。
- cookie、会话有效期、开发模式与 CSRF 细节见 SPEC.md §8。
  登录也要求通过来源检查；curl 等没有 Origin / Sec-Fetch-Site 的客户端可正常调用。
- JSON 请求上限 8 KiB，拒绝未知字段、多余 JSON 值、无效字段与错误 Content-Type，返回 400。
  `me` 支持 HEAD（同 GET 状态/响应头，无 body）。所有认证响应 `Cache-Control: no-store`。
- T1.2 只注册上表认证接口；未知 API 返回 JSON 404，不实现后续系统/用户/节点等资源。

## 引导 / 系统

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/setup/status` | `{completed, step, domain, hysteria2}`，见 T5.1 约定 |
| PUT | `/setup/status` | `{completed?, step?, domain?, hysteria2?}` 保存进度及场景选择 |
| GET | `/system/check` | 环境自检，见下 |
| GET | `/system/upgrade` | 读取主控升级状态，返回 `{state,current?,target?}` |
| POST | `/system/upgrade` | 空请求体。检查最新正式版，有更新时启动安装并返回 202，当前已是同版或更高时返回 200 |
| POST | `/system/bbr` | 开启 BBR（写 sysctl.d 并立即生效） |
| GET | `/system/status` | `{cpu, mem:{used,total}, disk, uptime, net:{rx_rate,tx_rate}, singbox:{running, version, uptime}}` |
| GET | `/system/public-ip` | 重新探测 `{ipv4, ipv6}` |
| POST | `/system/port-probe` | `{port}` → 本机公网 TCP 自连结果，见 T5.1 约定 |

### T5.1 引导状态、域名校验与端口自测

上述 setup GET/PUT 与 port-probe 已注册，全部要求登录、no-store；PUT/POST 遵守现有
CSRF、8 KiB 严格 JSON 和统一错误契约。GET 支持 HEAD。前端不再对 setup 404 兼容放行。

`GET /setup/status` / `PUT /setup/status` 成功均返回 200，例如：

```json
{"completed":false,"step":"scenario","domain":"","hysteria2":true}
```

- step 仅接受 welcome/check/scenario/node/user/firewall/finish；省略字段保持，空对象为无变化。
  completed 可独立设置（跳过引导），不要求先创建节点/用户；允许返回任一步和重新开启引导。
  非法步骤/类型、null、重复/未知字段、多余 JSON 为 400。GET 读取一个一致事务快照。
- 原有 setup_completed/setup_step 继续使用；新增 settings.setup_preferences 保存
  `{domain,hysteria2}`，只在第一次保存时写入。旧库缺此键视为 domain=""、hysteria2=true，
  无需修改已发布迁移。该键不存凭证、订阅链接或表单草稿。
- domain 非空时只接受 ASCII DNS 名（不接受 IP/URL/端口），trim/转为小写。
  **每次显式提交非空 domain 都重新校验**：先探测本机公网 IPv4/IPv6，再进行可注入的 DNS
  A/AAAA 查询，共享 3 秒窗口（不支持的平台不会额外查询 DNS）。至少一个答案，且每个答案都必须是本机探测出的公网 IP；
  IPv4-only 或 IPv6-only 均可。其他地址、私网、fake-ip、无答案/查询失败/超时返回
  `409 domain_not_pointing_here`，提示修正记录、关闭 CDN、等待 DNS 生效或选择无域名。
  公网探测失败为统一系统错误；开发/非 Linux 默认 `409 system_unsupported`，不伪造匹配。
- 验证成功后在同一 SQLite 事务内写 domain、public_host 及其余进度字段，任一步失败全部回滚。
  从已保存域名改回空字符串时重新探测并恢复公网 IP（IPv4 优先），探测失败不修改设置。
  初次无域名保留初始化的 public_host；--dev 的初始回环地址可用于界面演示。
  T6.3：事务提交后通知证书管理器；acme 模式自动为新 public_host 签发，失败使用匹配新地址的自签备用证书。
- T6.6：PUT 实际改变 public_host 时，成功响应额外包含 `panel_url`、`panel_tls_mode`，例如
  `{"completed":false,"step":"node","domain":"node.example.com","hysteria2":true,"panel_url":"https://node.example.com:2053/panel/","panel_tls_mode":"acme"}`。
  URL 只由已保存主机、TLS 模式及实际监听端口/路径构造（含 CLI 覆盖），不信任请求 Host/代理头；
  在事务内计算、提交成功才返回。主机未改变、普通进度保存及 GET 不带这两个字段。
  域名改回 IP 也返回新地址；开发 http 返回供提示，前端不跳转。原错误/回滚/认证契约保持。
- setup/public_host 是引导及客户端元数据，沿用 `/settings` 的普通事务约定，不重启内核、
  不写配置、不返回 Apply 生效标记。节点和用户仍调用原 CRUD → Manager.Apply，保持
  check/生效/回滚及 dev-db-only 响应；引导没有第二条写配置路径。

`POST /system/port-probe {"port":443}` 成功示例：

```json
{"port":443,"results":[{"ip":"203.0.113.1","ok":true},{"ip":"2001:db8::1","ok":false}],"hint":"仅检测本机 TCP 自连，不代表外网或云安全组已放行；失败请核对监听、本机防火墙、安全组及 NAT 回环支持，再从外部客户端验证。UDP 无法自动检测。"}
```

- 仅接受必填整数 port，范围 1–65535；host/target/url/proto 等未知字段一律 400。
  **仅从可信系统公网 IP 探测结果取目标**，再次验证公网地址；不读取可编辑的 public_host、
  请求 Host/代理头或请求提供的 DNS 目标。直接连接已验证的 IP 字面量，不再次解析 DNS，防 SSRF。
- 公网探测和所有 TCP 连接共享 **3 秒总超时**；IPv4/IPv6 并行，连接结束立即关闭。
  每个地址单独返回 ok；拒绝/网络超时返回 200 + ok=false，不暴露原始网络错误。
  无公网地址/非法探测地址为 500；开发/非 Linux 为 409 system_unsupported，不连接回环冒充成功。
- 自连成功不能证明外网或安全组可达；NAT 不支持回环时也可能失败。UDP 不探测、不推断成功。
  此接口不写 DB、不修改防火墙、不调用 Apply。依赖可注入：Options.System.PublicIP、
  Options.LookupIP、Options.DialContext；新增单测使用 fake/net.Pipe，绝不访问真实网络。

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
主控升级接口需要管理员会话。GET 读取本机任务状态，不访问发布服务。POST 由服务端选择官方仓库的最新稳定 tag，重复提交运行中的任务返回同一目标。
`state` 取 `idle`、`running`、`succeeded`、`failed`、`current` 或 `unsupported`。失败后可重试。开发模式或自定义布局下提交返回 422。

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
  T6.4：先以 `tcp4` 请求 IPv4 来源，再收集 IPv6；3 秒总窗口为 IPv6 保留至少一半剩余预算，
  避免 IPv4 超时阻止回退。check、域名校验及 port-probe 共享此实现，双栈响应格式不变；
  选择单一地址与本机 IP 列表均优先 IPv4。CLI `set-host` 复用 settings 的主机校验，详见 INSTALL.md。
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
- T6.10：Apply 正常工作（含锁排队）最多 12 秒，独立恢复最多 12 秒，入口起总预算 24 秒，
  低于 HTTP 写超时 30 秒。超时且恢复成功/尚未换文件返回 `503 singbox_apply_timeout`，
  `Retry-After: 1`；提示稍后刷新用户/内核状态，确认结果后再重试，禁止前端自动重发写操作。
  恢复失败/DB 状态不确定仍返回 500，保留原错误码及恢复提示，不声称成功。
  logs/config 在同进程生效时立即返回 `503 singbox_applying`；跨进程锁最多等待 100ms。
  普通订阅、会话及面板数据读取走独立只读 WAL 池，生效中返回上一份已提交快照，提交后可见新值。
  sing-box 1.14.4 SIGHUP 会关闭并重建实例，不能保持连接；实际改变生成配置的用户变更仍按 SPEC §7 重启；T7.1b 的配置/计划不变且运行中写操作只提交 DB。
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
| POST | `/reality/check` | `{server, port?}` → 单个检测结果；目标解析到 Cloudflare 时 `ok:false` 并在 `hint` 说明中转风险 |

## 服务器（T8.1–T9.6）

| 方法 | 路径 | 请求与成功响应 |
|---|---|---|
| GET | `/servers` | 200，服务器数组，本机优先，其余按 ID 升序 |
| GET | `/servers/{id}` | 200，指定服务器的元数据及入站计数 |
| PUT | `/servers/{id}` | `{name?, public_host?, auto_upgrade?}`，至少提供一项，200 返回更新后的服务器 |
| DELETE | `/servers/{id}` | 移除远程服务器、撤销常规节点权限、解除关联出口链路，204 |
| GET | `/servers/join-tokens` | 200，未过期且未使用令牌的元数据数组 |
| POST | `/servers/join-tokens` | `{"name":"服务器名称"}`，201，返回元数据、`token`、`master_url` 和 `master_pin` |
| GET | `/servers/join-tokens/{id}` | 200，指定未过期令牌的元数据，若已注册则附 `server_id` |
| DELETE | `/servers/join-tokens/{id}` | 撤销未使用的令牌，204（含已使用或已不存在） |

每项返回 `id`、`name`、`public_host`、`is_local`、`status`、`last_seen_at`、`version`、`created_at` 和
`updated_at`。状态为 `local`、`online`、`offline` 或 `pending`。时间使用 UTC Unix 秒，`last_seen_at` 可为 `null`。
`version` 为已记录的 boxpanel 版本，未记录时为空。`protocol` 为节点通信协议版本。本机报告当前协议版本。
远端协议低于主控最低要求时返回 `upgrade_required: true`，且 `sync_status` 为 `failed`，联系状态独立显示。
接口要求登录，改名沿用 CSRF 校验。
名称去首尾空白后须为 1–64 个字符，且原值中不得包含控制字符。无效名称返回 400，未知 ID 返回 404。
服务器资料中的 `public_host` 用于订阅，远端接受域名或裸 IPv4/IPv6，本机通过设置 API 修改。只读字段 `public_ip` 为本机探测或签名心跳报告的公网地址，首次成功探测前省略。
远端服务器还返回 `desired_generation`、`applied_generation`（0 表示尚未生效）、`sync_status`（`syncing`、`synced`、`failed`）
及最近心跳记录的 `singbox_active`。有失败时返回固定的 `sync_error` 代码，面板根据代码展示原因和处理建议。
`GET /servers` 的远端行还包含 `traffic_up`、`traffic_down`，单位为字节。两项汇总该服务器所有未删除用户已确认的用量，
包含历次节点 epoch，从 0 开始并饱和到 int64 上限。用户清零不改变服务器累计值。面板统一使用下述当前周期接口。
`GET /servers` 每项还包含 `inbound_counts: {ready, pending}`，按密钥就绪状态统计该服务器的入站，包含已停用的入站。
远端有已保存的心跳运行时长时返回 `uptime`，单位为秒，取最近心跳时的节点进程时长。
T9.3：远端行返回 `auto_upgrade`（布尔，默认 false），本机不返回；对本机设置返回 400。修改不递增期望版本、不重启任何内核，
随下次心跳回复下发。心跳回复增加可选字段 `auto_upgrade`（仅 true 时出现），旧节点忽略它，协议版本不变。
服务器列表排除 `removed`；本机、已移除或不存在的服务器无法通过 DELETE 移除，返回 404。
令牌元数据为 `id`、`name`、`expires_at`。只有签发响应含令牌明文。使用和撤销后从可用列表消失。
令牌详情的 `server_id` 在注册成功时写入，首次心跳前也会返回；向导通过 `/servers` 确认该服务器的 `last_seen_at` 非空。
令牌已过期、撤销、未知或关联服务器已移除时，详情返回 `404 not_found`。
无论主控当前使用自签或 ACME 证书，`master_pin` 都是固定回退公钥 SPKI DER 的 SHA-256（无填充 base64url）。
签发要求 HTTPS，HTTP 面板返回 `409 agent_requires_https`。令牌管理和服务器移除沿用管理员会话与 CSRF 校验。

### 节点 API（T8.2–T9.6）

前缀为 `/{web_path}/api/agent/v1`，只接受 HTTPS。带请求体的 POST 使用 `application/json`。节点使用签名认证。
请求体限 8 KiB，`/traffic` 限 1 MiB，`/interface-samples` 限 256 KiB，`/logs` 限 2 MiB。
路径不含查询参数。所有 base64url 值均不带填充。

| 方法 | 路径 | 请求与成功响应 |
|---|---|---|
| POST | `/join` | `{token, public_key, hostname, public_ip, version, protocol?}`，201 `{server_id}` |
| POST | `/heartbeat` | `{version, protocol?, interfaces?, singbox_active, uptime, applied_generation?, sync_error?, metrics?, restart_result?, exit?, public_ip?}`。提供 `protocol` 或 `applied_generation` 时 200 `{desired_generation, protocol, version, interface, log_request?, restart_pending?, report_public_ip?, proxy_test?}`。旧格式均省略时返回 204。顶层 uptime 为节点进程运行秒数 |
| GET | `/desired` | 空请求体，200 `{generation, inbounds, users, route, restart_id?, exit?}` |
| POST | `/inbound-keys` | `{inbound_id, key_id, reality_public_key?, short_id?, hy2_cert_sha256?, hy2_cert_pem?}`，204 |
| POST | `/traffic` | `{counters:[{user_id,epoch,up,down}]}`，204 |
| POST | `/interface-samples` | `{samples:[{sampled_at,interface,rx,tx}]}`，204 |
| POST | `/logs` | `{id,batch_id,snapshot,lines}`，204 确认该批日志 |
| POST | `/proxy-test-result` | `{id,ip?}`，204，失败时省略 `ip` |

当前协议为 4，普通同步的最低协议仍为 2。旧请求省略 `protocol` 时按 1 处理。协议 1 节点仍可注册和发心跳，
其签名认证通过的期望状态、流量、网卡样本及公开密钥请求返回 `426 agent_upgrade_required`。心跳回复的
`version` / `protocol` 属于主控，`interface` 是该节点的网卡偏好，空字符串表示自动检测。
`interfaces` 是最多 32 个互不重复的非回环 Linux 网卡名称。省略时保留上次选择列表，空数组会清空列表。
主控返回低于最低要求的协议、未返回协议，或同步接口返回 404 时，节点保持上次生效配置，并尝试用心跳上报
`sync_error: "master_outdated"`。旧主控可能拒绝新增字段或错误码，节点会使用旧格式保持联系，并在本地日志和 `boxpanel info` 显示升级提示。

注册按 TCP 对端 IP 限流，每分钟 10 次，返回 429 时带 `Retry-After`。`public_ip` 为空时记录连接对端 IP，
不会采用转发头。非公网地址（包括未传地址且对端为私网的情况）返回 `422 agent_public_ip_required`，
节点可补充 `--public-ip` 后使用同一有效令牌重试。失效、已使用、过期或撤销的令牌统一返回 `401 invalid_join_token`。

主控在可写心跳回复中通过 `report_public_ip: true` 声明支持接收公网 IP。节点收到后在后续心跳中附 `public_ip`。省略时保留最近的探测值，无效地址返回 `422 agent_public_ip_required`。旧版节点可继续使用登记时的链路地址。

注册后所有节点请求的签名头为 `X-Boxpanel-Server`（十进制 ID）、`X-Boxpanel-Timestamp`（Unix 秒）、
`X-Boxpanel-Nonce`（16 字节随机数的 base64url）和 `X-Boxpanel-Signature`（Ed25519 签名的 base64url）。
签名原文为 `method\npath\ntimestamp\nnonce\nsha256(body)`，使用原始请求体字节和小写十六进制 SHA-256。
`path` 包含面板路径，例如 `/k3m9x2p7q1ab/api/agent/v1/heartbeat`。
签名认证失败统一返回 `401 agent_unauthorized`。节点使用 401 响应的标准 `Date` 头诊断超过 100 秒的时钟偏差。
`/desired` 和 `/inbound-keys` 只使用签名认证所对应的服务器身份。指向别的服务器或其入站返回 401。
`/desired` 的 `inbounds` 为 `[{id,key_id,tag,protocol,port,enabled,remark,options}]`，只包括该服务器的入站。
其中 `key_id` 是入站材料版本。Reality options 的公私钥和 Short ID 为空，Hysteria2 options 不含证书材料。
`users` 为 `[{id,name,uuid,password}]`，只含主控判定的有效用户。入站混淆密码和用户凭证通过签名认证的 TLS 通道下发。
`route` 包含 `block_bittorrent`。节点同步响应使用 `Cache-Control: no-store`，日志只记路由和状态，节点限制期望状态体为 32 MiB。

节点在成功 Apply 后才上报该入站当前的公开材料。每次上报只提交对应协议的字段。Reality 使用无填充 base64url 的 32 字节公钥
和 16 位十六进制 Short ID。Hysteria2 提交公开叶证书 PEM 和 DER 的 SHA-256 小写十六进制，主控验证 SNI、有效期、自签名和指纹。
过期的 `key_id` 返回 `409 stale_inbound_keys`，无效的公开材料返回 `400 invalid_inbound_keys`。同一版本和材料允许重复上报。
`sync_error` 可为空或为 `check_failed`、`port_in_use`、`restart_failed`、`firewall_failed`、`apply_timeout`、
`rollback_failed`、`apply_failed`、`key_publication_failed`、`master_outdated`。主控拒绝其他值，避免向面板转发节点日志或凭证。

`/traffic` 只按签名认证取得的服务器 ID 记账，已移除服务器统一返回 `401 agent_unauthorized`。
每批最多 10000 条，`user_id` 和 `epoch` 为正整数，`up`、`down` 为非负 int64 累计字节。
重复 `user_id`、未知字段和无效 JSON 返回 `400 invalid_argument`，请求体超限返回 413。已删除的用户记录静默忽略。

`/interface-samples` 每批最多 1000 条，按签名取得的服务器 ID 记账。`sampled_at` 为区间结束时的 UTC Unix **纳秒**，
须为正数且最多比主控当前时间快 120 秒。`rx`、`tx` 是非负 int64 区间增量字节。非法网卡名、无效 JSON 或字段返回
`400 invalid_argument`，超出体积限制返回 413。重复 `(server_id, sampled_at)` 保留首次记录，超过 62 天的样本过期，
重传不会恢复过期数据。空批可用于确认接口存在。
请求成功表示整批已确认。节点以新签名重试同一快照不会重复计数。较旧 epoch 忽略，同一 epoch 的每个方向独立保留最高确认值。
收到较新 epoch 时，该 epoch 从零计量。总量和配额以主控为准。具体重置与补报口径见 [SPEC §19](SPEC.md#19-t84-远端流量汇总)。

T9.5 增补字段沿用协议 2。`metrics` 包含 `cpu`（0–100，保留一位小数）、`mem` 和 `disk`（`[已用,总量]`，十进制 MB）、
`net`（`[下行,上行]`，字节/秒）、`uptime`（系统运行秒数）、可选 `unavailable`（采样不可用）和
`core`（可选，含 `version`、`up` 内核运行秒数，省略内部字段时表示空字符串与 0）。
缺少整个 `metrics` 表示旧节点。新节点遇严格校验的旧主控会去掉 T9.5 可选字段并保留基础同步字段重试。

`log_request` 为 `{id,ttl_ms}`。节点仅在有请求时上报，使用同一 `id` 和每批唯一的 `batch_id`。
首批 `snapshot=true`，可为空，之后仅传新增行。每批最多 200 行，每行不超过 1024 字节，主控只接受该签名服务器的当前请求。
过期或不同租约返回 `409 log_request_expired`。批次确认后再推进节点本地游标，重复确认同一批不会重复追加。
`restart_pending=true` 提示节点拉取期望状态中的 `restart_id`。节点以 `restart_result:{id,error?}` 上报，省略 `error` 表示成功。
失败使用配置同步中的适用错误码或 `interrupted`。主控只接受匹配当前待处理指令的结果，重放旧结果不影响新指令。
冻结或退役的只读主控和验证候选地址的探测均不下发日志请求与重启指令。

T9.6 期望状态 `exit` 只发给已经报告协议 3+ 的参与节点。同一个节点可以同时收到
`outgoing:{id,revision,address,port,password,mode,failure,rules}` 和
`incoming:[{id,source,port,password}]`。`address` 是出口公网 IP，`source` 是入口公网 IP。
字段缺失表示没有相应方向的链路。节点将该字段与用户入站镜像一起生效。管理页面、公开订阅和预览均不传链路凭证。

协议 3+ 节点识别主控支持后，心跳可添 `exit:{probe?,usage?,ports?}`。`probe` 包含
`{id,revision,checked_at,failures,available,policy_down?}`。`checked_at` 为 UTC Unix 秒，
`available` 是已经确认的探测结果，`policy_down` 表示入口已应用故障路由。主控只接收签名身份属于当前入口、
版本匹配且时间更新的结果。`usage:{up,down}` 是作为出口的非负累计字节，重试保留各方向的最高值。
`ports` 为 1250 字节位图，经 zlib 压缩后用无填充标准 Base64 编码，表示 20000–29999 中在 TCP 或 UDP 上监听的端口。
位序从字节 0 最低位开始。与协议 2 主控联系时不发送该字段，收到严格解码 400 时会去掉它重试。

曾参与出口链路的节点被移除后，原公钥只能用于验证心跳并回复 `410 exit_server_removed`。
节点据此在本机生效空出口镜像并清理相应防火墙。该身份访问其他节点接口仍返回 401。

### T9.5 服务器详情与概览

以下路径要求管理员会话。写操作继续校验 CSRF。`id` 是服务器 ID，服务器已移除或不存在时读取返回 404。

| 方法 | 路径 | 请求与成功响应 |
|---|---|---|
| GET | `/servers/{id}/status` | 200 `{server_id,metrics,metrics_at,singbox,singbox_active,restart}` |
| GET | `/servers/{id}/traffic` | 200 `{server_id,billing,user_traffic,users}` |
| GET / PUT | `/servers/{id}/alerts` | 读取 `{config,interfaces}`。写入 config 的部分字段并返回更新后的 config |
| POST | `/servers/{id}/restart` | 空请求体。本机同步重启成功返回 204，远端返回 202 和 `{id,status:"pending"}` |
| GET | `/servers/{id}/logs` | 200 `{ready,lines}`，最近 200 行已脱敏日志。远端未收到首批时 `ready=false` |
| PUT / DELETE | `/servers/{id}/logs/{viewer}` | 空请求体。远端查看租约登记、续期或撤销成功返回 204 |
| GET | `/overview` | 200 `{servers,user_traffic,top_users,expiring_users,exhausted_users,unread,unread_count,recent}` |

- `status` 的 `metrics` 为 `{supported,cpu,mem,disk,net,uptime}`。`mem` 与 `disk` 均为 `{used_mb,total_mb}`，
  `net` 为 `{rx_rate,tx_rate}`，单位为字节/秒。远端 `metrics_at` 为最新心跳 UTC Unix 秒，本机实时采样时为 null。
  尚未报告指标的远端 `metrics=null`。采样不可用时 `supported=false`。`singbox` 为 `{installed,running,version,uptime}` 或 null，
  内核状态不完整时仍可通过 `singbox_active` 读取已知状态。`restart` 为 null 或 `{id,status,error?}`，状态为
  `pending`、`succeeded` 或 `failed`。同一服务器的远端重启指令待处理时再次提交会返回原 ID。
- `traffic` 的 `billing` 沿用 T7.9 账期结构，`user_traffic` 为 `{up,down}`，`users` 为本期有用量的 `[{id,name,up,down,traffic_limit,expire_at}]`。
  用户字节数使用各用户各自的当前周期。旧 epoch 不参与远端分项。本机分项包含升级前和移除服务器后未归属的用量。
- 远端日志租约以服务器 ID 及 32 位十六进制 `viewer` 为键，每次 PUT 续期 60 秒。同一服务器可以有多位独立查看者。
  GET 只读取日志。最后一位查看者撤销或过期后，主控清空该服务器的缓存。旧节点的日志读取、租约登记和重启返回
  `409 node_monitoring_required`。
- 概览的 `servers` 使用服务器元数据，另含 `system`、`billing`、`user_traffic` 和 `share_percent`。
  全局 `user_traffic` 合计所有用户当前周期，服务器占比用上传和下载之和计算。三个用户列表均最多 5 项，字段同上。
  用量榜按当期用量排序，到期榜按截止时间排序且限未来 72 小时内的启用用户，超额榜仅含启用且已达限额的用户。
  `unread`、`unread_count` 和 `recent` 沿用 T7.9 结构。

### T9.6 出口服务器

| 方法 | 路径 | 请求与成功响应 |
|---|---|---|
| GET | `/servers/{id}/exit` | 200 `{server_id,address,supported,external_supported,link,candidates,forwarded}` |
| PUT | `/servers/{id}/exit` | `{exit_server_id,mode?,failure?,rules?}` 或 `{external_proxy,mode?,failure?,rules?}`，204 |
| DELETE | `/servers/{id}/exit` | 空请求体，停用此入口的出口，204 |
| POST | `/servers/{id}/exit/test` | 请求体为代理参数，200 `{"ip":"…"}`，可在保存前调用 |

- `external_proxy` 与 `exit_server_id` 二选一。代理参数有 `type`（`socks5`、`http`、`https`）、`host`（公网 IP 或域名）、`port`（1–65535）。需要认证时同时填写 `username`、`password`，免认证时两者留空。
- GET 对外部出口返回 `link.exit_server_id=0` 与 `link.external_proxy={type,host,port,username,has_password}`。编辑、测试已保存的代理时，提交 `use_saved_password:true` 可沿用原密码，要求协议、地址、端口、账号相同且请求未填写密码。
- POST 测试直接接收上述代理参数。入口通过代理访问固定 HTTPS 服务取得来源 IP。测试独立于已保存链路、健康状态与分流规则。同一远程入口测试中返回 409，测试失败或等待节点超时返回 422。远程入口的 `external_supported` 要求协议 4，已管理服务器链路仍支持协议 3。
- 远程测试指令 `proxy_test={id,proxy}` 随所选入口的心跳响应传递，节点向 `POST /api/agent/v1/proxy-test-result` 签名提交 `{id,ip?}`。指令随测试请求结束而释放。

- `id` 是入口服务器 ID。`link=null` 代表入口直连。非空记录返回保存的设置和内部端口 `port`，
  最近确认的探测时间 `checked_at`（可为 null）及 `status`。状态为 `syncing`、`checking`、`available`、
  `unavailable`（已阻断）、`fallback`（已回退）、`policy_pending`、`stale` 或 `upgrade_required`。
- 从 v0.7.0 起，入口在 `split` 模式选择 `rules.presets` 中的 `ai` 即启用每日更新的域名和目的 IP 分流。主控和远端使用相同的设置格式。远端入口升级前继续执行本机版本的规则。
- `mode` 为 `split`（默认）或 `all`。`failure` 为 `block`（默认）或 `direct`。`rules` 有
  `presets`（`ai`、`streaming`）、`domains` 和 `domain_suffixes`（各最多 128 条）。输入规范化为小写，
  去掉末尾的点和重复项。仅提交 `exit_server_id` 时启用分流、阻断以及 AI 和流媒体预设。PUT 整体替换设置。`all` 保留规则以便切回 `split`；分流规则为空时全部直连。
- `candidates` 排除本服务器和已移除服务器。每项包含 `{id,name,address,unavailable_reason?}`，原因可能为
  `upgrade_required`、`pending`、`address_required`、`address_family` 或 `ports_pending`。
  新链路的远程出口需在最近两分钟内上报端口。保存时重新检查两端身份、能力、公网地址和端口。
  不满足条件返回 422，参数无效返回 400，端口用尽返回 409。全部接口需管理员会话，写操作检查 CSRF。
- 只读的 `address` 为当前生效的链路地址。主控根据已保存的 `public_ip` 自动更新，等待生效时继续返回旧地址。IPv4 与 IPv6 需两端一致。
  修改地址、换出口和停用均重新调和受影响两端。`X-Boxpanel-Apply` 沿用 `applied`、`queued` 或 `dev-db-only`。
- `forwarded` 和 `GET /servers/{id}/traffic` 的可选 `exit_traffic` 为 `{up,down}`，单位字节，
  汇总该出口向目标发送和收到的累计流量。已有充当出口记录的服务器才返回 `exit_traffic`。
  此值不加到 `user_traffic` 或网卡计费中。通知事件增加 `exit_down`、`exit_recovered`，按入口和故障去重。

## 入站

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/inbounds` | 列表（options 中私钥字段不返回，返回 `public_key`） |
| POST | `/inbounds` | 创建；body 见下；未给的字段自动生成 |
| GET | `/inbounds/{id}` | 详情 |
| PUT | `/inbounds/{id}` | 修改 |
| DELETE | `/inbounds/{id}` | 删除（同步关闭防火墙端口、清理端口跳跃） |
| POST | `/inbounds/{id}/toggle` | `{enabled}` |
| POST | `/inbounds/suggest` | `{protocol, server_id?}` → 返回可用默认参数，不落库 |

创建 body：

```json
{ "protocol": "vless_reality", "server_id": 1, "port": 443, "remark": "香港-Reality", "options": { "server_name": "..." } }
```

本机入站的持久化写操作使用 `singbox.Manager.Apply(ctx, mutation)`。配置和计划不变且内核运行时按 SPEC §7 的 T7.1b 约定
只提交数据库。仅涉及远端入站的写操作在一笔数据库事务中更新入站和其期望版本，响应头为 `X-Boxpanel-Apply: queued`。
跨服务器移动涉及本机时仍使用本机 Apply 的事务。创建返回 201，修改或启停返回 200 最新脱敏对象，删除成功返回 204。
入站响应含 `server_id`。创建省略时使用本机，修改省略时保持归属。可选择本机和 `online` 或 `offline` 的远端服务器。
未接入、已移除或未知的目标服务器返回 `422 server_unavailable`。远端入站响应附带 `key_status`（`pending` 或 `ready`）。
新建或修改 Reality 入站时，`options.server_name` / `handshake_server` 解析到 Cloudflare 返回 `400 invalid_argument`；
入站列表对目标已在 Cloudflare 的既有 Reality 入站附带 `warning`（解析结果按主机缓存 10 分钟，解析失败不标记）。

端口、标识和跳跃范围按目标服务器校验，主控只探测本机的实际端口和 Reality 推荐。远端 Reality 的目标需手动填写。
远端 Reality 写请求中的 `private_key`、`public_key` 和 `short_ids` 返回 400，由节点生成。
移动入站会按新归属重新分配 Reality 密钥或 Hysteria2 证书。修改远端 Hysteria2 的 SNI 会重新进入 `pending`。

## 用户

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/users` | 列表（含流量、到期、状态） |
| POST | `/users` | `{name, remark?, traffic_limit?, expire_at?, traffic_reset_day?, all_servers?, server_ids?}`，uuid/password/token 自动生成 |
| GET/PUT/DELETE | `/users/{id}` | |
| POST | `/users/{id}/toggle` | `{enabled}` |
| POST | `/users/{id}/reset-traffic` | 清零上传/下载；走 Manager.Apply，200 返回最新脱敏用户 |
| POST | `/users/{id}/reset-token` | 重置订阅 token（旧链接立即失效） |
| POST | `/users/{id}/reset-credentials` | 重置 uuid/password（需重新导入） |
| GET | `/users/{id}/links` | 见下 |

T9.2 可用服务器：用户响应总含 `all_servers`（布尔）与 `server_ids`（数组，`all_servers=true` 时为空，只列未移除服务器，升序）。
`all_servers=true`（默认，升级前的用户亦然）表示全部服务器，含以后新增的服务器。POST/PUT 可带 `all_servers` 与 `server_ids`：
只给 `server_ids` 等同 `all_servers=false`；`all_servers=false` 且省略 `server_ids` 时保留原选择；改为 true 清空选择。
`server_ids` 至多 1000 项、须为正整数并指向未移除服务器（可为本机、在线、离线或待连接），重复项忽略；结果为空时 400
`invalid_argument`（`field=server_ids`）。不可用的服务器不为该用户生成内核配置用户，也不出现在其订阅与 `/users/{id}/links` 中；
修改只使受影响的远端服务器期望版本递增，生成配置不变的服务器不重启。

多台未移除服务器时，`GET /users/{id}` 增加 `traffic_by_server: [{server_id, name, is_local, up, down}]`，本机在前，远端按 ID 升序。
`up`、`down` 是当前清零周期的字节数。远端账目缺失或用户周期不匹配时返回零。
本机项各方向为用户总量减去列表中远端的对应方向之和，下限为零。升级前尚未拆分的用量及本周期内已移除节点的用量归入本机项。
用户与明细取自同一数据库快照。单机详情、用户列表和写操作响应省略该字段。

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
删除、reset-token、reset-credentials、reset-traffic 使用空请求体；toggle 必须给出布尔 `enabled`。
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
- 端口须为 1–65535；事务内检查本机入站的唯一端口（含禁用记录）、面板 DB/实际监听端口、
  Clash TCP 控制端口及启用 Hysteria2 的跳跃冲突，返回 `409 port_in_use`；tag 重复为 `409 tag_exists`。
  Linux 生产在进入 Apply 前用可注入的 `ss -H -lntu` 快照检查对应协议的系统监听，
  保持原端口的已启用入站视为自身监听。检查失败返回 500 并提示安装 iproute2/检查权限。
  快照不预留端口；之后被抢占仍由 Apply 重启/存活检查发现并回滚。
- suggest 接收 `{protocol}`，返回可直接作为 POST body 的 `{protocol,port,options}`。
  T6.3：Reality 探测全部失败仍返回 200 和推荐端口；options.server_name/handshake_server 为空，
  附可操作的顶层 hint。创建仍要求手填目标，调用方不要把 hint 作为创建字段回传。
  原文“密钥…”与凭证不出 API 的规则冲突：最小修正为 suggest **不返回私钥，也不返回
  无法复用的临时公钥**；创建时在服务端生成新密钥并返回公钥。suggest 不落库、不预留端口。

**用户**：

- POST 的 name 必填；PUT 可部分更新 name/remark/traffic_limit/expire_at，其余字段不能直接修改。
  name 为 `[a-zA-Z0-9_-]{1,32}`、区分大小写且唯一，重名为 `409 name_exists`；
  remark 最多 1024 字节；流量上限、到期时间为非负 int64，0 分别表示不限/永不过期。
- 默认启用、流量计数为零；UUID v4、24 字节 base64url 密码与 token 均用 crypto/rand 生成。
  列表按 ID 升序，返回数组；对象为
  `{id,name,uuid,enabled,traffic_limit,traffic_up,traffic_down,traffic_reset_day,traffic_reset_at,expire_at,remark,created_at,status}`。
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
| GET | `/settings` | `{public_host, web_port, web_path, sub_path, panel_tls_mode, block_bittorrent, panel_name, subscription_auto_group}` |
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
  T6.6：未切换监听时 panel_url 保留实际监听端口/运行时路径（包括 CLI 覆盖）；
  主机修改后前端显示新地址提示框，不用 restart_after=0 推断无需提醒。
  public_host 接受 ASCII 域名或裸 IPv4/IPv6，不接受 URL/端口；panel_name 为 1–128 字节、无换行。
  web_path 为空或 1–64 位小写字母数字，sub_path 为 1–64 位小写字母数字，二者不能相同；
  sub_path 保留 api/assets/index。端口 1–65535，不得与任何入站或 Clash 控制端口冲突（409）。
  T6.3：生产 panel_tls_mode 接受 self_signed / acme，禁止降级 http；开发环境只能保持 http。
- 客户端/面板元数据仅在 SQLite 事务中保存；block_bittorrent **实际改变**时，整个 settings
  更新必须通过 Manager.Apply，失败全部回滚（包括同时提交的地址和名称）。开发替身保持 T3.1 标记。
  更新前后的比较防止并发覆盖；冲突为 `409 settings_changed`，要求刷新后重试。
- 改端口/面板路径时，先构造新 handler、预绑定新 TCP 监听，再持久化；占用/切换未结束时返回 409。
  事务失败释放预留监听；成功后延迟 2 秒启用新监听/路由，旧监听优雅关闭，同端口仅替换 handler。
  新 handler 同步更新 cookie Path、SPA basename 与 API 前缀。跨路径需重新登录，不重启整个进程。
  不自动更新云安全组；T6.3 在提交 public_host/TLS 策略变化后异步调整面板证书，详见证书接口。
  `--dev` 固定 2053/根路径，拒绝修改这两项；其他设置可正常编辑。
- sub_path 更新立即用于路由判断，旧路径失效；规则、名称、public_host 修改立即影响下一次订阅。
  正在处理的请求仍使用开始时的一致快照。此次不增加流量采样或定时封禁。
- T8.5：`subscription_auto_group` 为布尔值，默认 `false`。PUT 保存后下一次订阅和规则预览使用新值。
  只改变该项时 `restart_after=0`，不调用服务端 Apply，也不递增节点的 `desired_generation`。
- T8.5：`/users/{id}/links` 的节点按本机、远端服务器 ID、同服务器内入站 ID 的顺序排列。
  规则预览和管理员 links 沿用原响应结构、缓存与脱敏规则，不添加公开订阅的流量、下载和更新间隔响应头。

公开订阅 `/{sub_path}/{token}`：

- GET/HEAD，无登录 cookie；SHA-256 将两边 token 归一为固定长度，再用 crypto/subtle 常量时间比较。
  遍历所有存储 token，不使用 SQL `WHERE sub_token=?` 或找到匹配即提前退出。
  错误 token、未知路径、非法路径、禁用/到期/超额用户都是相同的空白 404（无订阅响应头）。
- 使用真实 TCP 对端 IP，不信任代理头；同 IP 每个 60 秒固定窗口允许 30 次，第 31 次起 429，
  带 Retry-After。计数包括错误 token、HEAD 和该订阅命名空间内的非法请求，合法 token 也不能绕过。
  内存限流表最多 10000 项，过期清理，满时拒绝新来源；重启清空，切换监听不重置。
- 显式 format 参数优先于 UA；不支持的值、空值或重复参数在 token 有效后返回 400；UA 表见 CONFIG.md。
  成功配置类型为 application/json、application/yaml 或 text/plain，GET 与 HEAD 均返回 CONFIG.md §4.2
  的三项响应头，HEAD 无响应体。流量来自主控已确认的本机与远端汇总，未设限额时 `total=0`，
  未设到期时省略 `expire`。Clash 下载名追加 `.yaml`，面板名称为空时使用 `boxpanel`。
  无法生成时返回无敏感细节的 503 与下一步建议，不透传 DB/证书错误。
- 浏览器 HTML 以 text/html 内联显示（不发 attachment），仍带流量/更新头；二维码在本地生成 PNG，
  页面使用 no-store、no-referrer、CSP、HTML 转义，无外部脚本、字体、QR 服务或请求日志中的 token。
  T6.6：采用跟随系统亮暗色的卡片布局，显示状态、换算后的流量及 UTC 到期日期/剩余天数、
  二维码、只读订阅地址/复制、sing-box/Clash 导入、共享客户端清单及其他格式折叠区。T8.5 多服务器时还显示分组节点列表。
  每次 HTML 响应（包括 HEAD）生成 crypto/rand 32 字节 base64url nonce，
  CSP 增加 `script-src 'nonce-…'`，与唯一内联复制脚本一致，其余指令不变。
  无 JS 时可手动选择地址。禁用/过期/超额仍保持空白 404，不因渲染器支持状态展示而放开。
- T6.5：成功 GET/HEAD 写响应后记录一条 `INFO subscription served`，业务字段仅
  `format`、`user_agent`、`user_id`。format 为 singbox/clash/uri（base64 记作 uri）；
  规格未列浏览器页面，补充为 html，避免误报客户端格式。格式取实际生成结果，显式参数仍优先。
  UA 删除控制/不可打印字符，按 Unicode 字符截断至 128；截断前屏蔽已知用户凭证、token、
  混淆密码、请求路径与来源 IP（含 URL 转义形式），避免 UA 反射泄漏。
  不记录 URL/query、客户端 IP、Cookie、认证头、响应体或原始错误；失败请求和写出失败不记成功事件。

自动化验证：T6.7 扩展为三种格式 × 三个预设 × 默认/完整选项/空节点/单节点/默认名及重名共 45 份 golden；
15 份 sing-box golden 逐一执行锁定内核 check。覆盖多节点三组、单节点无第二层、测速参数，
通过 Store.Load 校验友好默认名、名称去重与 JSON/YAML/URI/links 名称一致；命名约定见 CONFIG.md。
另覆盖 UA、URI 特殊字符/IPv6、规则顺序/源、有效期/流量边界、
限流并发/窗口/代理头、404 伪装、token 重置、认证/CSRF/HEAD、预览脱敏、settings Apply 回滚及真实本地监听切换。

## T7.5 备份与恢复

全部位于原 API 前缀下，要求登录和 no-store；POST 沿用 CSRF 检查。口令不出现在 URL、日志或响应 JSON 中。

| 方法 | 路径 | 请求与成功响应 |
|---|---|---|
| POST | `/backup` | JSON `{passphrase}`，至少 12 个 Unicode 字符、最多 1024 UTF-8 字节；200 加密二进制附件 `boxpanel-backup-<UTC时间>.tar.gz.enc` |
| GET | `/backup/status` | 200 `{last_attempt,last_success,result}`；时间为 Unix 秒，0 表示无记录，result 为空字符串 / success / failed；支持 HEAD |
| POST | `/restore` | multipart/form-data：`file` 加密备份、`passphrase`、`confirm="true"`；验证并成功排队后 202 `{accepted:true}` |

- 旧 GET `/backup` 返回 405。下载 Content-Type 为 application/octet-stream，带 attachment、no-store、nosniff；
  仅返回加密内容，不提供未加密证书/私钥下载。JSON 仍限 8 KiB，拒绝未知/重复/null 字段。
- 上传备份至多 64 MiB，multipart 总量额外允许 16 KiB，口令最多 1024 字节；只接受上述三个唯一字段。
  手动解析有界 multipart，不将口令或整个上传写入隐式临时文件。禁止网页恢复本地未加密自动备份。
- 解密、SHA-256、归档结构、schema、DB 完整性、迁移和证书引用先在 0700 的私有候选目录验证。
  任一步失败不停止服务、不替换现有 DB；错误口令、损坏/截断、危险条目、缺失固定证书统一
  `400 backup_invalid`；新 schema 为 `400 backup_schema_newer`，提示先升级。
- Linux 生产使用独立的 systemd-run 定时瞬态服务（3 秒后启动），复用 CLI 恢复事务；
  工作进程参数只有路径，口令不持久化。重复排队或系统操作失败返回 `503 backup_failed` 与下一步建议。
  202 只表示已排队；成功/失败详情可在服务器 `journalctl -u 'boxpanel-restore-*'` 查看。
  目标服务停完并关闭数据库连接后才替换目录；新面板通过 Startup 再次完整生效，并检查本机 HTTPS 就绪。
- 恢复会覆盖全部数据、停止代理连接、撤销全部备份会话，管理员使用备份中的凭证；
  浏览器提示约 30 秒后重新登录，慢启动可能更久，地址改变时使用备份中的 URL。
- 自定义外置 DB/证书/配置不在本版恢复布局内，返回 `409 backup_unavailable`；请先迁移到标准数据目录。
  dev 不运行每日自动备份、也不执行面板恢复；CLI restore 仅支持 Linux/systemd。

## T9.4 主控迁移

管理端使用 `/{web_path}/api/v1`，需要管理员会话及原有 CSRF 校验。

| 方法 | 路径 | 请求与响应 |
|---|---|---|
| GET | `/migration` | `{phase, target_url?, expires_at?, migrated, total, core_stopped, waiting?}`。`waiting` 为新主控上未切换的 `[{id,name}]` |
| POST | `/migration/token` | `{"confirm":true}`，201 返回 `{token, expires_at, source_url, master_pin, version}`。仅此次响应包含令牌全文 |
| POST | `/migration/cancel` | `{"confirm":true}`，204。中止尚未交接的迁移：issued 令牌立即失效，frozen 立即解冻并丢弃交换标识，在途新机确认就绪时得到 401 并自行回滚；已退役返回 409 `migration_committed`，无迁移时幂等返回 204。冻结期间仍可调用 |

`phase` 可为 `unavailable`、`idle`、`issued`、`frozen`、`incoming`、`active` 或 `retired`。时间使用 Unix 秒。
重签替换未认领令牌。冻结、待确认和退役期间，业务写接口返回 `409 migration_read_only`。会话登录、退出及加密备份下载仍可用。

主控间 HTTPS 接口挂在 `/{web_path}/api/migration/v1`，使用下表的 JSON 凭据校验。接口要求 TLS，遵循固定指纹或系统 CA 与主机名校验，客户端不跟随重定向。

| 方法 | 路径 | 请求与响应 |
|---|---|---|
| POST | `/claim` | `{token, secret, version, public_ip?}`。200 直接传输 `application/gzip` 的 §T7.5 归档，带 `Content-Length`、`X-Boxpanel-Migration-Target` 和 `X-Boxpanel-Migration-Expires` |
| POST | `/ready` | `{secret,target_url}`。目标必须等于认领时确定的 HTTPS 地址，并通过旧主控的远端检查。204 表示旧主控已经持久化退役，可安全重试同一请求 |
| POST | `/abort` | `{secret}`。204 表示对应认领已取消或失效。已提交退役返回 `409 migration_committed` |
| POST | `/status` | `{id}`。返回与该次迁移绑定的状态与匿名进度，用于验证新机就绪、查找退役决策和同步进度 |

`secret` 由新机使用 32 随机字节生成并按无填充 base64url 编码，`id` 为其字符串的 SHA-256 摘要，使用相同编码。
认领成功同时消费令牌并冻结旧主控。令牌过期、已用或认证失败返回 401。版本不符返回 426，无法确定新机公网地址返回 422。
就绪检查失败返回 422。冻结期间及退役后，旧主控的用户累计流量和网卡样本返回 `503 migration_read_only` 与 `Retry-After`，保留节点重试所需的确认水位。

心跳回复仍使用协议 2。本次增加以下可选字段。

- `read_only` 表示当前主控只接受心跳。节点可继续服务，并等待新地址或后续重试。
- `master_urls` 是优先级从高到低的 HTTPS 地址，`master_urls_key` 是固定回退公钥的 SPKI DER，`master_urls_signature` 是签名。
  公钥和签名均使用无填充 base64url。签名输入为 UTF-8 `boxpanel/master-urls/v1\n<本次请求 nonce>\n<Go JSON 编码的有序 URL 数组>`。
  ECDSA 使用 ASN.1 格式及 SHA-256，RSA 使用 PKCS#1 v1.5 及 SHA-256，Ed25519 直接签名消息。节点校验 SPKI 摘要等于已存 pin 并验签。
- 验证候选地址时，节点仍发送原格式的签名心跳，带可选 `X-Boxpanel-Master-Probe: 1`。探测读取已提交的期望版本，正常心跳才记录节点迁移进度。

退役主控对有效的机器订阅 GET/HEAD 返回 301，Location 保留原订阅路径及格式参数并加入 `bp_migrated=1`。
301 响应体和新主控对该标记的响应包含警告节点。新主控的常规订阅和旧节点的通信协议格式保持兼容。
浏览器分享页返回 200，显示新链接。无效令牌沿用 404。

## T6.3 面板证书

路径仍在 `/{web_path}/api/v1` 下（任务中的 `/api/cert` 为简称）。全部需认证、no-store，
写操作走现有 CSRF；GET 支持 HEAD，错误仍使用统一格式，不提供证书/私钥下载。

| 方法 | 路径 | 请求与成功响应 |
|---|---|---|
| GET | `/cert` | 下列公开状态，200 |
| POST | `/cert/issue` | 空 body；将期望策略设为 acme，提交异步签发，202 `{accepted:true}` |
| PUT | `/settings` | `{panel_tls_mode:"self_signed"}` 改回自签；`acme` 启动自动维护；原 settings 响应不变 |

```json
{
  "mode":"acme", "type":"self_signed", "host":"203.0.113.8",
  "issuer":"CN=boxpanel", "not_before":1790000000, "not_after":1821536000,
  "last_attempt":1790000300, "last_success":0,
  "last_error":"acme_port_unavailable", "hint":"TCP 80 被占用或无法绑定。请停止占用服务并检查权限，再重新申请。",
  "failures":1, "next_attempt":1790000600, "issuing":false, "warning":false
}
```

- mode 是期望策略，type 是当前实际证书类型：self_signed / acme，开发模式二者为 http。
  时间为 Unix 秒，0 表示尚无记录；last_error/hint 为空表示没有当前失败记录。
  issuing 表示排队或正在签发；warning 为连续至少两次失败且实际证书剩余 <24 小时。
  staging 仍是 acme 类型，面板明确提醒其不受公开信任，不保证任意自定义 CA 被客户端信任。
- issue 不接受任意域名、端口、目录或凭证参数；只使用已保存的 public_host 和启动时配置。
  申请进行中返回 `409 acme_busy`；开发模式或未装配服务返回 `409 acme_unavailable`。
  返回 202 仅表示任务已受理，前端轮询 cert 获取结果。签发失败不把已保存的设置回滚，
  不终止 HTTPS；保留仍有效的原证书，缺少有效可信证书时用自签备用证书。
- 错误码仅 `acme_port_unavailable`、`acme_invalid_host`、`acme_issue_failed`，并带可执行 hint；
  不透传 CA/网络/命令错误或挑战 token。手动申请可立即重试，后台失败按 SPEC.md §13 退避。
- public_host 或 panel_tls_mode 更新成功后异步通知 worker，不重启监听；restart_after 仍只表示
  原面板端口/路径切换。合并 block_bittorrent 的更新仍完整经过 Apply，失败不通知证书管理器。
  setup 的域名校验及普通事务规则保持，只有提交后才通知；旧订单不能覆盖新的主机或策略。


## T7.1 流量与月度重置

- POST/PUT 用户可选整数 `traffic_reset_day`，0（默认）关闭自动重置，1–28 表示 UTC 每月该日。
  PUT 省略保持；null、重复字段、非整数或超出范围为 `400 invalid_argument`，带修改后重试的提示。
  不接受客户端修改 `traffic_up/down`、`traffic_reset_at` 或内部 `traffic_epoch`。
- 用户列表、详情及创建/更新/重置响应包含 `traffic_reset_day` 和 `traffic_reset_at`（Unix 秒，0 表示尚未重置）。
  不返回内部 epoch，不改变普通 CRUD 的凭证脱敏规则。
- `POST /users/{id}/reset-traffic`：空请求体、需认证与 CSRF；非法 ID 为 400、不存在为 404、
  其他方法为 405。通过 Manager.Apply 同事务清零 up/down、记录 reset_at 并推进内部 epoch，
  配置改变时完成 check/重启/存活后返回 `200` 最新脱敏用户及原 Apply 标记；配置与防火墙计划不变
  且内核运行时只提交 DB，不 check/重启/同步防火墙，同样返回 `200` 与 `applied` 标记；失败沿用原回滚语义。
  清零可恢复仅因超额不可用的用户，不自动改变 enabled 或 expire_at；订阅 token/UUID/密码保持。
- UTC 月度边界及手动清零抑制重复、后台停用/恢复规则见 SPEC §14。后台不增加 API 或其他生效入口。
- T7.1c：用量为已落库的 nftables 内核计数（每 10 秒采集，通常每 60 秒保存）；列表/详情/公开分享页与
  `Subscription-Userinfo` 使用相同计数。上传=服务器发往目标站点、下载=服务器收到的 IP 层字节，
  包含协议头/重传，不含客户端加密开销或服务器自身 DNS；与客户端显示可能有差异。
  不再轮询 Clash 存活连接；重启首次只建基线、计数器删除/系统重建及崩溃边界见 SPEC §14，不用于精确计费。
  超额调度会额外考虑内存待写用量。公开订阅对禁用/过期/超额继续保持相同空白 404。
- T7.1b：所有资源写入仍进入 Manager.Apply；配置逐字节相同、防火墙计划相同且内核运行时只提交 DB
  与生效记账。修改仍有效用户的限额/到期/备注/月度重置日、重置 token、不改变有效用户集合的清零
  均不重启；修改姓名/连接凭证、增删启停有效用户，或因限额/到期/清零改变有效用户集合仍可能重启。
  显式 restart 永远走完整流程；成功标记不区分两条路径。面板仅对预计改变配置的操作提醒全连接中断。
  约 1 秒为正常体验提示，慢生效/恢复的有界预算保持 T6.10 契约；完整边界见 SPEC §7。

## T7.9 通知与告警

所有路由要求登录。写操作沿用 CSRF、严格 JSON 和 `Cache-Control: no-store` 规则。GET 支持 HEAD。
以下额度设置用十进制 GB，查询中的流量单位为字节，时间均为 UTC Unix 秒。

| 方法 | 路径 | 请求与成功响应 |
|---|---|---|
| GET | `/alerts` | 200 `{billing,unread,unread_count,recent}` |
| GET | `/alerts/settings` | 可选查询参数 `server_id`，200 `{config,channels,interfaces,billing}`，`interfaces` 是该服务器的可选网卡数组，`billing` 是当前账期用量 |
| PUT | `/alerts/settings` | 接受 config 的部分字段及可选 `server_id`。也可在查询中指定 ID，同时提交时两处必须相同。200 返回更新后的 config |
| PUT | `/alerts/channels/{channel}` | 接受通道部分字段，200 返回该通道的脱敏配置 |
| POST | `/alerts/channels/{channel}/test` | 空请求体，202 `{id}` 表示已把测试通知加入持久化队列 |
| POST | `/alerts/events/{key}/read` | 空请求体，204 标记已读，可重复请求。key 取自 unread，未知 key 返回 404 |

- 省略 `server_id` 选中本机。显式 ID 必须为正整数，服务器已移除或不存在时返回 404。远端设置不改变节点期望版本。
  T9.5 起，远端可修改 `server_quota_80`、`server_quota_90`、`server_quota_100`。远端 90% 默认关闭。通知通道为主控共享配置。
- `config` 包含 `interface`（空表示自动检测）、`quota_gb`（整数 0–9223372036）、`billing_day`（整数 1–28）、
  `billing_mode`（`out` / `both` / `max` / `in`）及 `events`（事件类型到布尔值的映射）。
  PUT 中省略的字段和事件保持原值。事件类型有 `user_quota_80`、`user_quota_100`、`user_expiring`、
  `user_expired`、`user_stopped`、`server_quota_80`、`server_quota_90`、`server_quota_100`、
  `certificate_warning`、`core_down`、`core_recovered`、`server_offline`、`server_recovered`。本机设置中的
  `server_offline` 默认开启，关闭时同时停用离线和恢复事件。恢复事件要求该次离线已有记录。
- `channels` 按 `bark`、`server_chan`、`telegram` 为键。写入字段为 `enabled`、`credential`，Bark 可传
  `url`（空用默认 `https://api.day.app`），Telegram 可传 `chat_id`（数字 ID 字符串或 `@频道名`）。
  每次省略凭证字段保留原值，显式空字符串清除凭证。启用和测试需已有完整凭证。通道读取返回
  `enabled`、`credential_mask`，适用时返回 `url` 与 `chat_id_mask`。1–4 位的凭证仅返回 `****`，更长的附尾四位。
  测试使用已保存的配置，可在已保存凭证的停用通道执行。通过 `/alerts` 的 recent 查看同一 id 的投递结果。
- `billing` 包含 `supported`、`interface`（最近采样的网卡）、`sampled_at`（0 为尚无样本）、`stale`、
  `starts_at`、`ends_at`、`billing_mode`、`rx`、`tx`、`used`、`quota`、`percent`。未设额度时最后两项为 0。
  当最近样本超过三分钟或与手选网卡不一致时返回 `stale: true`。
- `/alerts` 的 `billing` 沿用本机口径。`unread_count` 是所有服务器的未读总数，`unread` 按最新优先最多 30 条，
  字段为 `key`、`server_id`、`kind`、`address`、`used`、`limit`、`created_at`，适用时包含 `username`、`threshold`、
  `deadline`。`server_recovered` 附 `offline_seconds`，为上次心跳至本次成功联系的秒数。`key` 是不透明的去重标识。
- `recent` 取最近更新的至多 30 条投递。每条包含 `id`、`channel`、`kind`（测试为 `test`）、`status`（`pending`、
  `sent`、`failed`）、`attempts`、`updated_at`、`grouped`，适用时包含 `username` 和固定的 `error` 代码。
  失败代码包括 `unsafe_destination`、`timeout`、`network_error`、`http_error`、`invalid_response`、
  `provider_rejected`、`retry_expired` 或 `channel_disabled`。响应省略服务商正文和请求 URL。
