// gh-arborist is a gh CLI extension that reports which branches, pull requests
// and issues in a repository could be pruned. It never changes anything.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"

	"github.com/danudey/gh-arborist/internal/cmd"
)

// version is overridden at build time with -ldflags "-X main.version=..."
var version = "dev"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	root := cmd.New(version)
	if err := root.ExecuteContext(ctx); err != nil {
		if errors.Is(err, cmd.ErrFindings) {
			// The report is the output; --exit-code only asks for the status.
			os.Exit(1)
		}
		if errors.Is(err, context.Canceled) {
			fmt.Fprintln(os.Stderr, "cancelled")
			os.Exit(130)
		}
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}
