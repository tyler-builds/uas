package copr

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/tyler-builds/uas/internal/catalog"
	"github.com/tyler-builds/uas/internal/client/backend"
	"github.com/tyler-builds/uas/internal/client/backend/rpmostree"
	"github.com/tyler-builds/uas/internal/client/state"
)

// fakeSystem plays rpm-ostree, rpm and pkexec: it tracks layered packages and really copies or deletes
// repo files, so the test can check what ends up on disk.
type fakeSystem struct {
	t       *testing.T
	layered []string
	calls   []string
}

func (f *fakeSystem) Run(_ context.Context, name string, args []string, _ func(string)) (string, error) {
	call := name + " " + strings.Join(args, " ")
	f.calls = append(f.calls, call)
	switch {
	case call == "rpm-ostree status --json":
		pkgs := `[]`
		if len(f.layered) > 0 {
			pkgs = `["` + strings.Join(f.layered, `","`) + `"]`
		}
		return fmt.Sprintf(`{"deployments": [{"booted": true, "requested-packages": %s,
			"base-commit-meta": {"ostree.linux": "7.2.14-300.fc45.x86_64"}}]}`, pkgs), nil
	case name == "rpm":
		return "", errors.New("not installed")
	case name == "rpm-ostree" && args[0] == "install":
		f.layered = append(f.layered, args[len(args)-1])
		return "", nil
	case name == "rpm-ostree" && args[0] == "uninstall":
		f.layered = slices.DeleteFunc(f.layered, func(p string) bool { return p == args[1] })
		return "", nil
	case name == "pkexec" && args[0] == "install":
		b, err := os.ReadFile(args[len(args)-2])
		if err != nil {
			return "", err
		}
		return "", os.WriteFile(args[len(args)-1], b, 0o644)
	case name == "pkexec" && args[0] == "rm":
		return "", os.Remove(args[len(args)-1])
	}
	f.t.Errorf("unexpected command %q", call)
	return "", nil
}

func listing(project, pkg string) catalog.Listing {
	return catalog.Listing{Source: catalog.SourceCOPR, Ref: "fedora-45/" + project + "/" + pkg, Project: project,
		Package: pkg, Distro: "fedora-45"}
}

func TestInstallAndRemove(t *testing.T) {
	sys := &fakeSystem{t: t}
	dir := t.TempDir()
	repoDir := filepath.Join(dir, "yum.repos.d")
	os.Mkdir(repoDir, 0o755)
	ostree := rpmostree.New(context.Background(), sys, "rpm-ostree", "rpm", 44)
	b := &Backend{Ostree: ostree, Runner: sys, Root: []string{"pkexec"}, RepoDir: repoDir,
		State: state.Store{Path: filepath.Join(dir, "state.json")}}
	ctx := context.Background()

	if !b.Supports(listing("@team/notes", "notes")) || b.Supports(listing("@team/notes", "--x")) {
		t.Fatal("Supports")
	}
	l44 := listing("@team/notes", "notes")
	l44.Distro = "fedora-44"
	if b.Supports(l44) {
		t.Error("Supports accepted a listing for the running release instead of the target one")
	}

	for _, pkg := range []string{"notes", "notes-sync"} {
		if err := b.Install(ctx, listing("@team/notes", pkg), nil); err != nil {
			t.Fatal(err)
		}
	}
	repoFile := filepath.Join(repoDir, "_copr:copr.fedorainfracloud.org:group_team:notes.repo")
	content, err := os.ReadFile(repoFile)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"[copr:copr.fedorainfracloud.org:group_team:notes]",
		"baseurl=https://download.copr.fedorainfracloud.org/results/@team/notes/fedora-$releasever-$basearch/",
		"gpgcheck=1", "gpgkey=https://download.copr.fedorainfracloud.org/results/@team/notes/pubkey.gpg",
	} {
		if !strings.Contains(string(content), want) {
			t.Errorf("repo file lacks %q:\n%s", want, content)
		}
	}
	// The repo was enabled once, for the first package.
	if n := len(slices.DeleteFunc(slices.Clone(sys.calls), func(c string) bool { return !strings.HasPrefix(c, "pkexec install") })); n != 1 {
		t.Errorf("pkexec install ran %d times", n)
	}

	apps, err := b.Installed(ctx)
	if err != nil || len(apps) != 2 || apps[0].Origin != "copr:@team/notes" {
		t.Fatalf("installed = %+v, %v", apps, err)
	}
	// The Fedora backend hides packages COPR accounts for.
	ostree.Exclude = func(pkg string) bool { return pkg == "notes" || pkg == "notes-sync" }
	if fed, _ := ostree.Installed(ctx); len(fed) != 0 {
		t.Errorf("fedora backend still lists %+v", fed)
	}

	// The repo stays while another package uses it, and goes with the last one.
	if err := b.Remove(ctx, apps[0], nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(repoFile); err != nil {
		t.Error("repo removed while still in use")
	}
	if err := b.Remove(ctx, apps[1], nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(repoFile); !errors.Is(err, os.ErrNotExist) {
		t.Error("repo file left behind")
	}
	if len(sys.layered) != 0 {
		t.Errorf("still layered: %v", sys.layered)
	}
}

func TestExistingRepoIsKept(t *testing.T) {
	sys := &fakeSystem{t: t}
	dir := t.TempDir()
	name, _ := RepoFileName("solo/tools")
	os.WriteFile(filepath.Join(dir, name), []byte("[user's own]\n"), 0o644)
	b := &Backend{Ostree: rpmostree.New(context.Background(), sys, "rpm-ostree", "rpm", 44), Runner: sys,
		Root: []string{"pkexec"}, RepoDir: dir, State: state.Store{Path: filepath.Join(t.TempDir(), "state.json")}}
	ctx := context.Background()
	if err := b.Install(ctx, listing("solo/tools", "tool"), nil); err != nil {
		t.Fatal(err)
	}
	apps, _ := b.Installed(ctx)
	if err := b.Remove(ctx, apps[0], nil); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, name)); string(b) != "[user's own]\n" {
		t.Error("touched a repo file the user created")
	}
	for _, c := range sys.calls {
		if strings.HasPrefix(c, "pkexec") {
			t.Errorf("ran %q for a repo that was already enabled", c)
		}
	}
}

