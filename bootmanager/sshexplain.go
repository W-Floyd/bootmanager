package main

import (
	"regexp"
	"strings"
)

// logStamp is the date and time Go's log package puts in front of a line.
var logStamp = regexp.MustCompile(`^\d{4}/\d\d/\d\d \d\d:\d\d:\d\d(\.\d+)? `)

// explainSSH says in words why sshd stopped, from its last line: the cases
// this stage meets, and any other as the line itself without its time.
func explainSSH(line string) string {
	line = strings.TrimSpace(logStamp.ReplaceAllString(line, ""))
	switch {
	case strings.Contains(line, "authorized_keys") && strings.Contains(line, "no such file"):
		return "no SSH keys on this stage. They are read from the boot partition at start, and none was readable (an encrypted root is not open yet)"
	case strings.Contains(line, "ssh_host") && strings.Contains(line, "no such file"):
		return "no host key on this stage: it is read from the boot partition at start, and none was readable"
	case strings.Contains(line, "address already in use"):
		return "port 22 is already in use"
	case strings.Contains(line, "permission denied"):
		return "the keys could not be read (permission denied)"
	}
	return line
}
