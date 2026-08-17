// Monochrome, stroke-based icons (currentColor, 1.6px strokes) for the
// collapsed sidebar rail — deliberately plain/geometric rather than emoji,
// so they read as UI iconography instead of illustrative art.
const ICONS = {
  conversations: (
    <path d="M4 5.5A1.5 1.5 0 0 1 5.5 4h13A1.5 1.5 0 0 1 20 5.5v9A1.5 1.5 0 0 1 18.5 16H9l-4 3.5V16h-.5A1.5 1.5 0 0 1 3 14.5v-9A1.5 1.5 0 0 1 4 5.5Z" />
  ),
  model: (
    <>
      <circle cx="12" cy="12" r="8" />
      <path d="M8.5 13c.5-2 1.5-3 2-4.5.5 3.5 1 4.5 2 4.5s1.2-1 1.5-2" />
    </>
  ),
  skills: (
    <>
      <path d="M6 18 16 8" />
      <path d="M14 6l1 2 2 1-2 1-1 2-1-2-2-1 2-1 1-2Z" />
      <path d="M5 13l.6 1.4L7 15l-1.4.6L5 17l-.6-1.4L3 15l1.4-.6L5 13Z" />
    </>
  ),
  knowledge: (
    <>
      <path d="M4 5.2c0-.66.54-1.2 1.2-1.2H11v14H5.2A1.2 1.2 0 0 1 4 16.8V5.2Z" />
      <path d="M20 5.2c0-.66-.54-1.2-1.2-1.2H13v14h5.8c.66 0 1.2-.54 1.2-1.2V5.2Z" />
    </>
  ),
}

export default function SectionIcon({ name }) {
  return (
    <svg
      className="section-icon"
      width="18"
      height="18"
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.6"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
    >
      {ICONS[name]}
    </svg>
  )
}
