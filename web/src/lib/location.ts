// Full-page navigation is reserved for backend-managed panel address changes.
export function visitPanel(url: string) { window.location.assign(url) }

export function panelAddressDiffers(url: string) {
  const target = new URL(url)
  return target.origin !== window.location.origin
}
