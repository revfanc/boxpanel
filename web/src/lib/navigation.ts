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
