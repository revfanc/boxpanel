import { act, screen, waitFor, within } from '@testing-library/react'
import { expect, it } from 'vitest'
import { InboundsPage } from '.'
import { deferred, failure, inbound, mockAPI, mountPage, server } from '@/test/resources'
import { zhCN as t } from '@/i18n/zh-CN'

it('loads, renders an empty state, and recovers from a read error', async () => {
  const response = deferred<Response>()
  let first = true
  mockAPI({ '/inbounds': () => first ? response.promise : Response.json([]) })
  const { user } = mountPage(<InboundsPage />)
  expect(screen.getByText(t.common.loading)).toBeVisible()
  await act(async () => response.resolve(failure()))
  await screen.findByText('Try again')
  first = false
  await user.click(screen.getByRole('button', { name: t.common.retry }))
  expect(await screen.findByText(t.inbounds.empty)).toBeVisible()
})
it('confirms disable/delete, retains failures, prevents duplicate writes, and refreshes after deletion', async () => {
  let nodes = [inbound]
  let fail = true
  const response = deferred<Response>()
  const fetch = mockAPI({ '/inbounds': () => Response.json(nodes), 'POST /inbounds/1/toggle': () => failure(422), 'DELETE /inbounds/1': () => fail ? failure(409) : response.promise })
  const { user } = mountPage(<InboundsPage />)
  await user.click(await screen.findByRole('button', { name: t.common.disable }))
  expect(screen.getByRole('alertdialog')).toHaveTextContent(t.common.disableHint)
  await user.click(screen.getByRole('button', { name: t.common.cancel }))
  expect(fetch.mock.calls.filter(([, init]) => init?.method === 'POST')).toHaveLength(0)
  await user.click(screen.getByRole('button', { name: t.common.delete }))
  expect(screen.getByRole('alertdialog')).toHaveTextContent(t.inbounds.deleteHint)
  await user.click(screen.getByRole('button', { name: t.common.confirm }))
  await within(screen.getByRole('alertdialog')).findByText('Try again')
  fail = false
  await user.click(screen.getByRole('button', { name: t.common.confirm }))
  expect(screen.getByRole('button', { name: t.common.working })).toBeDisabled()
  nodes = []
  await act(async () => response.resolve(new Response(null, { status: 204 })))
  expect(await screen.findByText(t.inbounds.empty)).toBeVisible()
  await waitFor(() => expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument())
})

it('shows the server assignment without a selector', async () => {
  mockAPI({ '/inbounds': () => Response.json([inbound]) })
  mountPage(<InboundsPage />)
  expect(await screen.findByText(t.servers.owner('本机'))).toBeVisible()
  expect(screen.queryByRole('combobox')).not.toBeInTheDocument()
})

it('filters inbounds from the URL, updates it on selection and follows browser history', async () => {
  const edge = { ...server, id: 2, name: 'Edge', is_local: false, status: 'online' }
  const pending = { ...edge, id: 3, name: 'Pending', status: 'pending' }
  mockAPI({
    '/servers': () => Response.json([server, edge, pending]),
    '/inbounds': () => Response.json([inbound, { ...inbound, id: 2, server_id: 2, remark: 'Remote node' }]),
  })
  const { router, user } = mountPage(<InboundsPage />, '/inbounds?filter=2&keep=1', '/inbounds')
  const filter = await screen.findByRole('combobox', { name: t.inbounds.server })
  expect(filter).toHaveTextContent('Edge')
  expect(filter).toHaveClass('pr-2')
  expect(filter.querySelector('svg.lucide-chevron-down')).toHaveAttribute('aria-hidden', 'true')
  expect(await screen.findByText('Remote node')).toBeVisible()
  expect(screen.queryByText(inbound.remark)).not.toBeInTheDocument()
  expect(screen.getByRole('link', { name: t.pages.newInbound })).toHaveAttribute('href', '/inbounds/new?server=2')
  await user.click(filter)
  await user.click(await screen.findByRole('option', { name: 'Pending' }))
  expect(screen.getByText(t.inbounds.serverEmpty)).toBeVisible()
  expect(router.state.location.search).toBe('?filter=3&keep=1')
  await user.click(filter)
  await user.click(await screen.findByRole('option', { name: t.inbounds.allServers }))
  expect(router.state.location.search).toBe('?keep=1')
  expect(screen.getByText(inbound.remark)).toBeVisible()
  expect(screen.getByText('Remote node')).toBeVisible()
  await act(() => router.navigate(-1))
  expect(filter).toHaveTextContent('Pending')
  expect(screen.getByText(t.inbounds.serverEmpty)).toBeVisible()
})
