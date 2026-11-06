// Package ujust lists and runs the system's ujust recipes: the commands Universal Blue images (Bazzite,
// Bluefin, Aurora) ship for setting up drivers, tools and tweaks, such as "ujust install-coolercontrol".
//
// Recipes are read with just's JSON dump, without running anything. Many recipes ask questions or need a
// password, so they always run in a terminal window, where the user sees and answers them.
package ujust

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"

	"github.com/tyler-builds/uas/internal/client/run"
)

// Justfile is where Universal Blue images keep the recipes ujust runs.
const Justfile = "/usr/share/ublue-os/justfile"

// Recipe is one ujust command.
type Recipe struct {
	Name  string `json:"name"`
	Doc   string `json:"doc"`
	Group string `json:"group,omitempty"`
	// Options are the choices of its first parameter, when its description lists them
	// ("Options: status | install | uninstall").
	Options []string `json:"options,omitempty"`
	// Param is the name of the recipe's first parameter, or "".
	Param string `json:"param,omitempty"`
	// Asks is true when the recipe asks for its parameter itself if none is given (its default is "prompt"
	// or empty).
	Asks bool `json:"asks,omitempty"`
}

// Tools lists and runs recipes.
type Tools struct {
	Runner run.Runner
	Just   string // the just binary
	File   string // the justfile
}

// Detect returns the system's ujust tools, or nil when it has none.
func Detect() *Tools {
	just, err := exec.LookPath("just")
	if err != nil {
		return nil
	}
	if _, err := exec.LookPath("ujust"); err != nil {
		return nil
	}
	if _, err := os.Stat(Justfile); err != nil {
		return nil
	}
	return &Tools{Runner: run.Exec{}, Just: just, File: Justfile}
}

type dump struct {
	Recipes map[string]struct {
		Name       string `json:"name"`
		Doc        string `json:"doc"`
		Private    bool   `json:"private"`
		Attributes []any  `json:"attributes"`
		Parameters []struct {
			Name    string `json:"name"`
			Default any    `json:"default"`
		} `json:"parameters"`
	} `json:"recipes"`
}

// optionsList finds "Options: a | b | c" in a recipe's description.
var optionsList = regexp.MustCompile(`(?i)options:\s*([a-z0-9_-]+(?:\s*\|\s*[a-z0-9_-]+)+)`)

// Recipes lists the public recipes, by group and name.
func (t *Tools) Recipes(ctx context.Context) ([]Recipe, error) {
	out, err := t.Runner.Run(ctx, t.Just, []string{"--justfile", t.File, "--dump", "--dump-format", "json"}, nil)
	if err != nil {
		return nil, err
	}
	var d dump
	if err := json.Unmarshal([]byte(out), &d); err != nil {
		return nil, fmt.Errorf("ujust: reading recipes: %w", err)
	}
	var rs []Recipe
	for _, r := range d.Recipes {
		if r.Private || strings.HasPrefix(r.Name, "_") {
			continue
		}
		rec := Recipe{Name: r.Name, Doc: strings.TrimSpace(r.Doc), Group: group(r.Attributes)}
		if len(r.Parameters) > 0 {
			p := r.Parameters[0]
			rec.Param = p.Name
			def, _ := p.Default.(string)
			rec.Asks = def == "" || def == "prompt"
			if m := optionsList.FindStringSubmatch(rec.Doc); m != nil {
				for _, o := range strings.Split(m[1], "|") {
					rec.Options = append(rec.Options, strings.TrimSpace(o))
				}
			}
		}
		rs = append(rs, rec)
	}
	sort.Slice(rs, func(i, j int) bool {
		if rs[i].Group != rs[j].Group {
			return groupRank(rs[i].Group) < groupRank(rs[j].Group)
		}
		return rs[i].Name < rs[j].Name
	})
	return rs, nil
}

// group is a recipe's [group('...')] attribute, as just dumps it: {"group": "apps"}.
func group(attrs []any) string {
	for _, a := range attrs {
		if m, ok := a.(map[string]any); ok {
			if g, ok := m["group"].(string); ok {
				return g
			}
		}
	}
	return ""
}

// groupRank puts recipes with no group last.
func groupRank(g string) string {
	if g == "" {
		return "~"
	}
	return g
}

// namePattern and optionPattern keep anything but a recipe name and a plain option off the command line.
var (
	namePattern   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`)
	optionPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)
)

// Command is the command line that runs a recipe with an option ("" for none).
func Command(name, option string) ([]string, error) {
	if !namePattern.MatchString(name) {
		return nil, fmt.Errorf("ujust: invalid recipe %q", name)
	}
	cmd := []string{"ujust", name}
	if option != "" {
		if !optionPattern.MatchString(option) {
			return nil, fmt.Errorf("ujust: invalid option %q", option)
		}
		cmd = append(cmd, option)
	}
	return cmd, nil
}

// ErrNoTerminal is returned when no terminal app is found to run a recipe in.
var ErrNoTerminal = errors.New("no terminal app found to run the command in")

// terminals are terminal apps and how each runs a command, best first. xdg-terminal-exec runs the user's
// preferred terminal.
var terminals = []struct {
	bin  string
	args []string
}{
	{"xdg-terminal-exec", nil},
	{"ptyxis", []string{"--new-window", "--"}},
	{"kgx", []string{"--"}},
	{"konsole", []string{"-e"}},
	{"gnome-terminal", []string{"--"}},
	{"kitty", nil},
	{"alacritty", []string{"-e"}},
	{"foot", nil},
	{"wezterm", []string{"start", "--"}},
	{"xterm", []string{"-e"}},
}

// InTerminal returns the command line that runs cmd in a new terminal window. The window stays open after
// the command ends, so its output can be read.
func InTerminal(cmd []string, lookPath func(string) (string, error)) ([]string, error) {
	// A shell keeps the window open until Enter is pressed. cmd is checked (Command), so quoting it plainly
	// is safe.
	script := strings.Join(cmd, " ") + `; echo; read -r -p "Press Enter to close this window." _`
	inner := []string{"bash", "-c", script}
	for _, t := range terminals {
		p, err := lookPath(t.bin)
		if err != nil {
			continue
		}
		return append(append([]string{p}, t.args...), inner...), nil
	}
	return nil, ErrNoTerminal
}

// Run opens a terminal window running a recipe, and returns once the window has opened.
func Run(name, option string) error {
	cmd, err := Command(name, option)
	if err != nil {
		return err
	}
	argv, err := InTerminal(cmd, exec.LookPath)
	if err != nil {
		return err
	}
	c := exec.Command(argv[0], argv[1:]...)
	if err := c.Start(); err != nil {
		return err
	}
	go c.Wait() // reap it; the window lives on its own
	return nil
}
