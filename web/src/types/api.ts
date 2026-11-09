// Wire types follow internal/httpapi (Unix seconds and bytes, no envelopes on lists).
export interface APIErrorBody {
  code: string
  message: string
  hint: string
  detail?: string
}

export interface Admin { username: string }
export interface LoginInput { username: string; password: string }
export interface PasswordInput { old_password: string; new_password: string }

// The setup endpoint is reserved for T5.1; null in the query means unavailable.
export interface SetupStatus { completed: boolean; step: string }

export interface SingboxStatus {
  installed: boolean
  running: boolean
  version: string
  uptime: number
}

export interface Usage { used: number; total: number }
export interface SystemStatus {
  supported: boolean
  cpu: number
  mem: Usage
  disk: Usage
  uptime: number
  net: { rx_rate: number; tx_rate: number }
  singbox: SingboxStatus
}

export interface PublicIPs { ipv4: string; ipv6: string }
export interface BBR { enabled: boolean; available: boolean }
export interface SystemCheck extends PublicIPs {
  os: string
  arch: string
  virt: string
  bbr: BBR
  firewall: { type: 'none' | 'ufw' | 'firewalld' | 'nftables' | 'iptables' | 'unknown'; active: boolean }
  singbox: SingboxStatus
  ports: { port: number; proto: 'tcp' | 'udp'; free: boolean | null }[]
  items: { id: string; level: 'ok' | 'warn' | 'error'; title: string; hint: string; action?: 'enable_bbr' }[]
}

export interface PanelSettings {
  public_host: string
  web_port: number
  web_path: string
  sub_path: string
  panel_tls_mode: string
  block_bittorrent: boolean
  panel_name: string
}
export interface SettingsResult extends PanelSettings { panel_url: string; restart_after: 0 | 2 }
export interface RealityTarget {
  server: string
  tls13: boolean
  h2: boolean
  latency_ms: number
  ok: boolean
  hint?: string
}

export interface RealityOptions {
  server_name: string
  handshake_server: string
  handshake_port: number
  public_key: string
  short_ids: string[]
  fingerprint: string
}
export interface PortHopping { enabled: boolean; start: number; end: number }
export interface Hysteria2Options {
  sni: string
  cert_mode: 'self_signed'
  up_mbps: number
  down_mbps: number
  port_hopping: PortHopping
}
interface InboundBase {
  id: number
  tag: string
  port: number
  enabled: boolean
  remark: string
  created_at: number
  updated_at: number
}
// Responses never contain private_key / obfs_password. Write types are separate.
export type Inbound = InboundBase & (
  | { protocol: 'vless_reality'; options: RealityOptions }
  | { protocol: 'hysteria2'; options: Hysteria2Options }
)
export type Protocol = Inbound['protocol']
export type RealityInput = Partial<RealityOptions> & { private_key?: string }
export type Hysteria2Input = Partial<Omit<Hysteria2Options, 'port_hopping'>> & {
  obfs_password?: string
  port_hopping?: Partial<PortHopping>
}
export type InboundInput = Partial<Pick<InboundBase, 'tag' | 'port' | 'enabled' | 'remark'>> & (
  | { protocol: 'vless_reality'; options?: RealityInput }
  | { protocol: 'hysteria2'; options?: Hysteria2Input }
)
export type InboundSuggestion =
  | { protocol: 'vless_reality'; port: number; options: Omit<RealityOptions, 'public_key'> }
  | { protocol: 'hysteria2'; port: number; options: Hysteria2Options }

export interface User {
  id: number
  name: string
  uuid: string // masked; never use as a connection credential
  enabled: boolean
  traffic_limit: number
  traffic_up: number
  traffic_down: number
  expire_at: number
  remark: string
  created_at: number
  status: 'disabled' | 'expired' | 'exhausted' | 'active'
}
export interface UserInput { name: string; remark?: string; traffic_limit?: number; expire_at?: number }
export interface UserLinks {
  subscription: { auto: string; singbox: string; clash: string; base64: string }
  import: { singbox: string; clash: string }
  nodes: { inbound_id: number; remark: string; uri: string }[]
}
export interface RulesSettings {
  preset: 'bypass_cn' | 'gfw_only' | 'global'
  block_ads: boolean
  custom_direct: string[]
  custom_proxy: string[]
  source: 'metacubex' | 'sagernet'
  mirror: string
  update_interval: string
  dns: { remote: string; local: string; fakeip: boolean }
  ipv6: boolean
}
export type RulesPreview =
  | { format: 'singbox'; config: Record<string, unknown> }
  | { format: 'clash'; config: string }
export interface SingboxLogs { lines: string[] }
