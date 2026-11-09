import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { RouterProvider } from 'react-router/dom'
import { QueryClientProvider } from '@tanstack/react-query'
import { ThemeProvider } from '@/components/ThemeProvider'
import { TooltipProvider } from '@/components/ui/tooltip'
import { createQueryClient } from '@/lib/query'
import { applyTheme, readTheme } from '@/lib/theme'
import { router } from '@/router'
import '@/index.css'

applyTheme(readTheme())
const queryClient = createQueryClient()

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      <ThemeProvider>
        <TooltipProvider><RouterProvider router={router} /></TooltipProvider>
      </ThemeProvider>
    </QueryClientProvider>
  </StrictMode>,
)
