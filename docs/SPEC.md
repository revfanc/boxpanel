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
| 规则 | 远程 `.srs` / `.yaml` rule-set，客户端自动更新 | 规则保持活跃，不依赖已废弃的 geoip/geosite.db |
| 进程管理 | systemd 两个服务：`boxpanel`、`boxpanel-singbox` | 互相独立，面板挂了不影响代理 |

## 3. 运行时布局

```
/usr/local/bin/boxpanel                # 面板二进制（也是 CLI）
/usr/local/share/boxpanel/sing-box     # 受管的 sing-box 二进制（版本由面板管理）
/etc/boxpanel/
  boxpanel.db                          # SQLite
  agent.key                            # 节点专用 Ed25519 种子（0600）
  sing-box.json                        # 当前生效的服务端配置（面板生成，用户勿手改）
  sing-box.json.bak                    # 上一份可用配置，用于回滚
  certs/panel.{crt,key}                # 初始化的自签备用证书（保留）
  certs/panel-<sha256>.pem              # 当前/历史面板证书链与私钥，原子保存
  certs/account-<directory-sha256>.key  # 按 CA directory 隔离的 ACME 账号
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
| `/{web_path}/api/agent/v1/...` | 节点注册、心跳、同步及流量上报（HTTPS，见 §17–19） |
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
  agent/                      # 节点注册、签名、TLS、心跳状态
  httpapi/                    # handler、中间件、错误格式
  spa/                        # 静态资源 + history 回退 + index.html 注入
  singbox/                    # 二进制安装/升级、systemd 控制、check、apply+回滚、日志
  confgen/                    # 服务端 sing-box 配置生成
  subscription/               # UA 识别 + sing-box JSON / mihomo YAML / base64 渲染
  share/                      # 单节点分享 URI（vless://、hysteria2:// ...）
  rules/                      # 规则预设、rule-set 源
  reality/                    # x25519 密钥、short_id、伪装目标检测
  certs/                      # 自签面板/Hysteria2 证书
  acme/                       # 面板 ACME 签发、续期与 TLS 热替换
  alerts/                     # 网卡计费、告警事件与通知投递
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
--          setup_completed, setup_step, setup_preferences(T5.1), rules(见 CONFIG.md), singbox_version, node_role

CREATE TABLE servers (
  id INTEGER PRIMARY KEY,
  name TEXT NOT NULL,
  public_host TEXT NOT NULL,
  is_local INTEGER NOT NULL DEFAULT 0 CHECK (is_local IN (0, 1)),
  status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('local', 'online', 'offline', 'pending')),
  last_seen_at INTEGER,
  version TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL DEFAULT (unixepoch()),
  updated_at INTEGER NOT NULL DEFAULT (unixepoch()),
  agent_public_key TEXT,
  alert_preferences TEXT NOT NULL DEFAULT '{"interface":"","quota_gb":0,"billing_day":1,"billing_mode":"out"}'
);
CREATE UNIQUE INDEX servers_one_local ON servers (is_local) WHERE is_local = 1;

CREATE TABLE inbounds (
  id INTEGER PRIMARY KEY,
  tag TEXT NOT NULL,            -- 例：vless-reality-443
  protocol TEXT NOT NULL,              -- vless_reality | hysteria2 | (后期) tuic | anytls | trojan_ws
  port INTEGER NOT NULL,
  enabled INTEGER NOT NULL DEFAULT 1,
  remark TEXT NOT NULL DEFAULT '',     -- 显示名，订阅里节点名用它
  options TEXT NOT NULL,               -- JSON，协议特定参数（含私钥），见 CONFIG.md
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  server_id INTEGER NOT NULL DEFAULT 1 REFERENCES servers(id),
  UNIQUE (server_id, port),
  UNIQUE (server_id, tag)
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
  traffic_reset_day INTEGER NOT NULL DEFAULT 0, -- 0=不自动重置；1–28 为 UTC 每月重置日
  traffic_reset_at INTEGER NOT NULL DEFAULT 0,  -- 最近成功重置的 Unix 秒
  traffic_epoch INTEGER NOT NULL DEFAULT 0,     -- 内部采集代号，不出 API
  expire_at INTEGER NOT NULL DEFAULT 0,       -- unix 秒，0=永不
  remark TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL
);
```

启用的用户在所有服务器共享。每台服务器仅生成属于自己的启用入站。超额或过期的用户从配置中剔除。

### T1.1 存储约定

- `internal/db.Open(ctx, path)` 接收文件路径（不接收 DSN 或内存库），父目录须已存在；
  自动连接并迁移，失败关闭连接，成功由调用者 `Close()`。本任务不接入 CLI/HTTP 启动流程。
- 使用纯 Go 驱动 `modernc.org/sqlite v1.38.2`；最低 Go 版本见 §13（T6.3 升至 1.26）；
  这是本任务唯一直接新增依赖，其余为驱动的间接依赖，无需 CGO。
- 打开时启用持久化的 `journal_mode=WAL` 并检查返回值；新库并发切换 WAL 时可能立即遇到
  `SQLITE_BUSY`，仅对此错误重试，等待窗口为 5 秒且支持 context 取消。
  每个连接通过驱动的 DSN 参数设置 `foreign_keys=on`、`busy_timeout=5000`（毫秒），
  写连接池最多保留/打开一个连接；T6.10 的独立只读池见 §7 补充。写事务使用 `BEGIN IMMEDIATE`，
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
  `panel_name` 为 `string`。T1.1 的初始迁移仅建表。T8.1 新增的本机元数据在 §6 的服务器小节定义。
- 单测全部使用 `t.TempDir()` 下的文件数据库，覆盖迁移幂等/顺序/回滚/并发、连接参数、
  五张业务表的字段/约束/默认值，以及 settings 的持久化、类型检查、事务提交/回滚。

### T1.2 初始化默认值

- 初始化拒绝含有任何业务数据的 DB（允许仅迁移过的空库）；事务的写锁保证并发初始化只有一次成功。
- `setup_completed=false`、`setup_step="welcome"`、`panel_name="boxpanel"`、`block_bittorrent=true`。
- `rules` 使用 CONFIG.md §3 的 `bypass_cn` / `metacubex` / `1d` 和 DNS 默认值，
  自定义域名列表为空（文档中的 example 域名仅是示例）；广告过滤、fakeip、IPv6 默认关闭。
- `singbox_version=""` 表示尚未安装，不在 T1.2 硬编码或下载后续任务的内核版本。
- 不创建代理用户、入站或 sing-box 配置；仅初始化管理员、设置和面板证书。

### T8.1 服务器与角色

