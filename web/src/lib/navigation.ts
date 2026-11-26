// next is a router-relative absolute path, never a URL or a basename-prefixed path.
function unsafeCharacters(value: string) {
  return value.includes('\\') || Array.from(value).some((char) => char.charCodeAt(0) <= 32)
}

export function safeNext(value: string | null): string {
  if (!value?.startsWith('/') || value.startsWith('//') || unsafeCharacters(value)) return '/'
  try {
    const decoded = decodeURIComponent(value)
    if (decoded.startsWith('//') || unsafeCharacters(decoded)) return '/'
    const url = new URL(value, 'https://boxpanel.invalid')
    if (url.origin !== 'https://boxpanel.invalid' || /^\/login(?:\/|$)/i.test(decodeURIComponent(url.pathname))) return '/'
    return `${url.pathname}${url.search}${url.hash}`
  } catch { return '/' }
}

export function loginPath(next: string) {
  return `/login?${new URLSearchParams({ next: safeNext(next) })}`
}

export function serverID(value: string | null | undefined) {
  if (!value || !/^\d+$/.test(value)) return undefined
  const id = Number(value)
  return Number.isSafeInteger(id) && id > 0 ? id : undefined
}

export function serverPath(id: number, tab?: 'inbounds' | 'traffic' | 'alerts' | 'logs') {
  return `/servers/${id}${tab ? `?tab=${tab}` : ''}` as const
}
