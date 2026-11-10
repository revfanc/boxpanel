# 一键安装 / CLI / 发布规格

## 1. 用户视角

```bash
bash <(curl -fsSL https://raw.githubusercontent.com/revfanc/boxpanel/main/scripts/install.sh)
```

> 仓库地址在脚本顶部定义为变量 `REPO="${BOXPANEL_REPO:-revfanc/boxpanel}"`，可通过环境变量覆盖（便于 fork 测试）。

首次初始化时立即打印并保存以下凭证（密码仅此一次；后续步骤失败也不会丢失这次输出）；
安装结束再次用 `boxpanel info` 显示地址和用户名，忘记密码用 `reset-admin`：

```
✅ boxpanel 安装完成
面板地址: https://1.2.3.4:23456/k3m9x2p7q1ab/
用户名:   admin-7fq2
密码:     xxxxxxxxxxxxxxxx
面板证书: ACME 证书（若申请未完成或失败，则明确显示自签证书和下一步提示）
请确认云服务商安全组已放行 TCP 23456 和 TCP 80。
```

## 2. `scripts/install.sh` 行为

子命令：`install`（默认）、`update`、`uninstall`；选项 `--non-interactive`、
`--version <tag>`、`--port <n>`、`--yes`、`--purge`、`--dry-run`、`--help`。

install 流程：

1. 必须 root；`set -euo pipefail`；所有步骤打印 `[1/8]` 形式的进度。
2. 检测系统：Debian 11+/Ubuntu 20.04+/CentOS Stream 9/Rocky/Alma 8+/Alpine(后期)；架构 `amd64|arm64`（其他直接报错并说明）；需要 systemd。
3. 安装依赖：`curl tar ca-certificates coreutils`、`iproute2`（RHEL 系为 `iproute`）、`kmod`；
   没有 nft 且没有完整 iptables/ip6tables 时安装 `nftables`（端口跳跃用），不自动启用防火墙。
4. 从 GitHub Release 下载 `boxpanel-linux-{arch}.tar.gz` 与 `checksums.txt`，**sha256 校验**后安装到 `/usr/local/bin/boxpanel`。
5. 调用 `boxpanel init --port <随机或指定>`：初始化 DB、生成 web_path/sub_path/管理员账号/自签证书、探测公网 IP。
6. 调用 `boxpanel singbox install --preserve-version`：首次下载锁定版本，重装时保留 DB 已记录版本，必须通过官方校验文件或资产 API 的 SHA-256；
   缺少摘要时失败，不再允许仅结构/版本检查的兜底。单元仍由 Go 模板生成。
7. 调用 `boxpanel apply` 完成首次配置校验/生效，再调用 `boxpanel system firewall-open`，
   enable 两个单元并 restart 面板；同时确认服务 active 与本机 HTTPS 就绪。
8. 询问是否开启 BBR（非交互模式默认开启），输出 §1 的信息。

update：下载新版 boxpanel → 校验 → 同目录原子替换 → `systemctl restart boxpanel` → 检查本机 HTTPS。
脚本不初始化/重置 DB、不安装/升级 sing-box；`serve` 仍按既有约定迁移 DB 并执行 Startup/Apply，
因此“不动 DB 与 sing-box”不表示阻止正常启动时的重新生效。失败尝试恢复旧二进制并重启，
不回退数据库迁移、不声称整机事务回滚；旧版也无法启动时提示检查数据库兼容性和 journal。

uninstall：二次确认 → 停服务、删单元 → 清防火墙规则与端口跳跃规则 → 询问是否保留 `/etc/boxpanel`（默认保留，提示路径）。

脚本要求：幂等（重复执行 install 等于检测到已安装后提示改用 update）；`shellcheck` 无警告；不依赖 bash 4 以外的特性。

### T6.1 参数、重试与验证边界

- 脚本兼容 macOS 自带 Bash 3.2 进行离线测试，真正安装仅支持 Linux/systemd + root。
  支持 Debian 11+、Ubuntu 20.04+、CentOS Stream 9+、Rocky/AlmaLinux 8+，仅 amd64/arm64。
  非 root、非 systemd、旧系统和其他架构在下载/修改前拒绝；Alpine 仍留待后续。
