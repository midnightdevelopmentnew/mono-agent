package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"runtime/debug"
	"strings"
	"syscall"
	"time"
)

var (
	version   = ""
	buildDate = ""
)

func init() {
	if version == "" {
		if out, err := exec.Command("git", "describe", "--tags", "--always").Output(); err == nil {
			version = strings.TrimSpace(string(out))
		} else {
			version = "dev"
		}
	}
	if buildDate == "" {
		buildDate = time.Now().UTC().Format("2006-01-02T15:04:05Z")
	}
}

func main() {
	// Report-then-repanic: files a crash report as a side effect, then
	// re-panics so the process still crashes with the original trace and
	// exit behavior a user would otherwise see — this only observes, it
	// never swallows the panic.
	defer func() {
		if r := recover(); r != nil {
			reportCrash(r, debug.Stack())
			panic(r)
		}
	}()

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	if err := newRootCmd().ExecuteContext(ctx); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
