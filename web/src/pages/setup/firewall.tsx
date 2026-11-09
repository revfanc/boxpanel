import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { Button } from '@/components/ui/button'
import { Notice } from '@/components/ResourceUI'
import { ErrorNotice, Loading } from '@/components/Feedback'
import { settingsQuery } from '@/lib/query'
import { checkQuery, inboundsQuery } from '@/lib/resources'
import { api } from '@/lib/api'
import { useOperation } from '@/hooks/use-operation'
import type { PortProbe } from '@/types/api'
import type { StepProps } from './steps'
import { zhCN as t } from '@/i18n/zh-CN'

export function FirewallStep({ next, setBusy }: StepProps) {
  const settings = useQuery(settingsQuery)
  const nodes = useQuery(inboundsQuery)
  const check = useQuery(checkQuery)
  const probe = useOperation()
  const [results, setResults] = useState<PortProbe[]>([])
  const queries = [settings, nodes, check]
  if (queries.some((query) => query.isPending)) return <Loading />
  const failed = queries.find((query) => query.isError)
  if (failed) return <ErrorNotice error={failed.error} retry={() => { void failed.refetch() }} />
  if (!settings.data || !nodes.data || !check.data) return null
  const ports = [
    { key: 'panel', port: settings.data.web_port, proto: 'TCP', name: t.setup.panelPort },
    ...nodes.data.filter((node) => node.enabled).flatMap((node) => [
      { key: String(node.id), port: node.port, proto: node.protocol === 'vless_reality' ? 'TCP' : 'UDP', name: node.remark || node.tag },
      ...(node.protocol === 'hysteria2' && node.options.port_hopping.enabled ? [{ key: `${node.id}-hops`, port: `${node.options.port_hopping.start}–${node.options.port_hopping.end}`, proto: 'UDP', name: t.inbounds.hopping }] : []),
    ]),
  ]
  return <>
    <Notice>{check.data.items.some((item) => item.id === 'platform') ? t.setup.firewallDev : t.setup.firewall}</Notice>
    <ul className="space-y-2">{ports.map((port) => <li key={port.key} className="flex flex-wrap justify-between gap-3 rounded-lg border p-3"><span>{port.name}</span><strong>{port.port}/{port.proto}</strong></li>)}</ul>
    <Notice>{t.setup.udp}</Notice>
    <div className="space-y-2">{t.setup.providers.map((name) => <details key={name} className="rounded-lg border p-4"><summary className="cursor-pointer font-medium">{name}</summary><p className="mt-3 text-sm leading-6">{t.setup.providerHint}</p><p className="mt-2 text-sm text-muted-foreground">{t.setup.providerPlaceholder}</p></details>)}</div>
    <Button variant="outline" className="h-auto whitespace-normal py-2" disabled={probe.pending} onClick={() => {
      setBusy(true); setResults([])
      const tcp = [...new Set(ports.filter((port) => port.proto === 'TCP').map((port) => Number(port.port)))]
      void probe.run(() => Promise.all(tcp.map((port) => api<PortProbe>('/system/port-probe', { method: 'POST', body: { port } }))), setResults).finally(() => setBusy(false))
    }}>{probe.pending ? t.common.working : t.setup.probe}</Button>
    {probe.error != null && <ErrorNotice error={probe.error} />}
    {results.map((result) => <div key={result.port} className="space-y-2 text-sm" role="status">{result.results.map((address) => <p key={address.ip}>{address.ok ? t.setup.probeOK : t.setup.probeFailed} · {address.ip} · {result.port}/TCP</p>)}<p className="text-muted-foreground">{result.hint}</p></div>)}
    <p className="text-sm leading-6 text-muted-foreground">{t.setup.probeHint}</p>
    <Button disabled={probe.pending} onClick={() => { void next() }}>{t.setup.next}</Button>
  </>
}
