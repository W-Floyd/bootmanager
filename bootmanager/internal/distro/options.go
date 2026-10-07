package distro

import "strings"

// An Option is a change to a Linux entry's kernel command line that the user
// can switch for every boot: on, off, or neither (the entry's own words
// stand). What each position does is words to take off and words to put on.
type Option struct {
	ID     string
	Title  string
	Detail string
	On     Edit
	Off    Edit
	// Applies says whether the option means anything on this command line,
	// so a distribution's own words are not put on another's entries; nil:
	// it applies to every one.
	Applies func(cmdline string) bool
}

// Edit removes words and adds words. A removed word is a key: it takes off
// "key" and every "key=value".
type Edit struct {
	Remove []string
	Add    []string
}

// Apply is cmdline with the edit made: the words removed first, then the
// added ones appended (none that is already there).
func (e Edit) Apply(cmdline string) string {
	var out []string
	for _, f := range strings.Fields(cmdline) {
		drop := false
		for _, k := range e.Remove {
			if f == k || strings.HasPrefix(f, k+"=") {
				drop = true
				break
			}
		}
		if !drop {
			out = append(out, f)
		}
	}
	for _, w := range e.Add {
		have := false
		for _, f := range out {
			if f == w {
				have = true
				break
			}
		}
		if !have {
			out = append(out, w)
		}
	}
	return strings.Join(out, " ")
}

// Options are every option the stage offers: those that suit any system,
// then each distribution's.
func Options() []Option {
	opts := genericOptions()
	for _, d := range All {
		opts = append(opts, d.Options()...)
	}
	return opts
}

// ApplyOptions is cmdline with the options in states ("on" or "off" by ID;
// an option missing from it is left alone) applied, those that apply to it.
func ApplyOptions(cmdline string, states map[string]string) string {
	for _, o := range Options() {
		if o.Applies != nil && !o.Applies(cmdline) {
			continue
		}
		switch states[o.ID] {
		case "on":
			cmdline = o.On.Apply(cmdline)
		case "off":
			cmdline = o.Off.Apply(cmdline)
		}
	}
	return cmdline
}

func genericOptions() []Option {
	return []Option{{
		ID:     "quiet",
		Title:  "Quiet boot",
		Detail: "On hides the kernel's messages; off shows them all",
		On: Edit{
			Remove: []string{"quiet", "loglevel", "ignore_loglevel", "debug"},
			Add:    []string{"quiet", "loglevel=3"},
		},
		Off: Edit{
			Remove: []string{"quiet", "loglevel", "ignore_loglevel"},
			Add:    []string{"loglevel=7", "ignore_loglevel"},
		},
	}}
}
