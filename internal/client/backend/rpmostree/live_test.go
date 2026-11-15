package rpmostree

import (
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/tyler-builds/uas/internal/client/backend"
)

// A trimmed rpm-ostree status on Bazzite after "rpm-ostree install --apply-live gnome-tweaks": the staged
// deployment layers gnome-tweaks on the booted image, and the booted one was live-updated to it.
const liveStatusJSON = `{"deployments": [
  {"booted": false, "staged": true, "version": "45.20261112",
   "checksum": "142ded83036b3f523e3f62dcbf40d5d98b4e5f43de0f1f24101c7bd160a06fb8",
   "base-checksum": "4272c61f976625d1c57b24c2921c7037d79b9f52b4ed12b3fbe9d0eab2df1ae0",
   "requested-packages": ["gnome-tweaks"], "requested-local-packages": [],
   "base-commit-meta": {"ostree.linux": "7.3.3-300.fc45.x86_64"}},
  {"booted": true, "staged": false, "version": "45.20261112",
   "checksum": "4272c61f976625d1c57b24c2921c7037d79b9f52b4ed12b3fbe9d0eab2df1ae0",
   "live-replaced": "142ded83036b3f523e3f62dcbf40d5d98b4e5f43de0f1f24101c7bd160a06fb8",
   "requested-packages": [], "requested-local-packages": [],
   "base-commit-meta": {"ostree.linux": "7.3.3-300.fc45.x86_64"}}
]}`

// After that, htop was layered for the next restart: the staged deployment is no longer the live one.
const liveThenLayeredJSON = `{"deployments": [
  {"booted": false, "staged": true, "checksum": "9a0c", "base-checksum": "4272c6",
   "requested-packages": ["gnome-tweaks", "htop"],
   "base-commit-meta": {"ostree.linux": "7.3.3-300.fc45.x86_64"}},
  {"booted": true, "checksum": "4272c6", "live-replaced": "142ded", "requested-packages": [],
   "base-commit-meta": {"ostree.linux": "7.3.3-300.fc45.x86_64"}}
]}`

// A new image is staged, waiting for a restart.
const updateStagedJSON = `{"deployments": [
  {"booted": false, "staged": true, "checksum": "d405cd", "requested-packages": [],
   "base-commit-meta": {"ostree.linux": "7.3.4-300.fc45.x86_64"}},
  {"booted": true, "checksum": "4272c6", "requested-packages": [],
   "base-commit-meta": {"ostree.linux": "7.3.3-300.fc45.x86_64"}}
]}`

