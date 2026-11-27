import type { ReactNode } from 'react'
import { Link } from 'react-router'
import { zhCN as t } from '@/i18n/zh-CN'
import { serverPath } from '@/lib/navigation'
import type { Server } from '@/types/api'
import { ServerBadges } from './status-badges'

export function ServerTable<T extends Server>({ servers, columns, children }: {
  servers: T[]
  columns: string[]
  children: (server: T) => ReactNode
}) {
  return <div className="min-w-0 overflow-x-auto rounded-xl border bg-card">
    <table className="w-full text-left text-sm tabular-nums" aria-label={t.pages.servers}>
      <thead className="border-b bg-muted/40 text-muted-foreground">
        <tr>{[t.servers.name, t.servers.tabs.status, ...columns].map((column) =>
          <th key={column} scope="col" className="whitespace-nowrap px-4 py-3 font-medium">{column}</th>)}</tr>
      </thead>
      <tbody className="divide-y">
        {servers.map((server) => <tr key={server.id} className="align-top hover:bg-muted/20 [&>td]:px-4 [&>td]:py-3">
          <th scope="row" className="min-w-36 max-w-64 px-4 py-3 font-normal">
            <Link to={serverPath(server.id)} className="font-semibold underline-offset-4 hover:underline">{server.name}</Link>
            {server.public_host && <div className="mt-1 text-xs text-muted-foreground">{server.public_host}</div>}
          </th>
          <td><ServerBadges server={server} /></td>
          {children(server)}
        </tr>)}
      </tbody>
    </table>
  </div>
}
