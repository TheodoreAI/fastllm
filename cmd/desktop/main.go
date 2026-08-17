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

	"fastllm/internal/appserver"
	"fastllm/internal/terminal"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// singleInstanceID scopes the Windows single-instance lock to this app
// specifically — Wails' lock is a named OS mutex, so this just needs to
// be unique to fastllm, not globally unique in any stronger sense.
const singleInstanceID = "fastllm-desktop-9f1e6b2a"

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

	app := &desktopApp{registry: built.TerminalRegistry}

	err = wails.Run(&options.App{
		Title:  "fastllm",
		Width:  1280,
		Height: 860,
		AssetServer: &assetserver.Options{
			Handler: built.Mux,
		},
		BackgroundColour: &options.RGBA{R: 30, G: 30, B: 30, A: 1},
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

// desktopApp holds the state the Wails lifecycle hooks need to clean up —
// mirrors what cmd/server's quitHandler does on POST /api/quit, since
// there's no HTTP request driving shutdown here, just the window closing.
type desktopApp struct {
	ctx      context.Context
	registry *terminal.Registry
}

func (a *desktopApp) startup(ctx context.Context) {
	a.ctx = ctx
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
