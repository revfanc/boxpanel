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
bash <(curl -fsSL https://raw.githubusercontent.com/OWNER/boxpanel/main/scripts/install.sh)
```

## 开发

需要 Go 1.23+、Node 20+、pnpm。

```bash
make dev-server   # 终端 1
make dev-web      # 终端 2
```

## 免责声明

本项目仅供学习与合法用途，请遵守你所在地区的法律法规。
