package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/karloscodes/matcha"
)

// serverAdopt moves an app that matcha runs on this server to Chasen, or back.
//
// Chasen is built on matcha: the same proxy, the same names of containers,
// the same directories for the data. So an app does not move at all. Only its
// record moves, from the file of matcha to the database of Chasen. The container
// keeps running, with its env, its domains, and its databases, and no
// visitor notices. After that the commands of Chasen work on the app, and
// matcha does not know it any more.
//
// The first `chasen restart` of an adopted app starts it again from the same
// record, with the health check and the swap that matcha did.
func serverAdopt(args []string) error {
	flags := flag.NewFlagSet("adopt", flag.ContinueOnError)
	from := flags.String("from", matcha.ConfigPath(), "the config file of matcha")
	undo := flags.Bool("undo", false, "give the app back to matcha")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 1 {
		return errors.New("usage: chasen-server adopt [--undo] [--from <config of matcha>] <app>")
	}
	name := flags.Arg(0)
	if err := checkAppName(name); err != nil {
		return err
	}
	if _, err := loadServerConfig(); err != nil {
		return err
	}
	if err := lock(); err != nil {
		return err
	}

	// The record moves from the file of matcha to the database of Chasen, or back.
	var app matcha.AppConfig
	var err error
	if *undo {
		if app, err = loadApp(name); err != nil {
			return fmt.Errorf("Chasen has no app %q", name)
		}
		if _, err := matcha.LoadAppFrom(*from, name); err == nil {
			return fmt.Errorf("matcha has the app %q already: %s. Nothing changed", name, *from)
		}
		if isBuilt(name, app.Image) {
			return fmt.Errorf("%s runs an image that Chasen made for it (%s). matcha cannot get that image again, so the app stays", name, app.Image)
		}
	} else {
		if app, err = matcha.LoadAppFrom(*from, name); err != nil {
			return fmt.Errorf("matcha has no app %q in %s", name, *from)
		}
		if _, err := loadApp(name); err == nil {
			return fmt.Errorf("Chasen has the app %q already. Nothing changed", name)
		}
	}
	if _, err := activeContainer(name); err != nil {
		return fmt.Errorf("%s does not run now. Start it first, so the record matches what runs: nothing changed", name)
	}

	// A copy of the file of matcha from before, next to it: the way back by hand.
	if data, err := os.ReadFile(*from); err == nil {
		kept := fmt.Sprintf("%s.bak.pre-chasen-%s-%s", *from, name, time.Now().UTC().Format("20060102_150405"))
		if err := os.WriteFile(kept, data, 0600); err != nil {
			return err
		}
	}
	if *undo {
		if err := matcha.SaveAppTo(*from, name, app); err != nil {
			return err
		}
		if err := forgetApp(name); err != nil {
			// The app is in both places now. Take it out of the new one: one owner.
			matcha.RemoveAppFrom(*from, name)
			return fmt.Errorf("cannot take %s out of Chasen, so nothing changed: %w", name, err)
		}
	} else {
		if err := saveApp(name, app); err != nil {
			return err
		}
		if err := matcha.RemoveAppFrom(*from, name); err != nil {
			forgetApp(name)
			return fmt.Errorf("cannot take %s out of %s, so nothing changed: %w", name, *from, err)
		}
	}
	if *undo {
		forgetSettings(name)
		fmt.Printf("%s is with matcha again. Nothing restarted.\n", name)
		return nil
	}
	fmt.Printf("Adopted %s. Nothing restarted: the container, the data in %s, and the domains are the same.\n", name, appDir(name))
	fmt.Printf("  %s\n", strings.ReplaceAll(app.Domain, ",", "\n  "))
	fmt.Printf("Chasen runs it now, and matcha does not know it any more. See it: chasen -a %s status\n", name)
	fmt.Printf("To give it back to matcha: chasen-server adopt --undo %s\n", name)
	return nil
}
