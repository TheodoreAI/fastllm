import { lazy, Suspense } from 'react'

const MessageContent = lazy(() => import('./MessageContent'))

export default function DeferredMessageContent(props) {
  return (
    <Suspense fallback={null}>
      <MessageContent {...props} />
    </Suspense>
  )
}