- 非 TTY 输入自动采用非交互模式。非交互安装默认尝试 BBR；不可用时保留可用面板并提示在自检中重试。
  非交互卸载必须显式 `--yes`，默认保留数据；只有 `--purge` 明确授权删除数据。交互卸载会确认停止连接，
  再询问是否永久删除数据（默认否）。不卸载系统依赖、不关闭 BBR，以免影响其他服务。
- `--version` 接受 `vX.Y.Z` 及 `vX.Y.Z-rc.N` 等预发布 tag。省略时只解析一次 GitHub latest 的最终 tag，
  随后两个资产都从同一个固定 tag 下载，避免发布切换造成版本混用；没有稳定 Release 时请显式指定 rc tag。
  下载仅 HTTPS，重定向也必须 HTTPS，有连接/总超时与有限重试；校验文件至多 1 MiB，包至多 128 MiB。
  `checksums.txt` 必须恰有一条匹配文件名的 64 位 SHA-256（支持普通和 `*filename` 格式）。
  哈希通过后检查 tar 只含根目录普通文件 `boxpanel`，拒绝链接、目录、重复/额外条目，再执行 `version`
  核对 tag；通过前不替换旧二进制。checksum/Release 元数据经 HTTPS 获取，不递归校验校验清单自身。
- 安装器固定使用 §3/§4 的生产默认路径，并向每个 CLI 调用显式导出一致的路径环境变量，
  不接受继承环境中不一致的路径覆盖；复杂自定义部署请直接使用 CLI。
  `BOXPANEL_REPO` 可覆盖 fork，`BOXPANEL_PUBLIC_IP` 可传给 init，shell 不解析 DB、不生成凭证/证书/配置/防火墙规则。
- `/run/boxpanel-installer.lock` 互斥 install/update/uninstall；正常退出或信号清理锁与下载/staging 临时文件。
  SIGKILL/断电遗留锁需确认没有安装进程后手动删除。`/etc/boxpanel/.installing` 记录安装未完成；
  重跑 install 续接，已初始化的 DB 跳过 init；init 自身仍拒绝覆盖已有业务数据。
  重复成功 install 只显示 info 并提示 update，相同二进制 update 不重启，重复 uninstall 不报不存在错误。
- 卸载停止/禁用两个服务后调用 Go CLI 清理规则；清理失败则保留单元、二进制和数据供重试。
  缺少 CLI 而仍有受管文件时拒绝盲删，提示先恢复安装；仅处理两个受管单元和受管二进制。
  保留数据后重新安装复用账号/证书。若已有配置但内核二进制缺失，只允许恢复 DB 记录的同版本内核；
  不以重装名义升级已有配置。`singbox install --preserve-version` 自动选择原版本；该选项不能与 `--version` 同用。
- 新 CLI `system ready` 用安装证书验证本机 127.0.0.1 HTTPS 的认证接口（预期 401），不走公网地址/代理。
  检查有效期、证书 SAN 与信任链，不使用 insecure；单次至多 2 秒。脚本在最多 30 轮中检查
  systemd active 和 HTTPS，并在一秒后复核，避免把进程启动中视为已就绪。
- `--dry-run` 只校验参数/发行版/架构并打印操作计划，不要求 root/systemd，不联网、不调用业务 CLI、
  不安装依赖、不写文件。它不能证明真实 systemd、防火墙或客户端连通性。
  `scripts/install_test.sh` 通过临时目录和 uname/id/curl/systemctl/CLI 替身覆盖成功、重复、失败续接、
  更新回退、卸载确认/保留/清除、下载失败、坏/重复/缺失摘要、版本不符、链接/多条目包与平台拒绝；
  真正运行 tar/SHA-256，不需要 bats、root、网络或 systemd。`make test` 自动执行。

```bash
bash scripts/install.sh install --non-interactive --version v0.1.0-rc.1 --port 23456
bash scripts/install.sh update --non-interactive --version v0.1.0-rc.2
bash scripts/install.sh uninstall --non-interactive --yes          # 保留数据
bash scripts/install.sh uninstall --non-interactive --yes --purge  # 明确删除数据
bash scripts/install_test.sh                                      # macOS/Linux 离线测试
shellcheck scripts/*.sh
```

