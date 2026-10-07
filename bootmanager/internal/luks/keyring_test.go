package luks

import "testing"

func TestKeyring(t *testing.T) {
	var k Keyring
	k.Put("sde27", "abc", []byte("secret"))
	k.Put("sde28", "", []byte("no uuid, not kept"))
	if got := k.Take("nope"); got != nil {
		t.Errorf("Take(unknown) = %q", got)
	}
	if got := k.Take("abc"); string(got) != "secret" {
		t.Errorf("Take(abc) = %q", got)
	}
	if got := k.Take("abc"); got != nil {
		t.Errorf("a second Take(abc) = %q: it goes to one boot", got)
	}
	k.Put("sde27", "abc", []byte("again"))
	k.Forget("sde27")
	if got := k.Take("abc"); got != nil {
		t.Errorf("Take after Forget = %q", got)
	}
}
