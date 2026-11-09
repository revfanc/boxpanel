import { useRef, useState } from 'react'
import { useForm } from 'react-hook-form'
import { zodResolver } from '@hookform/resolvers/zod'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { Navigate, useSearchParams } from 'react-router'
import { Eye, EyeOff, LoaderCircle, LogIn } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Card, CardContent, CardDescription, CardHeader } from '@/components/ui/card'
import { ErrorNotice, Loading } from '@/components/Feedback'
import { AuthLayout } from '@/layouts/AuthLayout'
import { api, invalidResponse } from '@/lib/api'
import { safeNext } from '@/lib/navigation'
import { replaceSession, sessionQuery } from '@/lib/query'
import { loginSchema } from '@/schemas/auth'
import type { Admin, LoginInput } from '@/types/api'
import { zhCN } from '@/i18n/zh-CN'

export function LoginPage() {
  const client = useQueryClient()
  const session = useQuery(sessionQuery)
  const [params] = useSearchParams()
  const [showPassword, setShowPassword] = useState(false)
  const [error, setError] = useState<unknown>(null)
  const submitting = useRef(false)
  const form = useForm<LoginInput>({ resolver: zodResolver(loginSchema), defaultValues: { username: '', password: '' } })
  const onSubmit = async (values: LoginInput) => {
    if (submitting.current) return
    submitting.current = true
    setError(null)
    try {
      const admin = await api<Admin>('/auth/login', { method: 'POST', body: values })
      if (!admin || typeof admin.username !== 'string' || !admin.username) throw invalidResponse()
      form.reset({ username: '', password: '' })
      replaceSession(client, admin)
    } catch (cause) {
      setError(cause)
    } finally { submitting.current = false }
  }
  if (session.data) return <Navigate replace to={safeNext(params.get('next'))} />
  return <AuthLayout>
    {session.isPending ? <Loading label={zhCN.common.checkingSession} /> : session.isError ?
      <ErrorNotice error={session.error} retry={() => { void session.refetch() }} /> :
      <Card className="gap-6 py-7 shadow-sm">
        <CardHeader className="gap-2 px-6">
          <h1 className="text-2xl font-semibold tracking-tight">{zhCN.login.title}</h1>
          <CardDescription className="leading-relaxed">{zhCN.login.description}</CardDescription>
        </CardHeader>
        <CardContent className="space-y-6 px-6">
          <form onSubmit={(event) => { void form.handleSubmit(onSubmit)(event) }} noValidate className="space-y-5" aria-busy={form.formState.isSubmitting}>
            <div className="space-y-2">
              <Label htmlFor="username">{zhCN.login.username}</Label>
              <Input id="username" autoComplete="username" autoCapitalize="none" spellCheck={false} placeholder={zhCN.login.usernamePlaceholder}
                aria-invalid={!!form.formState.errors.username} aria-describedby={form.formState.errors.username ? 'username-error' : undefined}
                disabled={form.formState.isSubmitting} {...form.register('username')} />
              {form.formState.errors.username && <p id="username-error" role="alert" className="text-xs text-destructive">{form.formState.errors.username.message}</p>}
            </div>
            <div className="space-y-2">
              <Label htmlFor="password">{zhCN.login.password}</Label>
              <div className="relative">
                <Input id="password" type={showPassword ? 'text' : 'password'} autoComplete="current-password" placeholder={zhCN.login.passwordPlaceholder}
                  className="pr-11" aria-invalid={!!form.formState.errors.password} aria-describedby={form.formState.errors.password ? 'password-error' : undefined}
                  disabled={form.formState.isSubmitting} {...form.register('password')} />
                <Button type="button" variant="ghost" size="icon" className="absolute right-0 top-0" aria-label={showPassword ? zhCN.login.hidePassword : zhCN.login.showPassword}
                  aria-pressed={showPassword} onClick={() => setShowPassword(!showPassword)}>
                  {showPassword ? <EyeOff aria-hidden="true" /> : <Eye aria-hidden="true" />}
                </Button>
              </div>
              {form.formState.errors.password && <p id="password-error" role="alert" className="text-xs text-destructive">{form.formState.errors.password.message}</p>}
            </div>
            {error != null && <ErrorNotice error={error} />}
            <Button type="submit" className="w-full" disabled={form.formState.isSubmitting}>
              {form.formState.isSubmitting ? <LoaderCircle className="animate-spin motion-reduce:animate-none" aria-hidden="true" /> : <LogIn aria-hidden="true" />}
              {form.formState.isSubmitting ? zhCN.login.submitting : zhCN.login.submit}
            </Button>
          </form>
          <p className="border-t pt-4 text-xs leading-6 text-muted-foreground">{zhCN.login.help}<br /><code className="break-all font-mono text-foreground">{zhCN.login.resetCommand}</code></p>
        </CardContent>
      </Card>}
  </AuthLayout>
}
