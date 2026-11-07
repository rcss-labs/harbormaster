package cost

import "testing"

func TestContainerFloors(t *testing.T) {
	cases := []struct {
		name       string
		definition string
		want       Floors
	}{
		{
			name:       "no limits",
			definition: `[{"Name":"app"}]`,
			want:       Floors{CPUUnits: 0, MemoryMiB: 0},
		},
		{
			name:       "memory and reservation across containers",
			definition: `[{"Cpu":256,"Memory":512,"MemoryReservation":null},{"Cpu":0,"Memory":null,"MemoryReservation":768}]`,
			want:       Floors{CPUUnits: 256, MemoryMiB: 1280},
		},
		{
			name:       "hard limit above reservation",
			definition: `[{"Memory":512,"MemoryReservation":256}]`,
			want:       Floors{CPUUnits: 0, MemoryMiB: 512},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ContainerFloors([]byte(tc.definition))
			if err != nil {
				t.Fatalf("ContainerFloors error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("ContainerFloors = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestContainerFloors_MalformedJSONIsAnError(t *testing.T) {
	if _, err := ContainerFloors([]byte(`[{"Cpu":`)); err == nil {
		t.Fatal("ContainerFloors on malformed JSON returned no error")
	}
}
