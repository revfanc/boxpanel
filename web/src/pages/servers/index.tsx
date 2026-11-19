import { useState } from 'react'
import { zodResolver } from '@hookform/resolvers/zod'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useForm } from 'react-hook-form'
import { z } from 'zod'
import { ErrorNotice, Loading } from '@/components/Feedback'
import { Empty, Field, Page, Panel, Submit } from '@/components/ResourceUI'
import { Button } from '@/components/ui/button'
import { useOperation } from '@/hooks/use-operation'
import { zhCN as t } from '@/i18n/zh-CN'
import { api } from '@/lib/api'
import { serversQuery } from '@/lib/resources'
import type { Server } from '@/types/api'

const renameSchema = z.object({
  name: z.string().refine((name) => {
    const length = Array.from(name.trim()).length
    return length >= 1 && length <= 64 && !/\p{Cc}/u.test(name)
  }, t.servers.invalidName).transform((name) => name.trim()),
})

function RenameForm({ server, close }: { server: Server; close: () => void }) {
  const client = useQueryClient()
  const operation = useOperation()
  const form = useForm<z.infer<typeof renameSchema>>({
    resolver: zodResolver(renameSchema),
    defaultValues: { name: server.name },
  })
  return <form className="space-y-3" onSubmit={form.handleSubmit((body) => operation.run(
    () => api<Server>(`/servers/${server.id}`, { method: 'PUT', body }),
    async (updated) => {
      await client.cancelQueries({ queryKey: serversQuery.queryKey })
      client.setQueryData(serversQuery.queryKey, (current) => current?.map((row) => row.id === updated.id ? updated : row))
      close()
    },
  ))}>
    <Field label={t.servers.name} error={form.formState.errors.name?.message} disabled={operation.pending} {...form.register('name')} />
    {operation.error != null && <ErrorNotice error={operation.error} />}
    <div className="flex flex-wrap gap-2">
      <Submit pending={operation.pending} />
      <Button type="button" variant="outline" disabled={operation.pending} onClick={close}>{t.common.cancel}</Button>
    </div>
  </form>
}

export function ServersPage() {
  const query = useQuery(serversQuery)
  const [editing, setEditing] = useState<number | null>(null)
  return <Page title={t.pages.servers}>
    {query.isPending ? <Loading /> : query.isError ? <ErrorNotice error={query.error} retry={() => { void query.refetch() }} />
      : query.data.length === 0 ? <Empty>{t.servers.empty}</Empty> : <div className="grid min-w-0 gap-4 lg:grid-cols-2">
        {query.data.map((server) => <Panel key={server.id} title={server.name}>
          <dl className="grid grid-cols-[auto_minmax(0,1fr)] gap-x-4 gap-y-2 text-sm">
            <dt className="text-muted-foreground">{t.servers.address}</dt><dd>{server.public_host || t.common.unknown}</dd>
            <dt className="text-muted-foreground">{t.servers.status}</dt><dd>{t.servers.statuses[server.status]}</dd>
            <dt className="text-muted-foreground">{t.servers.version}</dt><dd>{server.version || t.common.unknown}</dd>
          </dl>
          {editing === server.id
            ? <RenameForm server={server} close={() => setEditing(null)} />
            : <Button variant="outline" disabled={editing !== null} onClick={() => setEditing(server.id)}>{t.servers.rename}</Button>}
        </Panel>)}
      </div>}
  </Page>
}
