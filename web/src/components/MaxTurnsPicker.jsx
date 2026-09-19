import { useEffect, useRef, useState } from 'react'

const PRESET_OPTIONS = [
  { value: 5, label: '5 turns', description: 'Quick fix (inspect & small edit)' },
  { value: 10, label: '10 turns', description: 'Standard task' },
  { value: 20, label: '20 turns', description: 'Recommended for multi-step tasks' },
  { value: 30, label: '30 turns', description: 'Complex refactor or bugfix' },
  { value: 50, label: '50 turns', description: 'Deep autonomous execution' },
]

function ChevronDownIcon(props) {
  return (
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true" {...props}>
      <polyline points="6 9 12 15 18 9" />
    </svg>
  )
}

function CheckIcon(props) {
  return (
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true" {...props}>
      <polyline points="20 6 9 17 4 12" />
    </svg>
  )
}

function LoopIcon(props) {
  return (
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true" {...props}>
      <path d="M21.5 2v6h-6M21.34 15.57a10 10 0 1 1-.57-8.38l5.67-5.19" />
    </svg>
  )
}

export default function MaxTurnsPicker({ value, onChange, disabled }) {
  const [open, setOpen] = useState(false)
  const [isCustom, setIsCustom] = useState(false)
  const [customInput, setCustomInput] = useState(String(value))
  const containerRef = useRef(null)
  const customInputRef = useRef(null)

  useEffect(() => {
    setCustomInput(String(value))
    const isPreset = PRESET_OPTIONS.some((opt) => opt.value === value)
    setIsCustom(!isPreset)
  }, [value])

  useEffect(() => {
    if (!open) return
    function handleClickOutside(e) {
      if (containerRef.current && !containerRef.current.contains(e.target)) {
        setOpen(false)
      }
    }
    function handleKeyDown(e) {
      if (e.key === 'Escape') setOpen(false)
    }
    document.addEventListener('mousedown', handleClickOutside)
    document.addEventListener('keydown', handleKeyDown)
    return () => {
      document.removeEventListener('mousedown', handleClickOutside)
      document.removeEventListener('keydown', handleKeyDown)
    }
  }, [open])

  function handleSelect(val) {
    onChange(val)
    setOpen(false)
  }

  function handleCustomSubmit(e) {
    e?.preventDefault()
    const parsed = parseInt(customInput, 10)
    if (!isNaN(parsed) && parsed > 0 && parsed <= 100) {
      onChange(parsed)
      setOpen(false)
    }
  }

  return (
    <div className="max-turns-picker" ref={containerRef}>
      <button
        type="button"
        className={`max-turns-trigger ${open ? 'is-open' : ''}`}
        onClick={() => !disabled && setOpen(!open)}
        disabled={disabled}
        title="Maximum tool turns for autonomous agent"
      >
        <LoopIcon className="max-turns-icon" />
        <span className="max-turns-current">{value} turns</span>
        <ChevronDownIcon className={`max-turns-chevron ${open ? 'is-flipped' : ''}`} />
      </button>

      {open && (
        <div className="max-turns-dropdown">
          <div className="max-turns-menu-header">Turn Budget</div>
          <div className="max-turns-options">
            {PRESET_OPTIONS.map((opt) => {
              const isSelected = value === opt.value
              return (
                <button
                  key={opt.value}
                  type="button"
                  className={`max-turns-option ${isSelected ? 'is-selected' : ''}`}
                  onClick={() => handleSelect(opt.value)}
                >
                  <div className="max-turns-option-text">
                    <span className="max-turns-option-label">{opt.label}</span>
                    <span className="max-turns-option-desc">{opt.description}</span>
                  </div>
                  {isSelected && <CheckIcon className="max-turns-check" />}
                </button>
              )
            })}
          </div>

          <div className="max-turns-custom-row">
            <label className="max-turns-custom-label" htmlFor="max-turns-custom-input">
              Custom limit:
            </label>
            <div className="max-turns-custom-input-wrap">
              <input
                id="max-turns-custom-input"
                ref={customInputRef}
                type="number"
                min="1"
                max="100"
                value={customInput}
                onChange={(e) => setCustomInput(e.target.value)}
                onKeyDown={(e) => {
                  if (e.key === 'Enter') {
                    e.preventDefault()
                    handleCustomSubmit()
                  }
                }}
              />
              <button
                type="button"
                className="max-turns-custom-btn"
                onClick={handleCustomSubmit}
              >
                Set
              </button>
            </div>
          </div>
        </div>
      )}
    </div>
  )
}
