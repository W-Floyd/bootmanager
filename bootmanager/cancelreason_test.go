package main

import (
	"strings"
	"testing"
)

func TestCancelReason(t *testing.T) {
	if got := cancelReason("", 0); got != "rpc autoboot.cancel (no reason given)" {
		t.Error(got)
	}
	if got := cancelReason("event2 gt9916 EV_KEY BTN_TOUCH 1", 12); got != "ui: event2 gt9916 EV_KEY BTN_TOUCH 1, input 12 ms earlier" {
		t.Error(got)
	}
	if got := cancelReason("a\nb\x00c"+strings.Repeat("x", 300), 0); strings.ContainsAny(got, "\n\x00") || len(got) > 260 {
		t.Errorf("not sanitised: %q", got)
	}
}
