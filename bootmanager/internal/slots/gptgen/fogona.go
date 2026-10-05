package gptgen

// Seed is the fixed seed of the GUIDs of Fogona's disks.
const Seed = "go-qbootctl test fixtures"

// The layout imitates a fogona (Moto G Play 2024) as the slot code sees it:
// UFS LUNs of 4096-byte blocks, boot_a and the other *_a partitions on one
// LUN (sdd), boot_b and the *_b partitions on another (sdf), xbl_a and xbl_b
// each on a LUN of their own (sdb, sdc: the GPTs that must never be
// written), and LUNs with no slot partitions at all (sda, sde).
// The partition names are the real ones; every number is made up.

// slotted are the A/B partitions, in the order they sit on their LUN.
// firmware ones carry the flag bits 0x68 below bit 48, the others 0x48:
// bits the slot code must leave alone.
var slotted = []struct {
	name     string
	firmware bool
	noTop    bool // bit 60 clear
}{
	{"tz", true, false}, {"rpm", true, false}, {"hyp", true, false},
	{"cmnlib", true, false}, {"cmnlib64", true, false}, {"keymaster", true, false},
	{"prov", true, false}, {"abl", true, false}, {"uefisecapp", true, false},
	{"devcfg", true, false}, {"qupfw", true, false}, {"storsec", true, false},
	{"modem", false, false}, {"bluetooth", false, false}, {"dsp", false, false},
	{"boot", false, false}, {"init_boot", false, false}, {"dtbo", false, false},
	{"recovery", false, false}, {"vendor_boot", false, true}, {"logo", false, false},
	{"vbmeta", false, false}, {"vbmeta_system", false, false}, {"fsg", false, true},
}

// SlottedCount is the number of A/B partitions on each of sdd and sdf.
func SlottedCount() int { return len(slotted) }

func slotParts(suffix string, ab func(name string) AB) []Part {
	var out []Part
	for _, s := range slotted {
		base := uint64(0x48)
		if s.firmware {
			base = 0x68
		}
		if !s.noTop {
			base |= 1 << 60
		}
		out = append(out, Part{s.name + suffix, Attr(base, ab(s.name))})
	}
	return out
}

// Fogona is the disks of a phone that booted slot _a and has marked it
// successful: boot_a is active at priority 3 with 3 tries left, every other
// *_a partition active and successful, and slot b untouched (all bits clear
// but the opaque flags). blockSize is the logical block size of every disk:
// 4096 as on UFS, or 512 to exercise the other. Each disk is 1 GiB.
func Fogona(blockSize int64) []Disk {
	blocks := int64(1<<30) / blockSize
	a := slotParts("_a", func(n string) AB {
		if n == "boot" {
			return AB{Priority: 3, Active: true, Retries: 3, Successful: true}
		}
		return AB{Active: true, Successful: true}
	})
	b := slotParts("_b", func(string) AB { return AB{} })
	mk := func(name string, parts ...Part) Disk {
		return Disk{Name: name, BlockSize: blockSize, Blocks: blocks, Seed: Seed, Parts: parts}
	}
	return []Disk{
		mk("sda", Part{"ssd", 0}, Part{"persist", 0x48}, Part{"misc", 0x48}),
		mk("sdb", Part{"xbl_a", 0x48}, Part{"xbl_config_a", 0x48}),
		mk("sdc", Part{"xbl_b", 0x48}, Part{"xbl_config_b", 0x48}),
		mk("sdd", a...),
		mk("sde", Part{"super", 0x48}, Part{"userdata", 0x48}),
		mk("sdf", b...),
	}
}

// FogonaImages builds Fogona's disks.
func FogonaImages(blockSize int64) ([]Image, error) {
	var out []Image
	for _, d := range Fogona(blockSize) {
		im, err := d.Build()
		if err != nil {
			return nil, err
		}
		out = append(out, im)
	}
	return out, nil
}
