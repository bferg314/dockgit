// Command dockgit is a terminal dashboard that ties your Docker containers
// to the git repos and compose files they come from.
package main

import (
	"flag"
	"fmt"
	"os"

	tea "charm.land/bubbletea/v2"

	"github.com/bferg314/dockgit/internal/cache"
	"github.com/bferg314/dockgit/internal/config"
	"github.com/bferg314/dockgit/internal/dock"
	"github.com/bferg314/dockgit/internal/ui"
)

var version = "dev"

func main() {
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage:\n  dockgit [flags]\n\nFlags:\n")
		flag.PrintDefaults()
	}
	flag.Parse()

	var err error
	if *showVersion {
		fmt.Println("dockgit", version)
	} else {
		err = run()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "dockgit:", err)
		os.Exit(1)
	}
}

func run() error {
	cfgPath, err := config.Path()
	if err != nil {
		return err
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return err
	}
	app := ui.New(cfg, cfgPath, dock.CLI{})
	if path, err := cache.BuildsPath(); err == nil {
		app.UseBuilds(path)
	}
	_, err = tea.NewProgram(app).Run()
	return err
}
