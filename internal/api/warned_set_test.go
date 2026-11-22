package api

import (
	"strconv"
	"testing"
)

func TestWarnedSet_ReportsOnceAndStaysBounded(t *testing.T) {
	var w warnedSet
	if !w.add("a") || w.add("a") {
		t.Fatal("a key must be new once, then seen")
	}
	for i := 0; i < 3*maxWarnedKeys; i++ {
		w.add(strconv.Itoa(i))
	}
	if len(w.keys) > maxWarnedKeys {
		t.Fatalf("%d keys, want at most %d", len(w.keys), maxWarnedKeys)
	}
}
