// Command fakellm serves the scripted test model on a port, for driving
// the UI by hand without a real model:
//
//	go run ./internal/testopenai/cmd/fakellm -addr 127.0.0.1:8099
//	bin/coder serve --llm http://127.0.0.1:8099/v1
package main

import (
	"flag"
	"fmt"
	"net/http"
	"os"

	"github.com/pietjan/coder/internal/testopenai"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:8099", "address to listen on")
	flag.Parse()
	srv := testopenai.NewServer()
	defer srv.Close()
	fmt.Fprintf(os.Stderr, "fake model on http://%s/v1 (scripts: echo:, tool:, tools:, describe:, toolong, hang)\n", *addr)
	if err := http.ListenAndServe(*addr, srv.Config.Handler); err != nil { //nolint:gosec // a dev server
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
