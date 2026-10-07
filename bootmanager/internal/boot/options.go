package boot

import (
	"fmt"

	"bootmanager.local/bootmanager/internal/distro"
)

// OptionState is one command-line option and where the user has it: "" (the
// entries' own words stand), "on" or "off".
type OptionState struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Detail string `json:"detail"`
	State  string `json:"state"`
}

// Options lists the options with their states.
func (m *Manager) Options() []OptionState {
	m.loadSettings()
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []OptionState
	for _, o := range distro.Options() {
		out = append(out, OptionState{o.ID, o.Title, o.Detail, m.settings.Options[o.ID]})
	}
	return out
}

// SetOption puts an option on, off, or ("") back to leaving the entries
// alone, and keeps it for every boot after.
func (m *Manager) SetOption(id, state string) error {
	if state != "" && state != "on" && state != "off" {
		return fmt.Errorf("an option is on, off, or unset, not %q", state)
	}
	known := false
	for _, o := range distro.Options() {
		known = known || o.ID == id
	}
	if !known {
		return fmt.Errorf("no boot option %q", id)
	}
	return m.change(func() error {
		if state == "" {
			delete(m.settings.Options, id)
			return nil
		}
		if m.settings.Options == nil {
			m.settings.Options = map[string]string{}
		}
		m.settings.Options[id] = state
		return nil
	})
}

// optionsCmdline is cmdline with the user's options applied.
func (m *Manager) optionsCmdline(cmdline string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return distro.ApplyOptions(cmdline, m.settings.Options)
}
