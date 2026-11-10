import { useEffect, useState, type ReactNode } from 'react'
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogDescription, DialogFooter } from '@/components/ui/dialog'
import { Button } from '@/components/ui/button'
import { CopyField } from '@/components/CopyField'
import { PanelAddressContext, type PanelAddressChange } from '@/lib/panel-address'
import { visitPanel } from '@/lib/location'
import { zhCN as t } from '@/i18n/zh-CN'

// Keep the saved destination outside route/session guards: certificate changes
// can make background requests to the old origin fail while the dialog is open.
export function PanelAddressProvider({ children }: { children: ReactNode }) {
  const [change, setChange] = useState<PanelAddressChange | null>(null)
  return <PanelAddressContext.Provider value={setChange}>
    {children}
    {change && <PanelAddressDialog key={`${change.url}:${change.readyAt}`} change={change} onClose={() => setChange(null)} />}
  </PanelAddressContext.Provider>
}

export function PanelAddressDialog({ change, onClose }: { change: PanelAddressChange; onClose: () => void }) {
  const [ready, setReady] = useState(() => Date.now() >= change.readyAt)
  useEffect(() => {
    if (ready) return
    const timer = window.setTimeout(() => setReady(true), Math.max(0, change.readyAt - Date.now()))
    return () => window.clearTimeout(timer)
  }, [change.readyAt, ready])
  return <Dialog open onOpenChange={(open) => { if (!open) onClose() }}>
    <DialogContent showCloseButton={false} className="max-h-[90svh] max-w-[calc(100vw-2rem)] overflow-y-auto sm:max-w-lg">
      <DialogHeader>
        <DialogTitle>{t.addressChange.title}</DialogTitle>
        <DialogDescription className="leading-6">{change.dev ? t.addressChange.dev : t.addressChange.hint}</DialogDescription>
      </DialogHeader>
      <CopyField label={t.addressChange.url} value={change.url} />
      {!change.dev && <p className="text-sm leading-6 text-muted-foreground">{t.addressChange.certificate}</p>}
      {!change.dev && !ready && <p role="status" className="text-sm text-muted-foreground">{t.addressChange.waiting}</p>}
      <DialogFooter>
        <Button variant="outline" onClick={onClose}>{change.dev ? t.addressChange.continue : t.addressChange.stay}</Button>
        {!change.dev && <Button disabled={!ready} onClick={() => visitPanel(change.url)}>{t.addressChange.go}</Button>}
      </DialogFooter>
    </DialogContent>
  </Dialog>
}