func readSample(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestParseDryRun(t *testing.T) {
	plan, ok := parseDryRun(readSample(t, "dry-run-htop.txt"))
	if !ok {
		t.Fatal("not ok")
	}
	want := []nevra{
		{"gnome-tweaks", "49.0", "4.fc45", "noarch"},
		{"htop", "3.5.3", "1.fc45", "x86_64"},
		{"hwloc-libs", "2.14.0", "3.fc45", "x86_64"},
	}
	if !reflect.DeepEqual(plan.install, want) || len(plan.other) != 0 {
		t.Errorf("plan = %+v", plan)
	}

	plan, ok = parseDryRun(readSample(t, "dry-run-remove.txt"))
	if !ok || len(plan.install) != 1 || !reflect.DeepEqual(plan.other, []string{"Removing 1 packages:"}) {
		t.Errorf("remove: plan = %+v, ok = %v", plan, ok)
	}

	// Cut short, or not a dry run's output at all.
	for _, out := range []string{
		"Resolving dependencies...done\nInstalling 1 packages:\n  htop-3.5.3-1.fc45.x86_64 (fedora)\n",
		"error: Package/capability 'gnome-tweaks' is already requested\n",
		"",
		"Installing 1 packages:\n  htop\nExiting because of '--dry-run' option\n",
	} {
		if _, ok := parseDryRun(out); ok {
			t.Errorf("parseDryRun(%q) ok", out)
		}
	}
	// An unknown part of the transaction counts as a change.
	plan, ok = parseDryRun("Installing 1 packages:\n  htop-3.5.3-1.fc45.x86_64 (fedora)\nUpgraded:\n" +
		"  libfoo-2-1.fc45.x86_64\nExiting because of '--dry-run' option\n")
	if !ok || len(plan.other) != 1 {
		t.Errorf("upgrade: plan = %+v, ok = %v", plan, ok)
	}
}

func TestParseNEVRA(t *testing.T) {
	for in, want := range map[string]nevra{
		"hwloc-libs-2.14.0-3.fc45.x86_64": {"hwloc-libs", "2.14.0", "3.fc45", "x86_64"},
		"foo-2:1.0-1.fc45.noarch":         {"foo", "1.0", "1.fc45", "noarch"},
		"kernel-debug-core-7.3.3-300.fc45.x86_64": {"kernel-debug-core", "7.3.3", "300.fc45",
			"x86_64"},
	} {
		if got, ok := parseNEVRA(in); !ok || got != want {
			t.Errorf("parseNEVRA(%q) = %+v, %v", in, got, ok)
		}
	}
	for _, in := range []string{"htop", "htop-3.5.3", "-1-2.x86_64", "a-b-c."} {
		if _, ok := parseNEVRA(in); ok {
			t.Errorf("parseNEVRA(%q) ok", in)
		}
	}
}

func TestRestartOnly(t *testing.T) {
	for _, n := range []string{"kernel", "kernel-core", "kernel-debug-core", "kernel-modules-extra", "kmod-nvidia",
		"akmod-nvidia", "akmods", "dkms", "v4l2loopback-dkms", "xone-kmod-common", "dracut-network", "grub2-efi-x64",
		"shim-x64", "systemd-boot-unsigned", "plymouth-theme-spinner", "linux-firmware", "iwlwifi-mvm-firmware",
		"microcode_ctl", "selinux-policy-devel", "container-selinux", "xorg-x11-drv-amdgpu", "nvidia-settings",
		"bootc"} {
		if !restartOnly(n) {
			t.Errorf("restartOnly(%q) = false", n)
		}
	}
	for _, n := range []string{"htop", "gnome-tweaks", "kernel-headers", "kernel-devel", "kernelshark",
		"kmodtool", "grubby", "firmware-manager", "shimmer", "steam-devices", "gcc"} {
		if restartOnly(n) {
			t.Errorf("restartOnly(%q) = true", n)
		}
	}
}

// liveRunner answers like a Fedora 45 system without htop, akmods and friends, where gnome-tweaks was
// applied live and libfoo 2.0 is part of the image.
func liveRunner(dryRun string) *fakeRunner {
	return &fakeRunner{
		outputs: map[string]string{
			"rpm-ostree status --json":                      liveStatusJSON,
			"rpm-ostree install --dry-run --idempotent pkg": dryRun,
		},
		fail: map[string]error{"rpm -q --quiet pkg": errors.New("exit status 1")},
	}
}

// answerVersions makes rpm -q --qf answer for the names in the dry run, as rpm does.
func answerVersions(r *fakeRunner, dryRun string, have map[string]string) {
	plan, _ := parseDryRun(dryRun)
	args := []string{"rpm", "-q", "--qf", `%{NAME}.%{ARCH} %{VERSION}-%{RELEASE}\n`}
	var out strings.Builder
	missing := false
	for _, p := range plan.install {
		args = append(args, p.name)
		if v, ok := have[p.name]; ok {
			out.WriteString(p.name + "." + p.arch + " " + v + "\n")
		} else {
			out.WriteString("package " + p.name + " is not installed\n")
			missing = true
		}
	}
	call := strings.Join(args, " ")
	r.outputs[call] = out.String()
	if missing {
		r.fail[call] = errors.New("exit status 1")
	}
}

func TestCheckLive(t *testing.T) {
	cases := []struct {
		sample string
		have   map[string]string
		ok     bool
		why    string
	}{
		// Only adds packages; gnome-tweaks, from the earlier live install, is the same version.
		{"dry-run-htop.txt", map[string]string{"gnome-tweaks": "49.0-4.fc45"}, true, ""},
		{"dry-run-akmods.txt", nil, false, "it includes akmods, which works only after a restart"},
		{"dry-run-kernel-debug-core.txt", nil, false,
			"it includes kernel-debug-core, which works only after a restart"},
		{"dry-run-dracut-network.txt", nil, false, "it includes dracut-network, which works only after a restart"},
		{"dry-run-replace.txt", map[string]string{"libfoo": "2.0-1.fc45"}, false,
			"it would replace libfoo in the system image"},
		{"dry-run-remove.txt", nil, false, "it would change or remove packages in the system image"},
	}
	for _, c := range cases {
		out := readSample(t, c.sample)
		r := liveRunner(out)
		answerVersions(r, out, c.have)
		b := newBackend(r)
		got := b.CheckLive(context.Background(), listing("fedora-45", "pkg"))
		if got.OK != c.ok || got.Why != c.why {
			t.Errorf("%s: CheckLive = %+v, want ok %v why %q", c.sample, got, c.ok, c.why)
		}
	}
}

func TestCheckLiveSaysNo(t *testing.T) {
	ctx := context.Background()
	l := listing("fedora-45", "pkg")

	// The dry run fails.
	r := liveRunner("")
	r.fail["rpm-ostree install --dry-run --idempotent pkg"] = errors.New("exit status 1")
	if got := newBackend(r).CheckLive(ctx, l); got.OK || got.Why != cantTell {
		t.Errorf("failed dry run: %+v", got)
	}

	// rpm can't say what the system has.
	out := readSample(t, "dry-run-htop.txt")
	r = liveRunner(out)
	if got := newBackend(r).CheckLive(ctx, l); got.OK || got.Why != cantTell {
		t.Errorf("no rpm answer: %+v", got)
	}

	// The image already has it.
	r = liveRunner(out)
	delete(r.fail, "rpm -q --quiet pkg")
	if got := newBackend(r).CheckLive(ctx, l); got.OK || got.Why != "it is already on the system" {
		t.Errorf("in image: %+v", got)
	}

	// A system update is staged: no dry run at all.
	r = liveRunner(out)
	r.outputs["rpm-ostree status --json"] = updateStagedJSON
	if got := newBackend(r).CheckLive(ctx, l); got.OK || got.Why != "a system update is waiting for a restart" {
		t.Errorf("update staged: %+v", got)
	}
	for _, c := range r.calls {
		if strings.Contains(c, "--dry-run") {
			t.Errorf("ran %q with an update staged", c)
		}
	}

	// A listing for another release.
	if got := newBackend(liveRunner(out)).CheckLive(ctx, listing("fedora-44", "pkg")); got.OK {
		t.Errorf("other release: %+v", got)
	}
}

func TestInstallLive(t *testing.T) {
	r := liveRunner("")
	b := newBackend(r)
	rescanned := false
	b.Rescan = func() { rescanned = true }
	var progress []string
	err := b.InstallLive(context.Background(), listing("fedora-45", "pkg"),
		func(p backend.Progress) { progress = append(progress, p.Message) })
	if err != nil {
		t.Fatal(err)
	}
	if last := r.calls[len(r.calls)-1]; last != "rpm-ostree install --idempotent --apply-live pkg" {
		t.Errorf("last call = %q", last)
	}
	for _, c := range r.calls {
		if strings.Contains(c, "allow-replacement") {
			t.Errorf("ran %q", c)
		}
	}
	if !rescanned {
		t.Error("launchers not rescanned")
	}

	// A failed apply doesn't rescan.
	r = liveRunner("")
	r.fail["rpm-ostree install --idempotent --apply-live pkg"] = errors.New("exit status 1")
	b = newBackend(r)
	rescanned = false
	b.Rescan = func() { rescanned = true }
	if err := b.InstallLive(context.Background(), listing("fedora-45", "pkg"), nil); err == nil || rescanned {
		t.Errorf("err = %v, rescanned = %v", err, rescanned)
	}
}

func TestInstalledAfterLiveApply(t *testing.T) {
	r := &fakeRunner{outputs: map[string]string{"rpm-ostree status --json": liveStatusJSON}}
	apps, err := newBackend(r).Installed(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(apps) != 1 || apps[0].Package != "gnome-tweaks" || apps[0].Pending {
		t.Errorf("apps = %+v, want gnome-tweaks, not pending", apps)
	}

	// htop was layered for the next restart since; the running system's rpm database has gnome-tweaks only.
	r = &fakeRunner{
		outputs: map[string]string{
			"rpm-ostree status --json": liveThenLayeredJSON,
			"rpm -q gnome-tweaks htop": "gnome-tweaks-49.0-4.fc45.noarch\npackage htop is not installed\n",
		},
		fail: map[string]error{"rpm -q gnome-tweaks htop": errors.New("exit status 1")},
	}
	apps, err = newBackend(r).Installed(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	pending := map[string]bool{}
	for _, a := range apps {
		pending[a.Package] = a.Pending
	}
	if !reflect.DeepEqual(pending, map[string]bool{"gnome-tweaks": false, "htop": true}) {
		t.Errorf("pending = %v", pending)
	}
}
