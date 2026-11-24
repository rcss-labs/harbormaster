package provider

import (
	"strings"
	"testing"
)

func TestParseCatalogue_Valid(t *testing.T) {
	raw := `[{"provider":"anthropic","models":[
		{"id":"model-a","input_per_mtok_usd":"3.00","output_per_mtok_usd":"15","context_tokens":200000},
		{"id":"model-b","input_per_mtok_usd":"0.075","output_per_mtok_usd":"0.000001","context_tokens":1000}]},
		{"provider":"openai","models":[{"id":"model-a","input_per_mtok_usd":"1.25","output_per_mtok_usd":"10.5","context_tokens":128000}]}]`
	c, err := ParseCatalogue(raw)
	if err != nil {
		t.Fatalf("ParseCatalogue: %v", err)
	}
	m, ok := c.Find("anthropic", "model-a")
	if !ok || m.InputMicroUSDPerMTok != 3_000_000 || m.OutputMicroUSDPerMTok != 15_000_000 || m.ContextTokens != 200000 {
		t.Fatalf("model-a = %+v, ok=%v", m, ok)
	}
	m, _ = c.Find("anthropic", "model-b")
	if m.InputMicroUSDPerMTok != 75_000 || m.OutputMicroUSDPerMTok != 1 {
		t.Fatalf("model-b = %+v", m)
	}
	if m, ok := c.Find("openai", "model-a"); !ok || m.OutputMicroUSDPerMTok != 10_500_000 {
		t.Fatalf("same model id under another provider = %+v, ok=%v", m, ok)
	}
	if _, ok := c.Find("google", "model-a"); ok {
		t.Fatal("Find returned a model for a provider not in the catalogue")
	}
	if _, ok := c.Find("anthropic", "nope"); ok {
		t.Fatal("Find returned an unknown model")
	}
}

func TestParseCatalogue_Rejects(t *testing.T) {
	model := func(fields string) string {
		return `[{"provider":"anthropic","models":[{"id":"m",` + fields + `}]}]`
	}
	const ok = `"input_per_mtok_usd":"3.00","output_per_mtok_usd":"15.00","context_tokens":1000`
	cases := map[string]struct{ raw, want string }{
		"unknown provider":    {`[{"provider":"mystery","models":[{"id":"m",` + ok + `}]}]`, "provider"},
		"duplicate model":     {`[{"provider":"anthropic","models":[{"id":"m",` + ok + `},{"id":"m",` + ok + `}]}]`, "duplicate"},
		"missing input price": {model(`"output_per_mtok_usd":"15.00","context_tokens":1000`), "input_per_mtok_usd"},
		"missing output":      {model(`"input_per_mtok_usd":"3.00","context_tokens":1000`), "output_per_mtok_usd"},
		"float price":         {model(`"input_per_mtok_usd":3.0,"output_per_mtok_usd":"15.00","context_tokens":1000`), "input_per_mtok_usd"},
		"exponent price":      {model(`"input_per_mtok_usd":"3e0","output_per_mtok_usd":"15.00","context_tokens":1000`), "input_per_mtok_usd"},
		"negative price":      {model(`"input_per_mtok_usd":"-3.00","output_per_mtok_usd":"15.00","context_tokens":1000`), "input_per_mtok_usd"},
		"too many decimals":   {model(`"input_per_mtok_usd":"3.0000001","output_per_mtok_usd":"15.00","context_tokens":1000`), "input_per_mtok_usd"},
		"zero context":        {model(`"input_per_mtok_usd":"3.00","output_per_mtok_usd":"15.00","context_tokens":0`), "context_tokens"},
		"negative context":    {model(`"input_per_mtok_usd":"3.00","output_per_mtok_usd":"15.00","context_tokens":-5`), "context_tokens"},
		"empty models":        {`[{"provider":"anthropic","models":[]}]`, "models"},
		"empty model id":      {`[{"provider":"anthropic","models":[{"id":"",` + ok + `}]}]`, "id"},
		"not json":            {`nope`, ""},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := ParseCatalogue(tc.raw)
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not mention %q", err, tc.want)
			}
		})
	}
}

func TestModel_CostMicroUSD(t *testing.T) {
	m := Model{InputMicroUSDPerMTok: 3_000_000, OutputMicroUSDPerMTok: 15_000_000}
	if got := m.CostMicroUSD(Usage{InputTokens: 1_000_000}); got != 3_000_000 {
		t.Fatalf("1M input tokens = %d", got)
	}
	if got := m.CostMicroUSD(Usage{InputTokens: 1_000_000, OutputTokens: 2_000_000}); got != 33_000_000 {
		t.Fatalf("mixed = %d", got)
	}
	// 1 token at 3 USD/MTok is 3 micro-dollars; 1 token at 0.5 micro-USD/MTok is a fraction, rounded up.
	cheap := Model{InputMicroUSDPerMTok: 1, OutputMicroUSDPerMTok: 1}
	if got := cheap.CostMicroUSD(Usage{InputTokens: 1}); got != 1 {
		t.Fatalf("fraction rounds up, got %d", got)
	}
	if got := cheap.CostMicroUSD(Usage{}); got != 0 {
		t.Fatalf("zero usage = %d", got)
	}
	// Rounding happens once on the total, not per side: 0.4 + 0.4 -> 1, not 2.
	if got := (Model{InputMicroUSDPerMTok: 400_000, OutputMicroUSDPerMTok: 400_000}).CostMicroUSD(Usage{InputTokens: 1, OutputTokens: 1}); got != 1 {
		t.Fatalf("round once on total, got %d", got)
	}
}

func TestModel_MaxRunCostMicroUSD(t *testing.T) {
	m := Model{InputMicroUSDPerMTok: 3_000_000, OutputMicroUSDPerMTok: 15_000_000}
	if got := m.MaxRunCostMicroUSD(200_000); got != 3_000_000 {
		t.Fatalf("200k tokens at 15 USD/MTok = %d, want 3000000", got)
	}
	if got := (Model{InputMicroUSDPerMTok: 20_000_000, OutputMicroUSDPerMTok: 1}).MaxRunCostMicroUSD(1); got != 20 {
		t.Fatalf("higher price is the input price, got %d", got)
	}
}
