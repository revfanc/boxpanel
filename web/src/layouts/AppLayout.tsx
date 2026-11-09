import { useEffect, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Box, FileText, LayoutDashboard, LogOut, Menu, Network, Settings, Shuffle, Users, X, LoaderCircle } from 'lucide-react'
import { Link, NavLink, Outlet, useLocation, useNavigate } from 'react-router'
import { Button } from '@/components/ui/button'
import { Sidebar, SidebarContent, SidebarFooter, SidebarGroup, SidebarGroupLabel, SidebarHeader, SidebarMenu, SidebarMenuButton, SidebarMenuItem, SidebarProvider } from '@/components/ui/sidebar'
import { Sheet, SheetClose, SheetContent, SheetDescription, SheetHeader, SheetTitle, SheetTrigger } from '@/components/ui/sheet'
import { ThemeSwitch } from '@/components/ThemeSwitch'
import { ErrorNotice } from '@/components/Feedback'
import { api } from '@/lib/api'
import { replaceSession, sessionQuery, settingsQuery, systemQuery } from '@/lib/query'
import { cn } from '@/lib/utils'
import { zhCN } from '@/i18n/zh-CN'

const navigation = [
  { to: '/', label: zhCN.pages.home, icon: LayoutDashboard },
  { to: '/inbounds', label: zhCN.pages.inbounds, icon: Network },
  { to: '/users', label: zhCN.pages.users, icon: Users },
  { to: '/rules', label: zhCN.pages.rules, icon: Shuffle },
  { to: '/logs', label: zhCN.pages.logs, icon: FileText },
  { to: '/settings', label: zhCN.pages.settings, icon: Settings },
]

function Navigation({ close }: { close?: () => void }) {
  const location = useLocation()
  return <nav aria-label={zhCN.app.navigation}>
    <SidebarGroup>
      <SidebarGroupLabel>{zhCN.app.navigationGroup}</SidebarGroupLabel>
      <SidebarMenu className="gap-1">
        {navigation.map(({ to, label, icon: Icon }) => <SidebarMenuItem key={to}>
          <SidebarMenuButton asChild isActive={to === '/' ? location.pathname === '/' : location.pathname === to || location.pathname.startsWith(`${to}/`)} className="h-11 gap-3 px-3">
            <NavLink to={to} end={to === '/'} onClick={close}><Icon aria-hidden="true" /><span>{label}</span></NavLink>
          </SidebarMenuButton>
        </SidebarMenuItem>)}
      </SidebarMenu>
    </SidebarGroup>
  </nav>
}

function KernelStatus() {
  const status = useQuery(systemQuery)
  const current = status.data
  const running = !status.isError && current?.supported && current.singbox.running
  const text = status.isError ? zhCN.status.unavailable : status.isPending ? zhCN.status.loading : !current?.supported ? zhCN.status.unsupported : running ? zhCN.status.running : current.singbox.installed ? zhCN.status.stopped : zhCN.status.missing
  return <div className="flex min-w-0 items-center gap-2 text-xs text-muted-foreground" role="status">
    <span aria-hidden="true" className={cn('size-2 shrink-0 rounded-full', running ? 'bg-emerald-500' : status.isError ? 'bg-amber-500' : 'bg-muted-foreground/45')} />
    <span className="truncate">{zhCN.status.label} · {text}</span>
    {status.isError && <Button variant="ghost" size="sm" className="h-6 shrink-0 px-1 text-xs" onClick={() => { void status.refetch() }} disabled={status.isFetching} title={zhCN.status.errorHint}>{zhCN.common.retry}</Button>}
  </div>
}

