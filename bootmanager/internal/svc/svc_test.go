package svc

import (
	"os"
	"strings"
	"testing"
	"time"
)

// A child that dies on its own is "off" with its last line as the reason,
// and the change is announced.
func TestAChildThatDiesIsReported(t *testing.T) {
	os.Remove("/tmp/sh.log")
	changed := make(chan struct{}, 1)
	s := NewSet(&Service{
		Name: "x", Title: "X", Log: "/tmp/sh.log",
		Start: Process("sh", "-c", "echo 'sshd: no host key' >&2; exit 1"),
	})
	s.Changed = func() { changed <- struct{}{} }
	if err := s.Set("x", true); err != nil {
		t.Fatal(err)
	}
	select {
	case <-changed:
	case <-time.After(5 * time.Second):
		t.Fatal("no change announced")
	}
	st := s.List()[0]
	if st.Running || !strings.Contains(st.Error, "no host key") {
		t.Fatalf("status %+v", st)
	}
	// Switching it on again clears the error.
	s.list[0].Start = Process("sleep", "30")
	if err := s.Set("x", true); err != nil {
		t.Fatal(err)
	}
	if st := s.List()[0]; !st.Running || st.Error != "" {
		t.Fatalf("after a restart: %+v", st)
	}
	s.Set("x", false)
}
