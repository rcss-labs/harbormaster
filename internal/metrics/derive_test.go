package metrics

import (
	"fmt"
	"testing"
)

func f(v float64) *float64 { return &v }

func vals(s Series) []float64 {
	out := make([]float64, len(s.Values))
	for i, v := range s.Values {
		if v == nil {
			out[i] = -1
		} else {
			out[i] = *v
		}
	}
	return out
}

func TestPercent_NilAndZeroReserved(t *testing.T) {
	u := Series{ID: "u", Values: []*float64{f(256), f(256), nil, f(600)}}
	r := Series{ID: "r", Values: []*float64{f(512), f(0), f(512), f(512)}}
	got := vals(Percent(u, r))
	want := []float64{50, -1, -1, 100}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v want %v", got, want)
		}
	}
	if neg := Percent(Series{Values: []*float64{f(-5)}}, Series{Values: []*float64{f(10)}}); *neg.Values[0] != 0 {
		t.Errorf("negative not clamped")
	}
}

func TestPerMinute_Period1200(t *testing.T) {
	got := PerMinute(Series{Values: []*float64{f(13600), nil}}, 1200)
	if *got.Values[0] != 680 || got.Values[1] != nil {
		t.Fatalf("got %v", vals(got))
	}
}

func TestSumMeanToKBpsLastSumAll(t *testing.T) {
	a := Series{Values: []*float64{f(1), nil, nil}}
	b := Series{Values: []*float64{f(3), f(4), nil}}
	s := SumSeries("s", a, b)
	if got := vals(s); got[0] != 4 || got[1] != 4 || got[2] != -1 || s.Label != "s" {
		t.Errorf("sum %v", got)
	}
	m := MeanSeries("m", a, b)
	if got := vals(m); got[0] != 2 || got[1] != 4 || got[2] != -1 {
		t.Errorf("mean %v", got)
	}
	if k := ToKBps(Series{Values: []*float64{f(2048)}}); *k.Values[0] != 2 {
		t.Errorf("kbps")
	}
	if l := Last(b); l == nil || *l != 4 {
		t.Errorf("last")
	}
	if Last(Series{Values: []*float64{nil}}) != nil {
		t.Errorf("last nil")
	}
	if SumAll(b) != 7 {
		t.Errorf("sumall")
	}
}

func TestTopWithOthers_SevenPlusOthers(t *testing.T) {
	var in []Series
	for i := 1; i <= 12; i++ {
		in = append(in, Series{ID: fmt.Sprint("s", i), Label: fmt.Sprint("s", i), Values: []*float64{f(0), f(float64(i))}})
	}
	out := TopWithOthers(in, 7, "others", MeanSeries)
	if len(out) != 8 {
		t.Fatalf("len %d", len(out))
	}
	for i := 0; i < 7; i++ {
		if want := float64(12 - i); *Last(out[i]) != want {
			t.Errorf("out[%d] last = %v want %v", i, *Last(out[i]), want)
		}
	}
	o := out[7]
	if o.Label != "others" || *o.Values[1] != 3 { // mean of 1..5
		t.Errorf("others = %+v %v", o.Label, vals(o))
	}
	if len(in) != 12 || *Last(in[0]) != 1 {
		t.Errorf("input mutated")
	}
	if same := TopWithOthers(in[:8], 7, "others", MeanSeries); len(same) != 8 || same[0].ID != "s1" {
		t.Errorf("8 inputs should be unchanged")
	}
}
