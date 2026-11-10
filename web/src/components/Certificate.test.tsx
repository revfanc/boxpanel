import { act, screen, waitFor } from '@testing-library/react'
import { expect, it } from 'vitest'
import { CertificatePanel, CertificateWarning, SetupCertificate } from './Certificate'
import { certificate, deferred, failure, mockAPI, mountPage, settings } from '@/test/resources'
import { zhCN as t } from '@/i18n/zh-CN'

it('shows failure guidance and metadata, requests issuance, then switches back through settings', async () => {
  let status = { ...certificate, mode: 'acme', type: 'self_signed', host: 'example.com', issuer: 'Test issuer', last_attempt: 10, not_after: 1800000000, last_error: 'acme_port_unavailable', hint: 'Release TCP 80 and retry', next_attempt: 20 }
  const issuing = deferred<Response>()
  const fetch = mockAPI({ '/cert': () => Response.json(status), 'POST /cert/issue': () => issuing.promise, 'PUT /settings': () => { status = { ...status, mode: 'self_signed' }; return Response.json({ ...settings, panel_tls_mode: 'self_signed' }) } })
  const { user } = mountPage(<CertificatePanel />)
  await screen.findByText('Release TCP 80 and retry')
  expect(screen.getByText('Test issuer')).toBeVisible()
  expect(screen.getByText(t.cert.types.self_signed)).toBeVisible()
  await user.click(screen.getByRole('button', { name: t.cert.issue }))
  expect(screen.getByRole('button', { name: t.cert.issuing })).toBeDisabled()
  status = { ...status, type: 'acme', last_error: '', hint: '' }
  await act(async () => issuing.resolve(Response.json({ accepted: true }, { status: 202 })))
  await screen.findByText(t.cert.accepted)
  expect(await screen.findByText(t.cert.types.acme)).toBeVisible()
  await user.click(screen.getByRole('button', { name: t.cert.selfSigned }))
  await screen.findByText(t.cert.switched)
  expect(fetch.mock.calls.some(([url, init]) => url.endsWith('/settings') && init?.method === 'PUT' && JSON.parse(String(init?.body)).panel_tls_mode === 'self_signed')).toBe(true)
})

it('reports request errors and disables issuance in dev or while issuing', async () => {
  let status = { ...certificate, type: 'acme', mode: 'acme' }
  mockAPI({ '/cert': () => Response.json(status), 'POST /cert/issue': () => failure(409, 'acme_busy') })
  const { user, client } = mountPage(<CertificatePanel />)
  await user.click(await screen.findByRole('button', { name: t.cert.issue }))
  await screen.findByText('Try again')
  status = { ...status, type: 'http', mode: 'http' }
  await act(async () => { await client.invalidateQueries({ queryKey: ['cert'] }) })
  expect(await screen.findByText(t.cert.dev)).toBeVisible()
  expect(screen.queryByRole('button', { name: t.cert.issue })).not.toBeInTheDocument()
})

it('makes expiring renewal failures prominent and explains self-signed TLS only when active', async () => {
  let status = { ...certificate, type: 'self_signed', mode: 'acme', warning: true, hint: 'Check TCP 80' }
  mockAPI({ '/cert': () => Response.json(status) })
  const { client } = mountPage(<><CertificateWarning /><SetupCertificate /></>)
  expect(await screen.findByRole('alert')).toHaveTextContent(t.cert.expiring)
  expect(screen.getByRole('link', { name: t.cert.settings })).toHaveAttribute('href', '/settings')
  expect(screen.getByText(t.setup.tlsHint, { exact: false })).toBeVisible()
  status = { ...status, type: 'acme', warning: false, hint: '' }
  await act(async () => { await client.invalidateQueries({ queryKey: ['cert'] }) })
  await waitFor(() => expect(screen.queryByRole('alert')).not.toBeInTheDocument())
  expect(screen.queryByText(t.setup.tlsHint, { exact: false })).not.toBeInTheDocument()
  expect(screen.getByText(t.cert.acmeActive)).toBeVisible()
})
