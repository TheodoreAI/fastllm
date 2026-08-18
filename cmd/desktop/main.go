// Command desktop runs fastllm as a native Windows desktop app via Wails
// instead of a browser tab. It wires up exactly the same backend as
// cmd/server (same internal/* packages, same routes, same embedded
// web.FS() frontend) and hands the resulting http.ServeMux directly to
// Wails as its AssetServer Handler — the webview talks to it in-process,
// with no TCP listener and no localhost port involved, unlike the
// sidecar-process model a prior Tauri attempt used. See
// internal/appserver for the shared wiring both entrypoints call.
package main

import (
	"context"
	"log"
	"net"
	"net/http"

	"fastllm/internal/appserver"
	"fastllm/internal/chat"
	"fastllm/internal/folderpicker"
	"fastllm/internal/screenshot"
	"fastllm/internal/terminal"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/menu"
	"github.com/wailsapp/wails/v2/pkg/menu/keys"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// singleInstanceID scopes the Windows single-instance lock to this app
// specifically — Wails' lock is a named OS mutex, so this just needs to
// be unique to fastllm, not globally unique in any stronger sense.
const singleInstanceID = "fastllm-desktop-9f1e6b2a"

// appTitle is the native window's title — used both to set it via Wails'
// options.App and to find the window again for screenshot.CaptureWindow,
// so the two can never drift apart.
const appTitle = "fastllm"

func main() {
	cfg, err := appserver.ConfigFromEnv()
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	// Unlike cmd/server, a double-clicked desktop app has no predictable
	// working directory to resolve a relative DB path against, so this
	// entrypoint always anchors it under the OS user-config dir instead
	// of cmd/server's CWD-relative default.
	dbPath, err := appserver.DesktopDBPath()
	if err != nil {
		log.Fatalf("resolve desktop db path: %v", err)
	}
	cfg.DBPath = dbPath

	built, err := appserver.Build(cfg)
	if err != nil {
		log.Fatalf("build server: %v", err)
	}
	defer built.DB.Close()

	// Registered directly here rather than in internal/appserver — real
	// window capture only means something for this Wails-native
	// entrypoint, so it shouldn't be threaded through the shared package
	// cmd/server also calls.
	built.Mux.HandleFunc("GET /api/screenshot", screenshotHandler)

	// The terminal's WebSocket route can't work over Wails' in-process
	// AssetServer bridge: websocket.Accept needs a real http.Hijacker, and
	// the handler's loopback check (net/terminal/handler.go) needs a real
	// r.RemoteAddr — neither exists on that in-process bridge, since it's
	// not backed by an actual TCP connection. Every other route is fine
	// in-process; only /api/terminal/ws needs a real socket, so this opens
	// one extra loopback-only listener serving the exact same mux
	// (identical routes, identical settings) purely so that one route has
	// somewhere real to upgrade from. The frontend learns the port via the
	// bound terminalBridge below and only uses it for the WS connection —
	// every other request still goes through the in-process bridge.
	termListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		log.Fatalf("listen for terminal bridge: %v", err)
	}
	defer termListener.Close()
	go func() {
		if err := http.Serve(termListener, built.Mux); err != nil {
			log.Printf("terminal bridge server stopped: %v", err)
		}
	}()
	bridge := &terminalBridge{port: termListener.Addr().(*net.TCPAddr).Port}

	app := &desktopApp{registry: built.TerminalRegistry, handler: built.Handler}

	err = wails.Run(&options.App{
		Title:  appTitle,
		Width:  1280,
		Height: 860,
		AssetServer: &assetserver.Options{
			Handler: built.Mux,
		},
		BackgroundColour: &options.RGBA{R: 30, G: 30, B: 30, A: 1},
		Bind:             []interface{}{bridge},
		Menu:             app.menu(),
		OnStartup:        app.startup,
		OnBeforeClose:    app.beforeClose,
		OnShutdown:       app.shutdown,
		// Without this, double-clicking the pinned shortcut while the app
		// is already open would spawn a second process — a second SQLite
		// connection and a second terminal registry against the same DB
		// file — rather than just surfacing the window that's already
		// running. OnSecondInstanceLaunch fires in the *first* (already
		// running) instance; the second process's own wails.Run exits
		// immediately without ever reaching this options.App at all.
		SingleInstanceLock: &options.SingleInstanceLock{
			UniqueId:               singleInstanceID,
			OnSecondInstanceLaunch: app.onSecondInstance,
		},
	})
	if err != nil {
		log.Fatalf("wails run: %v", err)
	}
}

