package main

import (
	"context"
	"os"
	"os/signal"

	"github.com/RussellLuo/qi/kernel/internal/build"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	os.Exit(build.Run(ctx, os.Args[1:], os.Stdout, os.Stderr))
}
