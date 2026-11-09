// Verified against the linked project-owned documentation on 2026-11-09.
// Download URLs are exclusively official GitHub Releases or official stores.
export const platforms = ['Windows', 'macOS', 'Android', 'iOS', 'Linux'] as const
export type Platform = typeof platforms[number]
export interface Client {
  name: string
  platforms: readonly Platform[]
  download: string
  source: string
  formats: readonly ['singbox' | 'clash' | 'base64', ...('singbox' | 'clash' | 'base64')[]]
  importTemplate?: string
  importSource?: string
}
export const clients: readonly Client[] = [
  {
    name: 'Clash Verge Rev', platforms: ['Windows', 'macOS', 'Linux'],
    download: 'https://github.com/clash-verge-rev/clash-verge-rev/releases',
    source: 'https://github.com/clash-verge-rev/clash-verge-rev', formats: ['clash'],
    importTemplate: 'clash://install-config?url={url}',
    importSource: 'https://www.clashverge.dev/guide/url_schemes.html',
  },
  {
    name: 'sing-box for Android', platforms: ['Android'],
    download: 'https://github.com/SagerNet/sing-box/releases',
    source: 'https://sing-box.sagernet.org/clients/android/', formats: ['singbox'],
    importTemplate: 'sing-box://import-remote-profile?url={url}#{name}',
    importSource: 'https://sing-box.sagernet.org/clients/general/',
  },
  {
    name: 'sing-box MT', platforms: ['iOS'],
    // This store ID is linked by the official Apple client download page.
    download: 'https://apps.apple.com/us/app/sing-box-mt/id6785326793',
    source: 'https://sing-box.sagernet.org/clients/apple/', formats: ['singbox'],
    importTemplate: 'sing-box://import-remote-profile?url={url}#{name}',
    importSource: 'https://sing-box.sagernet.org/clients/general/',
  },
]
export function clientImport(client: Client, url: string, name: string) {
  return client.importTemplate?.replace('{url}', encodeURIComponent(url)).replace('{name}', encodeURIComponent(name))
}
