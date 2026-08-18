// A minimal pub/sub so the many fetch* functions in api.js that swallow
// their failure and resolve to a safe default (empty array, null, etc. —
// by design, so callers don't need a null-check at every call site) can
// still surface "the backend is unreachable" somewhere, instead of that
// information only ever reaching the console. useConnectionStatus (in
// useConnectionStatus.js) subscribes to this from App.jsx to show one
// shared banner rather than teaching all 13 fetch functions to manage
// their own visible error state independently.
let offline = false
const listeners = new Set()

export function reportFetchFailure() {
  if (offline) return
  offline = true
  for (const fn of listeners) fn(offline)
}

export function reportFetchSuccess() {
  if (!offline) return
  offline = false
  for (const fn of listeners) fn(offline)
}

export function subscribeConnectionStatus(fn) {
  listeners.add(fn)
  return () => listeners.delete(fn)
}

export function isOffline() {
  return offline
}
