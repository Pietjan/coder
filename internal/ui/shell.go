// Package ui is the page: one shuttle component that shows a session's
// transcript, takes the next message, and answers the agent's approval
// requests.
package ui

import (
	"context"
	"io"

	"github.com/pietjan/shuttle"
)

// Shell renders the document around the app. It owes shuttle two things:
// Page.Attach in a data-init attribute on <body>, and Page.Scripts
// somewhere in the document (the popstate shim).
func Shell(w io.Writer, p shuttle.Page) error {
	return shellDocument(p).Render(context.Background(), w)
}
