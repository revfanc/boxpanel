# boxpanel

一条命令在 VPS 上部署 [sing-box](https://github.com/SagerNet/sing-box) 代理服务端，并附带 Web 管理面板：跟着引导点几下就能建好节点，最后得到一个可以导入客户端的订阅链接。

[![CI](https://github.com/revfanc/boxpanel/actions/workflows/ci.yml/badge.svg)](https://github.com/revfanc/boxpanel/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/revfanc/boxpanel)](https://github.com/revfanc/boxpanel/releases/latest)
[![License](https://img.shields.io/github/license/revfanc/boxpanel)](LICENSE)

## 功能

- **一键安装**：自动下载并校验 sing-box，配置 systemd、本机防火墙，可选开启 BBR；重复执行是安全的。
- **小白引导**：环境自检 → 选择场景（有无域名）→ 自动生成节点 → 创建用户 → 放行端口 → 扫码导入，共 7 步。
- **无需域名**：默认协议 VLESS + Reality，可选 Hysteria2（网络不稳定时更快）。
- **可信证书**：自动申请 Let's Encrypt 证书（支持纯 IP 和域名）并自动续期，客户端拉订阅不再因自签证书失败；申请失败时自动回退自签证书，不影响使用。
- **一个订阅链接**：按客户端自动返回 sing-box / Clash（mihomo）/ 通用 base64 格式；浏览器打开则显示带二维码和一键导入的订阅页。
- **持续更新的分流规则**：远程规则集自动更新，预设「绕过大陆 / 仅 GFW / 全局」，支持自定义直连和代理域名。
- **防 DNS 泄漏**：境外域名交给服务端解析，DNS 查询不从本地网络发出（已用 ipleak.net 实测）。
- **简单的节点菜单**：客户端里只有一个「节点选择」列表，点哪个节点就用哪个。

## 安装

**系统要求**：Debian 11+、Ubuntu 20.04+、CentOS Stream 9+、Rocky / AlmaLinux 8+，amd64 或 arm64，systemd，root 权限。

```bash
bash <(curl -fsSL https://raw.githubusercontent.com/revfanc/boxpanel/main/scripts/install.sh)
```

安装结束会打印**面板地址、用户名和密码**（密码只显示这一次，请保存）。用浏览器打开面板地址，按引导操作即可。

**云服务商安全组需要放行以下端口**（本机防火墙由安装脚本处理，安全组要在云服务商控制台手动添加）：

| 端口 | 用途 |
|---|---|
| TCP 面板端口 | 安装时随机生成，见安装输出 |
| TCP 80 | 申请和续期 Let's Encrypt 证书 |
| 节点端口 | 引导「放行端口」一步会列出，如 Reality 的 TCP 443、Hysteria2 的 UDP 端口 |

## 推荐客户端

| 平台 | 客户端 | 订阅格式 |
|---|---|---|
| Windows / macOS / Linux | [Clash Verge Rev](https://github.com/clash-verge-rev/clash-verge-rev/releases) | Clash |
| Android | [sing-box](https://github.com/SagerNet/sing-box/releases) | sing-box |
| iOS | [sing-box MT](https://apps.apple.com/us/app/sing-box-mt/id6785326793) | sing-box |

其他基于 mihomo 内核的 Clash 类客户端也可以直接使用订阅链接。sing-box 客户端需要 1.14 或更新版本。

## 常用命令

在服务器上执行：

```bash
boxpanel info                          # 查看面板地址、用户名和证书状态
boxpanel reset-admin                   # 忘记密码时重置管理员账号
boxpanel set-host <IP或域名>           # 节点/面板地址不对时修改，会自动重新申请证书
```

升级到最新版或卸载：

```bash
bash <(curl -fsSL https://raw.githubusercontent.com/revfanc/boxpanel/main/scripts/install.sh) update
```

```bash
bash <(curl -fsSL https://raw.githubusercontent.com/revfanc/boxpanel/main/scripts/install.sh) uninstall
```

卸载默认保留 `/etc/boxpanel` 数据，加 `--purge` 才会删除。更多参数用 `--help` 查看，详见 [INSTALL.md](docs/INSTALL.md)。

## 常见问题

**面板打不开？**
先确认云服务商安全组已放行面板端口。若本机开着 Clash 等代理软件的 TUN 模式，可把服务器 IP 设为直连后再试。

**浏览器提示证书不安全？**
说明可信证书还没申请成功，通常是安全组没放行 TCP 80。放行后等一分钟左右，或到面板「设置 → 面板 HTTPS 证书」点「申请 / 重新申请可信证书」，那里也会显示失败原因。

**改成域名后旧地址打不开了？**
证书已切换到新域名，旧地址会因证书不匹配而无法访问。用 `boxpanel info` 查看新地址。

**节点能测速但网页打不开？**
在客户端里更新订阅。如仍有问题，确认云安全组放行了节点端口（Hysteria2 是 UDP）。

**安装时识别的公网 IP 不对？**
执行 `boxpanel set-host <正确的IP>`；或重新安装时设置环境变量 `BOXPANEL_PUBLIC_IP`。

## 开发

需要 Go 1.26+（CI 使用 1.27）、Node 24+ 和 pnpm（版本见 `web/package.json`）。

```bash
pnpm --dir web install --frozen-lockfile
make tools        # 下载锁定版本的 sing-box 到 .tools/bin，测试会用到
make dev-server   # 后端开发模式：HTTP 127.0.0.1:2053，首次启动显示随机账号
make dev-web      # 另开终端启动 Vite，/api 代理到后端
make lint test build
```

产物是单个静态二进制 `bin/boxpanel`（前端通过 `go:embed` 打包，`CGO_ENABLED=0`）。推送 `v*` 标签会由 GitHub Actions 构建 linux amd64/arm64 并发布 Release。

文档：

- [SPEC.md](docs/SPEC.md)：产品目标、架构、数据模型
- [CONFIG.md](docs/CONFIG.md)：sing-box 服务端配置与订阅生成
- [API.md](docs/API.md)：后端 REST API
- [FRONTEND.md](docs/FRONTEND.md)：面板路由与引导流程
- [INSTALL.md](docs/INSTALL.md)：安装脚本、CLI、systemd、发布
- [TASKS.md](docs/TASKS.md)：里程碑与验收记录
- [AGENTS.md](AGENTS.md)：开发约定

## 免责声明

本项目仅供学习与合法用途，请遵守你所在地区的法律法规。

## 许可证

[GPL-3.0](LICENSE)
