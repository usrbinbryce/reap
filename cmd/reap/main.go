package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
)

const version = "0.1.0"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "reap:", err)
		os.Exit(1)
	}
}

func run() error {
	fs := flag.NewFlagSet("reap", flag.ContinueOnError)
	showVersion := fs.Bool("version", false, "print the version")

	if err := fs.Parse(os.Args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}

	if *showVersion {
		fmt.Println("reap", version)
		return nil
	}

	return nil
}
