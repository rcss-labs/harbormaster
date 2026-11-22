package snapcache

import (
	"strings"
	"testing"
)

func TestDecode_RefusesAnOverCapPayload(t *testing.T) {
	body, err := Encode(map[string]string{"x": strings.Repeat("a", maxDecoded+1)})
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]string
	err = Decode(body, &out)
	if err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Fatalf("Decode of an over-cap payload = %v, want the size error", err)
	}
}

func TestEncodeDecode_RoundTripsAcrossPooledWriters(t *testing.T) {
	for i := 0; i < 3; i++ {
		body, err := Encode(map[string]int{"n": i})
		if err != nil {
			t.Fatal(err)
		}
		var out map[string]int
		if err := Decode(body, &out); err != nil || out["n"] != i {
			t.Fatalf("round trip %d = %v err %v", i, out, err)
		}
	}
}
