package boot

import (
	"errors"
	"fmt"
	"maps"
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
	li, ok := asLinux(img)
	if !ok {
		return "", fmt.Errorf("%s has no kernel command line to edit", img.Label())
	}
	return m.optionsCmdline(img, li.Cmdline), nil
}

// EntryOption is an option as one entry has it: whether the line it will
// boot with is as the option's On has it, and whether the entry has a
// choice of its own for it (else the global one, or the entry's own words).
type EntryOption struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	On       bool   `json:"on"`
	Override bool   `json:"override"`
}

// EntryOptions are the options that apply to entry i, as it will boot.
func (m *Manager) EntryOptions(i int) ([]EntryOption, error) {
	img, err := m.Image(i)
	if err != nil {
		return nil, err
	}
	li, ok := asLinux(img)
	if !ok {
		return nil, nil
	}
	cmdline := m.optionsCmdline(img, li.Cmdline)
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []EntryOption
	for _, o := range distro.Options() {
		if o.Applies != nil && !o.Applies(li.Cmdline) {
			continue
		}
		_, own := m.settings.EntryOptions[m.keyOfLocked(img)][o.ID]
		out = append(out, EntryOption{o.ID, o.Title, o.IsOn(cmdline), own})
	}
	return out, nil
}

// SetEntryOption puts an option on or off for one entry, or ("") back to
// what every entry has, and keeps it.
func (m *Manager) SetEntryOption(i int, id, state string) error {
	if state != "" && state != "on" && state != "off" {
		return fmt.Errorf("an option is on, off, or unset, not %q", state)
	}
	img, err := m.Image(i)
	if err != nil {
		return err
	}
	known := false
	for _, o := range distro.Options() {
		known = known || o.ID == id
	}
	if !known {
		return fmt.Errorf("no boot option %q", id)
	}
	return m.change(func() error {
		key := m.keyOfLocked(img)
		if key == "" {
			return errors.New("that entry is not in the list any more")
		}
		if state == "" {
			delete(m.settings.EntryOptions[key], id)
			if len(m.settings.EntryOptions[key]) == 0 {
				delete(m.settings.EntryOptions, key)
			}
			return nil
		}
		if m.settings.EntryOptions == nil {
			m.settings.EntryOptions = map[string]map[string]string{}
		}
		if m.settings.EntryOptions[key] == nil {
			m.settings.EntryOptions[key] = map[string]string{}
		}
		m.settings.EntryOptions[key][id] = state
		return nil
	})
}

// keyOfLocked is the key of img (keysOf), "" if it is not an entry. m.mu is
// held.
func (m *Manager) keyOfLocked(img boot.OSImage) string {
	for i, e := range m.images {
		if e == img && i < len(m.keys) {
			return m.keys[i]
		}
	}
	return ""
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

// optionsCmdline is cmdline with the user's options applied: every entry's,
// then the choices made for img alone over them.
func (m *Manager) optionsCmdline(img boot.OSImage, cmdline string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	states := maps.Clone(m.settings.Options)
	if own := m.settings.EntryOptions[m.keyOfLocked(img)]; len(own) > 0 {
		if states == nil {
			states = map[string]string{}
		}
		maps.Copy(states, own)
	}
	return distro.ApplyOptions(cmdline, states)
}
