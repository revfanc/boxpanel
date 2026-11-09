# boxpanel

基于 [sing-box](https://github.com/SagerNet/sing-box) 的一键部署工具与 Web 管理面板：

- **一条命令安装**，自动配置 systemd、防火墙、BBR
- **小白引导**：环境自检 → 选场景 → 自动生成节点 → 放行端口 → 扫码导入
- **默认协议**：VLESS + Reality（无需域名）、Hysteria2
- **活跃规则**：远程 rule-set 自动更新，预设「绕过大陆 / 仅 GFW / 全局」
- **一个订阅链接**：按客户端自动返回 sing-box / mihomo(Clash Meta) / 通用 base64 格式

> 🚧 开发中。规格见 [docs/](docs/)，开发约定见 [AGENTS.md](AGENTS.md)。

## 安装（发布后可用）

```bash
bash <(curl -fsSL https://raw.githubusercontent.com/revfanc/boxpanel/main/scripts/install.sh)
```

## 开发

需要 Go 1.23+、Node 24+、pnpm（`web/package.json` 固定版本，当前为 12.18.0）。
当前完成 T1.2：SQLite 初始化、HTTPS、管理员认证与会话；前端仍提供概览、用户列表、用户详情三个占位页，登录页在 T4.1 实现。

```bash
pnpm --dir web install --frozen-lockfile
make dev-server   # HTTP 127.0.0.1:2053，首次创建 ./dev.db 并显示随机账号密码
make dev-web      # 在另一个终端启动 Vite，/api 代理到后端
make lint
make test
make build
bin/boxpanel version
DATA_DIR=$(mktemp -d)
bin/boxpanel init --data-dir "$DATA_DIR" --public-ip 127.0.0.1
bin/boxpanel info --data-dir "$DATA_DIR"
bin/boxpanel serve --data-dir "$DATA_DIR" --listen-host 127.0.0.1
```

`make build` 先构建前端，再以 `CGO_ENABLED=0` 编译到 `bin/boxpanel`。
默认版本为 `dev`，可用 `make build VERSION=v0.1.0` 覆盖。
`web/dist/` 的构建产物不提交；保留 `.gitkeep`，全新检出也能编译 embed 包。

普通 `serve` 从 DB 读取随机端口/路径并提供 HTTPS；在 `info` 显示的地址后加 `users/3` 可验证深链接。
`--data-dir` / `BOXPANEL_DATA_DIR` 可覆盖 `/etc/boxpanel`，也可单独覆盖 DB、证书与私钥路径，
本机无需 root，参数见 [INSTALL.md](docs/INSTALL.md#3-cli-boxpanel-二进制)。
`info` 不显示密码；忘记凭证可使用相同数据目录执行 `reset-admin`，旧会话立即失效。
认证接口见 [API.md](docs/API.md#认证)，用 curl 的 cookie jar 可完成登录、me 与登出；
本机 HTTPS 请求可用 `--cacert "$DATA_DIR/certs/panel.crt"` 验证自签证书。
`make dev-server` 会先构建嵌入页面，开发模式不探测外网或调用 systemctl。

T1.2 新增 `golang.org/x/crypto v0.41.0` 实现 Argon2id，保持 Go 1.23 最低版本与纯 Go 构建；
密码哈希参数和限流约定见 [SPEC.md](docs/SPEC.md#t12-认证实现约定)。

T0.2 新增 `react-router` 依赖，用于规格要求的 `createBrowserRouter` history 路由、
运行时 basename 和站内 `Link` 导航；版本由 `web/package.json` 与 lockfile 固定。

前端使用 TypeScript 7 稳定编译器（包别名 `typescript-compiler`）执行严格类型检查；
`typescript` 6.0.3 仅作为 ESLint 的兼容依赖，因为当前稳定版 `typescript-eslint`
依赖 TypeScript 的 JavaScript API，支持范围为 `>=4.8.4 <6.1.0`。
两者通过 lockfile 固定，不会让类型检查退回旧编译器。

shadcn/ui 通过以下非交互 CLI 初始化，生成的 UI 组件保持原样：

```bash
pnpm dlx shadcn@4.23.0 init --cwd web --template vite --base radix --preset nova --yes --no-monorepo --no-rtl card
```

## 免责声明

本项目仅供学习与合法用途，请遵守你所在地区的法律法规。

## 许可证

[GPL-3.0](LICENSE)
