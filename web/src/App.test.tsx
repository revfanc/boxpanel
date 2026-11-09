import { renderToStaticMarkup } from 'react-dom/server'
import { createMemoryRouter, RouterProvider } from 'react-router'
import { describe, expect, it } from 'vitest'
import { routes } from '@/routes'
import { zhCN } from '@/i18n/zh-CN'

describe('placeholder routes', () => {
  it.each(['/', '/k3m9x2p7q1ab'])('renders and navigates under basename %s', async (basename) => {
    const base = basename === '/' ? '' : basename
    const router = createMemoryRouter(routes, {
      basename,
      initialEntries: [`${base}/users/3`],
    })
    try {
      const html = renderToStaticMarkup(<RouterProvider router={router} />)

      expect(html).toMatch(new RegExp(`<h1[^>]*>${zhCN.app.name}</h1>`))
      expect(html).toContain('data-slot="card"')
      expect(html).toContain(zhCN.app.description)
      expect(html).toContain(zhCN.app.placeholder)
      expect(html).toContain(zhCN.pages.userDetail)
      expect(html).toContain('<span>3</span>')
      expect(html).toContain(`href="${base}/users"`)

      await router.navigate('/users')
      expect(router.state.location.pathname).toBe(`${base}/users`)
      expect(renderToStaticMarkup(<RouterProvider router={router} />)).toContain(`href="${base}/users/3"`)

      await router.navigate('/')
      expect(router.state.location.pathname).toBe(base || '/')
      expect(renderToStaticMarkup(<RouterProvider router={router} />)).toMatch(
        new RegExp(`<h2[^>]*>${zhCN.pages.home}</h2>`),
      )
    } finally {
      router.dispose()
    }
  })
})