## 3. CLI（`boxpanel` 二进制）

| 命令 | 作用 |
|---|---|
| `boxpanel serve` | 启动面板（systemd 用） |
| `boxpanel serve --dev` | 开发模式：`web_path=""`，HTTP，监听 127.0.0.1:2053，默认 DB 在 `./dev.db`；首次自动初始化并打印随机账号密码，不调用 systemctl |
| `boxpanel init [--port N]` | 首次初始化（已初始化则拒绝） |
| `boxpanel info` | 打印面板地址、用户名（不打印密码） |
| `boxpanel reset-admin` | 重置管理员用户名与密码并打印 |
| `boxpanel set port <N>` / `set path <p>` | 修改面板端口/路径并重启面板 |
| `boxpanel singbox install [--version v]` | 安装 sing-box；未生成配置时可切换稳定版本，已有配置的升级留在 T7.6 |
| `boxpanel singbox status` | 版本、运行状态 |
| `boxpanel apply` | 重新生成并生效 sing-box 配置 |
| `boxpanel version` | |
| `boxpanel system firewall-open` | 读取 DB 面板端口并用既有防火墙适配器放行，记录可重放/清理的端口 |
| `boxpanel system firewall-cleanup` | 两个服务已停止时清理受管端口和跳跃规则；保留 DB/配置/恢复记录 |
| `boxpanel system bbr` | 复用 sysinfo.EnableBBR，不在 shell 写 sysctl |
| `boxpanel system ready` | 验证本机 HTTPS 认证接口已就绪，用于安装/更新存活检查 |

T6.1 的 `system` 子命令支持已有数据/证书/内核路径参数，仅在 Linux 下工作，无开发 mock 成功。
`firewall-open` 在触碰防火墙前提交 `settings.panel_firewall_ports`（整数数组、去重），
保证部分失败仍能卸载清理；不改 schema。只有已登记的安装实例才在生产 serve 启动时重放当前面板端口。
保留历史端口便于清理，API 热切换端口仍遵守 T3.2 的手动放行约定；重启后登记并放行当前端口。
`firewall-cleanup` 复用 Apply 的进程锁/flock、DB 快照及 Firewall.Sync，合并 applied_firewall、
未完成 `.pending` 的前后计划和 panel_firewall_ports，目标为空；运行中服务、损坏恢复记录或
检测失败时拒绝清理。不重启来恢复 pending，不写 sing-box JSON，不删除恢复材料；重装后 Apply 可恢复。

### T1.2 初始化、路径与启动约定

`init`、`info`、`reset-admin`、`serve` 均支持以下路径参数；优先级是显式参数 > 环境变量 > 默认值。
不要求 root，只需目录可写且监听端口可用。

| 参数 | 环境变量 | 默认值 |
|---|---|---|
| `--data-dir` | `BOXPANEL_DATA_DIR` | `/etc/boxpanel` |
| `--db-path` | `BOXPANEL_DB_PATH` | `<data-dir>/boxpanel.db` |
| `--cert-file` | `BOXPANEL_CERT_FILE` | `<data-dir>/certs/panel.crt` |
| `--key-file` | `BOXPANEL_KEY_FILE` | `<data-dir>/certs/panel.key` |

- `init --port N` 指定端口；省略时用 `crypto/rand` 在 10000–60000 中选择当前可绑定的端口。
  检测后立即释放端口，不保证后续启动时仍空闲；占用时 `serve` 报错并提示更换 `--port`。
- `init --public-ip IP` / `BOXPANEL_PUBLIC_IP` 跳过网络探测，支持 IPv4/IPv6；本机验证用 `127.0.0.1`。
  未指定时按 CONFIG.md §6 探测，失败不猜测地址，提示显式传入 IP。
- 初始化创建随机 12 位 web_path/sub_path、`admin-` 加 8 位随机用户名、24 字节随机密码
  （base64url），仅存 Argon2id 哈希。账号、默认设置在同一 DB 事务提交；已有任何业务数据则拒绝，
  不覆盖已有账号、设置或证书。正常失败回滚 DB 并清理本次新建证书；中断遗留的证书需保留备份，
  再用新的 `--cert-file` / `--key-file` 路径重试。
