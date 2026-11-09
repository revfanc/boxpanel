# AGENTS.md — 给 Codex 的开发约定

本项目 **boxpanel**：一键部署 sing-box 服务端 + Web 管理面板（含小白引导）+ 多格式订阅导出。
动手前先读 `docs/` 下的规格文档，按 `docs/TASKS.md` 的里程碑顺序实现，**一次只做一个任务**。

## 必读文档

| 文档 | 内容 |
|---|---|
| `docs/SPEC.md` | 产品目标、架构、目录结构、数据模型 |
| `docs/CONFIG.md` | sing-box 服务端配置生成、客户端订阅（sing-box / mihomo / URI）生成、规则预设 |
| `docs/API.md` | 后端 REST API 契约 |
| `docs/FRONTEND.md` | 面板路由（history 模式）、页面、引导流程 |
| `docs/INSTALL.md` | 一键安装脚本、systemd、CLI |
| `docs/TASKS.md` | 里程碑任务与验收标准 |

规格与实现冲突时：**以规格为准**；规格本身有误或缺失时，在 PR 描述里说明并按最小合理方案实现，同时更新对应文档。

## 技术栈（不要擅自替换）

- 后端：Go 1.23+，标准库 `net/http`（1.22+ 路由模式），`modernc.org/sqlite`（纯 Go，**禁止 CGO**），`log/slog`
- 前端：Vite + React 19 + TypeScript（strict）+ Tailwind CSS v4 + shadcn/ui + React Router（`createBrowserRouter`，**history 模式**）+ TanStack Query + react-hook-form + zod
- 包管理：前端用 `pnpm`；Go 用 modules
- 前端产物通过 `//go:embed all:dist` 打进单个二进制（`web/embed.go`）

## 命令

```bash
make dev-web      # 前端开发服务器（代理 /api 到后端）
make dev-server   # 后端开发模式（web_path 为空，监听 127.0.0.1:2053）
make build        # 先构建前端，再构建 Go 二进制到 bin/boxpanel
make test         # go test ./... + 前端 typecheck/lint/vitest
make lint         # go vet + gofmt 检查 + eslint
```

每个任务完成前必须 `make test` 和 `make build` 通过。

## 硬性约定

1. **sing-box 配置先校验后生效**：写临时文件 → `sing-box check -c` → 原子替换 → 重启 → 检查存活，失败自动回滚。任何绕过这条路径写配置的代码都不接受。
2. **sing-box 版本集中管理**：目标版本写在 `internal/singbox/version.go` 的常量里；配置字段以 [sing-box 官方文档](https://sing-box.sagernet.org/) 对应版本为准，不确定的字段必须查文档，不要凭记忆。
3. **配置生成必须有 golden test**：`internal/confgen`、`internal/subscription` 的输出放 `testdata/*.golden`，`go test -update` 可更新。若 PATH 中有 `sing-box`，测试额外对生成的 JSON 跑 `sing-box check`，否则 `t.Skip`。
4. **密钥与凭证**：随机数一律用 `crypto/rand`；日志与 API 响应里不得出现私钥、密码、订阅 token 全文（预览接口要脱敏）。
5. **前端路由使用 history 模式**，basename 来自运行时注入的 `window.__BOXPANEL__.base`，详见 `docs/FRONTEND.md`。不要改成 hash 路由。
6. **UI 文案**集中在 `web/src/i18n/zh-CN.ts`，组件里不写硬编码中文，便于以后加英文。
7. shadcn/ui 组件用 CLI 生成到 `web/src/components/ui/`，不要手改生成的组件，需要定制就包一层。
8. Go 错误要带上下文（`fmt.Errorf("apply config: %w", err)`），HTTP 错误统一格式见 `docs/API.md`。
9. 不引入重量级框架（gin/echo/gorm 等）；新增依赖需在 PR 描述中说明理由。
   **禁止绕过供应链保护**：不得添加 `minimumReleaseAgeExclude`、不得降低 `minimumReleaseAge`，
   也不得用其他方式安装刚发布、尚未过 pnpm 冷静期的版本；需要新版本时选冷静期之外的最新版。
10. 所有面向小白的报错要给出「下一步怎么办」，而不只是错误原文。

## 提交规范

- Conventional Commits：`feat(confgen): add hysteria2 inbound`
- 一个任务一个 PR，PR 描述写明对应 `TASKS.md` 中的任务编号与验收项勾选情况。
