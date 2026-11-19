import { screen, waitFor } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import { BackupPanel } from './Backup'
import { mountPage, mockAPI, failure, deferred } from '@/test/resources'
import { zhCN as t } from '@/i18n/zh-CN'
const status = { last_attempt: 1, last_success: 1, result: 'success' }

describe('backup and restore', () => {
  it('shows status, validates passphrase and downloads a binary outside query caches', async () => {
    const create = vi.fn(() => 'blob:backup')
    vi.stubGlobal('URL', Object.assign(URL, { createObjectURL: create, revokeObjectURL: vi.fn() }))
    const clicked = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {})
    const fetch = mockAPI({ '/backup/status': () => Response.json(status), 'POST /backup': () => new Response(new Blob(['encrypted']), { headers: { 'Content-Type': 'application/octet-stream' } }) })
    const { user, client } = mountPage(<BackupPanel />)
    await waitFor(() => expect(screen.getAllByText(/1970-01-01/)).toHaveLength(2))
    expect(screen.getByText(t.backup.localWarning)).toBeVisible()
    await user.type(screen.getByLabelText(t.backup.passphrase), 'short')
    await user.click(screen.getByRole('button', { name: t.backup.download }))
    expect(await screen.findByText(t.backup.passphraseError)).toBeVisible()
    expect(fetch.mock.calls.filter(([, init]) => init?.method === 'POST')).toHaveLength(0)
    await user.clear(screen.getByLabelText(t.backup.passphrase))
    await user.type(screen.getByLabelText(t.backup.passphrase), 'long-test-passphrase')
    await user.click(screen.getByRole('button', { name: t.backup.download }))
    await waitFor(() => expect(clicked).toHaveBeenCalledTimes(1))
    expect(create).toHaveBeenCalledTimes(1)
    expect(screen.getByLabelText(t.backup.passphrase)).toHaveValue('')
    expect(JSON.stringify(client.getQueryCache().getAll().map((q) => q.state.data))).not.toContain('long-test-passphrase')
    expect(client.getMutationCache().getAll()).toHaveLength(0)
    clicked.mockRestore()
  })
  it('confirms before uploading, preserves errors and clears passphrase on acceptance', async () => {
    let fail = true
    const pending = deferred<Response>()
    const fetch = mockAPI({ '/backup/status': () => Response.json(status), 'POST /restore': () => fail ? failure(400, 'backup_invalid') : pending.promise })
    const { user } = mountPage(<BackupPanel />)
    await user.upload(screen.getByLabelText(t.backup.file), new File(['encrypted'], 'backup.tar.gz.enc'))
    await user.type(screen.getByLabelText(t.backup.restorePassphrase), 'long-test-passphrase')
    await user.click(screen.getByRole('button', { name: t.backup.restore }))
    expect(await screen.findByRole('alertdialog')).toHaveTextContent(t.backup.confirmHint)
    expect(fetch.mock.calls.filter(([url]) => String(url).endsWith('/restore'))).toHaveLength(0)
    await user.click(screen.getByRole('button', { name: t.common.cancel }))
    expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: t.backup.restore }))
    await user.click(screen.getByRole('button', { name: t.common.cancel }))
    expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument()
    expect(fetch.mock.calls.filter(([, init]) => init?.method === 'POST')).toHaveLength(0)
    await user.click(screen.getByRole('button', { name: t.backup.restore }))
    await user.click(screen.getByRole('button', { name: t.common.confirm }))
    expect(await screen.findByText('Try again')).toBeVisible()
    expect(screen.getByLabelText(t.backup.restorePassphrase)).toHaveValue('long-test-passphrase')
    fail = false
    await user.click(screen.getByRole('button', { name: t.common.confirm }))
    expect(screen.getByRole('button', { name: t.common.working })).toBeDisabled()
    pending.resolve(Response.json({ accepted: true }, { status: 202 }))
    expect(await screen.findByText(t.backup.restoring)).toBeVisible()
    expect(screen.getByLabelText(t.backup.restorePassphrase)).toHaveValue('')
    const init = fetch.mock.calls.filter(([url]) => String(url).endsWith('/restore')).at(-1)?.[1]
    expect(init?.body).toBeInstanceOf(FormData)
    expect((init?.body as FormData).get('confirm')).toBe('true')
    expect(init?.headers).not.toHaveProperty('Content-Type')
  })
  it('reports missing file and failed automatic backup with next steps', async () => {
    mockAPI({ '/backup/status': () => Response.json({ last_attempt: 1, last_success: 0, result: 'failed' }) })
    const { user } = mountPage(<BackupPanel />)
    expect(await screen.findByText(new RegExp(t.backup.failed))).toBeVisible()
    await user.type(screen.getByLabelText(t.backup.restorePassphrase), 'long-test-passphrase')
    await user.click(screen.getByRole('button', { name: t.backup.restore }))
    expect(await screen.findByText(t.backup.fileError)).toBeVisible()
    expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument()
  })
})
