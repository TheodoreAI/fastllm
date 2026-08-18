import { useEffect, useState } from 'react'
import { isOffline, subscribeConnectionStatus } from './connectionStatus'

// Tracks whether the most recent background fetch (conversations, models,
// skills, documents, editor tree, git status, …) succeeded — see
// connectionStatus.js for why this is a shared signal rather than each of
// api.js's 13 silently-defaulting fetch functions managing its own
// visible error state.
export function useConnectionStatus() {
  const [offline, setOffline] = useState(isOffline)

  useEffect(() => {
    return subscribeConnectionStatus(setOffline)
  }, [])

  return offline
}
