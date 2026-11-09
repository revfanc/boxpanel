import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'
import App from '@/App'
import { zhCN } from '@/i18n/zh-CN'

describe('scaffold', () => {
  it('renders the localized application shell', () => {
    const html = renderToStaticMarkup(<App />)

    expect(html).toMatch(new RegExp(`<h1[^>]*>${zhCN.app.name}</h1>`))
    expect(html).toContain('data-slot="card"')
    expect(html).toContain(zhCN.app.description)
    expect(html).toContain(zhCN.app.placeholder)
  })
})
