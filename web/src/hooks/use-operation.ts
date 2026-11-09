import { useRef, useState } from 'react'

// Keep request bodies (including passwords) out of the Query mutation cache.
// The ref prevents duplicate submission before React has rendered pending state.
export function useOperation() {
  const busy = useRef(false)
  const [pending, setPending] = useState(false)
  const [error, setError] = useState<unknown>(null)
  const [success, setSuccess] = useState(false)
  async function run<T>(request: () => Promise<T>, done?: (result: T) => void | Promise<void>) {
    if (busy.current) return
    busy.current = true
    setPending(true); setError(null); setSuccess(false)
    try {
      const result = await request()
      await done?.(result)
      setSuccess(true)
      return result
    } catch (failure) { setError(failure) }
    finally { busy.current = false; setPending(false) }
  }
  return { pending, error, success, run, reset: () => { setError(null); setSuccess(false) } }
}
