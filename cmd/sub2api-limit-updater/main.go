package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"runtime"
	"syscall"
)

var version = "dev"

func main() {
	if len(os.Args) != 2 {
		printUsage()
		os.Exit(2)
	}

	switch os.Args[1] {
	case "version", "--version", "-version":
		fmt.Printf("sub2api-limit-updater %s (%s/%s)\n", version, runtime.GOOS, runtime.GOARCH)
	case "apply":
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		if err := runApply(ctx, productionConfig(), version); err != nil {
			fmt.Fprintf(os.Stderr, "sub2api-limit-updater: %s\n", publicError(err))
			os.Exit(1)
		}
	default:
		printUsage()
		os.Exit(2)
	}
}

func printUsage() {
	fmt.Fprintln(os.Stderr, "usage: sub2api-limit-updater <apply|version>")
}
