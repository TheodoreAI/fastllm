import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import './index.css'
import App from './App.jsx'
// Dev tool, not a feature — floating panel for live-tweaking design tokens
// (color/typography/spacing/motion), live WCAG contrast readouts on every
// color pair, and a click-to-copy element inspector. fastllm is a
// single-user local desktop app with no dev/prod split to gate on, so this
// stays always-available but toggle-hidden (collapsed behind the 🎛
// button) rather than shown by default. See TweakBar.jsx for the full
// rationale. Rendered as a sibling of <App/>, not inside it — it's global
// chrome, not app UI, and position: fixed means DOM nesting doesn't matter.
import TweakBar from './components/TweakBar.jsx'

createRoot(document.getElementById('root')).render(
  <StrictMode>
    <App />
    <TweakBar />
  </StrictMode>,
)
