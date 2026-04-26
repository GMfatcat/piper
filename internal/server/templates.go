// Package server — templates.go
//
// Embeds the web/templates and web/static directories, parses all HTML
// templates once at server start (sync.Once protected), and provides a
// render helper used by the web page handlers.
//
// Static asset layout (embedded under web/static/):
//   - pico.min.css  — Pico.css v2 (classless theme), downloaded from CDN.
//   - htmx.min.js   — HTMX v2, downloaded from CDN.
//   - piper.css     — custom Piper UI styles.
//
// NOTE FOR PRODUCTION DEPLOYMENT: pico.min.css and htmx.min.js must be the
// real Pico v2 / HTMX v2 minified files. If a placeholder was committed (due
// to an offline build environment), replace the files before shipping:
//
//	curl -sSLo internal/server/web/static/pico.min.css \
//	  https://cdn.jsdelivr.net/npm/@picocss/pico@2/css/pico.min.css
//	curl -sSLo internal/server/web/static/htmx.min.js \
//	  https://unpkg.com/htmx.org@2/dist/htmx.min.js
package server

import (
	"embed"
	"fmt"
	"html/template"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/GMfatcat/piper/internal/service"
	"github.com/GMfatcat/piper/internal/store"
)

// ---------------------------------------------------------------------------
// Embedded FS
// ---------------------------------------------------------------------------

//go:embed web/templates/*.html web/static/*
var webFS embed.FS

// ---------------------------------------------------------------------------
// TemplateData
// ---------------------------------------------------------------------------

// TemplateData is the single data bag passed to every page template render.
// Page handlers fill only the fields relevant to their page; other fields
// default to their zero values which the templates treat as "nothing to show".
type TemplateData struct {
	Title       string
	IsLocalhost bool          // controls write-button visibility per §5.4
	Now         time.Time     // current time (set by handlers via Deps.now())
	LastScanAt  time.Time     // injected from snapshot for "Last scan: X" text
	UFWActive   bool          // false → top banner shown
	Ports       []service.PortStatus // overview page
	Reservations []store.Reservation  // reservations page
	Events      []store.Event         // history page
}

// ---------------------------------------------------------------------------
// Template cache (parsed once, sync.Once protected)
// ---------------------------------------------------------------------------

var (
	tmplOnce  sync.Once
	tmplCache *template.Template
	tmplErr   error
)

// templates returns the parsed template set, initialising it on first call.
// An error during template parsing is stored in tmplErr and returned on every
// subsequent call; the cache itself is never populated on error.
func templates() (*template.Template, error) {
	tmplOnce.Do(func() {
		t, err := parseTemplates()
		if err != nil {
			tmplErr = err
			return
		}
		tmplCache = t
	})
	return tmplCache, tmplErr
}

// parseTemplates parses the layout plus all page templates from the embedded
// FS. It is exported only for testing; callers should prefer templates().
func parseTemplates() (*template.Template, error) {
	// Read the layout template file first.
	layoutBytes, err := webFS.ReadFile("web/templates/layout.html")
	if err != nil {
		return nil, fmt.Errorf("read layout.html: %w", err)
	}

	// Create a new template set with the layout as root (named "layout").
	t, err := template.New("layout").Parse(string(layoutBytes))
	if err != nil {
		return nil, fmt.Errorf("parse layout.html: %w", err)
	}

	// Parse each page template into the same set. The page templates use
	// {{define "title"}} / {{define "content"}} which are picked up by the
	// layout's {{block}} calls.
	pages := []string{
		"web/templates/overview.html",
		"web/templates/reservations.html",
		"web/templates/history.html",
	}
	for _, page := range pages {
		b, err := webFS.ReadFile(page)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", page, err)
		}
		if _, err := t.New(page).Parse(string(b)); err != nil {
			return nil, fmt.Errorf("parse %s: %w", page, err)
		}
	}

	return t, nil
}

// ---------------------------------------------------------------------------
// render helper
// ---------------------------------------------------------------------------

// render executes the named page template through the layout and writes the
// result to w. On error it writes a plain-text 500 response. The named template
// must be the full embed-FS path (e.g. "web/templates/overview.html").
//
// Each call parses a fresh template pair (layout + page) so that the page's
// {{define}} blocks are always the active ones regardless of parse order. This
// is slightly less efficient than caching but guarantees correct block
// isolation for a locally-deployed tool. The sync.Once cache (templates()) is
// used for validation/testing only.
func render(w http.ResponseWriter, name string, data TemplateData) {
	renderT, err := buildRenderTemplate(name)
	if err != nil {
		http.Error(w, "template render error: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := renderT.ExecuteTemplate(w, "layout", data); err != nil {
		// Headers already written; can't change status code.
		_, _ = fmt.Fprintf(w, "\n<!-- template execution error: %v -->", err)
	}
}

// buildRenderTemplate parses layout + the specific page template into a fresh
// *template.Template, so the page's {{define}} blocks are the active ones.
func buildRenderTemplate(pagePath string) (*template.Template, error) {
	layoutBytes, err := webFS.ReadFile("web/templates/layout.html")
	if err != nil {
		return nil, fmt.Errorf("read layout.html: %w", err)
	}
	t, err := template.New("layout").Parse(string(layoutBytes))
	if err != nil {
		return nil, fmt.Errorf("parse layout.html: %w", err)
	}
	pageBytes, err := webFS.ReadFile(pagePath)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", pagePath, err)
	}
	if _, err := t.New(pagePath).Parse(string(pageBytes)); err != nil {
		return nil, fmt.Errorf("parse %s: %w", pagePath, err)
	}
	return t, nil
}

// ---------------------------------------------------------------------------
// isLocalhost helper
// ---------------------------------------------------------------------------

// isLocalhostRequest reports whether the HTTP request originated from the
// loopback interface (127.0.0.1 or ::1). This mirrors the requireLocalhost
// middleware logic but returns a bool rather than short-circuiting the chain.
func isLocalhostRequest(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		// Malformed RemoteAddr — treat as non-local for safety.
		return false
	}
	return host == "127.0.0.1" || host == "::1"
}
