// Command costmeter is a macOS menu bar app showing month-to-date Azure spend.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"sync/atomic"
	"time"

	"costmeter/internal/azure"
	"costmeter/internal/cache"
	"costmeter/internal/config"
	"costmeter/internal/paths"
	"costmeter/internal/provider"
	"costmeter/internal/provider/fake"
	"costmeter/internal/scheduler"
	"costmeter/internal/settings"
	"costmeter/internal/ui"
)

func main() {
	useFake := flag.Bool("fake", false, "use built-in sample data and separate state files")
	flag.Parse()

	p, err := paths.New(*useFake)
	if err != nil {
		fatal(err)
	}
	if logFile, err := os.OpenFile(p.Log, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600); err == nil {
		log.SetOutput(logFile)
	}
	// An app started from Finder or launchd does not get the shell's PATH, and finds `az` by running it.
	os.Setenv("PATH", withToolDirs(os.Getenv("PATH")))

	lock, err := acquireLock(p.Lock)
	if errors.Is(err, errAlreadyRunning) {
		fmt.Println(err)
		return
	}
	if err != nil {
		fatal(err)
	}
	defer lock.Release()

	if err := run(p, *useFake); err != nil {
		fatal(err)
	}
}

func fatal(err error) {
	log.Print(err)
	fmt.Fprintln(os.Stderr, "costmeter:", err)
	os.Exit(1)
}

func run(p paths.Paths, useFake bool) error {
	var prov provider.CostProvider = fake.New()
	if !useFake {
		az, err := azure.NewCLI(config.Default().Azure)
		if err != nil {
			return err
		}
		prov = az
	}

	store := settings.New(p.Settings)
	sched := scheduler.New(scheduler.Options{
		Provider:   prov,
		Cache:      cache.New(p.Cache),
		LoadConfig: configLoader(store),
	})
	var latest atomic.Pointer[scheduler.Update]
	choices := newChooser(store, func() []provider.Account {
		if u := latest.Load(); u != nil {
			return u.Tracked
		}
		return nil
	}, sched.Apply, sched.Reload)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ui.Run(ui.Actions{
		RefreshNow:  sched.RefreshNow,
		Pick:        choices.Pick,
		ResetScopes: choices.Reset,
		SetRefresh:  choices.SetRefresh,
		SetServices: choices.SetServices,
		OnExit:      cancel,
	}, func(tray *ui.Tray) {
		sched.Subscribe(func(u scheduler.Update) {
			latest.Store(&u)
			tray.Show(ui.Build(u, ui.Input{Now: time.Now(), Loc: time.Local, Saved: choices.Saved()}))
		})
		go sched.Run(ctx)
	})
	return nil
}
