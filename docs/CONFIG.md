# 配置生成规格

> ⚠️ 以下 JSON 是**结构示意**。字段名以 `internal/singbox/version.go` 锁定版本的
> [官方文档](https://sing-box.sagernet.org/configuration/) 为准，实现时逐项核对，并用 `sing-box check` 验证。
> 目标格式：sing-box **1.12+**（新 DNS server 格式、rule action，不使用已移除的 geoip/geosite 字段与 block/dns 特殊出站）。

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

- 密钥用 Go `crypto/ecdh` X25519 生成（与 `sing-box generate reality-keypair` 等价，需单测对比格式）。
- 伪装目标默认从内置候选列表中自动挑选，见 §5。

### hysteria2

```json
{
  "sni": "<证书 CN/SAN，默认 www.bing.com 或用户域名>",
  "cert_mode": "self_signed",          // self_signed | acme(后期)
  "obfs_password": "",                 // 空=不启用 salamander
  "up_mbps": 0, "down_mbps": 0,        // 0=不限，交给 BBR
  "port_hopping": { "enabled": false, "start": 20000, "end": 30000 }
}
```

- 自签证书在 `certs/hy2.{crt,key}`，记录 SHA-256 指纹，用于客户端 pin。
- 端口跳跃：面板用 nftables（无则 iptables）把 UDP `start-end` DNAT/REDIRECT 到 `port`，面板启动时重新下发，删除入站时清理。

## 2. 服务端配置（confgen）

```json
{
  "log": { "level": "warn", "timestamp": true },
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
      { "ip_is_private": true, "action": "reject" }
    ],
    "final": "direct"
  },
  "experimental": {
    "clash_api": { "external_controller": "127.0.0.1:<随机端口>", "secret": "<随机>" }
  }
}
```

- 用户列表仅包含「启用 && 未超额 && 未过期」的用户。
- 入站按 `id` 排序，用户按 `id` 排序，保证输出稳定（golden test）。
- 「屏蔽 BT」做成设置项，默认开。

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
