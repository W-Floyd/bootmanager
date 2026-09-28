// Package slots reads and sets A/B slot state as a Qualcomm bootloader
// (ABL) keeps it: in the GPT attributes of the boot_a and boot_b entries,
// which it reads to pick the slot to boot, plus an "active" bit on every
// slotted partition. Android's boot HAL writes the same bits.
//
// Only the GPTs of the disks holding boot_a and boot_b are written. ABL's
// set_active also marks xbl_a/xbl_b, on UFS each on a LUN of its own,
// but the boot ROM picks the xbl to run by a UFS attribute, not by those
// bits, and those LUNs' GPTs are the ones a bad write would make the
// device unbootable by.
package slots

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
)

// The attribute bits, as ABL reads them.
const (
	bitPriority   = 48 // 2 bits: the highest bootable slot is booted
	bitActive     = 50
	bitRetries    = 51 // 3 bits: tries left before unbootable
	bitSuccessful = 54 // booted once; ABL stops counting down
	bitUnbootable = 55 // read only while not successful
	maxRetries    = 7
)

type Slot struct {
	Name       string `json:"name"` // "a"
	Booted     bool   `json:"booted"`
	Active     bool   `json:"active"`
	Priority   int    `json:"priority"`
	Retries    int    `json:"retries"`
	Successful bool   `json:"successful"`
	Unbootable bool   `json:"unbootable"`
}

func slotOf(name string, attr uint64) Slot {
	return Slot{
		Name:       name,
		Active:     attr>>bitActive&1 == 1,
		Priority:   int(attr >> bitPriority & 3),
		Retries:    int(attr >> bitRetries & 7),
		Successful: attr>>bitSuccessful&1 == 1,
		Unbootable: attr>>bitUnbootable&1 == 1,
	}
}

var slotNames = []string{"a", "b"}

// Store is the device's slots.
type Store struct {
	// Open opens every whole disk, for writing when write is set.
	Open func(write bool) ([]Disk, error)
	// Booted is the slot the bootloader booted ("a"), "" when unknown.
	Booted string

	mu sync.Mutex
}

var ErrNoSlots = errors.New("no A/B slots (boot_a and boot_b)")

// part is an entry and the table it is in.
type part struct {
	t *table
	entry
}

func (p *part) set(attr uint64) {
	p.attr = attr
	p.t.setAttr(p.idx, attr)
}

// load reads every disk's GPT and returns the partitions by name, the
// slotted ones on the boot disks only.
func (s *Store) load(write bool) (map[string]*part, []*table, func(), error) {
	disks, err := s.Open(write)
	if err != nil {
		return nil, nil, nil, err
	}
	closeAll := func() {
		for _, d := range disks {
			d.Close()
		}
	}
	var tables []*table
	var errs []error
	for _, d := range disks {
		t, err := readTable(d)
		if errors.Is(err, errNoGPT) {
			continue
		}
		if err != nil {
			errs = append(errs, err)
			continue
		}
		tables = append(tables, t)
	}
	parts := map[string]*part{}
	var boot []*table
	for _, t := range tables {
		for _, e := range t.list() {
			if e.name == "boot_a" || e.name == "boot_b" {
				if parts[e.name] != nil {
					closeAll()
					return nil, nil, nil, fmt.Errorf("%s is on %s and %s", e.name, parts[e.name].t.disk.Name(), t.disk.Name())
				}
				boot = append(boot, t)
			}
			parts[e.name] = &part{t, e}
		}
	}
	if parts["boot_a"] == nil || parts["boot_b"] == nil {
		closeAll()
		// A disk that could not be read may have held them.
		return nil, nil, nil, errors.Join(append([]error{ErrNoSlots}, errs...)...)
	}
	for n, p := range parts {
		if !slices.Contains(boot, p.t) {
			delete(parts, n)
		}
	}
	return parts, boot, closeAll, nil
}

// List is the slots' state.
func (s *Store) List() ([]Slot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	parts, _, done, err := s.load(false)
	if err != nil {
		return nil, err
	}
	defer done()
	var out []Slot
	for _, n := range slotNames {
		sl := slotOf(n, parts["boot_"+n].attr)
		sl.Booted = n == s.Booted
		out = append(out, sl)
	}
	return out, nil
}

func other(slot string) (string, error) {
	switch slot {
	case "a":
		return "b", nil
	case "b":
		return "a", nil
	}
	return "", fmt.Errorf("no slot %q", slot)
}

// update applies f to the boot disks' partitions and writes the tables it
// changed, the one holding boot_<first> first.
func (s *Store) update(first string, f func(map[string]*part)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	parts, boot, done, err := s.load(true)
	if err != nil {
		return err
	}
	defer done()
	f(parts)
	lead := parts["boot_"+first].t
	slices.SortStableFunc(boot, func(x, y *table) int {
		switch {
		case x == y:
			return 0
		case x == lead:
			return -1
		case y == lead:
			return 1
		}
		return 0
	})
	for _, t := range slices.Compact(boot) {
		if t.changed() {
			if err := t.write(); err != nil {
				return err
			}
		}
	}
	return nil
}

func setBits(attr uint64, bit, width int, v uint64) uint64 {
	mask := uint64(1)<<width - 1
	return attr&^(mask<<bit) | (v&mask)<<bit
}

// SetActive makes slot the one booted next, as ABL's set_active does:
// its boot partition at the top priority with every try and neither
// successful nor unbootable, the other's a priority below and not
// active, and every slotted partition's active bit to match.
func (s *Store) SetActive(slot string) error {
	alt, err := other(slot)
	if err != nil {
		return err
	}
	// Written first: its disk makes slot bootable, so a failure before
	// the other's leaves both bootable, never neither.
	return s.update(slot, func(parts map[string]*part) {
		for name, p := range parts {
			base, ok := strings.CutSuffix(name, "_"+slot)
			if !ok || parts[base+"_"+alt] == nil {
				continue
			}
			p.set(p.attr | 1<<bitActive)
			q := parts[base+"_"+alt]
			q.set(q.attr &^ (1 << bitActive))
		}
		b := parts["boot_"+slot]
		b.set(setBits(b.attr, bitPriority, 8, 3|1<<2|maxRetries<<3))
		o := parts["boot_"+alt]
		o.set(setBits(o.attr, bitPriority, 3, 2))
	})
}

// MarkSuccessful marks slot as having booted: ABL stops counting its
// tries down.
func (s *Store) MarkSuccessful(slot string) error {
	if _, err := other(slot); err != nil {
		return err
	}
	return s.update(slot, func(parts map[string]*part) {
		b := parts["boot_"+slot]
		b.set(b.attr&^(1<<bitUnbootable) | 1<<bitSuccessful)
	})
}

// MarkUnbootable marks slot as failed, as ABL does when its tries run
// out: unbootable, not successful (ABL reads unbootable only then), not
// active and at no priority. ABL then boots the other slot, if it can.
func (s *Store) MarkUnbootable(slot string) error {
	if _, err := other(slot); err != nil {
		return err
	}
	return s.update(slot, func(parts map[string]*part) {
		b := parts["boot_"+slot]
		b.set(setBits(b.attr, bitPriority, 3, 0)&^(1<<bitSuccessful) | 1<<bitUnbootable)
	})
}
