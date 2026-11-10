import { z } from 'zod'
import data from './clients.json'

// Shared with the Go-rendered subscription page; verified sources: 2026-11-09.
const platform = z.enum(['Windows', 'macOS', 'Android', 'iOS', 'Linux'])
const format = z.enum(['singbox', 'clash', 'base64'])
const client = z.object({
  name: z.string().min(1),
  platforms: z.array(platform).min(1),
  download: z.url(), source: z.url(),
  formats: z.tuple([format]).rest(format),
  importTemplate: z.string().optional(), importSource: z.url().optional(),
})
const catalog = z.object({ platforms: z.array(platform), clients: z.array(client) }).parse(data)
export type Platform = z.infer<typeof platform>
export type Client = z.infer<typeof client>
export const platforms: readonly Platform[] = catalog.platforms
export const clients: readonly Client[] = catalog.clients
export function clientImport(client: Client, url: string, name: string) {
  return client.importTemplate?.replace('{url}', encodeURIComponent(url)).replace('{name}', encodeURIComponent(name))
}
