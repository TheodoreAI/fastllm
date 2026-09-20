// Single source of truth for fastllm's global keyboard shortcuts and the
// Command Palette's (Ctrl/Cmd+Shift+P) command list. App.jsx's keydown
// handler dispatches by finding the command whose key/shift match the
// event, instead of a hardcoded if/else chain, so the palette can never
// list a command that behaves differently from the raw shortcut — there is
// only one place either one is implemented.
export function buildCommands({
  setSidebarCollapsed,
}) {
  const commands = [
    {
      id: 'toggle-model-settings',
      label: 'Toggle Model Settings',
      key: 'm',
      shift: true,
      run: () => setSidebarCollapsed((c) => !c),
    },
  ]
  return commands
}

// Formats a command's binding for display in the palette, e.g. "Ctrl+B" /
// "Cmd+Shift+M" — the display-only counterpart to the key/shift fields
// handleKeyDown actually matches against.
export function shortcutLabel(cmd) {
  const mod = navigator.platform.toLowerCase().includes('mac') ? 'Cmd' : 'Ctrl'
  return cmd.shift ? `${mod}+Shift+${cmd.key.toUpperCase()}` : `${mod}+${cmd.key.toUpperCase()}`
}
