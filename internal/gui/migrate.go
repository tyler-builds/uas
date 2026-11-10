package gui

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/tyler-builds/jamb/icons"
	"github.com/tyler-builds/jamb/ui"

	"github.com/tyler-builds/uas/internal/client/backend"
	"github.com/tyler-builds/uas/internal/client/session"
)

// migratePlan is an open Migrate dialog: scanning, then what the scan found and what the user picked of it.
type migratePlan struct {
	scanning bool
	err      error
	scan     session.MigrationScan
	// picked says, for each of scan.Apps, whether to migrate it.
	picked []*ui.Signal[bool]
	// leftovers says whether to clear the records Gear Lever keeps of AppImages that are gone.
	leftovers *ui.Signal[bool]
}

// chosen are the apps picked to migrate.
func (p *migratePlan) chosen() []session.Migration {
	var out []session.Migration
	for i, m := range p.scan.Apps {
		if m.Blocked == "" && p.picked[i].Get() {
			out = append(out, m)
		}
	}
	return out
}

// migrateRow is the Settings row that opens the Migrate dialog.
func (a *App) migrateRow(ctx *ui.Context) ui.Element {
	t := ctx.Theme()
	_, busy := a.running("migrate")
	return settingsRow(ctx, ui.Icon(icons.ArrowRightLeft).Size(18).Color(t.TextMuted),
		"Migrate apps to uas",
		"Find AppImages that Gear Lever, AppImageLauncher or you set up, and let uas manage and update them.",
		ui.Button("Migrate…").Icon(icons.Search).Secondary().Small().Loading(busy).
			Tooltip("Look for AppImages uas doesn't manage yet; nothing changes until you choose").
			OnClick(func() { a.openMigrate(ctx) }))
}

// openMigrate opens the Migrate dialog and scans the system off the UI thread.
func (a *App) openMigrate(ctx *ui.Context) {
	a.migrate.Set(&migratePlan{scanning: true})
	go func() {
		scan, err := a.scanMigrations(context.Background())
		ctx.Post(func() {
			if a.migrate.Peek() == nil {
				return // closed while scanning
			}
			a.migrate.Set(newMigratePlan(scan, err))
		})
	}()
}

// newMigratePlan is the dialog for a scan's results, with the suggested apps picked.
func newMigratePlan(scan session.MigrationScan, err error) *migratePlan {
	p := &migratePlan{scan: scan, err: err, leftovers: ui.NewSignal(len(scan.Leftovers) > 0)}
	for _, m := range scan.Apps {
		p.picked = append(p.picked, ui.NewSignal(m.Suggested && m.Blocked == ""))
	}
	return p
}

// migrateNote says, under an app in the Migrate dialog, where it is and what uas will do with it.
func migrateNote(m session.Migration) string {
	where := "A file in " + m.Folder
	switch m.From {
	case "":
	case "a launcher":
		where = "In " + m.Folder + ", with a launcher"
	default:
		where = "From " + m.From + ", in " + m.Folder
	}
	switch {
	case m.Blocked != "":
		return where + ". Can't be migrated: " + m.Blocked + "."
	case m.Catalog:
		return where + ". uas will update it from the catalog (" + m.Updates + ")."
	case m.Updates != "":
		return where + ". uas will update it from " + m.Updates + "."
	}
	return where + ". It doesn't say where its updates come from, so uas can't update it."
}