- 已初始化的库在迁移 0004 时默认获得 `node_role=master`，新安装由 `init` 写入。预留值 `agent` 由同一二进制存储并由
  `boxpanel info` 显示。两种角色在 T8.1 执行同一套本机流程。
- 迁移建立本机行 `id=1`、`is_local=1`、`status=local`、名称「本机」。空库的本机地址在初始化时填入。
  本机地址以 `settings.public_host` 为准。SQLite 触发器在插入或更新该设置的同一事务同步本机的
  `servers.public_host`，覆盖初始化、设置页、引导、CLI 和备份还原后的修改。其他服务器的地址记录在各自的行。
  面板与订阅链接仍由本机设置生成，订阅中的节点地址取入站所属服务器。服务器名称允许重复，订阅按原规则去重。
- `version` 是 boxpanel 版本，本机在 `serve` 启动时记录。空字符串表示尚无版本记录，`last_seen_at` 可为空。
  入站与服务器通过外键关联，端口和标识分别按服务器唯一。旧入站保留 ID、原始 options、状态、备注和时间戳。
- 迁移按 SQLite 的建表、复制、删除旧表、重命名顺序执行。在同一事务内重建表、恢复索引和触发器、检查外键并登记版本。
  外键模式仅在专用连接上暂时调整，结束时恢复。原入站 ID 的外键引用及告警发件箱对事件的引用一并保留。失败整批回滚。
  [SQLite 表结构迁移说明](https://www.sqlite.org/lang_altertable.html#making_other_kinds_of_table_schema_changes)
- `server_traffic_samples.server_id` 和 `alert_events.server_id` 引用 `servers.id`。
  原 `settings.alert_preferences` 移至本机行，保留计费网卡、额度、账期、口径和事件开关。
  本地采样、账期计算、事件归属与未读概览按本机 ID 读取，通知通道继续按原设置存储。
- 备份仍归档完整数据库和本机证书材料。从 v0.3 备份恢复时，候选数据库先迁移，再检查外键及本机的 Hysteria2 证书引用。

## 7. 配置生效流程（核心，务必严格实现）

```text
所有影响本机服务端配置的写操作（入站/用户/服务端设置/节点同步），以及启动、CLI apply、API restart
  → 进程内互斥锁 + sing-box.json.lock 的 flock（跨 serve/CLI 进程）
  → BEGIN IMMEDIATE（保持写锁直到恢复完成；调用方只能使用传入的 *sql.Tx）
  → 处理上次中断的 .pending 恢复记录
  → 在事务内写 DB，读取一致快照，confgen.Build
  → T7.1b：非 nil mutation + 已有配置逐字节相同 + 防火墙计划相同 + is-active
      → 仅更新 applied_firewall / applied_users / apply_generation 并 COMMIT，返回
    否则继续完整流程：
  → 写 sing-box.json.tmp（0600，同目录）
  → sing-box check -c tmp             失败：删除 tmp、ROLLBACK，不改变当前文件/服务
  → 当前 sing-box.json 原子复制为 .bak（0600）
  → 写 .pending 恢复记录（0600，无凭证）
  → rename(tmp, sing-box.json)        原子替换，文件与目录均同步落盘
  → systemctl restart boxpanel-singbox.service
  → 轮询 is-active，最多 5 秒
  → 同步防火墙服务端口及 Hysteria2 端口跳跃
  → 更新 applied_firewall / applied_users / apply_generation，COMMIT
  → 删除 .pending，释放锁
```

仅涉及远端的入站修改在主控提交期望状态，由节点在本机执行上述流程。见 §18。

### T7.1b 配置不变时只提交数据库

在原进程锁、flock、BEGIN IMMEDIATE 和 pending 恢复之后执行 mutation 并生成配置。
只有 mutation 非 nil、当前文件存在、生成结果与文件**逐字节相同**、前后防火墙计划相同，
且 `systemctl is-active` 成功返回 active 时，才走只提交 DB 的分支：同事务写入
applied_firewall、applied_users 和新随机 apply_generation，再提交资源变更。
不写配置、临时文件、备份或新 pending，不执行 check/restart/firewall；健康探测不是重启。
已有 pending 必须先核对/恢复并清理；这次恢复本身仍可能重启内核，不能跳过恢复直接短路。

显式 Restart/Startup/CLI apply 使用 nil mutation，无论配置是否相同都走完整流程。
内核未运行、状态探测失败、缺少当前配置、配置字节不同或防火墙计划不同也走完整流程；
例如只改 Hysteria2 跳跃范围虽不改变 JSON，仍必须同步防火墙。
状态探测或提交前的取消/超时不提交 mutation。
只提交 DB 的 COMMIT 失败同样丢弃底层 SQLite 连接，并在有界独立 context 中核对 generation：
未提交则返回失败，已提交则返回成功；无法确认返回 500 和核对建议，不声称已回滚。
因该分支没有改变外部状态，不通过重启旧配置来补偿 DB 失败，也不创建新恢复记录。

保持有效的用户编辑上限/有效期/备注/月度重置日、重置 token，以及不改变有效用户集合的
手动/月度清零，现在不会中断其他连接。用户名进入认证配置与路由，改有效用户名称仍会重启；
增加/删除/启停有效用户、变更其被启用协议使用的连接凭证、以及恢复或移除到期/超额用户仍会重启。
节点服务参数、端口、跳跃或 BT 设置改变同样走完整流程；节点备注/客户端 fingerprint 不改变服务端配置。
最终以生成字节、计划和运行状态为准，不按接口名白名单绕过。具体操作表见 [T7.1b.md](T7.1b.md)。

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
  恢复使用独立 context（T6.10 收敛为最多 12 秒，总预算见下文）。外部命令沿用每条最多 5 秒、输出最多 1 MiB 的 runner。
- `.lock` 是持久的锁文件，不删除；进程退出由内核释放 flock，不需人工清理陈旧锁。
  同一受管配置必须对应同一 DB。持有事务期间其他写操作可能等待，只读请求走独立连接；跨连接写竞争仍受
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
- `applied_firewall` 存储最近成功的 `{ports:[{port,proto}],hops:[{start,end,port}],traffic_users:[ID...]}` JSON；
  `apply_generation` 是 32 字节 crypto/rand 的 base64url 值，仅用于中断恢复。
  Clash API 运行参数和 Hysteria2 证书约定见 CONFIG.md §2 的 T2.3 补充。
- T2.3 单测以临时 DB/文件、fake runner/systemctl/firewall 覆盖成功、生成/check 失败、重启/存活失败、
  文件失败、防火墙失败、恢复也失败、提交失败、请求取消、并发、启动及恢复记录；真实集成测试只调用
  `.tools/bin/sing-box check` 校验双入站，服务控制/防火墙仍为 fake，无工具时 Skip。
  T3.3 另在 `internal/e2e` 启动临时服务端/客户端进程验证两协议的离线回环，不调用生产服务控制；
  测试专用配置调整与执行边界见 CONFIG.md/INSTALL.md 的 T3.3 说明。

### T6.10 生效期间的读取与超时

- `serve` 在迁移写库之后，对同一绝对文件路径调用 `db.OpenReader`，退出时先关闭读池再关闭写池。
  读池不迁移、不创建库，要求已启用 WAL；使用 `mode=ro`、`query_only=1`、`foreign_keys=1`、
  `busy_timeout=500`，最多 8 个连接，事务使用默认 deferred BEGIN，不能使用写库的 `_txlock=immediate`。
  仅将 `TxOptions.ReadOnly` 加在原写库上不足以解除 BEGIN IMMEDIATE 的锁竞争。
- 公开订阅（包括 sub_path 路由判断）、认证会话检查、用户/入站/规则/设置/引导状态读取、
  links/preview 和证书状态使用读池；多表读取保留单事务快照。生效未提交时返回上一份已提交数据，
  提交后新请求看到新数据；不返回候选用户或 token。不延长读事务到证书文件读取、网络或系统命令。
  Apply/资源 mutation、登录/凭证更新及普通设置写入继续使用写池；不缩短 Apply 的事务锁范围。
- 生效期间上述数据读取不因 Apply 排队超过 1 秒。日志和磁盘配置诊断仍需稳定配置/凭证视图，
  对进程锁使用 TryLock、跨进程 flock 最多等待 100ms；忙时立即返回
  `503 singbox_applying` + Retry-After=1，提示稍后刷新，不暴露候选配置或未经完整脱敏的日志。
- Apply 的进程锁等待、flock 等待和正常工作共用最多 **12 秒**预算。已开始的 SQLite 写事务
  生命周期仍独立于请求取消，以免在恢复前释放写锁；取连接用工作 context，BEGIN 的 SQLite
  busy timeout 仍为 5 秒。已有 pending 的核对/恢复也受总预算约束，恢复未完成不能开始新变更。
- 失败恢复使用独立于请求取消、最多 **12 秒**的 context；正常工作与恢复共用从 Apply 入口计算的
  **24 秒绝对截止时间**，小于 HTTP WriteTimeout=30 秒。COMMIT 失败后的 generation 核对
  也在此截止时间内。优先恢复服务/文件/防火墙，再用剩余预算（至多 1 秒）读取脱敏 journal。
  本地文件同步与 SQLite 调用完成后仍检查截止时间；不启动脱离请求的后台资源写入。
- 正常预算耗尽、恢复成功（或尚未替换配置）时返回 `503 singbox_apply_timeout`，数据库不提交，
  提示刷新核对后再重试。恢复失败/结果无法确认仍优先返回原 `500 singbox_rollback_failed` 语义，
  保留 `.pending/.bak`，后续 Apply 先恢复；不能用 503 声称已恢复。check 的子命令失败/5 秒健康检查
  失败本身不等于总时限耗尽，原 422/500 契约保持。
- sing-box **1.14.4** 没有经官方确认可保持连接的用户配置热替换路径。
  [官方服务管理文档](https://sing-box.sagernet.org/installation/package-manager/#service-management)
  提供 restart；[锁定版本 SIGHUP 处理源码](https://github.com/SagerNet/sing-box/blob/v1.14.4/cmd/sing-box/cmd_run.go#L178-L213)
  先 check，再 cancel/Close 旧实例，然后重新 create，仍会断开旧连接。
  因此不把 SIGHUP 当作无损重载；配置改变时保留 restart/check/回滚，T7.1b 仅优化配置未改变的写操作。
  不新增内核字段、动态用户 API 或 T7.1 流量采样/封禁。

## 8. 安全基线

- 面板默认 HTTPS；新安装尝试 ACME，未签发成功时使用自签备用证书。引导按实际证书类型解释浏览器警告，详见 §13。
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

## 11. T5.1 引导元数据

`setup_preferences` 是可选的 `{domain:string,hysteria2:bool}` 设置键，旧库不存在时按空域名、
Hysteria2=true 读取，首次保存时写入，不修改已发布迁移。setup/status 原子保存进度和场景，
域名需通过本机公网 A/AAAA 校验才写 public_host。这里仅更新客户端元数据；节点/用户创建仍
复用既有 API/Manager.Apply。端口自测只连接系统探测的本机公网 IP，不信任请求或 public_host。
详细 API、超时/注入及开发边界见 API.md 的 T5.1 约定；不实现 M6/M7。

## 12. T6.1/T6.2 安装与发布

`scripts/install.sh` 只协调系统依赖、经 SHA-256 校验的 Release 安装/替换和 systemd 生命周期。
初始化、内核安装、配置生效、防火墙、BBR、就绪探测都调用 Go CLI；不在 shell 解析 DB 或生成配置。
新增可选 settings 键 `panel_firewall_ports`（整数数组）记录安装器开放过的面板 TCP 端口，
无新迁移；成功安装实例的生产启动重放当前端口，卸载合并当前/未完成生效计划后清理规则。
保留数据的卸载不清除配置恢复记录，重装只允许恢复 DB 记录的同版本内核，不推进 T7.6。
生命周期、失败重试、非交互确认、路径/下载约束及 CI/release 产物契约详见 INSTALL.md §2/§3/§5。


## 13. T6.3 面板可信证书

- `panel_tls_mode` 是期望策略：生产 `acme | self_signed`，开发独有 `http`。
  新 init 写入 `acme` 并生成原有自签备用证书，首次签发由 serve 后台执行；HTTPS 先就绪，
  签发失败不阻断安装/启动。旧库保持原策略，由设置页主动申请。失败时期望策略仍为 acme，
  实际证书类型在 `/cert.type` 显示 self_signed，避免停止自动重试或把自签误报为可信。
- 新包 `internal/acme` 使用 `golang.org/x/crypto/acme v0.58.0`，复用依赖、不引入 lego、
  不手写 JWS。原 v0.41.0 缺少 profiles；v0.58.0（2026-10-09，已超过 7 天）新增
  [WithOrderIssuanceProfile](https://pkg.go.dev/golang.org/x/crypto/acme#WithOrderIssuanceProfile)
  并支持 IPIDs。按其 [go.mod](https://github.com/golang/crypto/blob/v0.58.0/go.mod)
  将项目最低 Go 版本从 1.23 提至 **1.26**，x/sys 随之更新为 v0.49.0；CI 继续用 Go 1.27，CGO=0。
- IP 使用 `ip` 标识符、IP SAN 和强制 `shortlived` profile；域名使用 `dns` 标识符、DNS SAN，
  不传 profile，采用 CA 默认（LE classic）。按 [LE 官方公告](https://letsencrypt.org/2026/01/15/6day-and-ip-general-availability/)
  IP 证书约 160 小时。续期统一按证书实际 NotBefore/NotAfter 的 **1/2 寿命**计算，
  不硬编码“classic 30 天”（90 天证书的半寿命实际是 45 天）。
- 仅 HTTP-01：每次签发/续期临时监听 TCP :80，其他路径空白 404，结束或取消关闭监听和在途连接；
  80 占用立即失败，不借用 443、不停止其他服务。每次签发总窗口 2 分钟。
  生产启动及签发前复用本机防火墙适配器放行 TCP 80，先登记 `panel_firewall_ports`，
  可供卸载清理；不修改云安全组。改回自签停止签发和续期，保留已登记放行规则供卸载清理。
- 账号按 directory 哈希隔离；材料在 `<data-dir>/certs/`，目录 0700、文件 0600。
  每次证书链和私钥作为一个 PEM bundle，临时文件 → fsync → rename → 目录 fsync，
  名称按内容 SHA-256 生成；原 `panel.crt/key` 不覆盖；T7.5 在成功切换后仅保留当前和上一份 panel-*.pem。
  切换主机或证书到期回退时，用同一长期回退密钥为当前主机签发自签证书，详见 §17。
- 可选 settings 键 `panel_certificate` 保存实际类型、主机、签发者、有效期、上次尝试/成功、
  固定错误码及下一步提示、连续失败数、下次重试、内部 bundle 文件名及 directory；没有新迁移。
  内部文件名和 directory 不出 API，私钥/账号/挑战 token/CA 原始错误不进日志或 API。
  证书启用前验证公私钥匹配、SAN、有效期、非 CA 叶证书及 serverAuth 用途。
- 后台每分钟检查一次；失败从 5 分钟指数退避，上限 6 小时，策略随重启保留。
  续期失败保留仍有效的当前可信证书；过期则恢复有效自签备用证书并继续重试。
  连续至少两次失败且当前证书剩余 <24 小时返回 warning，面板全局醒目显示下一步。
  `tls.Config.GetCertificate` 从原子指针读取当前证书；新连接立即使用新证书，现有连接无需重启。
- 设置提交后通知 worker；public_host 改变且策略为 acme 时自动重新申请；self_signed 保持自签。
  设置变更取消在途订单，激活前在数据库事务内重新核对主机/策略，丢弃过时的签发结果。
  签发、网络和防火墙操作不持有 settings/Apply 事务，不引入第二条 sing-box 配置写入路径。
- 时钟、Issuer、HTTP client、directory、challenge listener 和放行端口均可注入。
  测试使用临时 DB/文件、假 CA HTTP 服务器及本地 TLS 握手；不访问 LE 正式环境。
  未引入 Pebble 依赖或额外真实 CA 测试。


## 14. T7.1c 内核流量统计与自动停用

T7.1 的 Clash `/connections` 快照在真机 20 MB 下载中只计到约 26%，已移除连接轮询，
不提供近似退路。历史调研见 [T7.1.md](T7.1.md)，本节为当前契约。

- 每个合格用户仍使用 `auth_user` → `direct-u<ID>`，位于 sniff/BT reject/resolve/private reject
  之后。Linux 生产配置增加整数 `routing_mark = 0xb7000000 + ID`，保留高字节 `0xb7` 的范围，
  ID 必须在 1–16777215；超界拒绝生成并给修复建议，禁止截断/回绕。普通 direct、服务器 DNS
  和 Reality 握手不加 mark。开发/非 Linux 不加 mark、不操作 nft、不运行采集调度。
  字段依据 [锁定 v1.14.4 Dial Fields](https://github.com/SagerNet/sing-box/blob/v1.14.4/docs/configuration/shared/dial.md#routing_mark)：
  支持整数和十六进制字符串，仅 Linux。必须为此预留 fwmark/ct mark 范围，避免管理员策略路由冲突。
- 使用独立 `table inet boxpanel_traffic`，`output`、`input` 都是 filter 基链，priority mangle
  （-150，在 conntrack -200 之后），policy accept，不改变系统准入策略。output 匹配用户 meta mark
  及 TCP/UDP，执行 `ct mark set meta mark` 并累计 `up_u<ID>`；input 匹配用户 ct mark、
  TCP/UDP 且 `ct direction reply`，累计 `down_u<ID>`。因此 TCP、UDP/QUIC 的每个包都计数，
  不依赖连接是否仍存活；方向限制避免回环请求被算作下载。两地址族共用 inet 表。
  [nftables 官方手册](https://www.netfilter.org/projects/nftables/manpage.html) 的 CHAIN、COUNTER、
  CONNTRACK STATEMENT 为语法与优先级依据。
- 表、规则和用户计数器纳入 `applied_firewall.traffic_users` 及 pending 前后计划；旧记录缺字段视为空。
  完整 Apply 同步时原子 flush 本表两条链并重建规则引用，仅为新增用户创建命名计数器、删除离开用户的
  计数器；原有用户的 counter 对象不重置。恢复/启动重放同样差量处理，卸载清理本表。
  T7.1b 的配置/计划不变短路不触碰 nft。统计基链不参与“活动 INPUT 防火墙”的检测或端口放行。
  nft 缺失/权限/语法失败不能静默降级；生效按原机制回滚，并提示安装 nftables/核对 NET_ADMIN 后重试。
- 每 10 秒执行 `nft -j list counters table inet boxpanel_traffic`，不请求 Clash API。
  主控进程第一次成功读取仅建立基线，节点恢复上次落库的内核基线（§19）；随后每用户、每方向累计非负差值。
  某方向计数变小视为重建，以当前值计增量；新出现的用户以当前值计增量。未知用户忽略，
  删除用户回收基线，读取失败保留基线和待写增量；非法 JSON/负数/重复/不完整计数对不推进基线。
  uint64 超过 int64 时饱和处理。无生效用户时建立空基线，无需访问尚不存在的表。
- 口径为**服务器与目标站点之间的 IP 层字节**：上传=服务器发出、下载=服务器收到，包含 IP/TCP/UDP
  头、ACK 和重传，不含客户端到服务器的加密开销，也不含服务器自身 DNS 解析；与客户端显示可能相差
  几个百分点，小包占比高/重传多时差异更大。既有历史计数保留，不追溯补算旧版漏计。
  主控面板停止期间至重启首次基线的用量不补算，节点同一次系统启动内可按 §19 补算。系统重启/表被外部重建、用户计数器删除时
  尚未采样的尾段无法恢复。正常存活计数器保留短连接完整累计值，采集暂时失败后可补差值。

运行与一致性（延续 T7.1/T7.1b）：

- 主控每 60 秒、节点每 30 秒在同一个短写事务中批量 `traffic_up/down += 增量`，无网络、内核或防火墙操作。
  Flush 使用至多 1 秒 context（含写池排队），失败全部回滚、保留增量重试。
  SQLite BEGIN 的底层 busy timeout 仍沿用 §6；读请求继续走 T6.10 独立只读 WAL 池。
  写入同时保存内部 `settings.traffic_batch` 标记；若上次 COMMIT 结果无法确认，下次写入前核对标记，
  扣除已提交的批次后仅重试剩余增量，避免生效事务结果不确定时重复累计。
  正常退出尽力最后采样并落库（采样 5 秒、落库至多 1 秒）；失败记录下一步提示。
  主控崩溃或强制终止可能丢失尚未落库的增量，通常至多 60 秒，写失败期间可能更长。节点按 §19 从内核补差值。
- 页面/API/Subscription-Userinfo 均读取主控已落库值。自动停用检查额外纳入主控内存增量，
  不必等待下一轮落库。面板用户列表/详情每 60 秒刷新；不在读取请求中触发采样或落库。
- 新迁移 `0002_user_traffic.sql` 追加 reset_day/reset_at/epoch 三列，旧用户默认 0、不自动重置，旧计数保留。
  epoch 为内部防重置竞争代号。新建用户用 crypto/rand 生成正初值，用于隔离不同时期的采样。
  手动/月度清零与 epoch+1 在同一 Apply 事务中提交。旧 epoch 增量落库时条件更新并丢弃，
  同秒重复重置也不互相混淆。用户计数器跨 epoch 存续时，首次观察仅重建基线，不补记重置前字节。
- Manager 每次成功 Apply 在同一事务保存 `settings.applied_users`（ID 数组，无启用入站时为空），
  与 apply_generation 一起提交/回滚，表示当前配置实际包含的用户。启动重新生效会补齐旧实例的此键。
  调度器每 30 秒检查到期/超额且仍在该数组中的用户，使用与 confgen 相同的 UserEligible 判定，
  合并为一次 Manager.Apply；事务内再次核对并同步待写增量，禁止绕过 check/原子替换/重启/存活/回滚。
  成功后日志仅记用户 ID 和 expired/exhausted 原因，不修改 enabled；重启断开已有连接，入站不再接受其认证。
  普通情况到期/超额检测延迟至多约 30 秒，另加一次生效时间；失败从 30 秒指数退避至最多 10 分钟，
  下轮重试。用户提高限额、延期或清零时沿用资源 Apply 恢复，无额外手动操作。
- `traffic_reset_day=1–28` 时，在 **UTC 本月该日 00:00 后**首次检查清零并生效，
  `traffic_reset_at >= 本月边界` 即不重复。0 关闭；停机跨月后只补本月已到的边界，不补每个错过月份。
  手动重置也更新 reset_at，因此当月边界后的手动清零会抑制当月再次自动清零。
  月度清零在配置不变且内核运行时只提交 DB，不重启；跨 epoch 存续的计数器按上述规则重建基线。
  月度清零不限 enabled，可能恢复仅因超额被移除的用户（恢复会改变配置，仍重启）；到期/手动禁用仍保持不可用。
  多用户清零、到期、超额共用一次 Apply；失败时计数、reset_at、epoch、applied_users 一起回滚。

完整评审说明与验收记录见 [T7.1c.md](T7.1c.md)；前序记录见 T7.1/T7.1b。无新依赖。


## 15. T7.5 备份与恢复

实现集中在 `internal/backup`，CLI、HTTP 下载、每日 worker 共用归档生成；CLI 和面板后台共用恢复事务。
不增加依赖、迁移或 sing-box 配置入口，不涉及多服务器、告警和升级任务。

### 格式与密钥

- 归档 v1 为单个 gzip 压缩 tar，只有 `manifest.json`、`boxpanel.db` 和 `certs/<普通文件名>`。
  DB 使用参数绑定的 `VACUUM INTO ?` 生成一致性快照，不复制活动 db/wal。
  全部 certs 材料包括 Hysteria2、ACME 账号、当前/上一份面板 bundle、原自签材料和 `panel-fallback.key`。
  不含服务端 JSON、WAL/SHM、备份目录、锁或 bak/pending。manifest 记录 format=1、boxpanel_version、
  schema_version、created_at、public_host 及 files 的 SHA-256 小写 hex。
- 手动导出全部加密：外层文件 `.tar.gz.enc`，固定头 `BOXPANEL-BACKUP` + 0x00 + 0x01，
  接 16 字节随机盐、24 字节随机 nonce、XChaCha20-Poly1305 密文（含 tag）。整个头作为 AEAD AAD。
  scrypt 固定 N=32768、r=8、p=1，导出 32 字节密钥；不接受文件指定的任意 KDF 资源参数。
  使用现有 x/crypto/scrypt、chacha20poly1305.NewX；盐/nonce 均来自 crypto/rand。
  口令至少 12 个 Unicode 字符且不超过 1024 UTF-8 字节，不 trim、不保存，不通过进程参数传递。
- 输入上限 64 MiB，解压总量 256 MiB，至多 512 个数据文件加一份 manifest；单文件不超过 64 MiB，
  manifest 不超过 128 KiB。拒绝重复路径、路径穿越、绝对/Windows 路径、反斜杠、链接、目录、设备等非普通条目。
  要求 gzip CRC/尾部完整，拒绝多流/额外尾随内容；每个数据文件与 manifest 一一对应且 SHA-256 匹配。
- 本任务采用可迁移的标准目录布局：DB 为 `<data-dir>/boxpanel.db`，所有证书在 `<data-dir>/certs/`，
  主控回退密钥为 `certs/panel-fallback.key`，旧安装材料为 `certs/panel.crt/key`，
  配置为 `<data-dir>/sing-box.json`。内核二进制在数据目录之外。
  数据目录可覆盖；独立外置路径拒绝操作并给迁移建议，避免悄悄漏备份固定指纹材料。

### 生命周期与回滚

- 解密和验证在目标数据目录的同级 0700 临时目录完成；核对 manifest 与 DB 实际迁移号，
  新 schema 拒绝并提示升级，旧 schema 运行现有迁移。检查 integrity_check、foreign_key_check、
  管理员与面板配置，并验证引用的面板/Hysteria2 证书和匹配私钥、固定指纹，拒绝缺失材料。
  若存在 `panel_fallback_pin`，备份和恢复校验回退私钥与该 pin 一致。升级前的备份仍按原证书材料校验。
  在候选库清空 sessions、checkpoint 并关闭 DB；现有库尚不修改。
- 用数据目录同级 `.restore-lock` 互斥恢复（中断遗留时，确认无恢复进程后才人工移除）。
  systemctl stop 面板和内核，等待退出；读旧生效计划/已登记面板端口并关闭 CLI 连接，
  按既有 CleanupFirewall 清理目标旧规则（含 pending），不删除原恢复材料。
  原数据目录整体 rename 为 `.pre-restore-<UTC时间>-<随机后缀>`，候选目录 rename 到原位置并同步父目录。
- 备份中的 applied_firewall 是源机器运行记账，在目标先重置为空，再通过唯一的 Manager.Restart
  从 DB 生成 → 临时文件 → check → 原子替换 → 重启/存活 → 防火墙 → 提交；绝不恢复备份的 JSON。
  恢复面板端口，启动面板，其正常 Startup 同样完整生效；使用本机信任材料校验 HTTPS readiness。
  所有 SQLite 连接在目录替换前关闭；面板上传通过独立 systemd-run 工作进程完成上述操作。
- 停止、目录替换、生效或启动任一步失败，独立最多 90 秒恢复预算：再次停服务，失败候选另存 `.failed`，
  原目录移回，清理候选端口/计数器并恢复旧计划；原先运行的内核经 Manager.Restart 校验恢复，
  原先运行的面板重新启动并检查就绪。自动恢复也失败时明确报告需人工修复，不声称已恢复；保留诊断材料。
  普通失败有自动回滚；断电/SIGKILL 下不声称硬件级事务原子性，应使用完整 pre-restore 目录人工核对恢复。
- 成功保留整个原目录供审查/人工取回；备份身份保持不变，全部会话失效。
  public_host 为 IP 时尽力探测当前公网地址，不同或探测失败给出 set-host/DNS 提示；
  IP 迁移不能保持旧 IP 链接可达，使用域名并切 DNS 可保持客户端订阅地址。

### 每日与证书清理

- 生产 serve 每分钟检查，在 UTC 日期首次运行时备份（启动补当天一次），失败当天不重复，次日再试。
  `<data-dir>/backups/` 为 0700，自动 tar.gz 和状态文件为 0600；成功写完并 fsync 后才轮转，保留最近 7 份。
  状态 `{last_attempt,last_success,result}` 原子持久化，重启不重复当天任务；失败保留上次成功时间。
  不含该状态与自动备份自身，避免递归归档。面板提醒同机风险，建议定期异机保存加密文件。
- 成功切换证书后清理未引用的旧 panel-*.pem，DB panel_certificate.previous_file 记录上一份，跨重启保留。
  只删除文件名严格匹配的普通 bundle，不删除 fallback、账号或 Hysteria2 材料。
  备份快照/证书读取与证书激活/清理使用 certs.lock 的跨进程 flock，防止归档引用的 bundle 被并发清理。

依据：[SQLite VACUUM INTO](https://www.sqlite.org/lang_vacuum.html#vacuum_with_an_into_clause)、
[x/crypto scrypt](https://pkg.go.dev/golang.org/x/crypto/scrypt)、
[XChaCha20-Poly1305](https://pkg.go.dev/golang.org/x/crypto/chacha20poly1305)、
[systemd-run](https://www.freedesktop.org/software/systemd/man/latest/systemd-run.html)。

## 16. T7.9 服务器流量、通知与告警

### 网卡账期

- Linux 生产面板启动时读取 `/proc/net/dev`，随后每 60 秒保存所选网卡的 RX、TX 字节增量。`ip -4 route show default`
  优先选最低 metric 的 IPv4 默认路由，缺省时查 IPv6 默认路由。并列时采用首条适用路由，多路径采用该路由中首个 dev。
  设置可指定非回环网卡。首次采样和切换网卡时只建立基线。某个方向计数器减少时，以该方向当前计数作为增量。
- 样本按结束采样时刻归入 UTC 账期，周期为计费日 00:00 到下月同日 00:00，包含开始、排除结束。跨界采样间隔
  的整段增量归入结束侧。切换网卡时仍保留本期已记录的用量。样本保存最近 62 天，调整计费日后按新边界重算当前周期。
  面板停机到重启首次基线的间隔无法补计，系统重启前的未采样尾段也可能遗漏。
- `quota_gb` 为十进制 GB（10 亿字节每 GB）的非负整数，0 关闭服务器用量告警。默认计费日为 1，口径为 `out`。
  先汇总周期内 RX 和 TX，再按 `out=TX`、`in=RX`、`both=RX+TX`、`max=max(RX,TX)` 计算已用值。
  用户用量仍为 §14 的服务器与目标站点之间的 IP 层流量。双向网卡计费下，代理流量通常约为两倍用户用量，
  还含加密开销、端口扫描和系统流量。VPS 账单以服务商的账期、计费口径和计数为准。
- 迁移 `0003_alerts.sql` 添加 `server_traffic_samples`、`alert_events`、`alert_outbox` 和 `alert_attempts`。
  T8.1 将样本与告警事件的 `server_id` 关联到 `servers` 表。通知通过事件关联服务器。用户增加稳定的内部 `alert_identity`，
  从既有采集 epoch 初始化，新用户与随机初始 epoch 一起写入，避免复用 ID 与同一有效期的旧事件冲突。

### 事件与投递

- 默认打开各事件开关，三个通道默认关闭。扫描每分钟执行一次，使用已提交的用户流量和适用的告警开关。
  启用用户在用量达到 80%、100% 时按流量 epoch 去重，在剩余不超过 72 小时和已到期时按到期时间去重。
  有本机生效用户时，自动停用事件由 T7.1 调度器在实际生效事务内写入，并同时检查命中的用量和到期阈值。
  仅远端生效时，远端用量越额与告警在同一汇总事务提交（§19）。事务回滚时，关联的告警与通知行一并回滚。
- 服务器用量按账期起点、阈值去重。证书告警复用 `acme.Status.Warning`，按当前证书到期时间去重。
  Linux 生产内核探针复用 `systemctl is-active`，连续两次失败创建一次停机告警。连续失败数和告警状态跨面板重启保留。
  状态恢复时仅为已经记录停机告警的事件生成恢复告警。告警状态与去重表保存在 SQLite。
- 告警与当时启用的通道各一条发件箱行在同一事务提交。概览从告警表读取未读事件，独立于外部投递结果。
  每通道独立领取和发送，网络请求在事务外执行。投递失败按 30 秒、60 秒、120 秒等指数退避，上限一小时，
  24 小时停止重试并保留失败摘要。重启后继续待投递事务，领取后未记录结果的请求在短租约过期后可再投递。
  服务商已经接收请求而响应或本地提交丢失时，重试可能产生重复的外部通知。
- 每个通道滚动一小时最多发起 30 次 HTTPS 请求，含重试和测试。第 30 次有多条待发送时合并为一个摘要。
  满额期间的新待投递项排队合并，在下一可用名额发出一条摘要。每项事件和投递状态仍可在面板查询。
  停用通道时结束其待发的自动通知。已保存凭证的停用通道仍可测试，已经发出的请求按原接收方处理。
- 通道配置使用 SQLite settings。面板只返回掩码和固定错误代码。外发正文仅从服务器地址、事件、用户名、
  用量和 UTC 时间构造。所有投递要求 TLS 证书校验，且禁止代理和重定向。自建 Bark 在保存时校验 DNS，
  HTTPS 建连时使用 `reality.PublicIP` 和特殊地址检查验证全部 DNS 答案，并连接验证通过的 IP，TLS 仍验证原域名。
- 开发与非 Linux 环境可配置通道及发送测试。网卡和 systemd 探针仅在 Linux 生产模式启用。
  API 和页面行为见 [API.md](API.md#t79-通知与告警)、[FRONTEND.md](FRONTEND.md#12-t79-服务器流量与通知)，
  请求格式依据和评审记录见 [T7.9.md](T7.9.md)。

## 17. T8.2 / T8.2b 节点注册与心跳

- 同一个二进制按 `node_role` 运行主控或节点。节点的 `serve` 启动本机受管 sing-box、用户计数与网卡采样，
  并主动连接主控。节点没有管理监听、面板用户或 ACME worker。主控通过节点主动发起的 HTTPS 请求进行双向身份校验。
- 迁移 0005 扩展 `servers.status`，加入 `removed` 并保留原 ID、外键和历史流量。主控移除节点时清空公钥，
  保留服务器行及其历史，服务器列表和订阅忽略该行。离线节点仍沿用 T8.1 的订阅规则。
- `agent_join_tokens` 保存令牌的 SHA-256 摘要、服务器名称、过期秒数和用于撤销的编号。令牌是 32 字节随机值，
  以无填充 base64url 传输，主控对 UTF-8 令牌字符串计算摘要。消耗令牌与写入 `pending` 服务器行在同一事务完成。
  T8.6 的迁移 0008 保存已使用令牌与服务器 ID 的关联。已使用令牌立刻失效，只在原有效期内提供关联状态。
  签发时清理过期记录。备份恢复会清空尚存的加入令牌，防止快照恢复已使用或已撤销的令牌。
- Ed25519 密钥在节点生成。私钥种子存数据目录 `agent.key`（0600），公钥随注册发送。
  节点 SQLite 保存角色、主控 URL、可选证书 pin、服务器 ID、公钥和最近心跳结果。
- 主控首次启动证书管理时，将回退私钥持久化为 `certs/panel-fallback.key`（0600）。升级优先迁移当前或上一份
  自签 bundle 中的密钥，其次使用原 `panel.crt/key` 的自签密钥。没有可用自签材料时生成新密钥。
  `panel_fallback_pin` 记录该密钥的公钥指纹，已记录身份但文件缺失或不匹配时提示恢复原密钥。
  新 host、到期及 ACME 续期失败回退时沿用同一密钥签发自签叶证书。ACME 使用独立密钥。
- 节点以主控面板首页 HTTPS URL 为基地址，拒绝重定向。TLS 最低 1.2。HTTPS 主控生成的每条加入命令附带
  固定回退公钥 SPKI DER 的 SHA-256（无填充 base64url）。节点可凭有效期内匹配的叶证书 pin 建连，或凭
  标准 `x509.Verify` 验证系统 CA 链和主控 URL 的主机名建连。旧 pin 不匹配时可通过 CA 链建连。空 pin 只走系统信任，
  主控回退到系统未信任的自签证书时需要使用带固定 pin 的命令重新加入。若旧自签密钥已丢失，旧 pin 节点适用同一限制。
- 注册只接受公网节点地址。未提供 `public_ip` 时检查实际 TCP 对端地址。私网、回环、共享地址、文档示例和保留地址
  返回 422，令牌仍可用于修正后的请求。节点可通过 `--public-ip` 提供 NAT 对外映射的地址。
  [IANA IPv4 特殊用途地址](https://www.iana.org/assignments/iana-ipv4-special-registry/) ·
  [IANA IPv6 特殊用途地址](https://www.iana.org/assignments/iana-ipv6-special-registry/)
- 签名覆盖 HTTP 方法、含面板路径的完整 URL 路径、Unix 秒、nonce 和原始请求体 SHA-256。
  主控校验时间偏差上限 120 秒，公钥取自活动服务器行。每个节点的 nonce 在进程内缓存 5 分钟。
  缓存按最近成功用时清理过期项，最多 65,536 条。缓存已满时拒绝新 nonce，活动项保留到期。
  重放、时间偏差、未知或已移除节点、验签失败均返回同一 401。
  节点收到 401 后按秒比较响应 `Date` 和本地时钟。偏差超过 100 秒时记录 NTP 提示并清除连续认证失败计数。
  缺少有效 `Date` 或偏差不超过 100 秒时沿用原认证失败提示。
- 首次心跳使服务器变为 `online`，更新版本及最近联系时间。主控每 30 秒检查，超过 90 秒未成功联系则标为
  `offline`，重新联系时转为 `online`。`pending` 等待首次联系。断线时本地内核和计数继续运行。
- 心跳携带内核是否运行及节点进程运行秒数。主控更新健康、联系时间和版本。T8.6 还保存最近一次报告的运行秒数，未知时为空。

## 18. T8.3 期望状态同步

- 迁移 0006 为远端服务器保存期望版本、已生效版本、最近同步错误和内核健康。入站变更和受影响远端服务器的版本在同一事务提交。
  用户凭证或有效性变化、服务端 BT 开关的版本变更由数据库触发器纳入同一事务。流量样本只有越过配额边界时才使版本失效。
  自然到期通过每台节点的上次核对时间与当前时间比较，在主控响应心跳或读取期望快照的事务中推进版本。
  客户端专用分流规则、服务器名称和订阅地址不进入节点生效版本。
- 节点成功联系后每 20 秒发起一轮心跳，为通常 30 秒内生效留出配置校验和启动时间。收到不同版本立即签名拉取。
  拉取在同一事务核对身份、读取版本与入站、筛选已启用且未到期、未超额的用户，以及读取服务端路由设置。
  主控不可达按原 5–60 秒退避，继续使用最后成功生效的版本。节点采集、保存并上报本地流量（§19），由主控执行到期、超额和月度重置。
- 节点在一次 `Manager.Apply` 事务内替换镜像入站和有效用户，保存路由和已生效版本。镜像使用主控 ID 及节点的本机
  `server_id`。同 ID 和 UUID 的用户保留本地累计用量。镜像的到期、配额和月度重置为零，由主控提供成员资格。
  core 校验、原子替换、回滚、配置/防火墙计划不变时免重启与单机路径相同。节点完成尝试后立即心跳报告版本和安全错误代码。
- Reality 私钥只在节点 SQLite 和生成的内核配置中保存。Hysteria2 私钥文件只落在节点，权限为 0600。已应用证书使用
  与入站材料版本关联的独立路径，重试可复用暂存材料，旧路径供回滚。节点从已提交的配置读取可上报材料。
  若恢复后的本地 Reality 私钥或 Hysteria2 证书/密钥缺失，即使版本相同也重新同步并签发材料。
- 入站首次创建、换服务器或远端 Hysteria2 SNI 变更后等待匹配材料版本的上报。公开材料上报在写事务内校验签名身份仍有效、
  入站确实属于该服务器且版本匹配。上报只保存公开材料，不递增期望版本。订阅只导出远端已就绪入站，离线时继续导出。
  手工预置的旧版远端行在升级后会清除原有密钥字段，进入等待节点生成状态。单机配置和订阅沿用原输出。
- 节点 `leave` 清除镜像行和节点身份，保留现有内核文件及历史网卡样本。重新加入时按新服务器 ID 重新下发入站。
  切换入站归属涉及两个节点时，各节点按自己的下一轮心跳独立生效。
  LXD 和真实防火墙、端口转发及客户端连通性由部署验收确认。

## 19. T8.4 远端流量汇总

- 主控总量为本机和所有远端服务器的已确认用户用量之和。迁移 0007 增加 `server_user_traffic`，每个 `(server_id, user_id)`
  保存最近 epoch、两个方向的最高确认值和跨 epoch 累计。每次签名上报在同一事务内检查节点身份、推进基线并饱和增加 `users.traffic_up/down`。
  移除服务器保留账目，删除用户级联清除其账目。主控用户 ID 递增分配，防止离线节点的迟到报告归入新用户。
- 节点的镜像 epoch 对同一 ID 单调递增。新镜像或 UUID 更换时分配新 epoch，旧版本已有的本地 epoch 保留。
  收到更大的 epoch 时从零计算，较旧 epoch 忽略。同一 epoch 下某个方向回退时保留该方向的最高确认值，增量为零并记录无用户信息的警告。
  允许较低快照移动基线会使后续重放重复入账，因此节点用新 epoch 表示真正的累计重建。累计值和单调 epoch 共同实现幂等排序。
- 节点每 10 秒采样、30 秒落库，在正常每 20 秒的心跳前上报有变化的已落库快照。成功响应丢失时重试同一数据，主控只计一次。
  正常用量约一分钟内可在主控读取，其余节点在各自的下一轮心跳接收配额变更。
  上报失败不阻止节点拉取主控策略。镜像同步前尽力追加一次采样，在 Apply 事务中落库待写增量并留存被移除或替换用户的最终快照。
  留存快照跨进程重启，节点按照 epoch 顺序上报，同一用户的较旧快照确认后再送较新快照。`leave` 清理旧身份的待确认快照。
- Linux 节点把同次系统启动的内核采样基线与用量一并提交。在 sing-box 与 nft 计数器继续运行的情况下，boxpanel 重启后可从已落库基线补差值。
  内核计数器被重建、系统重启前未保存的旧内核尾量，以及首次建立基线前的用量，仍可能无法恢复。
- 主控已落库的总量越过配额时，沿用期望状态触发器推进所有远端版本，节点在下一轮同步移除用户。本机按原调度器生效。
  对未在本机生效的用户，主控在远端汇总事务内用相同的 T7.9 规则写入 80%、100% 和自动停用事件及发件箱，每周期一次。
  此处自动停用事件表示主控已提交停用策略，离线节点会在恢复联系后执行。到期仍由主控的期望状态检查处理。
- 手动或月度清零只重置用户周期总量，保留节点确认基线和服务器累计。自上次上报起、清零前仍未确认的用量可能进入新周期。
  月度清零使用户重新有效时，所有节点随下一轮期望状态恢复。主控忽略未知用户 ID，节点负责只上报实际镜像过的用户。
- `GET /servers` 展示每台远端服务器所记全部未删除用户的累计上传和下载字节。该口径与 T7.9 的网卡计费统计分开。
  用户按服务器拆分的明细见 §21。

## 20. T8.5 多服务器订阅

- 同一 SQLite 快照读取用户已确认的全网用量、订阅设置、服务器和入站。所有订阅格式、分享页和管理员 links
  统一按本机优先、远端服务器 ID 升序、同服务器内入站 ID 升序排列。沿用启用、远端就绪和服务器移除的过滤规则。
- 单机默认名为「协议 · 端口」。多台未移除服务器时，无备注节点前缀为「服务器名 · 」，已清理的非空备注优先。
  增删无命名碰撞的入站不改变现有节点名。顺序分配的去重后缀会随冲突顺序变化，切换单机和多服务器时默认名前缀也随之变化。
- `settings.subscription_auto_group` 存为 JSON 布尔值。缺失时按 `false` 读取，升级无需数据库迁移。
  该设置只影响客户端配置，不属于节点期望状态。开启且实际导出的节点至少三个时，sing-box / Clash
  在手动组最后加入自动测速选项，仍使用首个节点作为默认选项。
- 公开订阅的 GET/HEAD 通过响应头提供汇总流量、限额和可选到期时间。配置下载名基于面板名称，
  HTML 分享页继续内联。格式、字段与编码见 CONFIG.md §4。

## 21. T8.6 服务器与用户周期流量明细

- 迁移 0008 给远端用户账目增加 `user_epoch`、`cycle_up`、`cycle_down`。已有行记录当前 `users.traffic_epoch`，两个周期计数从零开始。
  以前已计入本周期的远端流量继续保留在用户总量中，在该周期结束前计入「本机及未归属」。节点上报基线与跨周期累计保持原值。
- 主控在接收新远端增量的事务内比较账目中的 `user_epoch` 与用户当前 `traffic_epoch`。不同时从零开始累计新的周期用量，
  更新账目 epoch，继续原样更新远端累计和用户总量。查询把旧 epoch 的周期用量视为零，清零后无需等待新的远端上报。
- 用户详情列出所有未移除的服务器。本机各方向的用量为用户周期总量减去已列出的远端周期用量，下限为零。
  因此本机项包含本机实际用量及未归属用量，后者包括本周期已移除服务器的用量。每台远端各自的用量在该服务器累计中独立记账。
