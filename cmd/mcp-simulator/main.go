// mcp-simulator serves only read-only V2 investigation tools on stdio.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"IncidentAI/internal/tooling"
)

func main() {
	root := flag.String("root", ".", "IncidentAI project root")
	flag.Parse()
	backend, err := tooling.NewLocal(*root)
	if err == nil {
		defer backend.Close()
		err = tooling.ServeMCP(context.Background(), os.Stdin, os.Stdout, backend)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
