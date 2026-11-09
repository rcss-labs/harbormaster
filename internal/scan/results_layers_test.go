package scan

import (
	"strings"
	"testing"
)

func TestParseResult_KeepsValidLayersAndBase(t *testing.T) {
	finding := sampleFinding(1)
	finding.LayerID = digestB
	body := resultBody(t, digestA, []Finding{finding}, func(m map[string]any) {
		m["layers"] = []string{digestB, digestA}
		m["base"] = BaseLayers{Ref: "node:20", Digest: digestA, Layers: []string{digestB}}
	})
	res, err := ParseResult(body, digestA)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Layers) != 2 || res.Findings[0].LayerID != digestB {
		t.Errorf("layers or layer id lost: %+v", res)
	}
	if res.Base == nil || res.Base.Ref != "node:20" || res.Base.Digest != digestA || len(res.Base.Layers) != 1 {
		t.Errorf("base lost: %+v", res.Base)
	}
}

func TestParseResult_DropsInvalidLayers(t *testing.T) {
	tooMany := make([]string, MaxLayers+1)
	for i := range tooMany {
		tooMany[i] = digestA
	}
	cases := map[string][]string{
		"bad digest":  {digestA, "sha256:xyz"},
		"too many":    tooMany,
		"not sha256":  {"md5:" + strings.Repeat("a", 64)},
		"empty entry": {digestA, ""},
	}
	for name, layers := range cases {
		t.Run(name, func(t *testing.T) {
			body := resultBody(t, digestA, nil, func(m map[string]any) {
				m["layers"] = layers
				m["base"] = BaseLayers{Ref: "node:20", Layers: layers}
			})
			res, err := ParseResult(body, digestA)
			if err != nil {
				t.Fatal(err)
			}
			if res.Layers != nil || res.Base == nil || res.Base.Layers != nil {
				t.Errorf("an invalid list should be dropped whole: %+v base %+v", res.Layers, res.Base)
			}
		})
	}
}

func TestParseResult_AcceptsMaxLayers(t *testing.T) {
	layers := make([]string, MaxLayers)
	for i := range layers {
		layers[i] = digestA
	}
	res, err := ParseResult(resultBody(t, digestA, nil, func(m map[string]any) { m["layers"] = layers }), digestA)
	if err != nil || len(res.Layers) != MaxLayers {
		t.Fatalf("want %d layers, got %d err %v", MaxLayers, len(res.Layers), err)
	}
}

func TestParseResult_SanitizesFindingLayerAndBase(t *testing.T) {
	good, bad := sampleFinding(1), sampleFinding(2)
	good.LayerID, bad.LayerID = digestA, "not-a-digest"
	body := resultBody(t, digestA, []Finding{good, bad}, func(m map[string]any) {
		m["base"] = BaseLayers{Ref: "node:20‮\n", Digest: "garbage", Error: "no\x00 access‮"}
	})
	res, err := ParseResult(body, digestA)
	if err != nil {
		t.Fatal(err)
	}
	if res.Findings[0].LayerID != digestA || res.Findings[1].LayerID != "" {
		t.Errorf("layer ids = %q, %q", res.Findings[0].LayerID, res.Findings[1].LayerID)
	}
	if res.Base == nil || res.Base.Ref != "node:20" || res.Base.Digest != "" || res.Base.Error != "no access" {
		t.Errorf("base not sanitized: %+v", res.Base)
	}
}

func TestParseResult_AbsentBaseStaysNil(t *testing.T) {
	res, err := ParseResult(resultBody(t, digestA, nil, nil), digestA)
	if err != nil || res.Base != nil || res.Layers != nil {
		t.Fatalf("got %+v err %v", res, err)
	}
}
