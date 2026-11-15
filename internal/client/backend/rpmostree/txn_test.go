package rpmostree

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tyler-builds/uas/internal/client/backend"
	"github.com/tyler-builds/uas/internal/client/run"
)

// hookRunner answers like liveRunner, but lets a test take over some commands.
type hookRunner struct {
	fakeRunner
	mu    sync.Mutex
	hooks map[string]func(ctx context.Context) (string, error)
}

func (h *hookRunner) Run(ctx context.Context, name string, args []string, onLine func(string)) (string, error) {
	call := name + " " + strings.Join(args, " ")
	h.mu.Lock()
	hook := h.hooks[call]
	h.mu.Unlock()
	if hook != nil {
		h.mu.Lock()
		h.calls = append(h.calls, call)
		h.mu.Unlock()
		return hook(ctx)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.fakeRunner.Run(ctx, name, args, onLine)
}

func busyErr() error {
	return &run.Error{Cmd: "rpm-ostree install", Err: errors.New("exit status 1"),
		Stderr: "error: Transaction in progress: install --dry-run htop \n You can cancel the current transaction " +
			"with `rpm-ostree cancel`\n"}
}

const dryRunCall = "rpm-ostree install --dry-run --idempotent pkg"

// An install started while a check's dry run runs cancels the check, and goes ahead.
func TestChangeCancelsCheck(t *testing.T) {
	started := make(chan struct{})
	h := &hookRunner{fakeRunner: *liveRunner(""), hooks: map[string]func(context.Context) (string, error){
		dryRunCall: func(ctx context.Context) (string, error) {
			close(started)
			<-ctx.Done() // a dry run that would take a while
			return "", ctx.Err()
		},
	}}
	b := New(context.Background(), h, "rpm-ostree", "rpm", 44)
	b.Rescan = nil
	l := listing("fedora-45", "pkg")

	result := make(chan backend.Live)
	go func() { result <- b.CheckLive(context.Background(), l) }()
	<-started
	if err := b.Install(context.Background(), l, nil); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-result:
		if got.OK || got.Why != cantTell {
			t.Errorf("cancelled check = %+v", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the check was not cancelled")
	}
	if last := h.calls[len(h.calls)-1]; last != "rpm-ostree install --idempotent pkg" {
		t.Errorf("last call = %q", last)
	}
}

// A check started during an install waits for it to finish.
func TestCheckWaitsForChange(t *testing.T) {
	release := make(chan struct{})
	installing := make(chan struct{})
	h := &hookRunner{fakeRunner: *liveRunner(readSample(t, "dry-run-htop.txt")),
		hooks: map[string]func(context.Context) (string, error){
			"rpm-ostree install --idempotent pkg": func(context.Context) (string, error) {
				close(installing)
				<-release
				return "", nil
			},
		}}
	b := New(context.Background(), h, "rpm-ostree", "rpm", 44)
	b.Rescan = nil
	l := listing("fedora-45", "pkg")

	done := make(chan error)
	go func() { done <- b.Install(context.Background(), l, nil) }()
	<-installing
	checked := make(chan backend.Live)
	go func() { checked <- b.CheckLive(context.Background(), l) }()
	time.Sleep(50 * time.Millisecond)
	h.mu.Lock()
	for _, c := range h.calls {
		if c == dryRunCall {
			t.Error("dry run started during the install")
		}
	}
	h.mu.Unlock()
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	<-checked
	h.mu.Lock()
	defer h.mu.Unlock()
	if !strings.Contains(strings.Join(h.calls, "\n"), dryRunCall) {
		t.Error("the check never ran its dry run")
	}
}

// An install waits while rpm-ostree is busy with a job of its own, such as a cancelled dry run winding down.
func TestChangeWaitsWhileBusy(t *testing.T) {
	defer func(w, r time.Duration) { busyWait, busyRetry = w, r }(busyWait, busyRetry)
	busyWait, busyRetry = time.Minute, time.Millisecond

	tries := 0
	h := &hookRunner{fakeRunner: *liveRunner(""), hooks: map[string]func(context.Context) (string, error){
		"rpm-ostree install --idempotent --apply-live pkg": func(context.Context) (string, error) {
			if tries++; tries < 3 {
				return "", busyErr()
			}
			return "", nil
		},
	}}
	b := New(context.Background(), h, "rpm-ostree", "rpm", 44)
	b.Rescan = nil
	var progress []string
	err := b.InstallLive(context.Background(), listing("fedora-45", "pkg"),
		func(p backend.Progress) { progress = append(progress, p.Message) })
	if err != nil || tries != 3 {
		t.Fatalf("err = %v after %d tries", err, tries)
	}
	if !strings.Contains(strings.Join(progress, "\n"), "Waiting for another rpm-ostree job to finish") {
		t.Errorf("progress = %q", progress)
	}

	// It gives up after busyWait, and other errors aren't retried.
	busyWait = 0
	tries = -10
	if err := b.InstallLive(context.Background(), listing("fedora-45", "pkg"), nil); !isBusy(err) || tries != -9 {
		t.Errorf("err = %v after %d tries", err, tries+10)
	}
	if isBusy(errors.New("Transaction in progress")) || isBusy(nil) {
		t.Error("isBusy accepts errors that aren't rpm-ostree's")
	}
}
