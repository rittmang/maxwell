package main

import (
	"log/slog"
	"os"

	"maxwell/internal/cli"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	slog.SetDefault(logger)
	runner := cli.NewRunner(os.Stdout, os.Stderr)
	os.Exit(runner.Execute(os.Args[1:]))
}
