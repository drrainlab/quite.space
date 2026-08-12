// Package wailsx is the ONLY place in the desktop shell that names Wails.
//
// The framework is an alpha on a daily release cadence, so the cost of an
// upgrade is decided by how many files mention it. Here it is one. Everything
// above this package speaks in Quiet's own words — a handler, a title, a quit
// callback — and the whole surface below is an application, a window and a
// tray. If the framework has to be replaced, this file is the work.
//
// Two behaviours of THIS alpha are load-bearing and were found by hand in
// cmd/wails-probe rather than read in a document; both are encoded here so
// nobody rediscovers them:
//
//   - app.Window.NewWithOptions makes a window that navigates. The
//     package-level application.NewWindow produced one that silently never
//     asked the AssetServer for anything.
//   - app.Show() does NOT unhide a hidden window, so a tray item that relied
//     on it could never bring the app back. Tray items call the window's own
//     Show() explicitly.
package wailsx

import (
	"net/http"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

// Options is what the shell asks for, in its own vocabulary.
type Options struct {
	Name        string
	Description string

	Title         string
	Width, Height int
	// URL is where the window starts. The pages navigate onward themselves;
	// nothing here ever pushes a URL into the WebView afterwards.
	URL string

	// Handler is the whole application — interface and API — served with no
	// TCP listener anywhere. That absence is the point: on a shared machine a
	// loopback port is reachable by every other process on it, and here there
	// is no port to reach.
	Handler http.Handler

	// Icon is the about-box/app image; TrayIcon must be a TEMPLATE image
	// (shape in the alpha channel) so macOS can recolour it for the menubar.
	Icon     []byte
	TrayIcon []byte

	ShowLabel string
	QuitLabel string

	// OnQuit runs before the process ends. It is called on the tray's Quit
	// AND after the run loop returns for any other reason, so it MUST be
	// idempotent — which is why the shell's Shutdown is a sync.Once.
	OnQuit func()
}

// Run owns the process until the application quits.
func Run(o Options) error {
	app := application.New(application.Options{
		Name:        o.Name,
		Description: o.Description,
		Icon:        o.Icon,
		Assets:      application.AssetOptions{Handler: o.Handler},
	})

	win := app.Window.NewWithOptions(application.WebviewWindowOptions{
		Title:  o.Title,
		Width:  o.Width,
		Height: o.Height,
		URL:    o.URL,
	})

	// The window is not the node. Closing it hides it — the node keeps
	// syncing, keeps its relay connection, keeps answering for the spaces it
	// holds — and only the tray's Quit, or the OS, ends the process.
	win.RegisterHook(events.Common.WindowClosing, func(e *application.WindowEvent) {
		win.Hide()
		e.Cancel()
	})

	tray := app.SystemTray.New()
	if len(o.TrayIcon) > 0 {
		tray.SetTemplateIcon(o.TrayIcon)
	}
	menu := app.NewMenu()
	menu.Add(o.ShowLabel).OnClick(func(_ *application.Context) {
		win.Show()
		win.Focus()
	})
	menu.Add(o.QuitLabel).OnClick(func(_ *application.Context) {
		if o.OnQuit != nil {
			o.OnQuit()
		}
		app.Quit()
	})
	tray.SetMenu(menu)

	err := app.Run()
	// Every other way out lands here: an OS shutdown, a fatal run-loop error,
	// or the tray's Quit having already run it once.
	if o.OnQuit != nil {
		o.OnQuit()
	}
	return err
}
