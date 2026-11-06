// Package updatecheck looks for app updates in the background and tells the user with a desktop
// notification. It never installs anything: the notification opens uas on its Updates page.
//
// The check runs from a systemd user timer (uas-update-check.timer), once a day and soon after login, which
// the Settings switch writes and enables. The service runs this program again with an argument that runs one
// check and exits.
package updatecheck

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/tyler-builds/uas/internal/client/notify"
	"github.com/tyler-builds/uas/internal/client/refresh"
	"github.com/tyler-builds/uas/internal/client/run"
	"github.com/tyler-builds/uas/internal/client/session"
	"github.com/tyler-builds/uas/internal/version"
)

// Unit is the systemd user units' name, without the suffix.
const Unit = "uas-update-check"

// DesktopEntry is the desktop app's launcher, which the notification is shown as.
const DesktopEntry = "io.github.tyler_builds.uas"

// Result is what one check found.
type Result struct {
	// Updates are the update keys (backend.UpdateKey) found, and New those not notified about before.
	Updates []string
	New     []string
	// Names are the apps with updates, by key, for the message.
	Names map[string]string
	// pending are the updates as remembered once notified: key and version.
	pending []string
}

// selfKey is the update key of uas itself.
const selfKey = "uas"

// Check refreshes the catalog (GitHub, GitLab and Codeberg updates come from it) and lists the installed
// apps that have updates, noting which weren't in the last notification. Only Notify remembers them.
func Check(ctx context.Context, s *session.Session) (Result, error) {
	// A failed refresh still leaves the last catalog, which is good enough for Flathub and Homebrew.
	_, _ = refresh.Refresh(ctx, session.RefreshOptions(s.CatalogPath))
	ups, err := s.Updates(ctx)
	if len(ups) == 0 && err != nil {
		return Result{}, err
	}
	installed, err := s.Installed(ctx)
	if err != nil {
		return Result{}, err
	}
	r := Result{Names: map[string]string{}}
	for _, in := range installed {
		if _, ok := ups[in.Key()]; ok {
			r.Updates = append(r.Updates, in.Key())
			r.Names[in.Key()] = in.Name
		}
	}
	// uas itself, for released builds.
	if version.Release() {
		if a, err := s.SelfUpdater().Check(ctx); err == nil && a != nil {
			r.Updates = append(r.Updates, selfKey)
			r.Names[selfKey] = "uas " + a.Release.Version
			ups[selfKey] = a.Release.Version
		}
	}
	sort.Strings(r.Updates)
	seen := loadNotified()
	for _, k := range r.Updates {
		if !slices.Contains(seen, k+"|"+ups[k]) {
			r.New = append(r.New, k)
		}
	}
	// With versions, so a newer version of the same app counts as new.
	for _, k := range r.Updates {
		r.pending = append(r.pending, k+"|"+ups[k])
	}
	return r, nil
}

// Message is the notification's title and text for a result.
func Message(r Result) (title, body string) {
	var names []string
	for _, k := range r.Updates {
		names = append(names, r.Names[k])
	}
	sort.Slice(names, func(i, j int) bool { return strings.ToLower(names[i]) < strings.ToLower(names[j]) })
	title = fmt.Sprintf("%d updates available", len(names))
	if len(names) == 1 {
		title = "An update is available"
	}
	switch {
	case len(names) <= 3:
		body = strings.Join(names, ", ")
	default:
		body = fmt.Sprintf("%s and %d more", strings.Join(names[:2], ", "), len(names)-2)
	}
	return title, body
}

// Notify shows a notification for new updates and, if it is clicked within wait, runs open.
func Notify(ctx context.Context, r Result, wait time.Duration, open func() error) error {
	if len(r.New) == 0 {
		return saveNotified(r.pending) // forget updates that were installed since
	}
	title, body := Message(r)
	shown, err := notify.Send(notify.Notification{AppName: "uas", Icon: DesktopEntry, Title: title, Body: body,
		DesktopEntry: DesktopEntry, Action: "Show updates"})
	if err != nil {
		return err
	}
	defer shown.Close()
	if err := saveNotified(r.pending); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	if shown.WaitAction(ctx) && open != nil {
		return open()
	}
	return nil
}