// migrateDialog scans for AppImages uas doesn't manage, lists them for the user to pick from, and hands the
// picked ones over to uas.
func (a *App) migrateDialog(ctx *ui.Context) ui.Element {
	p := a.migrate.Get()
	if p == nil {
		return ui.Div().Display(false)
	}
	t := ctx.Theme()
	const title = "Migrate apps to uas"
	closeIt := func() { a.migrate.Set(nil) }
	switch {
	case p.scanning:
		return ui.AlertDialog(title, "Looking for AppImages that uas doesn't manage yet…").
			Body(ui.Div().Row().Align(ui.Center).Gap(space3).PaddingY(space2).
				Child(ui.ProgressRing().Size(ui.RingSmall).Label("Scanning")).
				Child(muted(ctx, "Checking other tools' launchers and your AppImage folders. Nothing is changed."))).
			Width(ui.Px(560)).Cancel("", closeIt)
	case p.err != nil:
		return ui.AlertDialog(title, "The scan failed: "+errText(p.err)).Width(ui.Px(560)).Cancel("Close", closeIt)
	case len(p.scan.Apps) == 0 && len(p.scan.Leftovers) == 0:
		return ui.AlertDialog(title, "Nothing to migrate: uas found no AppImages that it doesn't manage already.").
			Width(ui.Px(560)).Cancel("Close", closeIt)
	}

	list := ui.Div().Column().Gap(space3).MaxHeight(ui.Px(340)).ScrollY().Key("migrate-list")
	for i, m := range p.scan.Apps {
		label := m.Name
		if m.Version != "" {
			label += " " + m.Version
		}
		list = list.Child(ui.Div().Column().Gap(2).
			Child(ui.Checkbox(label, p.picked[i]).Disabled(m.Blocked != "").Key("migrate:" + m.Path)).
			Child(ui.Div().PaddingLeft(space6).Child(ui.Text(migrateNote(m)).FontSize(12).TextColor(t.TextMuted))))
	}
	if n := len(p.scan.Leftovers); n > 0 {
		names := make([]string, n)
		for i, l := range p.scan.Leftovers {
			names[i] = l.Name
		}
		label := fmt.Sprintf("Clear %s Gear Lever still keeps of AppImages that are gone", plural(n, "record", "records"))
		list = list.Child(ui.Div().Column().Gap(2).
			Child(ui.Checkbox(label, p.leftovers).Key("migrate-leftovers")).
			Child(ui.Div().PaddingLeft(space6).Child(ui.Text(strings.Join(names, ", ") +
				". Only Gear Lever's notes and icons of them are removed.").FontSize(12).TextColor(t.TextMuted))))
	}

	chosen := p.chosen()
	clear := len(p.scan.Leftovers) > 0 && p.leftovers.Get()
	summary := "uas found " + plural(len(p.scan.Apps), "AppImage", "AppImages") + " it doesn't manage. For each one " +
		"you pick, uas moves the file into its own folder, replaces the other tool's launcher with its own, and " +
		"keeps it updated where it can."
	if len(p.scan.Apps) == 0 {
		summary = "uas found no AppImages to migrate, but Gear Lever still keeps records of some that are gone."
	}
	d := ui.AlertDialog(title, summary).Body(list).Width(ui.Px(560)).Cancel("", closeIt)
	label := "Migrate " + plural(len(chosen), "app", "apps")
	switch {
	case len(chosen) == 0 && clear:
		label = "Clear records"
	case len(chosen) == 0:
		return d // nothing picked: only Cancel
	}
	return d.Confirm(label, func() {
		a.migrate.Set(nil)
		var leftovers = p.scan.Leftovers
		if !clear {
			leftovers = nil
		}
		a.runMigrate(ctx, chosen, len(leftovers), func() error { return a.clearLeftovers(leftovers) })
	})
}

// runMigrate hands the chosen AppImages over to uas one after another, then clears leftovers.
func (a *App) runMigrate(ctx *ui.Context, apps []session.Migration, leftovers int, clear func() error) {
	title, done := "Migrating "+plural(len(apps), "app", "apps"), plural(len(apps), "app is", "apps are")+" now managed by uas"
	if len(apps) == 0 {
		title, done = "Clearing Gear Lever's records", "Gear Lever's leftover records are cleared"
	}
	a.startJob(ctx, "migrate", title, false, done, func(c context.Context, prog func(backend.Progress)) error {
		var errs []error
		for i, m := range apps {
			prog(backend.Progress{Message: fmt.Sprintf("Migrating %s (%d of %d)", m.Name, i+1, len(apps))})
			if err := a.migrateOne(c, m, prog); err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", m.Name, err))
			}
		}
		if leftovers > 0 {
			if err := clear(); err != nil {
				errs = append(errs, fmt.Errorf("Gear Lever's records: %w", err))
			}
		}
		return errors.Join(errs...)
	})
}
