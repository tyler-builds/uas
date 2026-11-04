package gui

import (
	"context"
	"maps"
	"strings"

	"github.com/tyler-builds/jamb/ui"

	"github.com/tyler-builds/uas/internal/client/backend"
)

// job is an install, update or removal running in the background.
type job struct {
	title  string // "Installing Firefox"
	status string // the latest progress line
	done   bool
}

// startJob runs work off the UI thread, keyed by id (an app ID), and reports its progress in a toast.
// success is the toast shown when it ends well. restart says the change waits for a restart.
func (a *App) startJob(ctx *ui.Context, id, title string, restart bool, success string,
	work func(context.Context, func(backend.Progress)) error) {
	if j, ok := a.jobs.Peek()[id]; ok && !j.done {
		return // one job per app at a time
	}
	a.setJob(id, job{title: title, status: "Starting…"})
	toastID := "job:" + id
	ctx.ToastWithID(toastID, ui.ToastInfo, "Starting…").Title(title).Sticky()

	go func() {
		err := work(context.Background(), func(p backend.Progress) {
			msg := strings.TrimSpace(p.Message)
			if msg == "" {
				return
			}
			ctx.Post(func() {
				j := a.jobs.Peek()[id]
				j.status = msg
				a.setJob(id, j)
				ctx.ToastWithID(toastID, ui.ToastInfo, msg).Title(title).Sticky()
			})
		})
		ctx.Post(func() {
			a.setJob(id, job{title: title, done: true})
			ctx.DismissToast(toastID)
			if err != nil {
				ctx.Toast(ui.ToastError, errText(err)).Title(title + " failed")
			} else {
				t := ctx.Toast(ui.ToastSuccess, success)
				if restart {
					t.Title("Restart to finish")
				}
			}
			ui.InvalidateQueries(ctx, func(key string) bool { return key == "installed" })
		})
	}()
}

func (a *App) setJob(id string, j job) {
	m := maps.Clone(a.jobs.Peek())
	m[id] = j
	a.jobs.Set(m)
}

// running returns the job for id if one is in progress.
func (a *App) running(id string) (job, bool) {
	j, ok := a.jobs.Get()[id]
	return j, ok && !j.done
}
