package updatecheck

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

type fakeRunner struct{ calls []string }

func (f *fakeRunner) Run(_ context.Context, name string, args []string, _ func(string)) (string, error) {
	f.calls = append(f.calls, name+" "+strings.Join(args, " "))
	return "", nil
}

func TestTimer(t *testing.T) {
	r := &fakeRunner{}
	tm := &Timer{Runner: r, Dir: t.TempDir()}
	if err := tm.Disable(context.Background()); err != nil || len(r.calls) != 0 {
		t.Fatalf("disabling a timer that isn't there: %v, %q", err, r.calls)
	}
	if err := tm.Enable(context.Background(), []string{"/home/me/go bin/uas-gui", "--check-updates"}); err != nil {
		t.Fatal(err)
	}
	svc, _ := os.ReadFile(filepath.Join(tm.Dir, Unit+".service"))
	if !strings.Contains(string(svc), `ExecStart="/home/me/go bin/uas-gui" --check-updates`) {
		t.Errorf("service:\n%s", svc)
	}
	if !tm.Enabled() {
		t.Error("not enabled after Enable")
	}
	if err := tm.Disable(context.Background()); err != nil {
		t.Fatal(err)
	}
	if tm.Enabled() {
		t.Error("still enabled after Disable")
	}
	want := []string{"systemctl --user daemon-reload", "systemctl --user enable --now uas-update-check.timer",
		"systemctl --user disable --now uas-update-check.timer", "systemctl --user daemon-reload"}
	if !slices.Equal(r.calls, want) {
		t.Errorf("calls = %q", r.calls)
	}
	if err := tm.Enable(context.Background(), []string{"uas"}); err == nil {
		t.Error("a relative path should be refused")
	}
	if err := tm.Enable(context.Background(), []string{"/bin/uas", "$(rm -rf ~)"}); err == nil {
		t.Error("shell-like text should be refused")
	}
}

func TestMessage(t *testing.T) {
	r := Result{Updates: []string{"a", "b", "c", "d"}, Names: map[string]string{"a": "Okular", "b": "gimp",
		"c": "KCalc", "d": "Zed"}}
	title, body := Message(r)
	if title != "4 updates available" || body != "gimp, KCalc and 2 more" {
		t.Errorf("got %q, %q", title, body)
	}
	title, body = Message(Result{Updates: []string{"a"}, Names: map[string]string{"a": "Okular"}})
	if title != "An update is available" || body != "Okular" {
		t.Errorf("got %q, %q", title, body)
	}
}