// A separate Sheet supplies translated dialog labels without editing generated UI.
function MobileNavigation() {
  const [open, setOpen] = useState(false)
  useEffect(() => {
    const media = window.matchMedia('(min-width: 768px)')
    const closeOnDesktop = () => { if (media.matches) setOpen(false) }
    media.addEventListener('change', closeOnDesktop)
    return () => media.removeEventListener('change', closeOnDesktop)
  }, [])
  return <Sheet open={open} onOpenChange={setOpen}>
    <SheetTrigger asChild><Button className="shrink-0 md:hidden" variant="ghost" size="icon" aria-label={zhCN.app.openNavigation}><Menu aria-hidden="true" /></Button></SheetTrigger>
    <SheetContent side="left" className="max-w-[calc(100vw-3rem)] gap-2 overflow-y-auto bg-sidebar" showCloseButton={false}>
      <SheetHeader className="pr-14">
        <SheetTitle>{zhCN.app.navigation}</SheetTitle>
        <SheetDescription>{zhCN.app.navigationHint}</SheetDescription>
      </SheetHeader>
      <SheetClose asChild><Button className="absolute right-3 top-3" variant="ghost" size="icon" aria-label={zhCN.app.closeNavigation}><X aria-hidden="true" /></Button></SheetClose>
      <Navigation close={() => setOpen(false)} />
    </SheetContent>
  </Sheet>
}

export function AppLayout() {
  const settings = useQuery(settingsQuery)
  const session = useQuery(sessionQuery)
  const client = useQueryClient()
  const navigate = useNavigate()
  const location = useLocation()
  const name = settings.data?.panel_name || zhCN.app.name
  const logout = useMutation({
    mutationFn: () => api<void>('/auth/logout', { method: 'POST' }),
    onSuccess: () => {
      replaceSession(client, null)
      void navigate('/login', { replace: true })
    },
  })
  return <SidebarProvider className="min-w-0">
    <Link to={{ hash: '#main-content' }} onClick={() => document.getElementById('main-content')?.focus()} className="sr-only focus:not-sr-only focus:fixed focus:left-4 focus:top-3 focus:z-50 focus:rounded-md focus:bg-background focus:p-3 focus:shadow-lg">{zhCN.app.skipToContent}</Link>
    <Sidebar collapsible="none" className="sticky top-0 hidden h-svh w-60 shrink-0 border-r md:flex">
      <SidebarHeader className="px-5 py-6">
        <Link to="/" className="flex min-w-0 items-center gap-3 font-semibold tracking-tight"><Box aria-hidden="true" className="size-6 shrink-0 text-primary" /><span className="truncate">{name}</span></Link>
        <p className="pt-1 text-xs text-muted-foreground">{zhCN.app.description}</p>
      </SidebarHeader>
      <SidebarContent className="px-2"><Navigation /></SidebarContent>
      <SidebarFooter className="border-t px-6 py-4 text-xs text-muted-foreground">
        <span>{zhCN.app.account}</span><span className="truncate font-medium text-foreground">{session.data?.username}</span>
        {window.__BOXPANEL__?.version && <span className="truncate" title={zhCN.app.version}>{window.__BOXPANEL__.version}</span>}
      </SidebarFooter>
    </Sidebar>
    <div className="flex min-w-0 flex-1 flex-col">
      <header className="sticky top-0 z-20 flex min-h-20 min-w-0 items-center gap-2 border-b bg-background/95 px-3 backdrop-blur-sm sm:gap-3 sm:px-6">
        <MobileNavigation key={location.key} />
        <div className="min-w-0 flex-1 space-y-1">
          <p className="truncate text-sm font-semibold">{name}</p>
          <KernelStatus />
        </div>
        <ThemeSwitch />
        <Button variant="ghost" size="icon" className="shrink-0" aria-label={logout.isPending ? zhCN.app.loggingOut : zhCN.app.logout} title={zhCN.app.logout} disabled={logout.isPending} onClick={() => { if (!logout.isPending) logout.mutate() }}>
          {logout.isPending ? <LoaderCircle className="animate-spin motion-reduce:animate-none" aria-hidden="true" /> : <LogOut aria-hidden="true" />}
        </Button>
      </header>
      <main id="main-content" tabIndex={-1} className="mx-auto w-full min-w-0 max-w-6xl flex-1 p-4 outline-none sm:p-6 lg:p-8">
        {logout.isError && <div className="mb-5"><ErrorNotice error={logout.error} /></div>}
        {settings.isError && <div className="mb-5"><ErrorNotice error={settings.error} retry={() => { void settings.refetch() }} /></div>}
        <Outlet />
      </main>
    </div>
  </SidebarProvider>
}