// OpenUpdates starts the desktop app (the program at gui) on its Updates page. It runs it as its own
// transient systemd unit when it can, so the app outlives the check's service.
func OpenUpdates(gui string) error {
	if p, err := exec.LookPath("systemd-run"); err == nil {
		return exec.Command(p, "--user", "--collect", "--quiet", gui, "--page", "updates").Run()
	}
	cmd := exec.Command(gui, "--page", "updates")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	return cmd.Start()
}

// notifiedPath is where the updates of the last notification are kept.
func notifiedPath() (string, error) {
	dir := os.Getenv("XDG_STATE_HOME")
	if !filepath.IsAbs(dir) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		dir = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(dir, "uas", "notified.json"), nil
}

func loadNotified() []string {
	p, err := notifiedPath()
	if err != nil {
		return nil
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return nil
	}
	var keys []string
	json.Unmarshal(b, &keys)
	return keys
}

func saveNotified(keys []string) error {
	p, err := notifiedPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	b, _ := json.Marshal(keys)
	return os.WriteFile(p, b, 0o644)
}

// Timer turns the background check on and off by writing and enabling a systemd user timer.
type Timer struct {
	Runner run.Runner
	// Dir is the systemd user unit folder, normally ~/.config/systemd/user.
	Dir string
}

// DefaultTimer returns the timer in the user's systemd folder.
func DefaultTimer() (*Timer, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return nil, err
	}
	return &Timer{Runner: run.Exec{}, Dir: filepath.Join(dir, "systemd", "user")}, nil
}

// Enable writes the units to run command (an absolute path and its arguments) and starts the timer.
func (t *Timer) Enable(ctx context.Context, command []string) error {
	if len(command) == 0 || !filepath.IsAbs(command[0]) {
		return errors.New("the update check needs an absolute path to run")
	}
	var quoted []string
	for _, c := range command {
		if strings.ContainsAny(c, "\"\\\n%$") {
			return fmt.Errorf("can't use %q in a systemd unit", c)
		}
		if strings.ContainsAny(c, " \t") {
			c = `"` + c + `"`
		}
		quoted = append(quoted, c)
	}
	service := "[Unit]\nDescription=Check for app updates (uas)\nAfter=network-online.target\n\n" +
		"[Service]\nType=oneshot\nExecStart=" + strings.Join(quoted, " ") + "\n" +
		// The check waits a while for a click on its notification.
		"TimeoutStartSec=2h\nNice=10\n"
	timer := "[Unit]\nDescription=Check for app updates daily (uas)\n\n" +
		"[Timer]\nOnStartupSec=15min\nOnCalendar=daily\nRandomizedDelaySec=1h\nPersistent=true\n\n" +
		"[Install]\nWantedBy=timers.target\n"
	if err := os.MkdirAll(t.Dir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(t.Dir, Unit+".service"), []byte(service), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(t.Dir, Unit+".timer"), []byte(timer), 0o644); err != nil {
		return err
	}
	if _, err := t.Runner.Run(ctx, "systemctl", []string{"--user", "daemon-reload"}, nil); err != nil {
		return err
	}
	_, err := t.Runner.Run(ctx, "systemctl", []string{"--user", "enable", "--now", Unit + ".timer"}, nil)
	return err
}

// Disable stops the timer and removes the units.
func (t *Timer) Disable(ctx context.Context) error {
	if !t.Enabled() {
		return nil
	}
	_, err := t.Runner.Run(ctx, "systemctl", []string{"--user", "disable", "--now", Unit + ".timer"}, nil)
	for _, suffix := range []string{".service", ".timer"} {
		if rerr := os.Remove(filepath.Join(t.Dir, Unit+suffix)); rerr != nil && !errors.Is(rerr, os.ErrNotExist) {
			err = errors.Join(err, rerr)
		}
	}
	if _, rerr := t.Runner.Run(ctx, "systemctl", []string{"--user", "daemon-reload"}, nil); rerr != nil {
		err = errors.Join(err, rerr)
	}
	return err
}

// Enabled reports whether the timer's units are in place.
func (t *Timer) Enabled() bool {
	_, err := os.Stat(filepath.Join(t.Dir, Unit+".timer"))
	return err == nil
}
