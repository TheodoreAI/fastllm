// Web-safe font stacks only — no external font loading. Shared between
// TweakBar.jsx (the <select>) and its style-injection logic.
export const FONT_STACKS = {
  'System UI (default)':
    "-apple-system, BlinkMacSystemFont, 'SF Pro Text', 'Segoe UI', 'Ubuntu', 'Cantarell', system-ui, sans-serif",
  Arial: 'Arial, Helvetica, sans-serif',
  Verdana: 'Verdana, Geneva, sans-serif',
  Tahoma: 'Tahoma, Geneva, sans-serif',
  'Trebuchet MS': "'Trebuchet MS', Helvetica, sans-serif",
  Georgia: 'Georgia, Cambria, serif',
  'Times New Roman': "'Times New Roman', Times, serif",
  Garamond: 'Garamond, Baskerville, serif',
  'Courier New': "'Courier New', Courier, monospace",
  Consolas: "Consolas, 'SF Mono', monospace",
  'Comic Sans MS': "'Comic Sans MS', 'Comic Sans', cursive",
  Impact: 'Impact, Haettenschweiler, sans-serif',
}

// accent-contrast text sits directly on the user-bubble background (real
// body text, so AA needs 4.5:1); warning/danger/success are status/icon
// text used against the app's general panel surfaces (AA large-text
// threshold, 3:1) — see the pairs list in TweakBar.jsx's useContrastPairs.
export const DEFAULTS = {
  fontFamily: FONT_STACKS['System UI (default)'],
  fontWeight: '400',
  fontSize: 1, // maps to --font-scale
  letterSpacing: 0, // px, applied to body text

  textColor: null, // resolved from --text at first open
  textDimColor: null, // --text-dim
  bgColor: null, // --bg-chat / --bg-window
  bgPanelColor: null, // --bg-panel
  bubbleUserColor: null, // --bg-bubble-user (accent-contrast text sits on this)
  accentColor: null, // --accent

  spaceScale: 1, // multiplies --space-1..5
  radius: null, // px, overrides --radius/--radius-sm/--radius-window

  motionPreset: 'none',
  motionSpeed: 1,
}

export const STORAGE_KEY = 'fastllm-tweak-bar-state'

export const MOTION_KEYFRAMES = {
  'fade-in': '@keyframes tweakbar-fade-in { from { opacity: 0; } to { opacity: 1; } }',
  'rise-in':
    '@keyframes tweakbar-rise-in { from { opacity: 0; transform: translateY(8px); } to { opacity: 1; transform: translateY(0); } }',
  'pulse-accent':
    '@keyframes tweakbar-pulse-accent { 0%, 100% { box-shadow: 0 0 0 0 color-mix(in srgb, var(--accent) 50%, transparent); } 50% { box-shadow: 0 0 0 6px color-mix(in srgb, var(--accent) 0%, transparent); } }',
}

export const MOTION_ANIM_NAME = {
  'fade-in': 'tweakbar-fade-in',
  'rise-in': 'tweakbar-rise-in',
  'pulse-accent': 'tweakbar-pulse-accent',
}

export const INSPECT_STYLE_KEYS = [
  'color',
  'backgroundColor',
  'fontFamily',
  'fontSize',
  'fontWeight',
  'lineHeight',
  'letterSpacing',
  'margin',
  'padding',
  'borderRadius',
  'display',
  'width',
  'height',
]
