package secretshape

import (
	"math/rand"
	"strings"
	"testing"

	"github.com/rcss-labs/harbormaster/internal/model"
)

// referenceLongRunSpans is longRunSpans as it was before ids were known.
func referenceLongRunSpans(text string) [][2]int {
	arns := arnSpan.FindAllStringIndex(text, -1)
	var spans [][2]int
	for _, m := range longRun.FindAllStringIndex(text, -1) {
		run := text[m[0]:m[1]]
		if !looksLikeRandomRun(run) || insideAny(arns, m) {
			continue
		}
		if hexOnly.MatchString(run) && hexRunIsDiagnostic(text, m[0]) {
			continue
		}
		spans = append(spans, [2]int{m[0], m[1]})
	}
	return spans
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

func generatedID(rng *rand.Rand) string {
	name := func() string { return randomText(rng, "abcdefghij-_XYZ0189", 1+rng.Intn(60)) }
	key := model.ClusterKey{AccountID: "123456789012", Region: []string{"us-east-1", "eusc-de-east-1", "eu-west-1"}[rng.Intn(3)], Cluster: name()}
	switch rng.Intn(3) {
	case 0:
		return key.ID()
	case 1:
		return model.ServiceKey{ClusterKey: key, Service: name()}.ID()
	}
	return model.TaskID(key.AccountID, key.Region, "arn:aws:ecs:"+key.Region+":"+key.AccountID+":task/"+key.Cluster+"/"+randomText(rng, lowerHex, 32))
}

// For generated texts, the characters the new rule shows that the old one hid are exactly whole runs that are ids, and
// it never hides what the old one showed.
func TestLongRunSpans_OnlyWholeIDRunsBecomeVisible(t *testing.T) {
	glue := []string{" ", "\n", `"`, "/", "-", "_", "/services/", "=", ":", "/logs", ""}
	for seed := int64(1); seed <= 20; seed++ {
		rng := rand.New(rand.NewSource(seed))
		for n := 0; n < 1000; n++ {
			var text strings.Builder
			for i, count := 0, 1+rng.Intn(6); i < count; i++ {
				switch rng.Intn(7) {
				case 0, 1, 2:
					text.WriteString(generatedID(rng))
				case 3:
					text.WriteString(randomText(rng, alnum+"-_", 20+rng.Intn(50)))
				case 4:
					text.WriteString(randomText(rng, lowerHex, 32+rng.Intn(40)))
				case 5:
					text.WriteString(strings.Repeat("0123456789abcdef", 3))
				default:
					text.WriteString(randomText(rng, "abcdefgh-", 5+rng.Intn(30)))
				}
				text.WriteString(glue[rng.Intn(len(glue))])
			}
			old, now := referenceLongRunSpans(text.String()), longRunSpans(text.String(), Names{})
			in := func(spans [][2]int, span [2]int) bool {
				for _, s := range spans {
					if s == span {
						return true
					}
				}
				return false
			}
			for _, span := range now {
				if !in(old, span) {
					t.Fatalf("seed %d: the new rule hides %q, which the old one let through, in %q", seed, text.String()[span[0]:span[1]], text.String())
				}
			}
			for _, span := range old {
				if !in(now, span) && !isHarbormasterID(text.String()[span[0]:span[1]]) {
					t.Fatalf("seed %d: %q became visible and is not an id, in %q", seed, text.String()[span[0]:span[1]], text.String())
				}
			}
			for _, span := range now {
				if isHarbormasterID(text.String()[span[0]:span[1]]) {
					t.Fatalf("seed %d: an id run was hidden: %q", seed, text.String()[span[0]:span[1]])
				}
			}
		}
	}
}