- 自签证书使用 ECDSA P-256，有效期一年，SAN 包含指定或探测到的 IP。新目录权限为 `0700`，
  DB、证书与私钥为 `0600`。仅持久化业务设置，覆盖的文件路径由每次命令参数/环境变量提供。
- 普通 `serve` 必须先初始化，从 DB 读取 `web_port`、`web_path`、`public_host`、`panel_tls_mode`；
  默认在所有接口提供 HTTPS（TLS 1.2+）。支持 `--listen-host IP` / `BOXPANEL_LISTEN_HOST`
  限制监听地址。无有效已保存材料且原证书文件缺失时拒绝启动；原证书过期或 SAN 不匹配时生成新的自签备用材料（T6.3），不自动降级 HTTP。
- T0.2 的 `serve --port N --web-path p` 保留为本次启动覆盖项，不写回 DB。
  `--web-path` 接受不带斜杠的 `[a-z0-9]+` 或空字符串，端口范围 1–65535。
- `--dev` 固定 HTTP、`127.0.0.1:2053`、空路径，覆盖监听地址、端口和路径选项。
  无数据目录参数/环境变量时使用 `./dev.db`；显式指定数据目录时用其中的 `boxpanel.db`。
  `--db-path` 仍有最高优先级。`init/info/reset-admin --dev` 使用同一套开发路径规则。
  开发初始化不探测外网、不生成证书；自动初始化只在空库发生，重启不再输出凭证。
  开发模式仍保留完整认证、限流和 CSRF，只取消 cookie 的 Secure 属性。
  T3.1 起入站/用户 API 使用明确标注的开发替身：仍调用 Manager.Apply，但仅提交 DB，
  不生成/check/生效配置或创建证书；成功写响应带 `X-Boxpanel-Apply: dev-db-only`。
  restart 与 CLI apply 仍不支持开发环境，详细约定见 API.md。
- `info` 不打印密码；`reset-admin` 原子更新账号密码并撤销该管理员的全部会话。
  `info/reset-admin` 及普通 `serve` 不会创建不存在的 DB。
- T1.2 不生成 systemd 单元、不调用 systemctl、防火墙或 sing-box；systemd 与 sing-box
  封装及 dev mock 随 T2.1 接入。T1.3 增加系统诊断 API 和防火墙适配包，但启动时不修改防火墙；
  节点端口同步在 T2.3、安装时面板端口放行在 T6.1 接入。
- 各命令支持 `--help`。Ctrl+C / SIGTERM 关闭监听并等待在途请求（最多 5 秒）。

本机无需 root 的 HTTPS 验证：

```bash
DATA_DIR=$(mktemp -d)
bin/boxpanel init --data-dir "$DATA_DIR" --public-ip 127.0.0.1
bin/boxpanel info --data-dir "$DATA_DIR"
bin/boxpanel serve --data-dir "$DATA_DIR" --listen-host 127.0.0.1
# 另一个终端用 init 输出的地址/凭证请求 /api/v1/auth/login。
# curl --cacert "$DATA_DIR/certs/panel.crt" 可信任本次自签证书。
```

- `make dev-server` 先执行 `build-web`，确保全新检出也有可嵌入的页面，再启动 `serve --dev`。可同时运行 `make dev-web` 使用 Vite 热更新及 `/api` 代理。

### T1.3 自检运行环境

- macOS / `--dev` 下不会读取 Linux `/proc`、`/sys` 或执行 Linux 系统命令；系统 API 的
  占位值与 409 返回约定见 API.md。防火墙包在这些环境为 no-op。
- Linux 生产环境的端口检查需要 `iproute2`（`ss`）；完整虚拟化识别使用
  `systemd-detect-virt`，可选 `kmod` 的 modinfo/modprobe 用于 BBR 模块检测/加载。
  防火墙检查和 BBR 开启需相应 root/capability 权限；T1.3 不自动安装依赖。
- 单测不要求这些工具；所有命令、系统文件、磁盘信息和网络探测使用注入数据。

