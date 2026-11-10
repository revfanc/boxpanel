import { createContext, useContext } from 'react'

export interface PanelAddressChange { url: string; dev: boolean; readyAt: number }
export const PanelAddressContext = createContext<((change: PanelAddressChange) => void) | null>(null)

export function usePanelAddress() {
  const show = useContext(PanelAddressContext)
  if (!show) throw new Error('PanelAddressProvider is required')
  return show
}
