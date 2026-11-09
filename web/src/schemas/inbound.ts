import { z } from 'zod'
import { hostSchema, portSchema, remarkSchema, byteLength } from '@/schemas/settings'
import type { Inbound, InboundInput, InboundSuggestion, Protocol } from '@/types/api'
import { zhCN as t } from '@/i18n/zh-CN'

export const inboundSchema = z.object({
  protocol: z.enum(['vless_reality', 'hysteria2']), port: portSchema.optional(),
  tag: z.string().refine((value) => !value || /^[a-zA-Z0-9_-]{1,64}$/.test(value), t.inbounds.tagInvalid),
  remark: remarkSchema, enabled: z.boolean(), server_name: z.string(), handshake_server: z.string(),
  handshake_port: z.number(), fingerprint: z.string(), short_ids: z.string(),
  sni: z.string(), up_mbps: z.number(), down_mbps: z.number(),
  obfs_password: z.string(), clear_obfs: z.boolean(), hopping: z.boolean(), start: z.number(), end: z.number(),
}).superRefine((value, ctx) => {
  const error = (path: string, message: string) => ctx.addIssue({ code: 'custom', path: [path], message })
  if (value.protocol === 'vless_reality') {
    if (!hostSchema.safeParse(value.server_name).success) error('server_name', t.settings.hostInvalid)
    if (value.handshake_server && !hostSchema.safeParse(value.handshake_server).success) error('handshake_server', t.settings.hostInvalid)
    if (!portSchema.safeParse(value.handshake_port).success) error('handshake_port', t.inbounds.portInvalid)
    if (!/^[a-zA-Z0-9_-]{1,64}$/.test(value.fingerprint)) error('fingerprint', t.inbounds.tagInvalid)
    const ids = value.short_ids.split(',').map((id) => id.trim())
    if (value.short_ids && (ids.length > 16 || ids.some((id) => !/^(?:[a-fA-F0-9]{2}){4,8}$/.test(id)))) error('short_ids', t.inbounds.shortInvalid)
  } else {
    if (!hostSchema.safeParse(value.sni).success) error('sni', t.settings.hostInvalid)
    for (const name of ['up_mbps', 'down_mbps'] as const) if (!Number.isSafeInteger(value[name]) || value[name] < 0) error(name, t.common.invalid)
    if (!portSchema.safeParse(value.start).success) error('start', t.inbounds.portInvalid)
    if (!portSchema.safeParse(value.end).success) error('end', t.inbounds.portInvalid)
    if (value.start > value.end) error('end', t.inbounds.rangeInvalid)
    if (byteLength(value.obfs_password) > 1024) error('obfs_password', t.login.passwordTooLong)
  }
})
export type InboundFormValues = z.infer<typeof inboundSchema>
export function inboundDefaults(protocol: Protocol, source?: Inbound | InboundSuggestion): InboundFormValues {
  const reality = source?.protocol === 'vless_reality' ? source.options : undefined
  const hy2 = source?.protocol === 'hysteria2' ? source.options : undefined
  const existing = source && 'id' in source ? source : undefined
  return {
    protocol, port: source?.port, tag: existing?.tag ?? '', remark: existing?.remark ?? '', enabled: existing?.enabled ?? true,
    server_name: reality?.server_name ?? '', handshake_server: reality?.handshake_server ?? '', handshake_port: reality?.handshake_port ?? 443,
    fingerprint: reality?.fingerprint ?? 'chrome', short_ids: reality?.short_ids.join(', ') ?? '',
    sni: hy2?.sni ?? 'www.bing.com', up_mbps: hy2?.up_mbps ?? 0, down_mbps: hy2?.down_mbps ?? 0,
    obfs_password: '', clear_obfs: false, hopping: hy2?.port_hopping.enabled ?? false,
    start: hy2?.port_hopping.start ?? 20000, end: hy2?.port_hopping.end ?? 30000,
  }
}
export function inboundBody(values: InboundFormValues): InboundInput {
  const common = { port: values.port, tag: values.tag || undefined, remark: values.remark, enabled: values.enabled }
  return values.protocol === 'vless_reality' ? {
    ...common, protocol: values.protocol, options: {
      server_name: values.server_name, handshake_server: values.handshake_server || values.server_name, handshake_port: values.handshake_port,
      fingerprint: values.fingerprint, ...(values.short_ids ? { short_ids: values.short_ids.split(',').map((id) => id.trim()) } : {}),
    },
  } : {
    ...common, protocol: values.protocol, options: {
      sni: values.sni, up_mbps: values.up_mbps, down_mbps: values.down_mbps,
      port_hopping: { enabled: values.hopping, start: values.start, end: values.end },
      ...(values.clear_obfs ? { obfs_password: '' } : values.obfs_password ? { obfs_password: values.obfs_password } : {}),
    },
  }
}
