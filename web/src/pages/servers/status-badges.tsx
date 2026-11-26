import { Badge } from '@/components/ui/badge'
import { zhCN as t } from '@/i18n/zh-CN'
import type { Server } from '@/types/api'

const online = 'border-emerald-500/35 bg-emerald-500/10 text-emerald-800 dark:text-emerald-300'
const colors: Record<Server['status'], string> = {
  local: online,
  online,
  offline: 'border-amber-500/35 bg-amber-500/10 text-amber-800 dark:text-amber-300',
  pending: 'border-border bg-muted text-muted-foreground',
}
const failed = 'border-red-500/35 bg-red-500/10 text-red-800 dark:text-red-300'

export function ServerBadges({ server }: { server: Server }) {
  return <div className="flex flex-wrap items-center gap-1.5">
    <Badge variant="outline" className={colors[server.status]}>{t.servers.statuses[server.status]}</Badge>
    {server.sync_status && <Badge variant="outline"
      className={server.sync_status === 'failed' ? failed : undefined}>
      {server.sync_status === 'syncing' && server.status !== 'online'
        ? t.servers.syncWaitingOnline
        : t.servers.syncStatuses[server.sync_status]}
    </Badge>}
  </div>
}
