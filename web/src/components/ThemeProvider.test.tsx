import { act, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { expect, it, vi } from 'vitest'
import { ThemeProvider } from './ThemeProvider'
import { ThemeSwitch } from './ThemeSwitch'
import { THEME_KEY } from '@/lib/theme'
import { zhCN } from '@/i18n/zh-CN'

it('follows system changes, persists explicit choices and synchronizes storage', async () => {
  const changes = new Set<() => void>()
  const media = { matches: true, addEventListener: (_: string, callback: () => void) => changes.add(callback), removeEventListener: (_: string, callback: () => void) => changes.delete(callback) }
  vi.stubGlobal('matchMedia', vi.fn(() => media))
  const user = userEvent.setup()
  const view = render(<ThemeProvider><ThemeSwitch /></ThemeProvider>)
  expect(document.documentElement).toHaveClass('dark')
  act(() => { media.matches = false; changes.forEach((change) => change()) })
  expect(document.documentElement).not.toHaveClass('dark')
  await user.click(screen.getByRole('button', { name: zhCN.theme.label }))
  await user.click(screen.getByRole('menuitemradio', { name: zhCN.theme.dark }))
  expect(document.documentElement).toHaveClass('dark')
  expect(localStorage.getItem(THEME_KEY)).toBe('dark')
  act(() => { media.matches = false; changes.forEach((change) => change()) })
  expect(document.documentElement).toHaveClass('dark')
  await user.click(screen.getByRole('button', { name: zhCN.theme.label }))
  await user.click(screen.getByRole('menuitemradio', { name: zhCN.theme.light }))
  expect(document.documentElement).not.toHaveClass('dark')
  act(() => window.dispatchEvent(new StorageEvent('storage', { key: THEME_KEY, newValue: 'dark' })))
  expect(document.documentElement).toHaveClass('dark')
  act(() => window.dispatchEvent(new StorageEvent('storage', { key: THEME_KEY, newValue: null })))
  expect(document.documentElement).not.toHaveClass('dark')
  view.unmount()
  expect(changes.size).toBe(0)
})

it('restores a saved choice and still works when storage is unavailable', async () => {
  localStorage.setItem(THEME_KEY, 'dark')
  const saved = render(<ThemeProvider><ThemeSwitch /></ThemeProvider>)
  expect(document.documentElement).toHaveClass('dark')
  saved.unmount()
  vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => { throw new Error('blocked') })
  vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => { throw new Error('blocked') })
  render(<ThemeProvider><ThemeSwitch /></ThemeProvider>)
  expect(document.documentElement).not.toHaveClass('dark')
  const user = userEvent.setup()
  await user.click(screen.getByRole('button', { name: zhCN.theme.label }))
  await user.click(screen.getByRole('menuitemradio', { name: zhCN.theme.dark }))
  expect(document.documentElement).toHaveClass('dark')
})
