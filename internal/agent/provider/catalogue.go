package provider

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"strings"
)

// Model prices are integer micro-dollars (1 USD = 1,000,000) per million tokens.
type Model struct {
	ID                    string
	InputMicroUSDPerMTok  int64
	OutputMicroUSDPerMTok int64
	ContextTokens         int64
}

type CatalogueEntry struct {
	Provider string
	Models   []Model
}

type Catalogue []CatalogueEntry

var knownProviders = map[string]bool{"anthropic": true, "openai": true, "google": true, "bedrock": true, "fake": true}

type rawModel struct {
	ID            string          `json:"id"`
	InputPrice    json.RawMessage `json:"input_per_mtok_usd"`
	OutputPrice   json.RawMessage `json:"output_per_mtok_usd"`
	ContextTokens json.RawMessage `json:"context_tokens"`
}

type rawEntry struct {
	Provider string     `json:"provider"`
	Models   []rawModel `json:"models"`
}

// ParseCatalogue reads the HM_AGENT_PROVIDERS JSON. Prices must be decimal
// strings; they are converted exactly, with no floating point.
func ParseCatalogue(raw string) (Catalogue, error) {
	var entries []rawEntry
	if err := json.Unmarshal([]byte(raw), &entries); err != nil {
		return nil, fmt.Errorf("provider catalogue is not valid JSON: %w", err)
	}
	var out Catalogue
	seenProvider := map[string]bool{}
	for _, e := range entries {
		if !knownProviders[e.Provider] {
			return nil, fmt.Errorf("provider catalogue: unknown provider %q", e.Provider)
		}
		if seenProvider[e.Provider] {
			return nil, fmt.Errorf("provider catalogue: duplicate provider %q", e.Provider)
		}
		seenProvider[e.Provider] = true
		if len(e.Models) == 0 {
			return nil, fmt.Errorf("provider catalogue: provider %q has no models", e.Provider)
		}
		entry := CatalogueEntry{Provider: e.Provider}
		seenModel := map[string]bool{}
		for _, rm := range e.Models {
			if rm.ID == "" {
				return nil, fmt.Errorf("provider catalogue: provider %q has a model with an empty id", e.Provider)
			}
			if seenModel[rm.ID] {
				return nil, fmt.Errorf("provider catalogue: duplicate model %q for provider %q", rm.ID, e.Provider)
			}
			seenModel[rm.ID] = true
			in, err := parsePrice(rm.InputPrice)
			if err != nil {
				return nil, fmt.Errorf("provider catalogue: model %q input_per_mtok_usd: %w", rm.ID, err)
			}
			out, err := parsePrice(rm.OutputPrice)
			if err != nil {
				return nil, fmt.Errorf("provider catalogue: model %q output_per_mtok_usd: %w", rm.ID, err)
			}
			var ctxTokens int64
			if err := json.Unmarshal(rm.ContextTokens, &ctxTokens); err != nil || ctxTokens <= 0 {
				return nil, fmt.Errorf("provider catalogue: model %q context_tokens must be a positive integer", rm.ID)
			}
			entry.Models = append(entry.Models, Model{ID: rm.ID, InputMicroUSDPerMTok: in, OutputMicroUSDPerMTok: out, ContextTokens: ctxTokens})
		}
		out = append(out, entry)
	}
	return out, nil
}

// parsePrice turns a JSON string such as "3.00" into micro-dollars. A JSON
// number, an exponent, a sign or more than six decimals is rejected.
func parsePrice(raw json.RawMessage) (int64, error) {
	if len(raw) == 0 {
		return 0, errors.New("is required")
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return 0, errors.New("must be a decimal string such as \"3.00\", not a number")
	}
	whole, frac, _ := strings.Cut(s, ".")
	if whole == "" || len(frac) > 6 || !allDigits(whole) || !allDigits(frac) || (strings.Contains(s, ".") && frac == "") {
		return 0, fmt.Errorf("%q is not a decimal price with at most six decimals", s)
	}
	frac += strings.Repeat("0", 6-len(frac))
	n, ok := new(big.Int).SetString(whole+frac, 10)
	if !ok || !n.IsInt64() {
		return 0, fmt.Errorf("%q is out of range", s)
	}
	return n.Int64(), nil
}

func allDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// Find returns the model with the given id under the given provider.
func (c Catalogue) Find(provider, model string) (Model, bool) {
	for _, e := range c {
		if e.Provider != provider {
			continue
		}
		for _, m := range e.Models {
			if m.ID == model {
				return m, true
			}
		}
	}
	return Model{}, false
}

const microPerMTok = 1_000_000

// CostMicroUSD is the price of u in micro-dollars, rounded up once on the total.
func (m Model) CostMicroUSD(u Usage) int64 {
	total := new(big.Int).Mul(big.NewInt(u.InputTokens), big.NewInt(m.InputMicroUSDPerMTok))
	total.Add(total, new(big.Int).Mul(big.NewInt(u.OutputTokens), big.NewInt(m.OutputMicroUSDPerMTok)))
	return ceilDivMTok(total)
}

// MaxRunCostMicroUSD bounds a run's cost: every token at the higher price.
func (m Model) MaxRunCostMicroUSD(maxRunTokens int64) int64 {
	price := max(m.InputMicroUSDPerMTok, m.OutputMicroUSDPerMTok)
	return ceilDivMTok(new(big.Int).Mul(big.NewInt(maxRunTokens), big.NewInt(price)))
}

// ceilDivMTok divides by one million, rounding up, saturating at MaxInt64.
func ceilDivMTok(n *big.Int) int64 {
	n.Add(n, big.NewInt(microPerMTok-1))
	n.Div(n, big.NewInt(microPerMTok))
	if !n.IsInt64() {
		return math.MaxInt64
	}
	return n.Int64()
}
