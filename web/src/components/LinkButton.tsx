import { useRender } from '@base-ui/react/use-render'
import type { VariantProps } from 'class-variance-authority'
import { cn } from 'cn'
import { buttonVariants } from '@/components/ui/button'

export function LinkButton({ render, variant, size, className, ...props }:
  useRender.ComponentProps<'a'> & VariantProps<typeof buttonVariants>) {
  return useRender({
    defaultTagName: 'a',
    render,
    props: { ...props, className: cn(buttonVariants({ variant, size }), className) },
  })
}
