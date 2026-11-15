package rpmostree

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/tyler-builds/uas/internal/client/run"
)

// How long a change waits for another rpm-ostree job to finish, and how often it tries again meanwhile.
// Variables so tests can shorten them.
var (
	busyWait  = 2 * time.Minute
	busyRetry = 2 * time.Second
)

// txns makes uas's rpm-ostree transactions take turns. rpm-ostree runs one transaction at a time and refuses
// a second ("Transaction in progress"), and a live check's dry run is a transaction too. A change (install
// or removal) cancels the checks that are running rather than wait for them, since a check is only advice;
// a check started during a change waits for it. Changes also wait out rpm-ostree jobs uas didn't start,
// and a cancelled check that rpm-ostree is still winding down. The zero value is ready to use.
type txns struct {
	once   sync.Once
	turn   chan struct{} // holds a token while a transaction runs
	mu     sync.Mutex
	checks map[int]context.CancelFunc // the checks running or waiting for their turn
	next   int
}

func (t *txns) init() {
	t.once.Do(func() {
		t.turn = make(chan struct{}, 1)
		t.checks = map[int]context.CancelFunc{}
	})
}

func (t *txns) take(ctx context.Context) error {
	select {
	case t.turn <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (t *txns) give() { <-t.turn }

// check runs fn, a dry run, in its turn. A change that starts meanwhile cancels the context fn gets.
func (t *txns) check(ctx context.Context, fn func(context.Context) error) error {
	t.init()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	t.mu.Lock()
	id := t.next
	t.next++
	t.checks[id] = cancel
	t.mu.Unlock()
	defer func() {
		t.mu.Lock()
		delete(t.checks, id)
		t.mu.Unlock()
	}()
	if err := t.take(ctx); err != nil {
		return err
	}
	defer t.give()
	return fn(ctx)
}

// change runs fn, which changes the system, in its turn, after cancelling the checks that are running. While
// rpm-ostree says another transaction is in progress, it tries again, up to busyWait.
func (t *txns) change(ctx context.Context, waiting func(), fn func() error) error {
	t.init()
	t.mu.Lock()
	for _, cancel := range t.checks {
		cancel()
	}
	t.mu.Unlock()
	if err := t.take(ctx); err != nil {
		return err
	}
	defer t.give()
	deadline := time.Now().Add(busyWait)
	for {
		err := fn()
		if !isBusy(err) || !time.Now().Before(deadline) {
			return err
		}
		if waiting != nil {
			waiting()
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(busyRetry):
		}
	}
}

// isBusy reports whether rpm-ostree refused to start because another transaction is running.
func isBusy(err error) bool {
	var re *run.Error
	return errors.As(err, &re) && strings.Contains(re.Stderr, "Transaction in progress")
}
