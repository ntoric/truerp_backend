package middleware

import (
	"testing"
	"time"
)

func TestFixedWindowLimiter(t *testing.T) {
	l := &fixedWindowLimiter{limit: 2, window: time.Minute, hits: map[string]*windowCounter{}}
	now := time.Now()
	if !l.allow("a", now) || !l.allow("a", now) {
		t.Fatal("first two requests should pass")
	}
	if l.allow("a", now) {
		t.Fatal("third request in window should be blocked")
	}
	if !l.allow("b", now) {
		t.Fatal("other keys have their own budget")
	}
	if !l.allow("a", now.Add(time.Minute)) {
		t.Fatal("budget resets after the window")
	}
}
