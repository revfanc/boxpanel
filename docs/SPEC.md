# boxpanel 产品与架构规格

## 1. 目标

让没有运维经验的用户，在一台全新的 Linux VPS 上：

1. 执行**一条命令**完成安装；
2. 打开面板，跟着**引导流程**点几下就生成可用节点；
3. 拿到**一个订阅链接/二维码**，导入任意主流客户端即可使用，分流规则自动生效。

非目标（MVP 不做）：多服务器集群、计费/售卖、Windows 服务端。

## 2. 关键决策

| 决策 | 选择 | 理由 |
|---|---|---|
| 内核 | sing-box（版本锁定，见 `internal/singbox/version.go`） | 协议全、性能好、客户端生态完整 |
| 后端 | Go 单二进制，内嵌前端 | 安装只需下载一个文件，无运行时依赖 |
| 存储 | SQLite（`modernc.org/sqlite`） | 零依赖、易备份 |
| 前端路由 | React Router **history 模式** + 运行时 basename | 面板挂在随机路径下，刷新深链接可用 |
| 默认协议 | 无域名：VLESS+Reality；可选 Hysteria2 | 无需证书、抗探测 |
| 规则 | 远程 `.srs` / `.mrs` rule-set，客户端自动更新 | 规则保持活跃，不依赖已废弃的 geoip/geosite.db |
| 进程管理 | systemd 两个服务：`boxpanel`、`boxpanel-singbox` | 互相独立，面板挂了不影响代理 |

## 3. 运行时布局

```
/usr/local/bin/boxpanel                # 面板二进制（也是 CLI）
/usr/local/share/boxpanel/sing-box     # 受管的 sing-box 二进制（版本由面板管理）
/etc/boxpanel/
  boxpanel.db                          # SQLite
  sing-box.json                        # 当前生效的服务端配置（面板生成，用户勿手改）
  sing-box.json.bak                    # 上一份可用配置，用于回滚
  certs/panel.{crt,key}                # 面板 HTTPS 证书（默认自签）
  certs/hy2.{crt,key}                  # Hysteria2 等需要 TLS 的入站证书
/etc/systemd/system/boxpanel.service
/etc/systemd/system/boxpanel-singbox.service
```

以上为生产默认路径；T1.2 的数据、数据库和证书路径均可用参数/环境变量覆盖，
本机开发及测试无需 root，详见 INSTALL.md §3。systemd 与 sing-box 的路径覆盖见 INSTALL.md §4。

## 4. 网络入口

面板二进制监听一个端口（默认随机 10000–60000，HTTPS），按路径分流：

| 路径 | 作用 |
|---|---|
| `/{web_path}/` | 面板 SPA（history 模式，深链接回退到 index.html） |
| `/{web_path}/api/v1/...` | 管理 API（需登录） |
| `/{sub_path}/{token}` | 订阅（无需登录，凭 token） |
| 其他任何路径 | 返回空白 404，不暴露面板特征 |

`web_path`、`sub_path` 均为安装时随机生成的 12 位 `[a-z0-9]`，可在设置中修改。

## 5. 目录结构

```
cmd/boxpanel/main.go          # CLI 入口：serve / info / reset-admin / set ... / version
internal/
  app/                        # 依赖装配、启动、优雅退出
  db/                         # 连接、迁移（embed SQL 文件）、查询
  auth/                       # 密码哈希(argon2id)、会话、登录限流、TOTP(后期)
  httpapi/                    # handler、中间件、错误格式
  spa/                        # 静态资源 + history 回退 + index.html 注入
  singbox/                    # 二进制安装/升级、systemd 控制、check、apply+回滚、日志
  confgen/                    # 服务端 sing-box 配置生成
  subscription/               # UA 识别 + sing-box JSON / mihomo YAML / base64 渲染
  share/                      # 单节点分享 URI（vless://、hysteria2:// ...）
  rules/                      # 规则预设、rule-set 源
  reality/                    # x25519 密钥、short_id、伪装目标检测
  certs/                      # 自签证书；后期 ACME
  sysinfo/                    # 环境自检、BBR、公网 IP 探测、端口占用
  firewall/                   # ufw / firewalld / nftables / iptables 适配
web/                          # Vite + React 前端；web/embed.go 内嵌 dist
scripts/install.sh            # 一键安装/升级/卸载
.github/workflows/            # ci.yml、release.yml
docs/
```

