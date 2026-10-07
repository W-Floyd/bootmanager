package boot

import (
	"errors"
	"fmt"
	"strings"

	"github.com/u-root/u-root/pkg/boot"

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

// Cmdline is the command line entry i would boot with: its own, with the
// options applied. It is what an edit for one boot starts from.
func (m *Manager) Cmdline(i int) (string, error) {
	img, err := m.Image(i)
	if err != nil {
		return "", err
	}
	li, ok := img.(*boot.LinuxImage)
	if !ok {
		return "", fmt.Errorf("%s has no kernel command line to edit", img.Label())
	}
	return m.optionsCmdline(li.Cmdline), nil
}

// StartWith boots entry i with cmdline as its whole command line, for this
// boot only: nothing is saved, and the entry keeps its own. Words the
// unlock key needs are still added when the line has none of its own.
func (m *Manager) StartWith(i int, cmdline string) error {
	cmdline = strings.Join(strings.Fields(cmdline), " ")
	if cmdline == "" {
		return errors.New("an empty command line")
	}
	m.mu.Lock()
	m.oneBoot = &cmdline
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		m.oneBoot = nil
		m.mu.Unlock()
	}()
	return m.Start(i)
}

// optionsCmdline is cmdline with the user's options applied.
func (m *Manager) optionsCmdline(cmdline string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return distro.ApplyOptions(cmdline, m.settings.Options)
}
