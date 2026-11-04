// Package telemetry is the hook point for optional, opt-in usage reporting.
//
// This version collects nothing: the only Sink is Nop. Code that wants to report something calls Emit, so a
// real sink can be added later without touching callers. Any real sink must only run when the user has turned
// telemetry on in settings, which is off by default.
//
// Events must never carry search text, file paths, usernames, hostnames or any machine identifier.
package telemetry

// Event is one anonymous occurrence, such as "install succeeded".
type Event struct {
	Name   string            // for example "install"
	Fields map[string]string // for example {"source": "flathub", "result": "ok"}
}

// Sink receives events.
type Sink interface {
	Emit(Event)
}

// Nop discards every event.
type Nop struct{}

// Emit implements Sink.
func (Nop) Emit(Event) {}

// ForSettings returns the sink to use. enabled is the user's telemetry setting. No real sink exists yet, so
// the result is always Nop.
func ForSettings(enabled bool) Sink {
	_ = enabled
	return Nop{}
}