## 6. 数据模型（SQLite）

```sql
CREATE TABLE admins (
  id INTEGER PRIMARY KEY,
  username TEXT NOT NULL UNIQUE,
  password_hash TEXT NOT NULL,         -- argon2id
  totp_secret TEXT,                    -- 后期
  created_at INTEGER NOT NULL
);

CREATE TABLE sessions (
  id TEXT PRIMARY KEY,                 -- 32 字节随机，base64url
  admin_id INTEGER NOT NULL REFERENCES admins(id) ON DELETE CASCADE,
  expires_at INTEGER NOT NULL,
  created_at INTEGER NOT NULL
);

CREATE TABLE settings (
  key TEXT PRIMARY KEY,
  value TEXT NOT NULL                  -- JSON
);
-- 关键 key：web_port, web_path, sub_path, public_host, panel_tls_mode,
--          setup_completed, setup_step, rules(见 CONFIG.md), singbox_version

CREATE TABLE inbounds (
  id INTEGER PRIMARY KEY,
  tag TEXT NOT NULL UNIQUE,            -- 例：vless-reality-443
  protocol TEXT NOT NULL,              -- vless_reality | hysteria2 | (后期) tuic | anytls | trojan_ws
  port INTEGER NOT NULL UNIQUE,
  enabled INTEGER NOT NULL DEFAULT 1,
  remark TEXT NOT NULL DEFAULT '',     -- 显示名，订阅里节点名用它
  options TEXT NOT NULL,               -- JSON，协议特定参数（含私钥），见 CONFIG.md
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);

CREATE TABLE users (
  id INTEGER PRIMARY KEY,
  name TEXT NOT NULL UNIQUE,           -- [a-zA-Z0-9_-]{1,32}
  uuid TEXT NOT NULL UNIQUE,           -- vless / tuic 用
  password TEXT NOT NULL,              -- hysteria2 / tuic / anytls / trojan 用，24 字节随机 base64url
  sub_token TEXT NOT NULL UNIQUE,      -- 24 字节随机 base64url
  enabled INTEGER NOT NULL DEFAULT 1,
  traffic_limit INTEGER NOT NULL DEFAULT 0,   -- 字节，0=不限
  traffic_up INTEGER NOT NULL DEFAULT 0,
  traffic_down INTEGER NOT NULL DEFAULT 0,
  expire_at INTEGER NOT NULL DEFAULT 0,       -- unix 秒，0=永不
  remark TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL
);
```

MVP 规则：**所有启用的用户加入所有启用的入站**。超额/过期用户视为禁用，从生成的配置中剔除。

### T1.1 存储约定

- `internal/db.Open(ctx, path)` 接收文件路径（不接收 DSN 或内存库），父目录须已存在；
  自动连接并迁移，失败关闭连接，成功由调用者 `Close()`。本任务不接入 CLI/HTTP 启动流程。
- 使用纯 Go 驱动 `modernc.org/sqlite v1.38.2`，保持 `go.mod` 的 Go 1.23 最低版本；
  这是本任务唯一直接新增依赖，其余为驱动的间接依赖，无需 CGO。