func TestDisableRepoRefusesOtherFiles(t *testing.T) {
	b := &Backend{RepoDir: "/etc/yum.repos.d", Runner: &fakeSystem{t: t}, Root: []string{"pkexec"}}
	for _, p := range []string{"/etc/yum.repos.d/fedora.repo", "/etc/passwd", "/etc/yum.repos.d/../shadow"} {
		if err := b.disableRepo(context.Background(), p); err == nil {
			t.Errorf("disableRepo(%q) succeeded", p)
		}
	}
}

// fakeDNF plays dnf, rpm and pkexec on package-based Fedora.
type fakeDNF struct {
	t         *testing.T
	installed map[string]string // package: version
	upgrades  string            // what "dnf list --upgrades" prints
	calls     []string
}

func (f *fakeDNF) Run(_ context.Context, name string, args []string, _ func(string)) (string, error) {
	call := name + " " + strings.Join(args, " ")
	f.calls = append(f.calls, call)
	switch {
	case call == "rpm -qa --qf %{NAME}\t%{VERSION}-%{RELEASE}\n":
		var b strings.Builder
		b.WriteString("bash\t5.3-1.fc45\n")
		for p, v := range f.installed {
			b.WriteString(p + "\t" + v + "\n")
		}
		return b.String(), nil
	case call == "dnf list --upgrades --quiet":
		return f.upgrades, nil
	case name == "pkexec" && args[0] == "dnf" && args[1] == "install":
		f.installed[args[len(args)-1]] = "1.0-1.fc45"
		return "", nil
	case name == "pkexec" && args[0] == "dnf" && args[1] == "upgrade":
		f.installed[args[len(args)-1]] = "1.1-1.fc45"
		return "", nil
	case name == "pkexec" && args[0] == "dnf" && args[1] == "remove":
		delete(f.installed, args[len(args)-1])
		return "", nil
	case name == "pkexec" && args[0] == "install":
		b, err := os.ReadFile(args[len(args)-2])
		if err != nil {
			return "", err
		}
		return "", os.WriteFile(args[len(args)-1], b, 0o644)
	case name == "pkexec" && args[0] == "rm":
		return "", os.Remove(args[len(args)-1])
	}
	f.t.Errorf("unexpected command %q", call)
	return "", nil
}

// On package-based Fedora, COPR packages install, update and remove with dnf, and need no restart.
func TestDNF(t *testing.T) {
	sys := &fakeDNF{t: t, installed: map[string]string{},
		upgrades: "Available upgrades\nnotes.x86_64  1.1-1.fc45  copr:copr.fedorainfracloud.org:group_team:notes\n" +
			"bash.x86_64  5.3-2.fc45  updates\n"}
	dir := t.TempDir()
	repoDir := filepath.Join(dir, "yum.repos.d")
	os.Mkdir(repoDir, 0o755)
	b := &Backend{DNFRelease: 45, Runner: sys, Root: []string{"pkexec"}, RepoDir: repoDir,
		State: state.Store{Path: filepath.Join(dir, "state.json")}}
	ctx := context.Background()

	l44 := listing("@team/notes", "notes")
	l44.Distro = "fedora-44"
	if !b.Supports(listing("@team/notes", "notes")) || b.Supports(l44) || b.NeedsRestart() {
		t.Fatal("Supports or NeedsRestart")
	}
	if err := b.Install(ctx, listing("@team/notes", "notes"), nil); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(sys.calls, "pkexec dnf install -y -- notes") {
		t.Errorf("calls = %q", sys.calls)
	}
	repoFile := filepath.Join(repoDir, "_copr:copr.fedorainfracloud.org:group_team:notes.repo")
	if _, err := os.Stat(repoFile); err != nil {
		t.Fatal("repo not enabled")
	}

	apps, err := b.Installed(ctx)
	if err != nil || len(apps) != 1 || apps[0].Version != "1.0-1.fc45" || apps[0].Pending {
		t.Fatalf("installed = %+v, %v", apps, err)
	}
	ups, err := b.Updates(ctx)
	if err != nil || len(ups) != 1 || ups[0].Ref != "notes" || ups[0].Version != "1.1-1.fc45" ||
		apps[0].Key() != backend.UpdateKey(catalog.SourceCOPR, ups[0].Scope, ups[0].Ref) {
		t.Fatalf("updates = %+v, %v", ups, err)
	}
	if err := b.Update(ctx, apps[0], nil); err != nil {
		t.Fatal(err)
	}
	if err := b.Remove(ctx, apps[0], nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(repoFile); !errors.Is(err, os.ErrNotExist) || len(sys.installed) != 0 {
		t.Error("package or repo left behind")
	}
}
