# 配置生成规格

> ⚠️ 以下 JSON 是**结构示意**。字段名以 `internal/singbox/version.go` 锁定版本的
> [官方文档](https://sing-box.sagernet.org/configuration/) 为准，实现时逐项核对，并用 `sing-box check` 验证。
> 服务端目标版本：sing-box **1.14.4**（新 DNS server 格式、rule action，不使用已移除的 geoip/geosite 字段与 block/dns 特殊出站）。
> T2.2 已逐字段核对 §1/§2，并用锁定二进制校验所有服务端 golden；客户端订阅仍按 §4 的独立版本约定，在 T3.2 实现。

## 1. 入站 options（存于 `inbounds.options`）

### vless_reality

```json
{
  "server_name": "www.example.com",
  "handshake_server": "www.example.com",
  "handshake_port": 443,
  "private_key": "<x25519 私钥 base64url>",
  "public_key": "<对应公钥>",
  "short_ids": ["<8~16 位 hex>"],
  "fingerprint": "chrome"
}
```

- 密钥用 Go `crypto/ecdh` X25519 生成；私钥输出前按 X25519 规则 clamp（最低 3 位清零，
  最高位清零、次高位置一）。私钥、公钥均为 32 字节、43 字符的**无 padding base64url**，
  与 `sing-box generate reality-keypair` 一致；测试实际调用该命令检查格式、位掩码和公私钥对应关系。
- `GenerateShortID` 生成 8 字节随机数，输出 16 位小写 hex；面板接受偶数长度的 8–16 位 hex。
  [TLS 文档](https://sing-box.sagernet.org/configuration/shared/tls/#short_id) 中 “zero to eight digits”
  表述有歧义：1.14.4 实际接受至多 **8 字节**（16 位 hex），已用二进制校验 8/16 位 short ID。
  `short_ids` 是面板存储字段，服务端字段为 `tls.reality.short_id` 数组；`public_key` 和
  `fingerprint` 仅供客户端使用，不输出到服务端配置。
- 伪装目标默认从内置候选列表中自动挑选，见 §5。

### hysteria2

```json
{
  "sni": "<证书 CN/SAN，默认 www.bing.com 或用户域名>",
  "cert_mode": "self_signed",          // self_signed | acme(后期)
  "obfs_password": "",                 // 空=不启用 salamander
  "up_mbps": 0, "down_mbps": 0,        // 0=省略该方向的上限；两者均为 0 时强制 BBR
  "port_hopping": { "enabled": false, "start": 20000, "end": 30000 }
}
```

- 自签证书默认在 `certs/hy2.{crt,key}`；`certs.Hysteria2(sni, now)` 只返回内存中的 PEM 证书、
  PKCS#8 私钥和指纹，不写文件。使用 ECDSA P-256，CN/SAN 为传入的 DNS 名或 IP，
  有效期为 `now-5分钟` 至 `now+1年`，仅用于 serverAuth，不是 CA。
  SHA-256 指纹为**叶证书 DER** 的 64 位小写 hex，不是 PEM 文本或 SPKI 公钥的哈希。
  后续调用方负责以 0600 保存证书/私钥并记录指纹；多入站可传各自证书路径，不新增 DB 字段。
- 依据 [Hysteria2 入站文档](https://sing-box.sagernet.org/configuration/inbound/hysteria2/)，
  两个带宽值均为 0 时省略 `up_mbps/down_mbps` 并设置 `ignore_client_bandwidth=true`，
  强制客户端使用 BBR；任一值为正时输出正值并省略 `ignore_client_bandwidth`。
  因此不能仅靠“不填带宽”表达“强制 BBR”。非空 `obfs_password` 映射到
  `obfs:{type:"salamander",password:...}`，为空则省略整个 `obfs`。
- 端口跳跃：面板用 nftables（无则 iptables）把 UDP `start-end` DNAT/REDIRECT 到 `port`，面板启动时重新下发，删除入站时清理。

## 2. 服务端配置（confgen）

```json
{
  "log": { "level": "warn", "timestamp": true },
  "dns": {
    "servers": [{ "type": "local", "tag": "dns-local" }],
    "final": "dns-local"
  },
  "inbounds": [
    {
      "type": "vless", "tag": "vless-reality-443",
      "listen": "::", "listen_port": 443,
      "users": [{ "name": "alice", "uuid": "<uuid>", "flow": "xtls-rprx-vision" }],
      "tls": {
        "enabled": true,
        "server_name": "www.example.com",
        "reality": {
          "enabled": true,
          "handshake": { "server": "www.example.com", "server_port": 443 },
          "private_key": "<private>",
          "short_id": ["<sid>"]
        }
      }
    },
    {
      "type": "hysteria2", "tag": "hy2-8443",
      "listen": "::", "listen_port": 8443,
      "users": [{ "name": "alice", "password": "<pwd>" }],
      "ignore_client_bandwidth": true,
      "tls": {
        "enabled": true, "alpn": ["h3"],
        "certificate_path": "/etc/boxpanel/certs/hy2.crt",
        "key_path": "/etc/boxpanel/certs/hy2.key"
      }
    }
  ],
  "outbounds": [{ "type": "direct", "tag": "direct" }],
  "route": {
    "rules": [
      { "action": "sniff" },
      { "protocol": "bittorrent", "action": "reject" },
      { "action": "resolve", "server": "dns-local" },
      { "ip_is_private": true, "action": "reject" }
    ],
    "final": "direct",
    "default_domain_resolver": "dns-local"
  },
  "experimental": {
    "clash_api": { "external_controller": "127.0.0.1:<随机端口>", "secret": "<随机>" }
  }
}
```

- 用户列表仅包含「启用 && 未超额 && 未过期」的用户；`traffic_limit=0` 不限流量，
  否则 `traffic_up + traffic_down >= traffic_limit` 即剔除（计算避免 int64 溢出）；
  `expire_at=0` 永不到期，否则 `expire_at <= now` 即剔除。负流量/负到期值报错。
  所有合格用户加入所有启用入站；禁用入站跳过。无合格用户时保留入站及 `users:[]`，
  不创建匿名用户；没有启用入站时输出 `inbounds:[]`。
- 入站按 `id` 排序，用户按 `id` 排序，保证输出稳定（golden test）。
- 「屏蔽 BT」做成设置项，默认开。
- 私网拦截前显式 `resolve`，使域名目标解析出的私网地址也参与 `ip_is_private` 判断；
  仅有原示意中的 IP 规则不足以覆盖域名目标。使用系统 DNS 的新 `local` server，
  并设置 `default_domain_resolver`，也供 Reality 握手域名解析使用。

### T2.2 接口与字段核对

`confgen.Build(snapshot, options) ([]byte, error)` 是纯生成函数：调用方提供一致 DB 快照中的
`Inbounds/Users/BlockBittorrent`，以及 `Options.Now`、按入站 ID 索引的 `Hysteria2TLS`
证书绝对路径、`ClashAPIPort/ClashAPISecret`。这些运行时参数必须明确提供；生成器不读取 DB、
时钟或证书文件，也不选择端口或生成随机值。Clash API 固定绑定 `127.0.0.1`，要求非空 secret，
拒绝与 VLESS TCP 端口冲突。调用方在后续生效流程中一次性生成/保存控制端口和随机 secret，
再在重新生成时复用；本任务不修改初始化设置或增加持久化键。

快照切片不会被排序操作修改；相同快照、时间及运行参数产生逐字节一致的两空格缩进 JSON，
末尾一个换行。输出包含真实凭证，只可交给后续校验/生效流程；不得直接写日志或作为预览响应。
启用入站 options 拒绝非法 JSON、未知字段、未知协议；错误只包含记录 ID、字段说明和修复建议，
不包含 options 原文、私钥、UUID 或密码。禁用记录及已剔除用户的凭证不参与生成。

2026-11-09 对照 1.14 官方文档（在线文档还可能列出更新版本字段，本实现不使用那些字段）：

| 输出字段 | 官方依据 / 处理 |
|---|---|
| `log.level/timestamp` | [Log](https://sing-box.sagernet.org/configuration/log/)，`warn`、`true` |
| `type/tag/listen/listen_port` | [Listen](https://sing-box.sagernet.org/configuration/shared/listen/)，监听 `::` |
| VLESS `users.name/uuid/flow`、`tls` | [VLESS](https://sing-box.sagernet.org/configuration/inbound/vless/)，flow 固定 `xtls-rprx-vision` |
| TLS `enabled/server_name/reality.enabled/handshake.server/handshake.server_port/private_key/short_id` | [TLS](https://sing-box.sagernet.org/configuration/shared/tls/)，存储字段映射见 §1 |
| Hysteria2 `users.name/password/up_mbps/down_mbps/ignore_client_bandwidth/obfs` | [Hysteria2](https://sing-box.sagernet.org/configuration/inbound/hysteria2/)，带宽与混淆见 §1 |
| Hysteria2 TLS `enabled/alpn/certificate_path/key_path` | [TLS](https://sing-box.sagernet.org/configuration/shared/tls/)，ALPN 为 `h3`，不输出 `cert_mode/port_hopping` |
| `dns.servers.type/tag`、`dns.final` | [Local DNS](https://sing-box.sagernet.org/configuration/dns/server/local/)、[DNS](https://sing-box.sagernet.org/configuration/dns/) |
| `outbounds.type/tag` | [Direct](https://sing-box.sagernet.org/configuration/outbound/direct/)，普通 direct 出站 |
| `route.rules.action/protocol/ip_is_private/server` | [Rule](https://sing-box.sagernet.org/configuration/route/rule/)、[Rule Action](https://sing-box.sagernet.org/configuration/route/rule_action/)，sniff → 可选 BT reject → resolve → private reject |
| `route.final/default_domain_resolver` | [Route](https://sing-box.sagernet.org/configuration/route/)，`direct` / `dns-local` |
| `experimental.clash_api.external_controller/secret` | [Clash API](https://sing-box.sagernet.org/configuration/experimental/clash-api/)，固定 loopback + 调用方提供的凭证 |

golden 覆盖两种单入站、双入站、无用户、用户过滤边界、BT 关闭、带宽/混淆/端口跳跃元数据、
无入站。`go test ./internal/confgen -update` 更新 `testdata/*.golden`；`make test` 自动发现
`.tools/bin/sing-box`，每份 golden 只替换证书路径为临时生成的证书/私钥，然后实际运行
锁定版本 `sing-box check -c`。PATH 无二进制时仅跳过 check 子测试，golden 比对仍运行。

T2.2 不写服务端 JSON、不重启服务、不生成端口跳跃规则、不实现 Reality 目标探测或 HTTP API；
这些分别属于 T2.3/T3.1。不新增依赖，不修改前端包清单或锁文件。

### T2.3 运行参数、证书与端口跳跃

- 首次成功生效在同一事务内持久化 `clash_api_port`（int）和 `clash_api_secret`（string）；
  端口用 crypto/rand 从 10000–60000 选择并探测 loopback 可绑定，排除面板端口及 VLESS 端口，
  secret 为 32 字节 crypto/rand 的无 padding base64url。后续生效/重启复用，端口抢占由重启检查发现。
- Hysteria2 自签证书由生效入口按入站生成，位于 `<singbox-config 所在目录>/certs/`，
  文件名 `hy2-<入站 ID>-<SNI SHA-256 前 16 位 hex>.{crt,key}`。此处细化原单入站 `hy2.{crt,key}`
  默认示意，以支持不同 SNI 并在修改失败时保留旧证书。目录 0700、文件 0600；不覆盖已有证书对，
  重用前校验私钥匹配、SAN 和有效期；缺半对/损坏/过期须恢复或更新证书后重试。
  证书在 DB 事务外是不可变材料；失败或删除入站时保留未引用文件，供回滚/人工恢复，避免误删有效凭证。
  `hysteria2_fingerprints` 为 settings 中按 ID 字符串索引的指纹 map，仅在成功生效时提交。
- 只为启用的入站开放服务端口：VLESS TCP、Hysteria2 UDP；删除/禁用会关闭上次成功计划中对应端口。
  端口跳跃的入站范围须在 1–65535、有序、互不重叠，不得覆盖其他启用的 Hysteria2 服务端口。
  本机 INPUT 在 PREROUTING 重定向后匹配目标端口，故开放目标 UDP 端口即可；云安全组仍需放行跳跃范围。
- nft 存在时使用专用 `inet boxpanel_hopping` 表，prerouting NAT 基链、priority dstnat，
  仅匹配本机目的地址（`fib daddr type local`）的 UDP 范围并 REDIRECT 到服务端口；
  一次 nft 批处理原子替换本表，空计划删除本表，不修改其他 NAT 表或系统规则集。
  nft 不存在时使用 iptables + ip6tables 的专用 `BOXPANEL_HY2` 链和带 `boxpanel:hy2`
  注释的 PREROUTING 跳转，同样只匹配 LOCAL 目的地址；两地址族都必需。
  iptables 更新不具备跨地址族原子性，部分失败交由 §7 恢复旧计划。
- 启动重放、重复生效、删除/禁用均重建或清理面板专有规则。专用表/链名称保留给面板；
  不在其他用途手动使用。只在工具缺失时降级，nft 权限/语法失败不会改用 iptables 绕过。
  生效过程中不要切换防火墙后端；多管理器、其他规则/云安全组的限制沿用 SPEC.md §9。
  外部命令通过可注入 host.Runner 参数数组调用；单测只用假输出，不操作宿主防火墙。
  语义依据 [nftables 官方手册](https://www.netfilter.org/projects/nftables/manpage.html)
  和 [iptables REDIRECT 手册](https://www.man7.org/linux/man-pages/man8/iptables-extensions.8.html#REDIRECT)。

## 3. 规则设置（`settings.rules`）

```json
{
  "preset": "bypass_cn",                // bypass_cn | gfw_only | global
  "block_ads": false,
  "custom_direct": ["example.cn"],      // 域名后缀
  "custom_proxy": ["example.com"],
  "source": "metacubex",                // metacubex | sagernet
  "mirror": "",                         // 空=GitHub 原始地址；可填 jsDelivr 等前缀
  "update_interval": "1d",
  "dns": { "remote": "https://1.1.1.1/dns-query", "local": "https://223.5.5.5/dns-query", "fakeip": false },
  "ipv6": false
}
```

### rule-set 源（实现时须确认 URL 仍然有效，放在 `internal/rules/sources.go`）

| 用途 | 名称 | metacubex（sing-box `.srs`） | metacubex（mihomo `.mrs`） |
|---|---|---|---|
| 国内域名 | `geosite-cn` | `MetaCubeX/meta-rules-dat@sing/geo/geosite/cn.srs` | `@meta/geo/geosite/cn.mrs` |
| 国内 IP | `geoip-cn` | `@sing/geo/geoip/cn.srs` | `@meta/geo/geoip/cn.mrs` |
| GFW 列表 | `geosite-gfw` | `@sing/geo/geosite/gfw.srs` | `@meta/geo/geosite/gfw.mrs` |
| 非中国 | `geosite-geolocation-!cn` | `@sing/geo/geosite/geolocation-!cn.srs` | `@meta/geo/geosite/geolocation-!cn.mrs` |
| 广告 | `geosite-ads` | `@sing/geo/geosite/category-ads-all.srs` | `@meta/geo/geosite/category-ads-all.mrs` |

`sagernet` 源：`SagerNet/sing-geosite@rule-set/geosite-*.srs`、`SagerNet/sing-geoip@rule-set/geoip-*.srs`（仅 sing-box 订阅使用；mihomo 始终用 metacubex）。

URL 拼接：`https://raw.githubusercontent.com/{repo}/{branch}/{path}`；设置了 mirror 时用 `{mirror}{原 URL}` 或镜像规则（实现成可插拔函数）。

### 预设语义

| 预设 | 直连 | 代理 | final |
|---|---|---|---|
| `bypass_cn` | 私有 IP、custom_direct、geosite-cn、geoip-cn | custom_proxy | proxy |
| `gfw_only` | 私有 IP、custom_direct | custom_proxy、geosite-gfw | direct |
| `global` | 私有 IP、custom_direct | 其余全部 | proxy |

`block_ads=true` 时在最前面加 geosite-ads → reject。

## 4. 客户端订阅

### 4.1 格式识别（`internal/subscription/detect.go`）

优先级：查询参数 `?format=singbox|clash|base64` > User-Agent：

| UA 包含（不区分大小写） | 格式 |
|---|---|
| `sing-box`、`SFA`、`SFI`、`SFM`、`SFT` | singbox |
| `clash`、`mihomo`、`stash`、`verge`、`flclash`、`nyanpasu` | clash |
| 其他（v2rayN、Shadowrocket、NekoBox、浏览器…） | base64 |

浏览器直接访问（UA 含 `Mozilla` 且无上述关键字、`Accept` 含 `text/html`）时，返回一个简洁的 HTML 页：二维码 + 各客户端一键导入按钮 + 到期/流量信息。

### 4.2 通用响应头

```
Subscription-Userinfo: upload=<n>; download=<n>; total=<limit>; expire=<unix>
Profile-Update-Interval: 12
Content-Disposition: attachment; filename*=UTF-8''<面板名>
```

### 4.3 sing-box 客户端 JSON（结构示意）

```json
{
  "log": { "level": "warn" },
  "dns": {
    "servers": [
      { "type": "https", "tag": "dns-remote", "server": "1.1.1.1", "detour": "proxy" },
      { "type": "https", "tag": "dns-local", "server": "223.5.5.5" }
    ],
    "rules": [ { "rule_set": ["geosite-cn"], "server": "dns-local" } ],
    "final": "dns-remote",
    "strategy": "prefer_ipv4"
  },
  "inbounds": [
    { "type": "tun", "tag": "tun-in",
      "address": ["172.19.0.1/30", "fdfe:dead:beef::1/126"],
      "auto_route": true, "strict_route": true },
    { "type": "mixed", "tag": "mixed-in", "listen": "127.0.0.1", "listen_port": 2080 }
  ],
  "outbounds": [
    { "type": "selector", "tag": "proxy", "outbounds": ["auto", "<节点…>"], "default": "auto" },
    { "type": "urltest", "tag": "auto", "outbounds": ["<节点…>"] },
    { "<各节点 outbound>": "..." },
    { "type": "direct", "tag": "direct" }
  ],
  "route": {
    "rules": [
      { "action": "sniff" },
      { "protocol": "dns", "action": "hijack-dns" },
      { "ip_is_private": true, "outbound": "direct" },
      "<按预设生成的规则>"
    ],
    "rule_set": [
      { "type": "remote", "tag": "geosite-cn", "format": "binary",
        "url": "<见 §3>", "download_detour": "proxy", "update_interval": "1d" }
    ],
    "final": "proxy",
    "auto_detect_interface": true,
    "default_domain_resolver": "dns-local"
  },
  "experimental": { "cache_file": { "enabled": true } }
}
```

- fakeip 开启时增加 fakeip server 与对应 DNS 规则。
- 节点 outbound：
  - vless_reality：`type: vless`, `flow: xtls-rprx-vision`, `tls.reality.{enabled, public_key, short_id}`, `tls.utls.{enabled, fingerprint}`, `tls.server_name`
  - hysteria2：`tls.server_name`；自签证书时在 `tls.certificate` 内嵌 PEM（优于 `insecure`）；端口跳跃用 `server_ports: ["20000:30000"]`；有 obfs 时 `obfs.{type: salamander, password}`
- 客户端版本：MVP 只生成 1.12+ 格式，不做旧格式兼容；订阅页与引导页提示「sing-box 客户端需 1.12 或更新」。

### 4.4 mihomo（Clash Meta）YAML

```yaml
mixed-port: 7890
allow-lan: false
mode: rule
log-level: warning
ipv6: false
dns:
  enable: true
  enhanced-mode: redir-host        # fakeip=true 时 fake-ip
  nameserver: [https://1.1.1.1/dns-query]
  nameserver-policy:
    "rule-set:geosite-cn": [https://223.5.5.5/dns-query]
  proxy-server-nameserver: [https://223.5.5.5/dns-query]
proxies:
  - name: <remark>
    type: vless
    server: <host>
    port: 443
    uuid: <uuid>
    network: tcp
    tls: true
    udp: true
    flow: xtls-rprx-vision
    servername: <server_name>
    client-fingerprint: chrome
    reality-opts: { public-key: <pub>, short-id: <sid> }
  - name: <remark>
    type: hysteria2
    server: <host>
    port: 8443
    ports: 20000-30000             # 端口跳跃时
    password: <pwd>
    sni: <sni>
    skip-cert-verify: true         # 自签时；同时给 fingerprint: <sha256>（若版本支持）
proxy-groups:
  - { name: 节点选择, type: select, proxies: [自动选择, <节点…>, DIRECT] }
  - { name: 自动选择, type: url-test, proxies: [<节点…>], url: https://www.gstatic.com/generate_204, interval: 300 }
rule-providers:
  geosite-cn: { type: http, behavior: domain, format: mrs, url: <见 §3>, interval: 86400 }
  geoip-cn:   { type: http, behavior: ipcidr, format: mrs, url: <见 §3>, interval: 86400 }
rules:
  - <按预设生成>
  - MATCH,节点选择
```

### 4.5 base64 URI

每行一个节点 URI，整体 base64（标准编码）：

```
vless://<uuid>@<host>:<port>?encryption=none&flow=xtls-rprx-vision&security=reality&sni=<sni>&fp=chrome&pbk=<pub>&sid=<sid>&type=tcp#<urlencoded remark>
hysteria2://<urlencoded pwd>@<host>:<port>/?sni=<sni>&insecure=1&pinSHA256=<hex>[&obfs=salamander&obfs-password=<p>][&mport=20000-30000]#<remark>
```

规则不随 URI 下发（URI 格式不支持），订阅页需提示「此类客户端请在客户端内选择『绕过大陆』等规则」。

## 5. Reality 伪装目标检测（`internal/reality`）

- 内置候选：若干大型站点域名（放常量列表，可在设置中覆盖）。
- 对每个候选从**服务器本机**发起 TLS 握手，判定：支持 TLS 1.3、ALPN 协商到 `h2`、证书有效、握手耗时。
- 推荐：满足条件者按耗时升序取前 3；引导流程默认选第 1 个。
- API 也支持检测用户自填的目标。

## 6. 节点地址 `public_host`

- 默认：安装时探测公网 IPv4（多个探测源取多数，超时 3 秒）。
- T1.2 初始化使用可注入的探测函数与 HTTP client/source 列表；三个源并发共享 3 秒总超时，
  默认使用 [ipify](https://www.ipify.org/) 的 `api64.ipify.org`、
  [ifconfig.me](https://ifconfig.me/) 的 `/ip`、[icanhazip](https://icanhazip.com/)。
  仅采纳 HTTP 200、最多 64 字节且合法的非私网/非回环 IP；优先 IPv4，
  无 IPv4 时采用 IPv6，要求所选地址族的有效响应有严格多数（仅一个成功响应也可用）。
  超时/全失败/意见冲突则提示重试或 `init --public-ip IP`，不写入业务初始数据。
  显式 IP 可用于离线初始化，单测仅使用注入的响应，不访问外网。
- T1.3 将探测实现共享到 `internal/sysinfo`，保留 init 的上述行为；系统自检/重新探测接口
  则对同样三个源分别用 IPv4、IPv6 连接并发请求（总计六次，共享 3 秒窗口，无重定向），
  每个地址族独立取有效响应的严格多数。未探测到或意见冲突的地址族返回空字符串，
  两者均为空才算整体失败；不要求 IPv4-only VPS 具有 IPv6。探测不会修改 `public_host`。
- 用户可改为域名。所有订阅/URI 中的 server 都用它。
- IPv6-only 机器：URI 中 IPv6 需加方括号。
