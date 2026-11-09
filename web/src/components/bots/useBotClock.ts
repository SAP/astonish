import { useEffect, useState } from 'react'

// Keep time-dependent controls current without reading the clock during render.
export function useBotClock() {
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    const timer = setInterval(() => setNow(Date.now()), 1000)
    return () => clearInterval(timer)
  }, [])
  return now
}
