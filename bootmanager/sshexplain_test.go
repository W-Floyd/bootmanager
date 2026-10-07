package main

import (
	"strings"
	"testing"
)

func TestExplainSSH(t *testing.T) {
	for _, c := range []struct{ line, want string }{
		{"1970/01/01 00:13:59 open /etc/ssh/authorized_keys: no such file or directory", "no SSH keys"},
		{"2026/10/07 12:00:00 open /etc/ssh/ssh_host_rsa_key: no such file or directory", "no host key"},
		{"listen tcp :22: bind: address already in use", "port 22 is already in use"},
		{"open /etc/ssh/authorized_keys: permission denied", "could not be read"},
		{"1970/01/01 00:00:01 something else broke", "something else broke"},
	} {
		if got := explainSSH(c.line); !strings.Contains(got, c.want) || logStamp.MatchString(got) {
			t.Errorf("%q -> %q, want it to say %q", c.line, got, c.want)
		}
	}
}
