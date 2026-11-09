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
当前完成 T0.1 仓库骨架；CLI 仅支持 `version` 和帮助，前端为占位页。

```bash
pnpm --dir web install --frozen-lockfile
make dev-web      # 查看前端占位页
make lint
make test
make build
bin/boxpanel version
```

`make build` 先构建前端，再以 `CGO_ENABLED=0` 编译到 `bin/boxpanel`。
默认版本为 `dev`，可用 `make build VERSION=v0.1.0` 覆盖。
`web/dist/` 的构建产物不提交；保留 `.gitkeep`，全新检出也能编译 embed 包。

`make dev-server` 已预留为 `go run ./cmd/boxpanel serve --dev`，目前会报命令不支持。
最小 `serve` 和 history 路由在 T0.2 实现，完整开发模式在 T1.2 实现。
Vite 已预设 `/api` 代理到 `http://127.0.0.1:2053`，供后续后端使用。

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
