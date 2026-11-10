# 配置生成规格

> ⚠️ 以下 JSON 是**结构示意**。字段名以 `internal/singbox/version.go` 锁定版本的
> [官方文档](https://sing-box.sagernet.org/configuration/) 为准，实现时逐项核对，并用 `sing-box check` 验证。
> 服务端目标版本：sing-box **1.14.4**（新 DNS server 格式、rule action，不使用已移除的 geoip/geosite 字段与 block/dns 特殊出站）。
> T2.2 已逐字段核对 §1/§2，并用锁定二进制校验所有服务端 golden；T3.2 客户端也按 **1.14** 字段生成并用同一锁定二进制逐份校验。

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

| 用途 | 名称 | metacubex（sing-box `.srs`） | metacubex（Clash `.yaml`） |
|---|---|---|---|
| 国内域名 | `geosite-cn` | `MetaCubeX/meta-rules-dat@sing/geo/geosite/cn.srs` | `@meta/geo/geosite/cn.yaml` |
| 国内 IP | `geoip-cn` | `@sing/geo/geoip/cn.srs` | `@meta/geo/geoip/cn.yaml` |
| GFW 列表 | `geosite-gfw` | `@sing/geo/geosite/gfw.srs` | `@meta/geo/geosite/gfw.yaml` |
| 非中国 | `geosite-geolocation-!cn` | `@sing/geo/geosite/geolocation-!cn.srs` | `@meta/geo/geosite/geolocation-!cn.yaml` |
| 广告 | `geosite-ads` | `@sing/geo/geosite/category-ads-all.srs` | `@meta/geo/geosite/category-ads-all.yaml` |

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
    "rules": [
      { "query_type": ["AAAA"], "action": "predefined", "rcode": "NOERROR" },
      { "rule_set": ["geosite-cn"], "action": "route", "server": "dns-local" }
    ],
    "final": "dns-remote"
  },
  "inbounds": [
    { "type": "tun", "tag": "tun-in",
      "address": ["172.19.0.1/30", "fdfe:dead:beef::1/126"],
      "auto_route": true, "strict_route": true },
    { "type": "mixed", "tag": "mixed-in", "listen": "127.0.0.1", "listen_port": 2080 }
  ],
  "outbounds": [
    { "type": "selector", "tag": "proxy", "outbounds": ["auto", "manual", "direct"], "default": "auto" },
    { "type": "urltest", "tag": "auto", "outbounds": ["<节点…>"],
      "url": "https://www.gstatic.com/generate_204", "interval": "3m", "tolerance": 50 },
    { "type": "selector", "tag": "manual", "outbounds": ["<节点…>"] },
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
        "url": "<见 §3>", "http_client": { "detour": "proxy" }, "update_interval": "1d" }
    ],
    "final": "proxy",
    "auto_detect_interface": true,
    "default_domain_resolver": "dns-local"
  },
  "experimental": { "cache_file": { "enabled": true } }
}
```

- T6.7：上例适用于至少两个节点。第一层只包含 auto/manual/direct，节点只在 auto/manual 的候选列表中；
  单节点只生成 proxy selector，`outbounds: ["<该节点>", "direct"]`，`default` 为该节点，不生成 auto/manual。
  tag 延续现有 proxy/auto/direct 命名并新增 manual；route final、远端 DNS detour 与
  rule-set `http_client.detour` 仍引用 proxy（gfw_only 的 final 仍为 direct）。
  [Selector](https://sing-box.sagernet.org/configuration/outbound/selector/) 的 default 未指定时使用首项；
  [URLTest](https://sing-box.sagernet.org/configuration/outbound/urltest/) 的 tolerance 单位为毫秒，显式输出 50。
  两类组均不新增 `interrupt_exist_connections`，沿用默认 false，不改变已有连接切换策略。
- fakeip 开启时增加 fakeip server 与对应 DNS 规则。
- 节点 outbound：
  - vless_reality：`type: vless`, `flow: xtls-rprx-vision`, `tls.reality.{enabled, public_key, short_id}`, `tls.utls.{enabled, fingerprint}`, `tls.server_name`
  - hysteria2：`tls.server_name`；自签证书时在 `tls.certificate` 内嵌 PEM（优于 `insecure`）；端口跳跃用 `server_ports: ["20000:30000"]`；有 obfs 时 `obfs.{type: salamander, password}`
- 客户端版本：MVP 只生成 **1.14+** 格式，不做旧格式兼容；订阅页提示「sing-box 客户端需 1.14 或更新」，后续引导页沿用此文案。

### 4.4 mihomo（Clash Meta）YAML

```yaml
mixed-port: 7890
allow-lan: false
mode: rule
log-level: warning
ipv6: false
dns:
  enable: true
  ipv6: false
  enhanced-mode: fake-ip
  respect-rules: true              # mihomo DNS 出站遵守 rules
  follow-rule: true                # Stash 的对应选项
  fake-ip-range: 198.18.0.1/16
  fake-ip-filter:
    - '*.lan'
    - '*.local'
    - localhost.ptlink
    - localhost.ptlogin2.qq.com
    - 'time.*.com'
    - 'ntp.*.com'
    - '+.pool.ntp.org'
    - '+.msftconnecttest.com'
    - '+.msftncsi.com'
    - captive.apple.com
    - 'rule-set:geosite-cn'          # 仅 bypass_cn
  default-nameserver: [https://223.5.5.5/dns-query]
  nameserver: [https://223.5.5.5/dns-query, https://doh.pub/dns-query]
  nameserver-policy:
    'rule-set:geosite-cn': [https://223.5.5.5/dns-query, https://doh.pub/dns-query]
    'rule-set:geosite-geolocation-!cn': ['https://1.1.1.1/dns-query']
    'time.*.com': ['https://1.1.1.1/dns-query']
    'ntp.*.com': ['https://1.1.1.1/dns-query']
    '+.pool.ntp.org': ['https://1.1.1.1/dns-query']
    '+.msftconnecttest.com': ['https://1.1.1.1/dns-query']
    '+.msftncsi.com': ['https://1.1.1.1/dns-query']
    captive.apple.com: ['https://1.1.1.1/dns-query']
  proxy-server-nameserver: [https://223.5.5.5/dns-query, https://doh.pub/dns-query]
proxies:
  - name: <节点显示名，见 §4.5 后的命名约定>
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
  - name: <节点显示名>
    type: hysteria2
    server: <host>
    port: 8443
    ports: 20000-30000             # 端口跳跃时
    password: <pwd>
    sni: <sni>
    skip-cert-verify: false        # 指纹固定验证自签证书
    fingerprint: <sha256>         # 必填，叶证书 DER 的 SHA-256 hex
proxy-groups:
  - { name: 节点选择, type: select, proxies: [自动选择, 手动选择, DIRECT] }
  - { name: 自动选择, type: url-test, proxies: [<节点…>], url: https://www.gstatic.com/generate_204, interval: 300, tolerance: 50, lazy: true }
  - { name: 手动选择, type: select, proxies: [<节点…>] }
rule-providers:
  geosite-cn: { type: http, behavior: domain, format: yaml, url: <见 §3>, interval: 86400, proxy: 节点选择 }
  geoip-cn:   { type: http, behavior: ipcidr, format: yaml, url: <见 §3>, interval: 86400, proxy: 节点选择 }
  geosite-geolocation-!cn: { type: http, behavior: domain, format: yaml, url: <见 §3>, interval: 86400, proxy: 节点选择 }
rules:
  - IP-CIDR,1.1.1.1/32,节点选择,no-resolve
  - IP-CIDR,223.5.5.5/32,DIRECT,no-resolve
  - DOMAIN,doh.pub,DIRECT
  - IP-CIDR,10.0.0.0/8,DIRECT,no-resolve
  - <其他私网规则与自定义域名，按预设生成>
  - RULE-SET,geosite-cn,DIRECT
  - RULE-SET,geoip-cn,DIRECT,no-resolve
  - MATCH,节点选择
```

T6.7（2026-11-09）代理组：上例适用于至少两个节点，节点选择默认首项“自动选择”，
不直接包含节点；自动/手动组均包含全部节点。单节点只有节点选择 `[该节点, DIRECT]`。
select/auto/manual 显示文案共享 zh-CN.ts，DIRECT 保持内置名。
按 [mihomo URL-test](https://wiki.metacubex.one/config/proxy-groups/url-test/) 与
[组通用字段](https://wiki.metacubex.one/config/proxy-groups/)，显式设置 300 秒间隔、50ms 容差和 lazy=true；
保留原规则、provider 下载及远端 DNS 到节点选择的引用。

T6.5（2026-11-09）防泄漏与兼容边界：

- 上例为 bypass_cn。Clash 输出固定 Fake-IP、`198.18.0.1/16`，顶层与 DNS 的 IPv6 均关闭；
  `settings.rules.dns.fakeip/ipv6` 继续保留，只控制 sing-box 输出，规则页注明区别，不迁移旧设置。
- 默认国内 DoH 用于直连域名及节点地址；显式修改 `dns.local` 时保留该单一服务器，管理员应选择
  国内可用的 DoH。`default-nameserver` 固定为 IP 地址的国内 DoH，只引导解析 DNS 服务器域名。
  普通代理 A 查询返回 Fake-IP，连接携带域名交服务端解析；所有 IP-CIDR/IP-CIDR6 和 geoip-cn
  规则均带 no-resolve，不为规则匹配提前查目标 IP。已知 IP 仍可命中国内/私网规则。
- custom_proxy、gfw_only 的 GFW DNS 策略使用 `dns.remote`，通过 `respect-rules: true` 遵守路由；
  远端 DoH 的精确域名/IP 路由置于最前并指向节点选择，国内 DoH/引导地址明确 DIRECT。
  同一主机同时用作 local/remote 时代理优先；空节点时远端 DNS 为 REJECT，不能自动直连。
  `proxy-server-nameserver` 隔离节点解析，Stash 用 `follow-rule: true` 遵守相同路由；
  不依赖 Stash 文档未确认的 `#代理名` 语法。bypass_cn/global 另加载
  geolocation-!cn 供真实 DNS 查询使用，NTP/连通性检测的真实 IP 例外也明确经代理。
  custom_direct 仍使用本地 DNS，同一后缀 direct 优先；gfw_only 中非 GFW 域名直连是预期。
  不输出直连境外 fallback。节点选择保留用户主动选择 DIRECT 的能力；防泄漏路径以选择代理节点为前提。
- Fake-IP 不能覆盖应用自行使用的 DNS/DoH；未列入远程 policy 的非 A/AAAA 查询仍按默认 nameserver
  处理。这里修复代理连接的地址解析路径及境外 DoH 直连，不声称接管设备所有 DNS 流量。
- [mihomo DNS](https://wiki.metacubex.one/config/dns/) 确认 `respect-rules`、节点独立 DNS、Fake-IP 与
  AAAA 回空；[规则说明](https://wiki.metacubex.one/config/rules/#no-resolve) 确认 no-resolve；
  [规则集合](https://wiki.metacubex.one/config/rule-providers/) 确认 YAML、behavior 和代理下载。
  [mihomo 集合语法](https://wiki.metacubex.one/handbook/syntax/) 与
  [Stash DNS 文档](https://stash.wiki/features/dns-server) 均确认 filter 支持 rule-set，
  因此仅在有 geosite-cn provider 的 bypass_cn 中加入该 filter；Stash 文档标注 iOS/tvOS 3.6+、macOS 4.3+。
  不使用 mihomo 的 rule 模式过滤器；旧客户端须升级，App 名称为“Clash”不能证明内核支持这些字段。
- 所有 Clash provider 使用 MetaCubeX 同路径 `.yaml`，包括 sagernet 选择和镜像前缀；
  保持通过节点选择下载，sing-box 仍用 `.srs`。URI 无分流/DNS 语义，保持原样。

### 4.5 base64 URI

每行一个节点 URI，整体 base64（标准编码）：

```
vless://<uuid>@<host>:<port>?encryption=none&flow=xtls-rprx-vision&security=reality&sni=<sni>&fp=chrome&pbk=<pub>&sid=<sid>&type=tcp#<urlencoded 节点显示名>
hysteria2://<urlencoded pwd>@<host>:<port>/?sni=<sni>&insecure=1&pinSHA256=<hex>[&obfs=salamander&obfs-password=<p>][&mport=20000-30000]#<urlencoded 节点显示名>
```

规则不随 URI 下发（URI 格式不支持），订阅页需提示「此类客户端请在客户端内选择『绕过大陆』等规则」。

### T3.2 客户端字段核对与生成边界

2026-11-09 对照以下官方文档核对；在线文档中的 1.15+ 字段不采用。
所有客户端 golden 直接交给锁定的 **1.14.4** `sing-box check`，不改写远程规则为本地规则；
check 只校验格式，不启动 TUN、不连接节点、不下载 rule-set。

| 字段 | 文档与具体选择 |
|---|---|
| TUN `type/tag/address/auto_route/strict_route` | [TUN](https://sing-box.sagernet.org/configuration/inbound/tun/)；只用统一 `address`，不用旧 inet4/inet6 字段或已弃用 stack；IPv6 关闭时不分配 IPv6 TUN 地址 |
| mixed `type/tag/listen/listen_port` | [Mixed](https://sing-box.sagernet.org/configuration/inbound/mixed/)；只监听 127.0.0.1:2080 |
| DNS `servers.type/tag/server/server_port/path/detour/domain_resolver` | [HTTPS server](https://sing-box.sagernet.org/configuration/dns/server/https/)；将 HTTPS URL 拆成新 server 对象；DNS 域名用独立的 local `dns-bootstrap` 解析，避免递归依赖 |
| DNS `rules.action/server/query_type/rcode`、`final` | [DNS actions](https://sing-box.sagernet.org/configuration/dns/rule_action/)；显式 route/reject；IPv6 关闭时 AAAA 回空 NOERROR，不输出已弃用 strategy；gfw_only 默认 local，其余默认 remote |
| FakeIP `type/tag/inet4_range/inet6_range` | [FakeIP server](https://sing-box.sagernet.org/configuration/dns/server/fakeip/)；198.18.0.0/15，可选 2001:2::/48；仅 A/AAAA 的代理查询使用 FakeIP，其他类型正常解析；缓存开启 store_fakeip |
| route `action/outbound/protocol/ip_is_private/domain_suffix/rule_set` | [Route rule](https://sing-box.sagernet.org/configuration/route/rule/)、[actions](https://sing-box.sagernet.org/configuration/route/rule_action/)；sniff → DNS hijack → 广告 reject → 私网 → custom_direct → custom_proxy → 预设；显式 route action，不使用 block/dns 特殊出站 |
| route `final/auto_detect_interface/default_domain_resolver` | [Route](https://sing-box.sagernet.org/configuration/route/)；节点域名通过 dns-local，接口自动检测避免 TUN 回环 |
| rule-set `type/tag/format/url/http_client/update_interval` | [Rule-set](https://sing-box.sagernet.org/configuration/rule-set/)、[HTTP client](https://sing-box.sagernet.org/configuration/shared/http-client/)；remote/binary，`http_client:{detour:"proxy"}` 取代 1.14 已弃用 download_detour，仅引用预设需要的规则 |
| selector / urltest `type/tag/outbounds/default/url/interval/tolerance` | [Selector](https://sing-box.sagernet.org/configuration/outbound/selector/)、[URLTest](https://sing-box.sagernet.org/configuration/outbound/urltest/)；多节点 proxy 默认 auto，manual 包含全部节点；单节点 proxy 默认该节点；测试 URL 为 gstatic generate_204，间隔 3m，容差 50ms（T6.7） |
| VLESS `server/server_port/uuid/flow/tls` | [VLESS](https://sing-box.sagernet.org/configuration/outbound/vless/)、[TLS](https://sing-box.sagernet.org/configuration/shared/tls/)；Vision、Reality 公钥和首个 short ID、uTLS 指纹；绝不包含服务器私钥 |
| Hysteria2 `server/password/up_mbps/down_mbps/obfs/tls/server_ports` | [Hysteria2](https://sing-box.sagernet.org/configuration/outbound/hysteria2/)；TLS 内嵌已生效证书 PEM、SNI、h3，不设置 insecure；端口跳跃时仅输出 server_ports，省略冲突的 server_port |
| mihomo VLESS / Reality | [VLESS](https://wiki.metacubex.one/config/proxies/vless/)、[TLS](https://wiki.metacubex.one/config/proxies/tls/)；network=tcp、udp=true、packet-encoding=xudp、Vision、servername、client-fingerprint、reality-opts.public-key/short-id |
| mihomo Hysteria2 | [Hysteria2](https://wiki.metacubex.one/config/proxies/hysteria2/)；ports 用 `start-end`，up/down 仅正数，obfs=salamander 与 obfs-password；skip-cert-verify=false + fingerprint，按官方 TLS 文档固定叶证书指纹，无需系统信任自签根 |
| mihomo rule-providers | [规则集合](https://wiki.metacubex.one/config/rule-providers/)；type=http、format=yaml、behavior=domain/ipcidr、interval 为整秒、proxy=节点选择；始终使用 MetaCubeX（T6.5） |

补齐的输入与输出约定：

- `rules` 更新是整体替换；三种预设、两种源之外的值拒绝。DNS 仅接受无凭证的 HTTPS URL，
  支持域名、IPv4/IPv6、端口与路径；更新间隔 1h–30d。mirror 为空或以 `/` 结尾的 HTTPS 前缀，
  默认拼接 `{mirror}{原 URL}`；`rules.Mirror` 函数可注入其他重写策略。
- 自定义域名每组至多 128 项，只接受 ASCII DNS 后缀，不接受 URL、通配符、IP、逗号。
  direct 优先于 proxy，proxy 优先于预设国内规则；广告优先于其他业务规则。
  Clash 的 gfw_only 使用本地默认 DNS，并为 GFW rule-set 指定经代理的远程 nameserver-policy（§4.4）。
  sing-box 在 bypass_cn 的域名决策之后、geoip-cn 之前显式 `resolve` 到 dns-remote，
  再检查私网和国内 IP，让 mixed/FakeIP 的域名目标也可命中 IP 规则；其他预设无需此解析。
  FakeIP 只处理 A/AAAA，代理域名的其他查询类型仍显式路由到远程 DNS。
- T6.7 节点命名：在 `subscription.Store.Load` 的统一导出路径中按入站 ID 升序处理，
  remark 去首尾空白；空白/清理后为空时按协议使用 `Reality · <端口>` / `Hysteria2 · <端口>`，
  模板集中在 zh-CN.ts。不再回退到内部 tag，不回写数据库或影响服务端入站 tag。
  ASCII 逗号替换为全角逗号；控制字符和 Unicode 空白（含换行、行/段分隔符）替换为空格并合并连续空白。
  其余标点保留，由 yaml.v3 / JSON / net/url 编码。所有格式及单节点 links 使用同一显示名。
  与已分配名称、proxy/auto/manual/direct、DIRECT/REJECT、i18n 策略组名冲突时，
  从原显示名追加 ` (2)`、` (3)`，取第一个未占用的名称；自然带后缀的备注也参与碰撞检查。
  按 ID 先到先分配，例如 `A, A, A (2)` 得到 `A, A (2), A (2) (2)`，不会覆盖先前节点。
  IPv6 URI 使用方括号；凭证、query、显示名片段由 net/url 编码。
- 每次订阅读取一致 DB 快照，只导出启用入站及当前用户凭证；公开订阅对禁用/超额/过期用户返回 404。
  无节点仍输出可解析配置，未匹配直连规则的流量 reject；base64 是空列表。
  T6.7 补齐空节点组结构：不生成 auto/manual；sing-box proxy 仅包含 direct 作为有效占位，
  仍保留末尾 reject 路由；Clash 节点选择为 `[REJECT, DIRECT]`、默认 REJECT，远端 DNS 及 MATCH 仍为 REJECT。
- Hysteria2 读取 T2.3 的不可变公开证书文件，校验 SNI、有效期与 DB 中叶证书指纹；缺失/不一致时
  返回不可用，不生成无指纹的 insecure 订阅。开发模式没有 Apply 证书，须在 Linux 成功生效后导出。
- 新增 Go 依赖 `gopkg.in/yaml.v3 v3.0.1`（结构化 YAML 编码）与
  `github.com/skip2/go-qrcode` 的 2020-06-17 固定版本（本地 PNG 二维码，无外部 QR 服务）；均为纯 Go。
  不改 web/package.json 或 pnpm-lock.yaml，不实现 M4 页面。独立订阅 HTML 的文案仍集中在
  `web/src/i18n/zh-CN.ts` 的标记 JSON 对象，通过 Go embed 共享，不加载外部脚本或资源。

### T3.2 / T6.5 规则源实测（2026-11-09）

源 URL 定义于 `internal/rules/sources.go`；实现时逐 URL 实际发送 HTTPS **HEAD**，测试不联网。
下表文件均相对 §3 对应 raw.githubusercontent.com 仓库/分支，最终常量共 14 个不同 URL。

| 规则 | MetaCubeX `sing/geo` `.srs` | MetaCubeX `meta/geo` `.yaml`（T6.5 重新 HEAD 均 200） | SagerNet `rule-set` `.srs` |
|---|---|---|---|
| 国内域名 | geosite/cn：200 | geosite/cn：200 | sing-geosite / geosite-cn：200 |
| 国内 IP | geoip/cn：200 | geoip/cn：200 | sing-geoip / geoip-cn：200 |
| GFW | geosite/gfw：200 | geosite/gfw：200 | geosite-gfw：**404**，最终常量回退至左列 MetaCubeX URL（200） |
| 非中国 | geosite/geolocation-!cn：200 | geosite/geolocation-!cn：200 | sing-geosite / geosite-geolocation-!cn：200 |
| 广告 | geosite/category-ads-all：200 | geosite/category-ads-all：200 | sing-geosite / geosite-category-ads-all：200 |

这是原规格的可用性缺口：SagerNet 没有 GFW 产物，只对这一列表回退 MetaCubeX；
不能用 geolocation-!cn 替代，否则会改变 gfw_only 的语义。选择 sagernet 时其余规则保持该源。
非中国规则供 Clash bypass_cn/global 的远程 DNS policy 使用；sing-box 的三个预设无需加载它。

### T3.3 离线端到端验证边界

`internal/e2e` 使用同一份固定入站/用户模型生成服务端，并将该模型写入临时 SQLite；
客户端通过生产 `subscription.Store.Load`（含 token、证书有效期和指纹校验）及 `Render` 生成。
服务端直接调用生产 `confgen.Build`，不手写协议 JSON，不增加生产代码例外。

- 测试生成随机 Reality 密钥、short ID、用户凭证和 Hysteria2 自签证书，所有文件仅在临时目录。
  `127.0.0.1` 上的 HTTPS handshake 目标强制 TLS 1.3、ALPN=h2、X25519；启动后实际握手验证
  TLS/ALPN。独立 HTTP 目标返回固定内容并计数。两者均不依赖公网证书、域名解析或外网服务。
- 服务端双入站、Clash API 使用预留的随机端口；仅在测试中将入站监听改成 `127.0.0.1`，
  移除唯一的 `ip_is_private` reject，其余生成字段（包括凭证、Vision、TLS、BT 规则）保持。
- 客户端保留生成的 mixed 入站并改为随机端口，移除 TUN；route 使用空规则和被测节点 final，
  移除 TUN 所需的自动网卡绑定。测试输入选用无远程 rule-set 的 global 预设，DNS URL 指向回环。
  移除 urltest 出站并将 selector 固定到被测节点，避免后台访问默认 gstatic 测速地址；协议出站保持
  原样。字段依据 [Route](https://sing-box.sagernet.org/configuration/route/) 和
  [URLTest](https://sing-box.sagernet.org/configuration/outbound/urltest/)。缓存落在子进程临时工作目录。
- 服务端及每份客户端配置均先执行锁定内核 `check` 再 `run`。经显式 HTTP mixed 代理分别验证
  VLESS Reality、Hysteria2 的 HTTP 200、固定内容及目标请求计数；HTTP client 禁止直连和重定向。
  反例仅替换客户端 Reality public_key 为另一份合法 X25519 公钥，要求请求失败且目标计数不变，
  并确认两个内核仍存活，避免把进程崩溃误判为认证失败。
- 端口预留至启动前，等待 TCP 连接成功及 UDP 绑定；每次请求、启动、内核进程寿命均有超时。
  `t.Cleanup` 杀掉并 `Wait` 回收子进程，失败打印脱敏后的日志。不执行 systemd、防火墙或生产 Apply，
  不覆盖受管配置；该临时测试进程不属于 SPEC.md §7 的生产配置生效操作。

运行及 Skip 边界见 INSTALL.md 的 T3.3 说明。此测试覆盖两种协议实际连接，不代替 T2.3 的
生效/回滚测试或 T3.2 的预设 golden，也不提前实现 M4。

## 5. Reality 伪装目标检测（`internal/reality`）

- 内置候选：若干大型站点域名，集中在 `internal/reality/probe.go` 的 `CandidateDomains` 常量；
  T3.1 不新增设置覆盖入口（设置接口属于 T3.2）。域名只是候选，每次须重新验证可用性。
- 对每个候选从**服务器本机**发起 TLS 握手，判定：支持 TLS 1.3、ALPN 协商到 `h2`、证书有效、握手耗时。
- 推荐：满足条件者按耗时升序取前 3；引导流程默认选第 1 个。
- API 也支持检测用户自填的目标。T3.1 的超时、公网地址限制、可注入 DNS/TLS 与失败返回见 API.md；
  探测在写事务之前完成，mutation 不访问网络。自动选择没有合格结果时拒绝创建并给出重试建议。

## 6. 节点地址 `public_host`

- T6.4：初始化、环境检查、`/system/public-ip`、域名校验和端口探测统一复用
  `internal/sysinfo` 的 **IPv4 优先**探测，最多 3 秒，不使用环境代理或跟随重定向。
  先并发请求 `https://api.ipify.org`、`https://ipv4.icanhazip.com`、`https://ifconfig.me/ip`，
  HTTP 拨号强制 `tcp4`。初始化得到有效 IPv4 后立即采用；没有可用 IPv4 才请求
  `https://api6.ipify.org`、`https://ipv6.icanhazip.com`、`https://ifconfig.me/ip`，拨号强制 `tcp6`。
  [ipify 官方说明](https://www.ipify.org/) 区分 IPv4/IPv6 专用入口与双栈 api64，
  不能仅靠双栈响应中的地址选择保证 IPv4；拨号地址族遵循 [Go net 文档](https://pkg.go.dev/net#Dialer.DialContext)。
- 超时预算补齐：IPv4 阶段至多占剩余总窗口的一半（通常 1.5 秒），其余留给 IPv6；
  父 context 更短时按其剩余时间分配，IPv4 超时不会耗尽回退预算。每阶段三个源并发，
  仅采纳 HTTP 200、最多 64 字节、合法非私网/非回环且属于当前地址族的 IP。
  每族独立取有效响应的严格多数（仅一个成功响应也可用）；缺失或冲突视为该族不可用。
  两族均不可用才整体失败，提示重试或 `init --public-ip IP`，初始化不写入业务数据。
- 自检/重新探测/域名校验/端口探测在完成 IPv4 阶段后仍收集 IPv6，供双栈展示和校验；
  返回结构保持 `{ipv4,ipv6}`，没有可用地址的族为空。选择单一地址及端口探测目标列表均先 IPv4。
  探测不会覆盖已有 `public_host`；旧实例地址可用 `boxpanel set-host <IP|域名>` 修复。
  探测函数、HTTP 客户端/拨号器可注入；显式 IP 可离线初始化，所有网络单测不访问外网。
- 用户可改为域名。所有订阅/URI 中的 server 都用它。
- IPv6-only 机器：URI 中 IPv6 需加方括号。
