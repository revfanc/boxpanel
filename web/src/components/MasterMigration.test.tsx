import { act, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { MasterMigration } from './MasterMigration'
import { mountPage, mockAPI } from '@/test/resources'
import { zhCN as t } from '@/i18n/zh-CN'

describe('master migration settings', () => {
  it('requires confirmation and renders a same-version command without caching the token', async () => {
    const token = 'migration-secret'
    const expiry = 2_000_000_000
    const fetch = mockAPI({
      '/migration': () => Response.json({ phase: 'idle', migrated: 0, total: 0 }),
      'POST /migration/token': () => Response.json({ source_url: 'https://8.8.8.8:2053/panel/', master_pin: 'sample-pin', token, version: 'v1.2.3-rc.1', expires_at: expiry }, { status: 201 }),
    })
    const { user, client } = mountPage(<MasterMigration publicHost="8.8.8.8" />)
    expect(screen.getByText(t.migration.ipHint)).toBeVisible()
    await user.click(await screen.findByRole('button', { name: t.migration.generate }))
    expect(screen.getByRole('alertdialog')).toHaveTextContent(t.migration.confirmHint)
    expect(screen.getByRole('alertdialog')).toHaveTextContent(t.migration.ipHint)
    expect(fetch.mock.calls.some(([, init]) => init?.method === 'POST')).toBe(false)
    await user.click(screen.getByRole('button', { name: t.common.confirm }))
    expect(await screen.findByLabelText(t.migration.command)).toHaveValue(`bash <(curl -fsSL https://raw.githubusercontent.com/revfanc/boxpanel/v1.2.3-rc.1/scripts/install.sh) --migrate-from 'https://8.8.8.8:2053/panel/' --token '${token}' --master-pin 'sample-pin' --version v1.2.3-rc.1`)
    expect(JSON.stringify(client.getQueryCache().getAll().map((q) => q.state.data))).not.toContain(token)
    expect(client.getMutationCache().getAll()).toHaveLength(0)
    await act(() => { client.setQueryData(['migration', 'status'], { phase: 'frozen', migrated: 0, total: 2 }) })
    expect(await screen.findByText(t.migration.frozen)).toBeVisible()
    expect(screen.queryByLabelText(t.migration.command)).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: t.migration.generate })).not.toBeInTheDocument()
  })

  it('lets the administrator abort a frozen migration after confirming', async () => {
    let phase = 'frozen'
    const fetch = mockAPI({
      '/migration': () => Response.json({ phase, migrated: 0, total: 2, expires_at: 2_000_000_000 }),
      'POST /migration/cancel': () => { phase = 'idle'; return new Response(null, { status: 204 }) },
    })
    const { user } = mountPage(<MasterMigration publicHost="panel.example.com" />)
    expect(await screen.findByText(t.migration.frozen)).toBeVisible()
    expect(screen.queryByRole('button', { name: t.migration.generate })).not.toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: t.migration.cancel }))
    expect(screen.getByRole('alertdialog')).toHaveTextContent(t.migration.cancelFrozen)
    expect(fetch.mock.calls.some(([url]) => String(url).endsWith('/migration/cancel'))).toBe(false)
    await user.click(screen.getByRole('button', { name: t.common.confirm }))
    expect(await screen.findByText(t.migration.cancelled)).toBeVisible()
    const call = fetch.mock.calls.find(([url]) => String(url).endsWith('/migration/cancel'))
    expect(call?.[1]?.body).toBe(JSON.stringify({ confirm: true }))
    expect(await screen.findByRole('button', { name: t.migration.generate })).toBeVisible()
    expect(screen.queryByRole('button', { name: t.migration.cancel })).not.toBeInTheDocument()
  })

  it('shows a manual switch command on the target and a shutdown hint on the retired master', async () => {
    const target = 'https://9.9.9.9:2053/panel/'
    mockAPI({ '/migration': () => Response.json({ phase: 'active', target_url: target, migrated: 1, total: 2, waiting: [{ id: 3, name: 'offline-edge' }] }) })
    const { client } = mountPage(<MasterMigration publicHost="panel.example.com" />)
    expect(await screen.findByText(t.migration.progress(1, 2))).toBeVisible()
    expect(screen.getByText(/offline-edge/)).toBeVisible()
    expect(screen.getByLabelText(t.migration.manual)).toHaveValue(`boxpanel set-master '${target}'`)
    expect(screen.queryByText(t.migration.ipHint)).not.toBeInTheDocument()
    await act(() => { client.setQueryData(['migration', 'status'], { phase: 'retired', target_url: target, migrated: 2, total: 2, core_stopped: true }) })
    expect(await screen.findByText(t.migration.shutdown)).toBeVisible()
    expect(screen.getByRole('link', { name: t.migration.newPanel })).toHaveAttribute('href', target)
    expect(screen.queryByLabelText(t.migration.manual)).not.toBeInTheDocument()
  })
})
