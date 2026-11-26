import type { ComponentProps } from 'react'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'

export function ChoiceSelect({ value, options, onChange, name, disabled, ...trigger }: Omit<ComponentProps<typeof SelectTrigger>, 'onChange' | 'value' | 'name' | 'disabled' | 'children'> & {
  value: string
  options: { value: string; label: string; disabled?: boolean }[]
  onChange: (value: string) => void
  name?: string
  disabled?: boolean
}) {
  return <Select name={name} value={value} items={options} disabled={disabled}
    onValueChange={(next) => { if (next !== null) onChange(next) }}>
    <SelectTrigger {...trigger}><SelectValue /></SelectTrigger>
    <SelectContent align="start" alignItemWithTrigger={false}>
      {options.map((option) => <SelectItem key={option.value} value={option.value} disabled={option.disabled}>{option.label}</SelectItem>)}
    </SelectContent>
  </Select>
}
