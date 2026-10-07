package luks

import "sync"

// KeyFile is where the booted system's initramfs finds the passphrase the
// Boot Manager was given for its root: the extra archive appended to the
// initramfs holds it under this name, readable by root only and exactly the
// passphrase's bytes. The pmOS unlocker (fde-unlock) tries it first and
// deletes it.
const KeyFile = "bman-unlock.key"

// Keyring remembers the passphrases the volumes were unlocked with, by LUKS
// UUID, so a boot of the system on one of them need not ask again. Nothing
// is written anywhere: they live here, and in the archive handed to the
// booted kernel.
type Keyring struct {
	mu   sync.Mutex
	keys map[string][]byte // uuid -> passphrase
	dev  map[string]string // device -> uuid
}

// Put keeps the passphrase that opened dev, a volume with the LUKS uuid.
func (k *Keyring) Put(dev, uuid string, pass []byte) {
	if uuid == "" {
		return
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.keys == nil {
		k.keys, k.dev = map[string][]byte{}, map[string]string{}
	}
	k.keys[uuid] = append([]byte(nil), pass...)
	k.dev[dev] = uuid
}

// Forget drops what dev's passphrase was, as the volume is closed.
func (k *Keyring) Forget(dev string) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if uuid, ok := k.dev[dev]; ok {
		clear(k.keys[uuid])
		delete(k.keys, uuid)
		delete(k.dev, dev)
	}
}

// Take returns the passphrase for the volume with this LUKS uuid, and
// forgets it: it goes to one boot.
func (k *Keyring) Take(uuid string) []byte {
	k.mu.Lock()
	defer k.mu.Unlock()
	pass, ok := k.keys[uuid]
	if !ok {
		return nil
	}
	delete(k.keys, uuid)
	for dev, u := range k.dev {
		if u == uuid {
			delete(k.dev, dev)
		}
	}
	return pass
}
