// Package authz implements Harbormaster's tag-based permission model.
// It depends only on the standard library and internal/model.
package authz

// Action is a single permission bit.
type Action uint8

const (
	View     Action = 1 << iota // V=1
	Logs                        // L=2
	Operate                     // O=4
	Deploy                      // D=8
	Edit                        // E=16
	Terminal                    // T=32
	// AcceptRisk is in no preset, Full included: accepting a vulnerability is a separate responsibility.
	AcceptRisk // R=64
)

// ActionSet is a bitmask of Actions granted on a tag.
type ActionSet uint8

// Presets are the named permission bundles offered in the UI.
var Presets = map[string]ActionSet{
	"None":     0,
	"Viewer":   ActionSet(View),
	"Operator": ActionSet(View | Logs | Operate),
	"Deployer": ActionSet(View | Logs | Operate | Deploy | Edit),
	"Full":     ActionSet(View | Logs | Operate | Deploy | Edit | Terminal),
}

// Has reports whether the set contains action a.
func (s ActionSet) Has(a Action) bool { return s&ActionSet(a) != 0 }

// Normalize enforces the invariant that any action implies View.
func Normalize(s ActionSet) ActionSet {
	if s == 0 {
		return 0
	}
	return s | ActionSet(View)
}

// PresetName returns the preset whose set exactly equals the normalized s,
// or "Custom" when none match.
func PresetName(s ActionSet) string {
	n := Normalize(s)
	for name, set := range Presets {
		if set == n {
			return name
		}
	}
	return "Custom"
}

// ActionNames lists every action in display order with its label and letter.
func ActionNames() []struct {
	Action Action
	Name   string
	Letter string
} {
	return []struct {
		Action Action
		Name   string
		Letter string
	}{
		{View, "View", "V"},
		{Logs, "Logs", "L"},
		{Operate, "Operate", "O"},
		{Deploy, "Deploy", "D"},
		{Edit, "Edit task definition", "E"},
		{Terminal, "Terminal", "T"},
		{AcceptRisk, "Accept risk", "R"},
	}
}
