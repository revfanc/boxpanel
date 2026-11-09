# 一键安装 / CLI / 发布规格

## 1. 用户视角

```bash
bash <(curl -fsSL https://raw.githubusercontent.com/revfanc/boxpanel/main/scripts/install.sh)
```

> 仓库地址在脚本顶部定义为变量 `REPO="${BOXPANEL_REPO:-revfanc/boxpanel}"`，可通过环境变量覆盖（便于 fork 测试）。

安装结束打印（密码仅此一次；之后 `boxpanel info` 只显示地址和用户名，忘记密码用 `reset-admin`）：

```
✅ boxpanel 安装完成
面板地址: https://1.2.3.4:23456/k3m9x2p7q1ab/
用户名:   admin-7fq2
密码:     xxxxxxxxxxxxxxxx
提示: 浏览器提示"不安全"是因为使用了自签证书，点击"高级 → 继续访问"即可。
请确认云服务商安全组已放行 TCP 23456。
```

## 2. `scripts/install.sh` 行为

子命令：`install`（默认）、`update`、`uninstall`、`--non-interactive`、`--version <tag>`、`--port <n>`。

install 流程：

1. 必须 root；`set -euo pipefail`；所有步骤打印 `[1/8]` 形式的进度。
2. 检测系统：Debian 11+/Ubuntu 20.04+/CentOS Stream 9/Rocky/Alma 8+/Alpine(后期)；架构 `amd64|arm64`（其他直接报错并说明）；需要 systemd。
3. 安装依赖：`curl tar ca-certificates`，以及 `nftables` 或 `iptables`（端口跳跃用）。
4. 从 GitHub Release 下载 `boxpanel-linux-{arch}.tar.gz` 与 `checksums.txt`，**sha256 校验**后安装到 `/usr/local/bin/boxpanel`。
5. 调用 `boxpanel init --port <随机或指定>`：初始化 DB、生成 web_path/sub_path/管理员账号/自签证书、探测公网 IP。
6. 调用 `boxpanel singbox install`：下载锁定版本的 sing-box（`SagerNet/sing-box` release 中 `sing-box-{ver}-linux-{arch}.tar.gz`；若官方提供校验文件则校验，否则至少校验 tar 结构与 `sing-box version` 输出）。
7. 写入并启用 systemd 单元（模板生成在 T2.1 实现，见 §4），开放面板端口防火墙。
8. 询问是否开启 BBR（非交互模式默认开启），输出 §1 的信息。

update：下载新版 boxpanel → 校验 → 替换 → `systemctl restart boxpanel`（不动 DB 与 sing-box）。

uninstall：二次确认 → 停服务、删单元 → 清防火墙规则与端口跳跃规则 → 询问是否保留 `/etc/boxpanel`（默认保留，提示路径）。

脚本要求：幂等（重复执行 install 等于检测到已安装后提示改用 update）；`shellcheck` 无警告；不依赖 bash 4 以外的特性。

## 3. CLI（`boxpanel` 二进制）

| 命令 | 作用 |
|---|---|
| `boxpanel serve` | 启动面板（systemd 用） |
| `boxpanel serve --dev` | 开发模式：`web_path=""`，HTTP，监听 127.0.0.1:2053，默认 DB 在 `./dev.db`；首次自动初始化并打印随机账号密码，不调用 systemctl |
| `boxpanel init [--port N]` | 首次初始化（已初始化则拒绝） |
| `boxpanel info` | 打印面板地址、用户名（不打印密码） |
| `boxpanel reset-admin` | 重置管理员用户名与密码并打印 |
| `boxpanel set port <N>` / `set path <p>` | 修改面板端口/路径并重启面板 |
| `boxpanel singbox install [--version v]` | 安装/切换 sing-box 版本 |
| `boxpanel singbox status` | 版本、运行状态 |
| `boxpanel apply` | 重新生成并生效 sing-box 配置 |
| `boxpanel version` | |

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
  限制监听地址。证书缺失或无效即拒绝启动，不自动降级 HTTP。
- T0.2 的 `serve --port N --web-path p` 保留为本次启动覆盖项，不写回 DB。
  `--web-path` 接受不带斜杠的 `[a-z0-9]+` 或空字符串，端口范围 1–65535。
- `--dev` 固定 HTTP、`127.0.0.1:2053`、空路径，覆盖监听地址、端口和路径选项。
  无数据目录参数/环境变量时使用 `./dev.db`；显式指定数据目录时用其中的 `boxpanel.db`。
  `--db-path` 仍有最高优先级。`init/info/reset-admin --dev` 使用同一套开发路径规则。
  开发初始化不探测外网、不生成证书；自动初始化只在空库发生，重启不再输出凭证。
  开发模式仍保留完整认证、限流和 CSRF，只取消 cookie 的 Secure 属性。
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

## 5. 发布（`.github/workflows/release.yml`）

- 触发：推送 `v*` tag。
- 步骤：pnpm 构建前端 → `CGO_ENABLED=0` 交叉编译 `linux/amd64`、`linux/arm64`（`-trimpath -ldflags "-s -w -X main.version=<tag>"`）→ 打包 `boxpanel-linux-{arch}.tar.gz` → 生成 `checksums.txt` → 创建 GitHub Release 并上传。
- `ci.yml`：PR 与 main 推送时跑 `make lint test build`、`shellcheck scripts/install.sh`；安装 sing-box 锁定版本以便 golden test 跑 `sing-box check`。

当前 M0 阶段：CI 在 Linux 与 macOS 上使用 Go 1.27、Node 24 和
`web/package.json` 固定的 pnpm，执行 `make lint test build` 及 `bin/boxpanel version`。
sing-box 安装检查随 T2.1/T2.2 接入；`shellcheck` 随 T6.1 的 `scripts/install.sh`
接入。骨架阶段尚无这些文件，不提前实现后续任务。
