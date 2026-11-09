import { useState } from 'react'
import { Navigate, useNavigate, useParams } from 'react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { SetupLayout } from '@/layouts/SetupLayout'
import { Button } from '@/components/ui/button'
import { ErrorNotice, Loading } from '@/components/Feedback'
import { Notice } from '@/components/ResourceUI'
import { useOperation } from '@/hooks/use-operation'
import { api } from '@/lib/api'
import { setupQuery, queryKeys } from '@/lib/query'
import { setupSteps, type SetupStatus, type SetupStep } from '@/types/api'
import { CheckStep, ScenarioStep, NodeStep, UserStep } from './steps'
import { FirewallStep } from './firewall'
import { FinishStep } from './finish'
import { zhCN as t } from '@/i18n/zh-CN'

export function SetupPage() {
  const setup = useQuery(setupQuery)
  const { step } = useParams()
  if (setup.isPending) return <Loading />
  if (setup.isError) return <ErrorNotice error={setup.error} retry={() => { void setup.refetch() }} />
  // The server is authoritative, including for stale bookmarks/browser Back.
  if (step !== setup.data.step) return <Navigate replace to={`/setup/${setup.data.step}`} />
  return <Wizard key={step} status={setup.data} />
}

function Wizard({ status }: { status: SetupStatus }) {
  const operation = useOperation()
  const [childBusy, setChildBusy] = useState(false)
  const client = useQueryClient()
  const navigate = useNavigate()
  const busy = operation.pending || childBusy
  async function save(patch: Partial<SetupStatus>, leave = false) {
    await operation.run(() => api<SetupStatus>('/setup/status', { method: 'PUT', body: patch }), async (saved) => {
      await client.cancelQueries({ queryKey: queryKeys.setup })
      client.setQueryData(queryKeys.setup, saved)
      void client.invalidateQueries({ queryKey: queryKeys.settings })
      void navigate(leave ? '/' : `/setup/${saved.step}`, { replace: true })
    })
  }
  const next = () => save({ step: setupSteps[Math.min(6, setupSteps.indexOf(status.step) + 1)] })
  const stepProps = { next, setBusy: setChildBusy }
  return <SetupLayout step={status.step} busy={busy} skip={() => { void save({ completed: true }, true) }} back={() => { void save({ step: setupSteps[Math.max(0, setupSteps.indexOf(status.step) - 1)] as SetupStep }) }}>
    {operation.error != null && <ErrorNotice error={operation.error} />}
    {operation.pending && <Loading label={t.common.saving} />}
    <fieldset aria-busy={busy} disabled={operation.pending} className="min-w-0 space-y-5">
      {status.step === 'welcome' && <><p className="leading-7">{t.setup.welcome}</p><Notice>{t.setup.certificate}</Notice><Button onClick={() => { void next() }}>{t.setup.start}</Button></>}
      {status.step === 'check' && <CheckStep {...stepProps} />}
      {status.step === 'scenario' && <ScenarioStep status={status} save={save} />}
      {status.step === 'node' && <NodeStep status={status} {...stepProps} />}
      {status.step === 'user' && <UserStep {...stepProps} />}
      {status.step === 'firewall' && <FirewallStep {...stepProps} />}
      {status.step === 'finish' && <FinishStep complete={() => { void save({ completed: true, step: 'finish' }, true) }} />}
    </fieldset>
  </SetupLayout>
}
