package gradual

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"
)

type testFixture struct {
	Description string     `json:"description"`
	Tests       []testCase `json:"tests"`
}

type testCase struct {
	Name     string                     `json:"name"`
	Flag     json.RawMessage            `json:"flag"`
	Context  EvaluationContext           `json:"context"`
	Segments map[string]SnapshotSegment  `json:"segments"`
	Options  testOptions                `json:"options"`
	Expected testExpected               `json:"expected"`
}

type testOptions struct {
	Now string `json:"now"`
}

type testExpected struct {
	Value             interface{}      `json:"value"`
	VariationKey      *string          `json:"variationKey"`
	Reasons           []map[string]interface{} `json:"reasons"`
	MatchedTargetName *string          `json:"matchedTargetName"`
	ReasonType        string           `json:"reasonType"`
	StepIndex         *int             `json:"stepIndex"`
	ValueOneOf        []interface{}    `json:"valueOneOf"`
}

func loadFixtures(t *testing.T) []struct {
	file string
	tc   testCase
} {
	t.Helper()
	dir := "../gradual-sdk-spec/testdata/evaluator"
	files, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		t.Fatalf("Failed to glob fixtures: %v", err)
	}
	sort.Strings(files)

	var cases []struct {
		file string
		tc   testCase
	}

	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("Failed to read %s: %v", f, err)
		}
		var fixture testFixture
		if err := json.Unmarshal(data, &fixture); err != nil {
			t.Fatalf("Failed to parse %s: %v", f, err)
		}
		base := filepath.Base(f)
		for _, tc := range fixture.Tests {
			cases = append(cases, struct {
				file string
				tc   testCase
			}{file: base, tc: tc})
		}
	}
	return cases
}

func parseFlag(t *testing.T, raw json.RawMessage) SnapshotFlag {
	t.Helper()
	var flag SnapshotFlag
	if err := json.Unmarshal(raw, &flag); err != nil {
		t.Fatalf("Failed to parse flag: %v", err)
	}
	return flag
}

func TestConformance(t *testing.T) {
	cases := loadFixtures(t)

	for _, c := range cases {
		testID := fmt.Sprintf("%s::%s", c.file, c.tc.Name)
		t.Run(testID, func(t *testing.T) {
			flag := parseFlag(t, c.tc.Flag)
			context := c.tc.Context
			if context == nil {
				context = EvaluationContext{}
			}
			segments := c.tc.Segments
			if segments == nil {
				segments = map[string]SnapshotSegment{}
			}

			var opts []time.Time
			if c.tc.Options.Now != "" {
				now, err := time.Parse(time.RFC3339, c.tc.Options.Now)
				if err != nil {
					t.Fatalf("Failed to parse now: %v", err)
				}
				opts = append(opts, now)
			}

			result := EvaluateFlag(flag, context, segments, opts...)
			expected := c.tc.Expected

			// Check value
			if expected.Value != nil || expected.VariationKey != nil {
				if !valuesEqualForTest(result.Value, expected.Value) {
					t.Errorf("value: got %v (%T), want %v (%T)",
						result.Value, result.Value, expected.Value, expected.Value)
				}
			}

			// Check variationKey
			if expected.VariationKey != nil {
				if result.VariationKey != *expected.VariationKey {
					t.Errorf("variationKey: got %q, want %q",
						result.VariationKey, *expected.VariationKey)
				}
			}

			// Check reasons
			if len(expected.Reasons) > 0 {
				actualReasons := make([]map[string]interface{}, len(result.Reasons))
				for i, r := range result.Reasons {
					b, _ := json.Marshal(r)
					json.Unmarshal(b, &actualReasons[i])
				}

				for _, expReason := range expected.Reasons {
					found := false
					for _, actReason := range actualReasons {
						if reasonMatches(actReason, expReason) {
							found = true
							break
						}
					}
					if !found {
						t.Errorf("Expected reason %v not found in %v", expReason, actualReasons)
					}
				}
			}

			// Check matchedTargetName
			if expected.MatchedTargetName != nil {
				if result.MatchedTargetName != *expected.MatchedTargetName {
					t.Errorf("matchedTargetName: got %q, want %q",
						result.MatchedTargetName, *expected.MatchedTargetName)
				}
			}

			// Check valueOneOf
			if len(expected.ValueOneOf) > 0 {
				found := false
				for _, v := range expected.ValueOneOf {
					if valuesEqualForTest(result.Value, v) {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("value %v not in %v", result.Value, expected.ValueOneOf)
				}
			}
		})
	}
}

func valuesEqualForTest(a, b interface{}) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	// Compare as JSON for type-agnostic equality
	aj, _ := json.Marshal(a)
	bj, _ := json.Marshal(b)
	return string(aj) == string(bj)
}

func reasonMatches(actual, expected map[string]interface{}) bool {
	for k, v := range expected {
		av, ok := actual[k]
		if !ok {
			return false
		}
		aj, _ := json.Marshal(av)
		ej, _ := json.Marshal(v)
		if string(aj) != string(ej) {
			return false
		}
	}
	return true
}
