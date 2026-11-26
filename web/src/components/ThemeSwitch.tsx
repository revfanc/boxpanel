import { Monitor, Moon, Sun } from 'lucide-react'
import { buttonVariants } from '@/components/ui/button'
import { DropdownMenu, DropdownMenuContent, DropdownMenuRadioGroup, DropdownMenuRadioItem, DropdownMenuTrigger } from '@/components/ui/dropdown-menu'
import { isTheme, useTheme } from '@/lib/theme'
import { zhCN } from '@/i18n/zh-CN'

export function ThemeSwitch() {
  const { theme, setTheme } = useTheme()
  const Icon = theme === 'system' ? Monitor : theme === 'dark' ? Moon : Sun
  return (
    <DropdownMenu>
      <DropdownMenuTrigger className={buttonVariants({ variant: 'ghost', size: 'icon' })} aria-label={zhCN.theme.label} title={zhCN.theme.label}>
        <Icon aria-hidden="true" />
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end">
        <DropdownMenuRadioGroup value={theme} onValueChange={(value) => { if (isTheme(value)) setTheme(value) }}>
          <DropdownMenuRadioItem value="light" closeOnClick><Sun aria-hidden="true" />{zhCN.theme.light}</DropdownMenuRadioItem>
          <DropdownMenuRadioItem value="dark" closeOnClick><Moon aria-hidden="true" />{zhCN.theme.dark}</DropdownMenuRadioItem>
          <DropdownMenuRadioItem value="system" closeOnClick><Monitor aria-hidden="true" />{zhCN.theme.system}</DropdownMenuRadioItem>
        </DropdownMenuRadioGroup>
      </DropdownMenuContent>
    </DropdownMenu>
  )
}
