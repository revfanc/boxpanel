/// <reference types="node" />
import { readFileSync } from 'node:fs'
import { fireEvent, screen, waitFor } from '@testing-library/react'
import { afterEach, expect, it, vi } from 'vitest'
import { clients, platforms } from './clients'
import { subscriptionText as t } from '@/i18n/zh-CN'

// Vitest runs from web/, like the rest of the frontend test suite.
const html = readFileSync('../internal/subscription/testdata/page.golden', 'utf8')
function mountPage(script = false) {
  const parsed = new DOMParser().parseFromString(html, 'text/html')
  document.body.innerHTML = parsed.body.innerHTML
  if (script) {
    // Execute only the checked-in, Go-rendered inline script; jsdom does not
    // implement CSP. Response nonce/header matching is covered by Go tests.
    const source = document.querySelector('script')!.textContent!
    new Function(source)()
  }
}
afterEach(() => { document.body.innerHTML = ''; vi.useRealTimers() })

it('is complete without JavaScript and shares all official clients with the panel', () => {
  mountPage()
  expect(screen.getByRole('heading', { name: 'boxpanel' })).toBeVisible()
  expect(screen.getByText('alice')).toBeVisible()
  expect(screen.getByText(t.active)).toBeVisible()
  expect(screen.getByLabelText(t.subscription, { selector: 'input' })).toHaveAttribute('readonly')
  expect(screen.getByRole('link', { name: t.singboxImport })).toHaveAttribute('href', expect.stringMatching(/^sing-box:\/\//))
  expect(screen.getByRole('link', { name: t.clashImport })).toHaveAttribute('href', expect.stringMatching(/^clash:\/\//))
  const details = document.querySelector('details')!
  details.open = true
  for (const label of [t.singbox, t.clash, t.base64]) expect(screen.getByLabelText(label, { selector: 'input' })).toHaveAttribute('readonly')
  for (const client of clients) {
    expect(screen.getByRole('link', { name: `${t.officialDownload} ${client.name}` })).toHaveAttribute('href', client.download)
    expect(screen.getByText(client.platforms.join(' / '))).toBeVisible()
  }
  for (const platform of platforms) expect(clients.some((client) => client.platforms.includes(platform))).toBe(true)
  expect([...document.querySelectorAll('[src]')].map((node) => node.getAttribute('src'))).toEqual([expect.stringMatching(/^data:image\/png;base64,/)])
})

it('copies each exact address and restores the button after a short success message', async () => {
  vi.useFakeTimers()
  const writeText = vi.fn().mockResolvedValue(undefined)
  vi.stubGlobal('navigator', { clipboard: { writeText } })
  mountPage(true)
  document.querySelector('details')!.open = true
  for (const label of [t.subscription, t.singbox, t.clash, t.base64]) {
    const button = screen.getByRole('button', { name: `${t.copy} ${label}` })
    fireEvent.click(button)
    await Promise.resolve()
    expect(writeText).toHaveBeenLastCalledWith((screen.getByLabelText(label, { selector: 'input' }) as HTMLInputElement).value)
    expect(button).toHaveTextContent(t.copied)
    expect(button).toBeDisabled()
    vi.advanceTimersByTime(2000)
    expect(button).toHaveTextContent(t.copy)
    expect(button).toBeEnabled()
  }
})

it.each([false, true])('selects the address with an actionable hint when clipboard is unavailable or rejects: %s', async (rejects) => {
  vi.stubGlobal('navigator', rejects ? { clipboard: { writeText: vi.fn().mockRejectedValue(new Error('denied')) } } : {})
  mountPage(true)
  fireEvent.click(screen.getByRole('button', { name: `${t.copy} ${t.subscription}` }))
  await waitFor(() => expect(screen.getByRole('status')).toHaveTextContent(t.copyFailed))
  const input = screen.getByLabelText(t.subscription, { selector: 'input' }) as HTMLInputElement
  expect(input).toHaveFocus()
  expect(input.selectionStart).toBe(0)
  expect(input.selectionEnd).toBe(input.value.length)
  expect(screen.getByRole('button', { name: `${t.copy} ${t.subscription}` })).toBeEnabled()
})
