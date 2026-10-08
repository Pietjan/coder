// Package assets carries what the browser needs, compiled into the binary:
// the stylesheet `make css` builds, the Datastar bundle (vendored, so the
// app works offline), and the window icon.
package assets

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
)

// FS holds static/styles.css and static/datastar.js, served under /static/.
//
//go:embed static/styles.css static/datastar.js
var FS embed.FS

// Icon is the window and taskbar icon.
//
//go:embed icon.png
var Icon []byte

// StylesheetLink is the <link> for the stylesheet, versioned by content so
// a browser never keeps an old sheet across a rebuild.
func StylesheetLink() string {
	css, _ := FS.ReadFile("static/styles.css")
	sum := sha256.Sum256(css)
	return fmt.Sprintf(`<link rel="stylesheet" href="/static/styles.css?v=%s">`, hex.EncodeToString(sum[:6]))
}
