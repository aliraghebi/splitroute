//go:build darwin || linux

// The list command: show and edit the configured lists.

package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"

	"github.com/aliraghebi/splitroute/internal/config"
	"github.com/aliraghebi/splitroute/internal/rules"
)

const listUsage = `usage:
  splitroute list                              show the configured lists
  splitroute list show <name>                  print a custom list's entries
  splitroute list create <name> [action]       new custom list (action: direct, block or vpn; default direct)
  splitroute list enable <release> [action]    add a release list (see splitroute lists)
  splitroute list remove <name>
  splitroute list rename <name> <new name>     custom lists only
  splitroute list action <name> <action>
  splitroute list move <name> <position>       1 is first
  splitroute list add <name> <entry>... | -    entries, or - to read them from stdin, one per line
  splitroute list delete <name> <entry>... | -`

func (a *app) listCmd(args []string) error {
	if len(args) == 0 {
		return a.printConfiguredLists()
	}
	sub, rest := args[0], args[1:]
	need := func(min, max int) error {
		if len(rest) < min || (max >= 0 && len(rest) > max) {
			return errors.New(listUsage)
		}
		return nil
	}
	action := func(i int) rules.Action {
		if i < len(rest) {
			return rules.Action(rest[i])
		}
		return rules.Direct
	}
	switch sub {
	case "show":
		if err := need(1, 1); err != nil {
			return err
		}
		return a.printEntries(rest[0])
	case "create", "enable":
		if err := need(1, 2); err != nil {
			return err
		}
		op := config.CreateList
		if sub == "enable" {
			op = config.EnableRelease
		}
		return a.editLists(func(c config.Config) (config.Config, error) { return op(c, rest[0], action(1)) },
			fmt.Sprintf("list %s added (%s)", rest[0], action(1)))
	case "remove":
		if err := need(1, 1); err != nil {
			return err
		}
		return a.editLists(func(c config.Config) (config.Config, error) { return config.RemoveList(c, rest[0]) },
			"list "+rest[0]+" removed")
	case "rename":
		if err := need(2, 2); err != nil {
			return err
		}
		return a.editLists(func(c config.Config) (config.Config, error) { return config.RenameList(c, rest[0], rest[1]) },
			fmt.Sprintf("list %s renamed to %s", rest[0], rest[1]))
	case "action":
		if err := need(2, 2); err != nil {
			return err
		}
		return a.editLists(func(c config.Config) (config.Config, error) { return config.SetAction(c, rest[0], action(1)) },
			fmt.Sprintf("list %s: %s", rest[0], action(1)))
	case "move":
		if err := need(2, 2); err != nil {
			return err
		}
		pos, err := strconv.Atoi(rest[1])
		if err != nil {
			return fmt.Errorf("position %q is not a number", rest[1])
		}
		return a.editLists(func(c config.Config) (config.Config, error) { return config.MoveList(c, rest[0], pos-1) },
			fmt.Sprintf("list %s moved to position %d", rest[0], pos))
	case "add", "delete":
		if err := need(2, -1); err != nil {
			return err
		}
		lines, err := a.entryArgs(rest[1:])
		if err != nil {
			return err
		}
		op := config.AddEntries
		if sub == "delete" {
			op = config.RemoveEntries
		}
		var ed config.EntryEdit
		err = a.editLists(func(c config.Config) (config.Config, error) {
			c, ed, err = op(c, rest[0], lines)
			return c, err
		}, "")
		if err != nil {
			return err
		}
		if sub == "add" {
			log.Printf("list %s: %d added, %d already there", rest[0], ed.Added, ed.Duplicates)
		} else {
			log.Printf("list %s: %d removed", rest[0], ed.Removed)
			for _, n := range ed.NotFound {
				log.Printf("warning: %s is not in %s", n, rest[0])
			}
		}
		return nil
	}
	return errors.New(listUsage)
}

// entryArgs returns the entries given on the command line, or read from stdin
// when the only one is "-".
func (a *app) entryArgs(args []string) ([]string, error) {
	if len(args) != 1 || args[0] != "-" {
		return args, nil
	}
	var lines []string
	sc := bufio.NewScanner(a.stdin)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	return lines, sc.Err()
}

// editLists applies f to the settings. In dry-run it prints the result
// instead of saving it.
func (a *app) editLists(f func(config.Config) (config.Config, error), done string) error {
	if a.dry {
		cfg, err := a.LoadConfig()
		if err == nil {
			cfg, err = f(cfg)
		}
		if err == nil {
			err = json.NewEncoder(a.stdout).Encode(cfg)
		}
		return err
	}
	if _, err := a.UpdateConfig(f); err != nil {
		return err
	}
	if done != "" {
		log.Printf("%s; the service applies it within %s (or run `sudo splitroute up`)", done, checkEvery)
	}
	return nil
}

func (a *app) printConfiguredLists() error {
	cfg, err := a.LoadConfig()
	if err != nil {
		return err
	}
	if len(cfg.Lists) == 0 {
		fmt.Fprintln(a.stdout, "no lists (add one with `splitroute list create` or `splitroute list enable`)")
		return nil
	}
	if cfg.Disabled {
		fmt.Fprintln(a.stdout, "turned off: no list is routed until it is turned back on")
	}
	for i, l := range cfg.Lists {
		fmt.Fprintf(a.stdout, "%3d  %-6s  %-20s  %s\n", i+1, l.Action, l.Name, describe(l))
	}
	return nil
}

// describe is a list's kind and, for custom lists, its entry counts.
func describe(l config.List) string {
	if !l.Custom {
		return "release"
	}
	es, err := rules.ParseEntries(l.Entries)
	if err != nil {
		return "custom (invalid: " + err.Error() + ")" // validated on load; unreachable
	}
	var parts []string
	if n := len(es.V4); n > 0 {
		parts = append(parts, fmt.Sprintf("%d IPv4", n))
	}
	if u := es.Unrouted(); u != "" {
		parts = append(parts, u)
	}
	if len(parts) == 0 {
		return "custom, empty"
	}
	return "custom: " + strings.Join(parts, ", ")
}

func (a *app) printEntries(name string) error {
	cfg, err := a.LoadConfig()
	if err != nil {
		return err
	}
	for _, l := range cfg.Lists {
		if l.Name != name {
			continue
		}
		if !l.Custom {
			return fmt.Errorf("%q is a release list; its entries come from the release (%s/%s.list)", name, cfg.ListsURL(), name)
		}
		for _, e := range l.Entries {
			fmt.Fprintln(a.stdout, e)
		}
		return nil
	}
	return fmt.Errorf("no list named %q", name)
}
