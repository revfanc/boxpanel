declare global {
  interface Window {
    __BOXPANEL__?: {
      base: string
      version: string
    }
  }
}

export const BASE = window.__BOXPANEL__?.base ?? ''
export const API_BASE = `${BASE}/api/v1`
