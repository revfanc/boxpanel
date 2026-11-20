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

export const setupSteps = ['welcome', 'check', 'scenario', 'node', 'user', 'firewall', 'finish'] as const
export type SetupStep = typeof setupSteps[number]
export interface SetupStatus { completed: boolean; step: SetupStep; domain: string; hysteria2: boolean }
export interface SetupResult extends SetupStatus { panel_url?: string; panel_tls_mode?: PanelSettings['panel_tls_mode'] }
export interface PortProbe { port: number; results: { ip: string; ok: boolean }[]; hint: string }

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
  subscription_auto_group: boolean
}
export interface SettingsResult extends PanelSettings { panel_url: string; restart_after: 0 | 2 }
export interface CertificateStatus {
  mode: 'http' | 'self_signed' | 'acme'
  type: 'http' | 'self_signed' | 'acme'
  host: string
  issuer: string
  not_before: number
  not_after: number
  last_attempt: number
  last_success: number
  last_error: string
  hint: string
  failures: number
  next_attempt: number
  issuing: boolean
  warning: boolean
}
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
export interface Server {
  id: number
  name: string
  public_host: string
  is_local: boolean
  status: 'local' | 'online' | 'offline' | 'pending'
  desired_generation?: number
  applied_generation?: number
  sync_status?: 'synced' | 'syncing' | 'failed'
  sync_error?: string
  singbox_active?: boolean
  uptime?: number
  inbound_counts?: { ready: number; pending: number }
  traffic_up?: number
  traffic_down?: number
  last_seen_at: number | null
  version: string
  created_at: number
  updated_at: number
}

export interface JoinToken {
  id: number
  name: string
  expires_at: number
  server_id?: number
}

export interface IssuedJoinToken extends JoinToken {
  token: string
  master_url: string
  master_pin: string
}

interface InboundBase {
  server_id: number
  key_status?: 'pending' | 'ready'
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
export type InboundInput = Partial<Pick<InboundBase, 'tag' | 'port' | 'enabled' | 'remark' | 'server_id'>> & (
  | { protocol: 'vless_reality'; options?: RealityInput }
  | { protocol: 'hysteria2'; options?: Hysteria2Input }
)
export type InboundSuggestion =
  | { protocol: 'vless_reality'; port: number; options: Omit<RealityOptions, 'public_key'>; hint?: string }
  | { protocol: 'hysteria2'; port: number; options: Hysteria2Options; hint?: string }

export interface User {
  id: number
  name: string
  uuid: string // masked; never use as a connection credential
  enabled: boolean
  traffic_reset_day: number
  traffic_reset_at: number
  traffic_limit: number
  traffic_up: number
  traffic_down: number
  expire_at: number
  remark: string
  created_at: number
  status: 'disabled' | 'expired' | 'exhausted' | 'active'
  traffic_by_server?: UserServerTraffic[]
}
export interface UserServerTraffic { server_id: number; name: string; is_local: boolean; up: number; down: number }
export interface UserInput { name: string; remark?: string; traffic_limit?: number; expire_at?: number; traffic_reset_day?: number }
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

export interface BackupStatus {
  last_attempt: number
  last_success: number
  result: '' | 'success' | 'failed'
}

export type BillingMode = 'out' | 'both' | 'max' | 'in'
export type NotificationChannel = 'bark' | 'server_chan' | 'telegram'
export type AlertKind = 'user_quota_80' | 'user_quota_100' | 'user_expiring' | 'user_expired' | 'user_stopped'
  | 'server_quota_80' | 'server_quota_90' | 'server_quota_100' | 'certificate_warning' | 'core_down' | 'core_recovered'

export interface AlertConfig {
  interface: string
  quota_gb: number
  billing_day: number
  billing_mode: BillingMode
  events: Record<AlertKind, boolean>
}

export interface NotificationChannelSettings {
  enabled: boolean
  url?: string
  credential_mask: string
  chat_id_mask?: string
}

export interface AlertSettings {
  config: AlertConfig
  channels: Record<NotificationChannel, NotificationChannelSettings>
  interfaces: string[]
}

export interface AlertEvent {
  key: string
  kind: AlertKind
  address: string
  username?: string
  used: number
  limit: number
  deadline?: number
  created_at: number
}

export interface NotificationDelivery {
  id: number
  channel: NotificationChannel
  kind: AlertKind | 'test'
  username?: string
  status: 'pending' | 'sent' | 'failed'
  attempts: number
  updated_at: number
  error?: string
  grouped: boolean
}

export interface AlertOverview {
  billing: {
    supported: boolean
    interface: string
    sampled_at: number
    stale: boolean
    starts_at: number
    ends_at: number
    billing_mode: BillingMode
    used: number
    quota: number
    percent: number
  }
  unread: AlertEvent[]
  unread_count: number
  recent: NotificationDelivery[]
}
