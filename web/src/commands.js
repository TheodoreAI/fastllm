// Single source of truth for fastllm's global keyboard shortcuts and the
// Command Palette's (Ctrl/Cmd+Shift+P) command list. App.jsx's keydown
// handler dispatches by finding the command whose key/shift match the
// event, instead of a hardcoded if/else chain, so the palette can never
// list a command that behaves differently from the raw shortcut — there is
// only one place either one is implemented.
//
// Ctrl+O and Ctrl+Q match TopBar's File → Open Folder…/Exit — those menu
// items had real native accelerators when they were Win32 HMENU entries
// (see TopBar.jsx's doc comment for why they're in-app components now); a
// plain <button> gets no OS-level shortcut for free, so these keep the
// bindings working. Desktop-only (isWails()), matching TopBar's own gating
// — there's no folder-open/quit concept in the browser build.
export function buildCommands({
  setEditorSidebarCollapsed,
  setTerminalPaneCollapsed,
  setSidebarCollapsed,
  setOpenFolderSignal,
  setQuitConfirmOpen,
  isWails,
}) {
  const commands = [
    {
      id: 'toggle-editor-sidebar',
      label: 'Toggle Files Sidebar',
      key: 'b',
      shift: false,
      run: () => setEditorSidebarCollapsed((c) => !c),
    },
    {
      id: 'toggle-terminal',
      label: 'Toggle Terminal',
      key: 'j',
      shift: false,
      run: () => setTerminalPaneCollapsed((c) => !c),
    },
    {
      id: 'toggle-model-settings',
      label: 'Toggle Model Settings',
      key: 'm',
      shift: true,
      run: () => setSidebarCollapsed((c) => !c),
    },
  ]
  if (isWails()) {
    commands.push(
      {
        id: 'open-folder',
        label: 'Open Folder…',
        key: 'o',
        shift: false,
        run: () => setOpenFolderSignal((n) => (n ?? 0) + 1),
      },
      {
        id: 'quit',
        label: 'Quit',
        key: 'q',
        shift: false,
        run: () => setQuitConfirmOpen(true),
      }
    )
  }
  return commands
}

// Formats a command's binding for display in the palette, e.g. "Ctrl+B" /
// "Cmd+Shift+M" — the display-only counterpart to the key/shift fields
// handleKeyDown actually matches against.
export function shortcutLabel(cmd) {
  const mod = navigator.platform.toLowerCase().includes('mac') ? 'Cmd' : 'Ctrl'
  return cmd.shift ? `${mod}+Shift+${cmd.key.toUpperCase()}` : `${mod}+${cmd.key.toUpperCase()}`
}