## 4. systemd 单元

T2.1 由 `internal/singbox.GenerateUnits` 生成以下两个单元。
`boxpanel singbox install` 写入单元并执行 `daemon-reload`，**不 enable/start/restart**；
启用服务属于 T6.1，配置生成与首次生效属于 T2.2/T2.3。
实际模板会为 ExecStart 中的绝对路径加引号，转义引号、反斜杠、`%`、`$`，
并显式传入 DB、证书和 sing-box 路径，保证重启后仍使用安装时的路径覆盖。
下方是默认路径下的简写示例。

`boxpanel.service`：

```ini
[Unit]
Description=boxpanel web panel
After=network-online.target
Wants=network-online.target

[Service]
ExecStart=/usr/local/bin/boxpanel serve
Restart=on-failure
RestartSec=3
LimitNOFILE=65535

[Install]
WantedBy=multi-user.target
```

`boxpanel-singbox.service`：

```ini
[Unit]
Description=sing-box managed by boxpanel
After=network-online.target
Wants=network-online.target

[Service]
ExecStart=/usr/local/share/boxpanel/sing-box run -c /etc/boxpanel/sing-box.json
Restart=on-failure
RestartSec=3
LimitNOFILE=1048576
CapabilityBoundingSet=CAP_NET_ADMIN CAP_NET_BIND_SERVICE CAP_NET_RAW
AmbientCapabilities=CAP_NET_ADMIN CAP_NET_BIND_SERVICE CAP_NET_RAW

[Install]
WantedBy=multi-user.target
```

服务名带前缀，避免与用户已装的 `sing-box.service` 冲突；安装时若检测到 443 被已有 sing-box/xray/nginx 占用，引导自检中给出警告。

### T2.1 安装、校验与开发工具

