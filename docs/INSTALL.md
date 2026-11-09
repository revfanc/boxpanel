# 一键安装 / CLI / 发布规格

## 1. 用户视角

```bash
bash <(curl -fsSL https://raw.githubusercontent.com/revfanc/boxpanel/main/scripts/install.sh)
```

> 仓库地址在脚本顶部定义为变量 `REPO="${BOXPANEL_REPO:-revfanc/boxpanel}"`，可通过环境变量覆盖（便于 fork 测试）。

安装结束打印（只打印这一次，之后用 `boxpanel info` 查看）：

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
7. 写入并启用 systemd 单元（由 `boxpanel init` 生成，见 §4），开放面板端口防火墙。
8. 询问是否开启 BBR（非交互模式默认开启），输出 §1 的信息。

update：下载新版 boxpanel → 校验 → 替换 → `systemctl restart boxpanel`（不动 DB 与 sing-box）。

uninstall：二次确认 → 停服务、删单元 → 清防火墙规则与端口跳跃规则 → 询问是否保留 `/etc/boxpanel`（默认保留，提示路径）。

脚本要求：幂等（重复执行 install 等于检测到已安装后提示改用 update）；`shellcheck` 无警告；不依赖 bash 4 以外的特性。

## 3. CLI（`boxpanel` 二进制）

| 命令 | 作用 |
|---|---|
| `boxpanel serve` | 启动面板（systemd 用） |
| `boxpanel serve --dev` | 开发模式：`web_path=""`，HTTP，监听 127.0.0.1:2053，DB 在 `./dev.db`，不调用 systemctl（sing-box 管理走 mock） |
| `boxpanel init [--port N]` | 首次初始化（已初始化则拒绝） |
| `boxpanel info` | 打印面板地址、用户名（不打印密码） |
| `boxpanel reset-admin` | 重置管理员用户名与密码并打印 |
| `boxpanel set port <N>` / `set path <p>` | 修改面板端口/路径并重启面板 |
| `boxpanel singbox install [--version v]` | 安装/切换 sing-box 版本 |
| `boxpanel singbox status` | 版本、运行状态 |
| `boxpanel apply` | 重新生成并生效 sing-box 配置 |
| `boxpanel version` | |

T0.2 临时启动参数（T1.2 接入持久化设置与 HTTPS 前）：

```bash
boxpanel serve --web-path k3m9x2p7q1ab --port 2053
boxpanel serve --dev
```

- 仅 HTTP，绑定 `127.0.0.1`；默认 `--port 2053 --web-path boxpanel`，端口范围 1–65535。
- `--web-path` 接受不带斜杠的 `[a-z0-9]+` 或空字符串。
- `--dev` 固定使用 HTTP、`127.0.0.1:2053`、空 `web_path`，覆盖上述两个参数；此阶段不创建 DB、不调用 systemctl。
- `make dev-server` 先执行 `build-web`，确保全新检出也有可嵌入的页面，再启动 `serve --dev`。可同时运行 `make dev-web` 使用 Vite 热更新及 `/api` 代理。
- `serve --help` 显示参数；Ctrl+C / SIGTERM 关闭监听并等待在途请求（最多 5 秒）。

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
