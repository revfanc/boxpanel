import { act, screen, waitFor, within } from '@testing-library/react'
import { expect, it } from 'vitest'
import { InboundsPage } from '.'
import { deferred, failure, inbound, mockAPI, mountPage } from '@/test/resources'
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
