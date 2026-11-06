package ujust

import (
	"context"
	"errors"
	"slices"
	"testing"
)

type fakeRunner struct{ out string }

func (f fakeRunner) Run(context.Context, string, []string, func(string)) (string, error) { return f.out, nil }

const sample = `{"recipes": {
 "_hidden": {"name": "_hidden", "doc": "x", "private": true, "attributes": [], "parameters": []},
 "install-coolercontrol": {"name": "install-coolercontrol", "private": false,
   "doc": "Manage CoolerControl for viewing sensors. Options: status | install | uninstall",
   "attributes": [{"group": "apps"}], "parameters": [{"name": "ACTION", "default": ""}]},
 "bios": {"name": "bios", "private": false, "doc": "Boot into this device's BIOS/UEFI screen",
   "attributes": [], "parameters": []},
 "get-lsfg": {"name": "get-lsfg", "private": false, "doc": "Set up LSFG-VK", "attributes": [{"group": "gaming"}],
   "parameters": [{"name": "ACTION", "default": "prompt"}]}
}}`

func TestRecipes(t *testing.T) {
	rs, err := (&Tools{Runner: fakeRunner{sample}, Just: "just", File: Justfile}).Recipes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, r := range rs {
		names = append(names, r.Name)
	}
	if !slices.Equal(names, []string{"install-coolercontrol", "get-lsfg", "bios"}) {
		t.Fatalf("names = %q", names)
	}
	cc := rs[0]
	if cc.Group != "apps" || cc.Param != "ACTION" || !cc.Asks ||
		!slices.Equal(cc.Options, []string{"status", "install", "uninstall"}) {
		t.Errorf("coolercontrol = %+v", cc)
	}
}

func TestInTerminal(t *testing.T) {
	cmd, err := Command("install-coolercontrol", "install")
	if err != nil {
		t.Fatal(err)
	}
	only := func(name string) func(string) (string, error) {
		return func(b string) (string, error) {
			if b == name {
				return "/usr/bin/" + b, nil
			}
			return "", errors.New("not found")
		}
	}
	argv, err := InTerminal(cmd, only("ptyxis"))
	if err != nil {
		t.Fatal(err)
	}
	if argv[0] != "/usr/bin/ptyxis" || argv[3] != "bash" ||
		argv[5] != `ujust install-coolercontrol install; echo; read -r -p "Press Enter to close this window." _` {
		t.Errorf("argv = %q", argv)
	}
	if _, err := InTerminal(cmd, only("none")); !errors.Is(err, ErrNoTerminal) {
		t.Errorf("no terminal: %v", err)
	}
	for _, bad := range [][2]string{{"x; rm -rf ~", ""}, {"bios", "$(id)"}, {"-h", ""}} {
		if _, err := Command(bad[0], bad[1]); err == nil {
			t.Errorf("Command(%q, %q) should fail", bad[0], bad[1])
		}
	}
}