// terminalBridge is bound into the webview (options.App.Bind) so the
// frontend can look up, via window.go.main.TerminalBridge.TerminalPort(),
// the loopback port serving the same mux as a real TCP listener — see the
// comment above termListener in main() for why the terminal WebSocket
// specifically needs this instead of the normal in-process route.
type terminalBridge struct {
	port int
}

func (b *terminalBridge) TerminalPort() int {
	return b.port
}

// desktopApp holds the state the Wails lifecycle hooks need to clean up —
// mirrors what cmd/server's quitHandler does on POST /api/quit, since
// there's no HTTP request driving shutdown here, just the window closing.
type desktopApp struct {
	ctx      context.Context
	registry *terminal.Registry
	handler  *chat.Handler
}

// menu builds fastllm's native Windows menu bar — just File → Open
// Folder… for now. This runs as a real native menu (not an in-app
// dropdown) because an in-app "File" button/dropdown, built the same way
// as the working Chat/Editor/Split tabs right next to it, was reliably
// unclickable specifically in the production build (worked fine under
// `wails dev`, in a plain browser serving the same built assets, and by
// every static check of the compiled JS/CSS — never isolated to a single
// root cause, so this sidesteps the whole class of problem rather than
// keep chasing it). The click handler can't call the folder-open flow
// directly — that logic (browseForFolder → save settings → refresh
// tree/git) lives in React state inside EditorView — so it emits a Wails
// event instead and lets the frontend react.
func (a *desktopApp) menu() *menu.Menu {
	m := menu.NewMenu()
	fileMenu := m.AddSubmenu("File")
	fileMenu.AddText("Open Folder…", keys.CmdOrCtrl("o"), func(_ *menu.CallbackData) {
		if a.ctx == nil {
			return
		}
		wailsruntime.EventsEmit(a.ctx, "menu:open-folder")
	})
	return m
}

func (a *desktopApp) startup(ctx context.Context) {
	a.ctx = ctx

	// Overrides the folderpicker.Choose default (see appserver.Build) now
	// that a real Wails context exists to open a native, in-process folder
	// dialog against — a.ctx isn't available any earlier than this hook.
	// Using Wails' own dialog instead of shelling out to a hidden
	// powershell.exe hosting a WinForms dialog is what fixes the window
	// flashing/flicker previously seen when clicking "Choose folder" in
	// Settings → File access: that subprocess approach opened a second,
	// unowned top-level window from a freshly starting PowerShell/CLR
	// process, which is exactly the kind of thing that flickers during
	// its own startup.
	a.handler.FolderChooser = func(ctx context.Context) (string, error) {
		path, err := wailsruntime.OpenDirectoryDialog(a.ctx, wailsruntime.OpenDialogOptions{
			Title: "Select the folder fastllm should be allowed to read and write",
		})
		if err != nil {
			return "", err
		}
		if path == "" {
			return "", folderpicker.ErrCancelled
		}
		return path, nil
	}
}

// onSecondInstance runs in the already-open instance when the user
// launches the app again (e.g. double-clicking the Start Menu pin) —
// bring the existing window to the front instead of leaving the new
// launch attempt looking like it silently did nothing.
func (a *desktopApp) onSecondInstance(_ options.SecondInstanceData) {
	wailsruntime.WindowUnminimise(a.ctx)
	wailsruntime.Show(a.ctx)
}

// beforeClose runs when the user closes the window (X button, Alt+F4, or
// the frontend's Quit button calling the Wails runtime's Quit()). Always
// allows the close (prevent = false) — cleanup happens in OnShutdown,
// which is guaranteed to run once the close is committed, rather than
// here where returning early on an error would leave the window stuck.
func (a *desktopApp) beforeClose(ctx context.Context) (prevent bool) {
	return false
}

// shutdown tears down every live terminal session so a closed desktop
// window can never leave an orphaned powershell.exe behind — same
// guarantee cmd/server's quitHandler provides, plus the Job Object
// kill-on-close safety net in internal/terminal for the case where the
// whole process dies before this even runs.
func (a *desktopApp) shutdown(ctx context.Context) {
	a.registry.CloseAll()
}

// screenshotHandler captures fastllm's own native window (see
// internal/screenshot) and returns it as a PNG. Only meaningful here —
// cmd/server's browser tab has no OS window handle to capture, which is
// why this route only exists on the desktop mux, not the shared one
// internal/appserver builds.
func screenshotHandler(w http.ResponseWriter, r *http.Request) {
	png, err := screenshot.CaptureWindow(appTitle)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Write(png)
}
