import { useEffect, useState } from 'react'

const STORAGE_KEY = 'fastllm-model'

function initialModel() {
  return localStorage.getItem(STORAGE_KEY) || ''
}

// Remembers the last-picked chat model across reloads, persisted to
// localStorage — mirrors useTheme/useFontFamily's pattern. An empty
// string (nothing stored yet, or explicitly cleared) is a valid value:
// App.jsx's mount effect falls back to the first model in the fetched
// list whenever the current value is falsy, the same way it already did
// before this was persisted, so a fresh browser profile or a model no
// longer being in the list both degrade the same way they always have.
export function useModel() {
  const [model, setModel] = useState(initialModel)

  useEffect(() => {
    if (model) {
      localStorage.setItem(STORAGE_KEY, model)
    } else {
      localStorage.removeItem(STORAGE_KEY)
    }
  }, [model])

  return [model, setModel]
}