- 锁定版本 **1.14.4**，官方 `published_at` 为 **2026-10-24T01:04:25Z**。
  2026-11-09 查证 [最新稳定 Release](https://github.com/SagerNet/sing-box/releases/tag/v1.14.4)
  及 [Release API](https://api.github.com/repos/SagerNet/sing-box/releases/tags/v1.14.4)：
  非 draft/prerelease，已发布超过 7 天。更新目标版本只改 `internal/singbox/version.go`，
  同步此处的查证记录；运行时不会自动跟随 latest。
- 该 Release 的 171 个资产中**没有独立 checksums/SHA-256 文件**，资产 API 的 `digest`
  提供 SHA-256。安装器核对该摘要，并检查 tar 结构与解包后二进制的 `sing-box version`。
  其他显式指定版本如提供校验文件则必须校验（支持 `SHA256  filename` / `SHA256 *filename`
  格式）；校验文件存在但缺项、损坏或格式不支持时失败，不降级跳过。
  T6.1 收紧：未提供校验文件/摘要时拒绝下载安装；结构与版本检查不能替代 SHA-256。
- 仅从官方固定仓库的 HTTPS Release API 和资产 URL 下载，校验实际 tag、发布时间和稳定状态；
  `--version` 接受 `1.14.4` 或 `v1.14.4`，拒绝 alpha/beta/rc、路径片段和发布未满 7 天的下载。
  Linux 生产支持 amd64/arm64，开发工具另支持 darwin/amd64、darwin/arm64；
  `x86_64`、`aarch64` 分别映射为 amd64、arm64。
- 下载总窗口 3 分钟，单 HTTP 请求至多 2 分钟；元数据 2 MiB、校验文件 1 MiB、
  压缩包 128 MiB、展开总量 256 MiB，最多 64 个 tar 条目。
  拒绝目录穿越、绝对路径、重复条目、链接及设备文件，必须有非空的
  `sing-box-{version}-{os}-{arch}/sing-box` 普通文件；只提取该二进制。
  在目标目录内 staging，校验 gzip CRC、摘要及版本后原子 rename，失败保留原二进制并清理临时文件。
- 生产安装需要先用同一套路径执行 `boxpanel init`，确认 Linux/systemd 与写入权限。
  相同版本重复安装会验证现有二进制，补写单元并 reload，不重复下载。
  安装锁在二进制目录 `.boxpanel-singbox-install.lock`；中断遗留时，确认没有安装进程后可删除锁重试。
  单元写入/reload/DB 记录失败会明确报错；此时二进制可能已经安装，重试可补齐剩余步骤。
  DB 的 `singbox_version` 仅在这些步骤成功后更新，status 始终从二进制和 systemd 读取实际状态。
- T2.1 不执行现有配置的升级验证与回滚；需要替换二进制时，运行中的服务或已有配置会使安装被拒绝。
  因此 `--version` 的版本切换仅适用于尚未配置的实例；已有配置的升级流程属于 T7.6。
  T6.1 增加卸载保留数据后的恢复例外：服务已停止且内核缺失时，可重新下载 DB 记录的相同版本；
  不改变配置，不允许此路径更换版本，之后仍须经 apply 校验/重启。
  不写入任何 sing-box JSON，不修改防火墙，不接入 `/singbox/*` 管理 API。
- systemctl 通过 `host.Runner` 的参数数组执行，每条命令最多 5 秒、输出最多 1 MiB；
  控制接口只接受 `boxpanel.service` 和 `boxpanel-singbox.service`。
  状态使用 `systemctl show` 的 LoadState/ActiveState/ActiveEnterTimestampMonotonic，
  运行时长结合 `/proc/uptime` 计算；未安装/未运行与检查失败分别处理，不将权限错误伪装为停止。
- `--dev` 的 install/status 为明确标注的 mock，不下载、不写文件/DB、不执行命令。
  非 Linux 的 status 返回同样占位状态，非 Linux 的服务安装报错并提示使用 `make tools`。
  `serve --dev` 和 macOS 自检仍不读取 Linux 系统数据。

路径优先级：显式参数 > 环境变量 > 默认值。`singbox install|status` 复用 §3 的数据路径参数；
`serve` 同样支持下表的前两项。所有写入路径不得互相重合或覆盖生成的服务单元。

| 参数 | 环境变量 | 默认值 |
|---|---|---|
| `--singbox-path` | `BOXPANEL_SINGBOX_PATH` | `/usr/local/share/boxpanel/sing-box` |
| `--singbox-config` | `BOXPANEL_SINGBOX_CONFIG` | `<data-dir>/sing-box.json`（不随独立 `--db-path` 移动） |
| `--unit-dir`（install） | `BOXPANEL_UNIT_DIR` | `/etc/systemd/system` |
| `--panel-binary`（install） | `BOXPANEL_BINARY` | `/usr/local/bin/boxpanel` |

非默认 unit-dir 需位于 systemd 的单元搜索路径；临时目录主要用于注入控制器的测试。

```bash
make tools                    # 下载锁定的宿主 OS/架构版本到 .tools/bin/sing-box
.tools/bin/sing-box version
make test                     # 工具存在时自动把 .tools/bin 加到 Go 测试的 PATH
```

`.tools/` 已忽略，不进入版本库；`make test` 本身不下载工具。
下载器、时钟、二进制命令、systemctl 和单元写入都可注入，单测只用假响应/假命令与临时文件，
不访问网络、不执行 systemctl。`make tools` 使用同一个安装器，但不访问 DB、配置或服务单元。

### T2.3 生效与恢复

`boxpanel apply` 复用数据路径参数及 `--singbox-path` / `--singbox-config`，优先级与 serve 一致。
必须先 init 和 singbox install，生产环境在 Linux/systemd 下运行；成功只在 check、重启存活与
防火墙同步全部完成后打印。macOS / `--dev` 返回失败说明；本机的真实 check 集成测试使用
`.tools/bin/sing-box`，不会调用真实 systemctl 或防火墙。`serve` 启动时重用同一生效路径。

配置目录必须已存在且可写，配置、`.tmp/.bak/.pending/.lock` 不得覆盖 DB、WAL/SHM、面板证书、
受管二进制或服务单元。Hysteria2 材料相对配置目录保存，重启无需额外路径参数。
`sing-box.json.lock` 不删除；`.pending` 由生效流程自动恢复/清理，不手工删除。
若提示自动恢复失败，检查 `.bak`、受管服务 journal 和面板专用防火墙表/链，修复权限/端口后
再次执行相同路径参数的 `boxpanel apply`。具体事务和恢复记录语义见 SPEC.md §7。

### T3.2 面板监听与订阅

`serve` 在 SPA 之前挂载独立的订阅 handler。管理 API 修改面板端口/路径时预先检查并绑定新监听，
持久化成功后 2 秒切换，原监听优雅关闭；进程、数据库连接与订阅限速状态保留。
新端口须在本机防火墙及云安全组放行；T6.3 的 acme 模式在 public_host 修改后自动重新签发。
`serve --dev` 仍固定 127.0.0.1:2053/根路径；开发创建的 Hysteria2 无生效证书，导出返回明确的 409。
这些接口不实现 CLI `set`、安装脚本、ACME、引导流程或内核升级。

### T3.3 本机离线回环测试

`make test` 默认执行 `internal/e2e`，仅当仓库 `.tools/bin/sing-box` 不存在或显式
`go test -short` 时跳过。工具存在但不可执行、版本不符、启动失败或请求失败均为测试失败，
不会自动下载工具或以 Skip 掩盖错误。版本仍以 `internal/singbox/version.go` 为准。

```bash
make tools                              # 首次准备工具时需要联网，测试本身完全离线
go test -count=1 -v ./internal/e2e       # 只跑两协议回环及错误 Reality 公钥反例
make lint
make test
make build
```

Linux/macOS 上均只需普通用户和回环 TCP/UDP 监听权限，不创建 TUN，不调用 systemd 或防火墙。
配置、DB、证书、日志和缓存全部使用临时目录，测试退出自动清理；失败日志脱敏后输出。
配置改写范围见 CONFIG.md 的 T3.3 说明。

macOS 受限沙箱可能禁止锁定内核注册系统 DNS 配置通知，报
`watch DNS configuration: register DNS configuration notification: status 9`；此时内核尚未启动代理。
应在允许该系统通知的普通终端/CI 环境复跑上述命令，不能将此环境错误当作协议测试通过，
也不为沙箱修改生产 DNS 或替换锁定内核。

## 5. 发布（`.github/workflows/release.yml`）

- 触发：推送 `v*` tag。
- 步骤：pnpm 构建前端 → `CGO_ENABLED=0` 交叉编译 `linux/amd64`、`linux/arm64`（`-trimpath -ldflags "-s -w -X main.version=<tag>"`）→ 打包 `boxpanel-linux-{arch}.tar.gz` → 生成 `checksums.txt` → 创建 GitHub Release 并上传。
- `ci.yml`：PR 与 main 推送时跑 `make lint test build`、`shellcheck scripts/install.sh`；安装 sing-box 锁定版本以便 golden test 跑 `sing-box check`。

T6.2 实现：CI 在 Linux 与 macOS 上使用 Go 1.27、Node 24 和 `web/package.json` 固定的 pnpm，
执行 `make tools lint test build` 及 `bin/boxpanel version`；Linux 强制运行 ShellCheck。
`make lint` 在本地工具存在时检查所有 `scripts/*.sh`，缺失时明确提示由 CI 强制检查。
CI 另用 `ubuntu:24.04` 容器执行 install dry-run、完整离线脚本测试以及真实 Linux 二进制 init/info。
容器步骤不启动 systemd，不代替干净 VPS 的端到端验收。

release.yml 在 Ubuntu 24.04 上先 lint/test/构建前端，再调用 `scripts/build-release.sh`；
`RELEASE_TAG` 先校验后用于 ldflags，保持 CGO_ENABLED=0。两个压缩包均恰含一个普通文件 `boxpanel`，
单独生成 `checksums.txt`；上传前校验摘要、tar 清单和 amd64 的版本输出，并执行 Ubuntu 容器 dry-run。
采用 `gh release create --verify-tag --draft` → 上传三个资产 → 发布，避免 latest 指向未上传完整的 Release；
失败可续接 draft，已发布 Release 不覆盖资产。带 `-` 的 tag 标为 prerelease 且不成为 latest。
同一 tag 串行执行，只有发布 job 有 contents:write，不需要额外 PAT。没有新增前端/Go 项目依赖。
CLI 参数依据 [gh release create](https://cli.github.com/manual/gh_release_create) /
[edit](https://cli.github.com/manual/gh_release_edit)。

2026-11-09 经官方 Release API 核对，以下均为非 draft/prerelease、发布超过 7 天的稳定版本，
CI/release 都锁定其完整 commit SHA（详见工作流注释）：

| Action | 版本 | published_at（UTC） |
|---|---|---|
| [actions/checkout](https://github.com/actions/checkout/releases/tag/v7.0.1) | v7.0.1 | 2026-07-20T15:10:05Z |
| [actions/setup-go](https://github.com/actions/setup-go/releases/tag/v7.0.0) | v7.0.0 | 2026-07-16T02:50:50Z |
| [actions/setup-node](https://github.com/actions/setup-node/releases/tag/v7.1.0) | v7.1.0 | 2026-10-08T02:52:34Z |
| [pnpm/action-setup](https://github.com/pnpm/action-setup/releases/tag/v6.1.0) | v6.1.0 | 2026-09-05T07:31:28Z |

原 CI 的 pnpm/action-setup v6.1.1 发布于 2026-11-03，尚未满 7 天，因此使用 v6.1.0；
`web/package.json`、锁文件与 pnpm 冷静期策略均不修改。


## 6. T6.3 ACME 安装与运行

- 新 `init` 默认策略是 acme，同时准备原有自签备用证书；不在 init 中调用 CA。
  安装器在初始化后启动 serve，由服务后台自动申请，HTTPS 及 `system ready` 不等待 CA。
  `info` 和安装完成输出显示当前实际证书类型及已记录的失败提示；未完成时明确显示自签备用。
  原自签安装升级后不强制切换，可在设置点击“申请 / 重新申请可信证书”。
- `serve --acme-directory URL` / `BOXPANEL_ACME_DIRECTORY` 指定 CA，默认
  `https://acme-v02.api.letsencrypt.org/directory`。测试部署请使用
  `https://acme-staging-v02.api.letsencrypt.org/directory`，此 CA 的证书不受客户端信任。
  参数优先于环境变量。该覆盖不是 DB 设置；`singbox install --acme-directory URL` 将值写入
  panel systemd 单元的 serve 参数；安装脚本保留环境变量并通过该流程传递，更新/重启继续使用同一值。
  单元新增显式 `--data-dir`，即使独立覆盖 DB 或原自签文件位置，ACME 材料仍在 `<data-dir>/certs/`。
- 仅允许 HTTPS directory；离线测试额外允许回环 IP 的 HTTP 假服务器，无 insecure 选项。
  `serve --dev` 始终不签发、不生成证书、不开放端口，证书 API 返回开发状态及可操作的 409。
- 请持续在云安全组放行 TCP 80（包括域名使用的 IPv4/IPv6），域名 A/AAAA 应指向本机并关闭 CDN。
  本机通过现有防火墙适配器放行并登记端口；80 有其他服务占用时不会强行关闭该服务，
  申请立即失败并给出提示。:80 仅在签发/续期期间监听，其余时间 TCP 自连失败是正常现象。
- 单次申请最多 2 分钟；首次失败回退自签，续期失败保留有效可信证书。按实际寿命一半续期，
  5 分钟起指数退避至最多 6 小时；证书热替换不重启面板，不改变 Hysteria2 自签/指纹。
  设置改回 self_signed 后停止续期；原自签文件及历史 bundle/账号均保留。
- `system ready` 读取 `panel_certificate` 指向的当前 PEM bundle（旧实例无状态则使用原 cert-file），
  仍只连接 127.0.0.1，不走外部网络/代理，使用安装材料作为本机信任锚并校验 SAN、有效期及 TLS 链，
  不用 insecure；因此能检查自签、正式或 staging 证书，**就绪成功不等于 staging 获得公开信任**。
- Go 最低版本为 1.26（x/crypto v0.58.0 profiles 支持的要求），开发/CI 使用 Go 1.27。
  依赖理由和证书存储/恢复细节见 SPEC.md §13。单测假 CA/假接口；本任务不调用 LE 正式签发。
