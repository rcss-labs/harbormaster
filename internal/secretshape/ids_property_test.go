package secretshape

import (
	"math/rand"
	"strings"
	"testing"

	"github.com/rcss-labs/harbormaster/internal/model"
)

// referenceRunIsRandom is the decision the long-run rule made before ids were known.
func referenceRunIsRandom(run string) bool { return looksLikeRandomRun(run) }

type pieceKind struct {
	name string
	// allowed pieces may become visible beside an id; the others must keep the run redacted.
	allowed bool
	make    func(*rand.Rand) string
}

func randomText(rng *rand.Rand, alphabet string, n int) string {
	out := make([]byte, n)
	for i := range out {
		out[i] = alphabet[rng.Intn(len(alphabet))]
	}
	return string(out)
}

const (
	alnum    = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	lowerHex = "0123456789abcdef"
)

func pieceKinds() []pieceKind {
	words := []string{"api", "services", "logs", "tasks", "task-definition", "revisions", "compare", "payments-worker"}
	return []pieceKind{
		{"id", true, func(rng *rand.Rand) string {
			name := func() string { return randomText(rng, "abcdefghij-_XYZ0189", 1+rng.Intn(40)) }
			key := model.ClusterKey{AccountID: "123456789012", Region: []string{"us-east-1", "eusc-de-east-1", "eu-west-1"}[rng.Intn(3)], Cluster: name()}
			switch rng.Intn(3) {
			case 0:
				return key.ID()
			case 1:
				return model.ServiceKey{ClusterKey: key, Service: name()}.ID()
			}
			return model.TaskID(key.AccountID, key.Region, "arn:aws:ecs:"+key.Region+":"+key.AccountID+":task/"+key.Cluster+"/"+randomText(rng, lowerHex, 32))
		}},
		{"empty", true, func(*rand.Rand) string { return "" }},
		{"short", true, func(rng *rand.Rand) string { return randomText(rng, alnum, rng.Intn(16)) }},
		{"uuid", true, func(rng *rand.Rand) string {
			h := randomText(rng, lowerHex, 32)
			return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
		}},
		{"task id", true, func(rng *rand.Rand) string { return randomText(rng, lowerHex, 32) }},
		{"words", true, func(rng *rand.Rand) string { return words[rng.Intn(len(words))] }},
		{"hex 40 to 64", false, func(rng *rand.Rand) string { return randomText(rng, lowerHex, 40+rng.Intn(25)) }},
		{"HEX 40", false, func(rng *rand.Rand) string { return strings.ToUpper(randomText(rng, lowerHex, 40)) }},
		{"patterned hex 40 to 63", false, func(rng *rand.Rand) string {
			return strings.Repeat("0123456789abcdef", 4)[:40+rng.Intn(24)]
		}},
		{"hex part", false, func(rng *rand.Rand) string { return "logs-" + strings.Repeat("0123456789ABCDEF", 3) }},
		{"hex 33", false, func(rng *rand.Rand) string { return randomText(rng, lowerHex, 33) }},
		{"random 16 to 31", false, func(rng *rand.Rand) string { return randomText(rng, alnum+"-_", 16+rng.Intn(16)) }},
		{"random 32 to 64", false, func(rng *rand.Rand) string { return randomText(rng, alnum+"-_", 32+rng.Intn(33)) }},
		{"long words", false, func(rng *rand.Rand) string { return strings.Repeat("payments-", 8+rng.Intn(4)) + "worker" }},
	}
}

// Over generated runs, the new rule hides everything the old one hid except runs made only of ids and plain pieces.
func TestRunIsRandom_OnlyIDsAndPlainPiecesBecomeVisible(t *testing.T) {
	kinds := pieceKinds()
	for seed := int64(1); seed <= 20; seed++ {
		rng := rand.New(rand.NewSource(seed))
		for n := 0; n < 1500; n++ {
			count := 1 + rng.Intn(5)
			var pieces, labels []string
			allAllowed := true
			for i := 0; i < count; i++ {
				kind := kinds[rng.Intn(len(kinds))]
				pieces = append(pieces, kind.make(rng))
				labels = append(labels, kind.name)
				allAllowed = allAllowed && kind.allowed
			}
			run := strings.Join(pieces, "/")
			if len(run) < 32 || strings.ContainsAny(run, " ") {
				continue
			}
			before, after := referenceRunIsRandom(run), runIsRandom(run)
			if after && !before {
				t.Fatalf("seed %d: the new rule hides what the old one let through: %q (%v)", seed, run, labels)
			}
			if before && !after && !allAllowed {
				t.Fatalf("seed %d: newly visible run holds a piece that is neither an id nor plain: %q (%v)", seed, run, labels)
			}
			hasID := false
			for i, label := range labels {
				hasID = hasID || label == "id"
				if label == "id" && !isHarbormasterID(pieces[i]) {
					t.Fatalf("seed %d: generated id %q was not recognised", seed, pieces[i])
				}
			}
			if before && !after && !hasID {
				t.Fatalf("seed %d: a run without an id became visible: %q (%v)", seed, run, labels)
			}
		}
	}
}