- 打开时启用持久化的 `journal_mode=WAL` 并检查返回值；新库并发切换 WAL 时可能立即遇到
  `SQLITE_BUSY`，仅对此错误重试，等待窗口为 5 秒且支持 context 取消。
  每个连接通过驱动的 DSN 参数设置 `foreign_keys=on`、`busy_timeout=5000`（毫秒），
  连接池最多保留/打开一个连接。事务使用 `BEGIN IMMEDIATE`，
  在读取后续写操作依赖的数据前取得写锁；跨连接竞争等待至多 5 秒。
  参数语义见 [驱动文档](https://pkg.go.dev/modernc.org/sqlite@v1.38.2#Driver.Open) 和
  [SQLite PRAGMA 文档](https://www.sqlite.org/pragma.html)。
- SQL 嵌入 `internal/db/migrations/`，文件名为 `0001_name.sql`，版本从 1 连续递增。
  `schema_migrations(version INTEGER PRIMARY KEY, applied_at INTEGER NOT NULL)` 记录已应用版本
  与 Unix 秒时间戳。一次迁移中所有待执行 SQL 及版本记录在同一事务内顺序执行；失败整批回滚，
  保留迁移前的数据与版本。重复迁移/重新打开不重放已应用版本，不接受有缺口或超出当前二进制的版本历史。
  已发布的迁移不修改，变更以新版本 SQL 追加。
- `GetSetting(ctx, dbOrTx, key)` / `SetSetting(ctx, dbOrTx, key, value)` 用 `SettingKey[T]`
  将键绑定到 Go 类型；支持 `*sql.DB` 与 `*sql.Tx`，写入为 JSON upsert。事务内必须传 `*sql.Tx`，
  不得再次使用单连接池查询。缺失键返回可用 `errors.Is(err, sql.ErrNoRows)` 判定的错误；
  非法 JSON、类型不匹配或顶层 `null` 返回错误及零值，不隐式补默认值。业务取值校验由后续调用方负责。
- 类型映射：`web_port` 为 `int`；`setup_completed`、`block_bittorrent` 为 `bool`；
  `rules` 为 `RulesSettings`（含 `DNSSettings`，字段对应 CONFIG.md §3）；
  `web_path`、`sub_path`、`public_host`、`panel_tls_mode`、`setup_step`、`singbox_version`、
  `panel_name` 为 `string`。迁移只创建表，不写入设置、账号或其他业务初始数据；初始化属于 T1.2。
- 单测全部使用 `t.TempDir()` 下的文件数据库，覆盖迁移幂等/顺序/回滚/并发、连接参数、
  五张业务表的字段/约束/默认值，以及 settings 的持久化、类型检查、事务提交/回滚。

### T1.2 初始化默认值

- 初始化拒绝含有任何业务数据的 DB（允许仅迁移过的空库）；事务的写锁保证并发初始化只有一次成功。
- `setup_completed=false`、`setup_step="welcome"`、`panel_name="boxpanel"`、`block_bittorrent=true`。
- `rules` 使用 CONFIG.md §3 的 `bypass_cn` / `metacubex` / `1d` 和 DNS 默认值，
  自定义域名列表为空（文档中的 example 域名仅是示例）；广告过滤、fakeip、IPv6 默认关闭。
- `singbox_version=""` 表示尚未安装，不在 T1.2 硬编码或下载后续任务的内核版本。
- 不创建代理用户、入站或 sing-box 配置；仅初始化管理员、设置和面板证书。

## 7. 配置生效流程（核心，务必严格实现）

```text
所有影响服务端配置的写操作（入站/用户/服务端设置），以及启动、CLI apply、API restart
  → 进程内互斥锁 + sing-box.json.lock 的 flock（跨 serve/CLI 进程）
  → BEGIN IMMEDIATE（保持写锁直到恢复完成；调用方只能使用传入的 *sql.Tx）
  → 处理上次中断的 .pending 恢复记录
  → 在事务内写 DB，读取一致快照，confgen.Build
  → 写 sing-box.json.tmp（0600，同目录）
  → sing-box check -c tmp             失败：删除 tmp、ROLLBACK，不改变当前文件/服务
  → 当前 sing-box.json 原子复制为 .bak（0600）
  → 写 .pending 恢复记录（0600，无凭证）
  → rename(tmp, sing-box.json)        原子替换，文件与目录均同步落盘
  → systemctl restart boxpanel-singbox.service
  → 轮询 is-active，最多 5 秒
  → 同步防火墙服务端口及 Hysteria2 端口跳跃
  → 更新 applied_firewall / apply_generation，COMMIT
  → 删除 .pending，释放锁
```

### T2.3 事务与恢复约定

原流程图的「先提交 DB」与校验失败回滚要求冲突，现明确采用**全程持有同一数据库事务**，
直到文件、服务、防火墙成功才提交；不采用事后补偿 DB 的方案。不增加表或依赖。
`singbox.Manager.Apply(ctx, mutation)` 是后续 T3 写操作的唯一入口，mutation 只能在传入事务中
读写，不可自行提交/回滚或执行外部副作用。`Restart` / CLI `apply` / `Startup` 复用此入口。

- 生成/check 失败返回 422；DB 所有写入（包括运行参数）均不提交。check 输出先脱敏，
  不把任意 SQL、命令或凭证原文写入日志。临时文件在所有正常退出路径清理。
- 替换后任一步失败（重启、5 秒内未存活、防火墙、DB 写入/提交）均恢复 `.bak` 并再次重启、
  再次检查存活；同时恢复旧防火墙计划，最后回滚 DB。首次生效没有旧配置时删除新文件并停止服务。
  不从不存在或上一轮遗留的 `.bak` 恢复首次配置。
- 失败响应为 500，附失败时 journal 最近 30 行的脱敏诊断。恢复失败使用
  `singbox_rollback_failed`，明确提示服务/防火墙可能尚未恢复；保留 `.bak` / `.pending`，
  下次生效先重试恢复，恢复仍失败则拒绝继续修改。恢复文件失败时尽力停止新服务。
- 请求取消不会提前释放 DB 写锁：事务生命周期独立于请求，正常工作仍检查请求 context；
  恢复使用独立最多 30 秒的 context。外部命令沿用每条最多 5 秒、输出最多 1 MiB 的 runner。
- `.lock` 是持久的锁文件，不删除；进程退出由内核释放 flock，不需人工清理陈旧锁。
  同一受管配置必须对应同一 DB。持有事务期间其他 DB 操作可能等待；跨连接写竞争仍受
  SQLite 的 5 秒 busy timeout 限制，超时需重试。
- 事务独占一个 `sql.Conn`。COMMIT 出错（例如延迟外键约束）后，丢弃并关闭底层连接，
  强制清理 SQLite 仍可能打开的事务；再用新连接核对 generation。确认未提交则恢复旧状态；
  已提交则保留匹配的新状态。若 DB 不可读、结果无法确认，保留恢复记录、返回 500 并明确提示
  「数据库状态无法确认」，修复 DB 后再自动核对，不声称已经回滚。
- `.pending` 保存本次随机 generation、原配置是否存在、前后防火墙计划。若进程在提交前中断，
  SQLite 回滚未提交事务；下次启动/生效先恢复 `.bak` 与旧规则，再从 DB 生成。
  若 DB 的 `apply_generation` 已匹配 `.pending`，说明已提交，仅清理记录，不错误恢复旧配置。
  这是崩溃后的恢复机制；SQLite、文件和系统服务之间不是硬件级分布式原子提交。
- 面板 Linux 生产启动时执行重新生成/check/生效并重放端口跳跃。失败拒绝启动并给修复建议。
  全新实例尚未安装二进制、无当前配置/恢复记录且无启用入站时，允许启动面板进入后续安装引导。
  非 Linux / `--dev` 启动跳过生效；管理 API 返回 409，CLI apply 失败并提示 Linux/systemd，
  不伪装为已成功生效、不写配置或执行系统命令。T3.1 为 `serve --dev` 资源编辑增加显式
  `DevDBOnly` 开发替身：仅非 nil mutation 可通过同一 Apply 入口提交 DB，始终不生效，
  响应以 `X-Boxpanel-Apply: dev-db-only` 标明；restart/CLI 不使用此替身，详见 API.md。
- `applied_firewall` 存储最近成功的 `{ports:[{port,proto}],hops:[{start,end,port}]}` JSON；
  `apply_generation` 是 32 字节 crypto/rand 的 base64url 值，仅用于中断恢复。
  Clash API 运行参数和 Hysteria2 证书约定见 CONFIG.md §2 的 T2.3 补充。
- 单测以临时 DB/文件、fake runner/systemctl/firewall 覆盖成功、生成/check 失败、重启/存活失败、
  文件失败、防火墙失败、恢复也失败、提交失败、请求取消、并发、启动及恢复记录；真实集成测试只调用
  `.tools/bin/sing-box check` 校验双入站，服务控制/防火墙仍为 fake，无工具时 Skip。

## 8. 安全基线

- 面板默认 HTTPS（安装时生成覆盖公网 IP 的自签证书），引导页解释浏览器警告。
- 会话 cookie：`HttpOnly; Secure; SameSite=Strict; Path=/{web_path}/`，有效期 7 天。
- 登录限流：同 IP 5 次失败锁 15 分钟。
- 管理 API 对写操作校验 `Origin`/`Sec-Fetch-Site`，防 CSRF。
- 订阅接口：token 常量时间比较；同 IP 每分钟 30 次限速；token 错误返回与「路径不存在」一致的 404。
- T3.2 的管理员 `/users/{id}/links` 和凭 token 的订阅是可导入凭证的专用出口，必须包含
  用户凭证/订阅 URL；普通 CRUD、预览和日志仍脱敏，服务端私钥永不导出。具体限流与接口边界见 API.md。
- 服务端出站拒绝私有 IP（防止被当跳板访问内网）。
- 面板进程以 root 运行（需要 systemctl、防火墙权限）；不执行任何来自请求参数拼接的 shell 命令，外部命令一律 `exec.Command` 参数数组。

### T1.2 认证实现约定

- 使用 `golang.org/x/crypto/argon2.IDKey`，PHC 格式保存 Argon2id v19：64 MiB 内存、3 次迭代、
  4 lanes、16 字节随机盐、32 字节输出；比较使用常量时间函数。参数见
  [Go Argon2 官方文档](https://pkg.go.dev/golang.org/x/crypto/argon2#IDKey)。仅接受本版本写出的参数，
  拒绝损坏哈希里的任意资源参数。新增直接依赖 `golang.org/x/crypto v0.41.0`，
  用于用户指定的 Argon2id 实现并保留 Go 1.23 最低版本；其间接依赖 `x/sys` 升至 v0.35.0。
- cookie 名称 `boxpanel_session`，32 字节 `crypto/rand` 随机 ID（base64url），数据库持久化，
  固定 7 天不过期续签；到期时刻即无效。登录替换当前 cookie 对应的旧会话并清理到期会话，
  登出删除当前会话；密码/用户名变更及 CLI 重置撤销该管理员全部会话，必须重新登录。
- 同一连接来源 IP 连续失败 5 次，**第 5 次即返回 429** 并锁定 15 分钟；
  正确密码也不能绕过锁定。成功登录清零；未锁定的失败记录在最后失败 15 分钟后清除。
  不存在的用户名也运行同参数的虚拟哈希。限流保存在单进程内存，重启清空；不信任转发 IP 头。
  哈希计算最多一个并发任务，忙时返回 `429 auth_busy` / `Retry-After: 1`；
  限流表最多 10000 个来源，满时拒绝新来源，不驱逐仍有效的锁定。
- 所有非 GET/HEAD/OPTIONS 管理请求（含登录）校验 CSRF。存在 Origin 时必须精确匹配
  实际请求的协议、主机、端口，拒绝 null、无效 Origin；Sec-Fetch-Site 若存在仅接受
  `same-origin` / `none`，同站的其他来源也拒绝。两者都缺失时允许 CLI 请求，
  含 JSON body 的认证操作要求 `application/json`。不添加通配 CORS 或信任代理头。
- 开发模式的 Vite 代理保留原 Host，浏览器仍走同源 `/api`；不放宽来源检查。
  `--dev` 是唯一 HTTP 模式，cookie 的 Path 为 `/`，仅 Secure 为 false。
- API 错误包含中文说明和下一步建议。请求日志仅记录已知 API 路由类别、状态、耗时；
  不记录原始 URL、query、body、Cookie、密码、哈希或 panic 值。panic 丢弃未提交的响应及 cookie，
  返回统一 JSON 500。当前仅缓冲小型认证 JSON 响应，不提供流式 API。
- T1.2 在可写路径、非特权端口运行无需 root；生产 root 要求针对后续 systemd/防火墙操作。

## 9. T1.3 系统自检与防火墙边界

本节补齐原规格未定义的采样、平台与防火墙行为；不实现 T2 的 sing-box 安装、systemd
服务管理、启动时规则同步或端口跳跃，也不增加 setup API、引导 UI 或防火墙 HTTP 写接口。
不新增依赖。

- `internal/host.Files` 封装文件读取、原子写入和删除，`host.Runner` 封装命令查找与执行。
  `/proc`、`/sys`、`/etc/os-release`、BBR 文件和全部系统命令均通过这些接口；磁盘统计、
  时钟、HTTP IP 探测及防火墙状态也可注入。原生命令用参数数组，无 shell，固定 `LC_ALL=C`，
  单条命令最多 5 秒，输出最多 1 MiB。单测只用假命令/假系统数据与临时普通文件。
- Linux 生产模式读取发行版 ID/版本、Go 架构名；虚拟化优先读取 `systemd-detect-virt`，
  缺失时从 cgroup / DMI 中识别常见类型，无法确定返回 `unknown`，不猜测为物理机。
  `ss -H -lntu` 检查推荐的 `443/tcp`、`8443/udp`；任一地址上的占用均视为该端口被占用，
  IPv4/IPv6 一并解析。此结果只是快照，不预留端口，也不代表云安全组已放行。
- 非 Linux 或 `--dev` 下自检只返回平台提示，状态指标为占位值；不读 Linux 系统文件、
  不探测外网、不执行系统命令。BBR 开启与重新探测 IP 返回不支持（API.md）；
  firewall `Status/Open/Close` 为 no-op（参数校验仍保留）。
- BBR `enabled` 表示默认 TCP 拥塞控制为 `bbr`；`available` 表示已注册 BBR 或
  `modinfo tcp_bbr` 确认模块可用。开启时按需 `modprobe tcp_bbr`，原子写入
  `/etc/sysctl.d/99-boxpanel-bbr.conf`（0644），仅包含 `net.core.default_qdisc=fq` 和
  `net.ipv4.tcp_congestion_control=bbr`，再用 `sysctl -p` 加载该文件并重读校验。
  不运行 `sysctl --system`。失败尽力恢复两个运行时参数及原文件（原来不存在则删除），
  即使请求取消也尝试恢复；恢复失败仍返回错误，不声称成功。已加载的模块不卸载。
  其作用是默认队列和新 TCP 连接，不替换已有连接或网卡现存队列。
  语义依据 [内核 TCP 参数](https://www.kernel.org/doc/html/latest/networking/ip-sysctl.html)
  与 [默认 qdisc 文档](https://www.kernel.org/doc/html/latest/admin-guide/sysctl/net.html)。
- firewall 暴露 `Status(ctx)`、`Open(ctx, port, proto)` / `Close(ctx, port, proto)`；
  `port` 为 1–65535，`proto` 仅接受小写 `tcp` / `udp`。操作串行化，每次重新检测，
  优先选择活动的 ufw → firewalld → nftables → iptables；已安装但未启用的工具不遮蔽
  后续活动后端。检测失败（含权限不足/异常输出）直接报错，不当成「没有防火墙」。
  没有活动后端则不写规则，也不自动启用防火墙、修改默认策略或清空规则集。
- ufw 使用带 `boxpanel:<proto>:<port>` 注释的 allow/delete；firewalld 使用默认 zone，
  分别查询/更新 runtime 和 permanent 的端口项，不 reload，避免丢弃其他运行时规则。
  这两个高层管理器按端口管理，调用方须保证端口专用于面板管理的服务；不以 Close
  强制 deny，也不删除其他形式的放行规则。自定义 firewalld zone 需管理员另行放行。
- nftables 解析 JSON 规则集，在每条 `inet/ip/ip6` 的 input filter 基链开头插入带标记的
  accept；重复 Open 不重复添加，Close 只按 handle 删除相同标记的规则，不建一个无法
  覆盖后续 drop 的孤立基链。iptables/ip6tables 用带相同标记的 INPUT 规则，先 `-C`
  检查，再 `-I/-D`，IPv4/IPv6 都处理，工具/权限不完整时报错。
  这两种后端只改运行时规则，持久重放留在 T2.3；不覆盖系统已有持久化配置。
  多条更新中途失败返回错误，可重试完成剩余更新，不做全局防火墙回滚。
  多套防火墙同时活动或 ingress/forward/云安全组规则不在单一后端适配范围。
  命令依据 [ufw 手册](https://manpages.ubuntu.com/manpages/jammy/man8/ufw.8.html)、
  [firewalld 手册](https://firewalld.org/documentation/man-pages/firewall-cmd) 与
  [nftables 手册](https://www.netfilter.org/projects/nftables/manpage.html)。

## 10. T2.1 二进制与 systemd 边界

- 锁定 sing-box 1.14.4（2026-10-24T01:04:25Z），版本与发布时间常量集中在
  `internal/singbox/version.go`；下载、校验、开发工具与路径覆盖约定见 INSTALL.md §4。
- 新增 CLI `singbox install|status`、两个 systemd 单元生成函数与可注入的控制接口。
  服务状态接入 T1.3 的系统自检与状态接口；状态不以 DB 版本记录代替实际二进制检查。
- 安装只原子替换验证后的二进制、写入单元并 daemon-reload；不启用/启动/重启服务。
  同版本可重试补齐单元及 DB 记录，已有配置时的版本升级需要 T7.6 的 check/回滚流程。
- 非 Linux/开发模式保持无系统副作用。下载和命令接口可注入，测试不依赖网络或 systemd。
  不新增 Go/前端依赖；配置生成、配置写入、check/apply、端口跳跃与防火墙同步留在 T2.2/T2.3。
