// Package notify shows desktop notifications through the freedesktop notification service on the session
// bus, which GNOME, KDE and the other desktops provide. It talks D-Bus directly rather than running
// notify-send, which not every system ships in working order.
package notify

import (
	"context"
	"errors"

	"github.com/godbus/dbus/v5"
)

const (
	service = "org.freedesktop.Notifications"
	path    = "/org/freedesktop/Notifications"
	iface   = "org.freedesktop.Notifications"
)

// Notification is one notification.
type Notification struct {
	AppName string
	// Icon is an icon name from the theme or a file path.
	Icon  string
	Title string
	Body  string
	// DesktopEntry is the app's desktop file name without ".desktop", so the desktop shows it as the app's.
	DesktopEntry string
	// Action, if set, is the label of the notification's default action (a click on it).
	Action string
}

// Shown is a notification on screen.
type Shown struct {
	conn *dbus.Conn
	id   uint32
	sig  chan *dbus.Signal
}

// ErrUnavailable is returned when there is no session bus or no notification service.
var ErrUnavailable = errors.New("no desktop notification service")

// Send shows n. Close the result when done with it.
func Send(n Notification) (*Shown, error) {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return nil, errors.Join(ErrUnavailable, err)
	}
	s := &Shown{conn: conn, sig: make(chan *dbus.Signal, 8)}
	if n.Action != "" {
		// Listen before sending, so a quick click isn't missed.
		if err := conn.AddMatchSignal(dbus.WithMatchObjectPath(path), dbus.WithMatchInterface(iface)); err != nil {
			conn.Close()
			return nil, err
		}
		conn.Signal(s.sig)
	}
	var actions []string
	if n.Action != "" {
		actions = []string{"default", n.Action}
	}
	hints := map[string]dbus.Variant{}
	if n.DesktopEntry != "" {
		hints["desktop-entry"] = dbus.MakeVariant(n.DesktopEntry)
	}
	call := conn.Object(service, path).Call(iface+".Notify", 0, n.AppName, uint32(0), n.Icon, n.Title, n.Body,
		actions, hints, int32(-1))
	if call.Err != nil {
		conn.Close()
		return nil, errors.Join(ErrUnavailable, call.Err)
	}
	if err := call.Store(&s.id); err != nil {
		conn.Close()
		return nil, err
	}
	return s, nil
}

// WaitAction waits until the notification's action is chosen (true), or it is closed or ctx ends (false).
func (s *Shown) WaitAction(ctx context.Context) bool {
	for {
		select {
		case <-ctx.Done():
			return false
		case sig, ok := <-s.sig:
			if !ok {
				return false
			}
			if len(sig.Body) == 0 {
				continue
			}
			if id, _ := sig.Body[0].(uint32); id != s.id {
				continue
			}
			switch sig.Name {
			case iface + ".ActionInvoked":
				return true
			case iface + ".NotificationClosed":
				return false
			}
		}
	}
}

// Close lets go of the session bus connection.
func (s *Shown) Close() error { return s.conn.Close() }
