package gradual

import (
	"encoding/json"
	"fmt"
	"sort"
	"time"
)

func getBucketValue(flagKey string, context EvaluationContext, rollout *SnapshotRollout, inputsUsed map[string]bool) int {
	inputsUsed[rollout.BucketContextKind+"."+rollout.BucketAttributeKey] = true
	bucketKey := "anonymous"
	if ctx, ok := context[rollout.BucketContextKind]; ok {
		if val, ok := ctx[rollout.BucketAttributeKey]; ok && val != nil {
			bucketKey = fmt.Sprintf("%v", val)
		}
	}
	seed := rollout.Seed
	hashInput := fmt.Sprintf("%s:%s:%s", flagKey, seed, bucketKey)
	return HashString(hashInput) % 100_000
}

type activeRollout struct {
	variations []SnapshotRolloutVariation
	stepIndex  int
}

func resolveActiveRollout(rollout *SnapshotRollout, now time.Time) activeRollout {
	if len(rollout.Schedule) == 0 || rollout.StartedAt == "" {
		return activeRollout{variations: rollout.Variations, stepIndex: -1}
	}

	startedAt, err := time.Parse(time.RFC3339, rollout.StartedAt)
	if err != nil {
		return activeRollout{variations: rollout.Variations, stepIndex: -1}
	}

	elapsedMs := float64(now.Sub(startedAt).Milliseconds())
	elapsedMinutes := elapsedMs / 60_000

	if elapsedMinutes < 0 {
		vars := rollout.Variations
		if len(rollout.Schedule) > 0 {
			vars = rollout.Schedule[0].Variations
		}
		return activeRollout{variations: vars, stepIndex: 0}
	}

	cumulativeMinutes := 0.0
	for i, step := range rollout.Schedule {
		if step.DurationMinutes == 0 {
			return activeRollout{variations: step.Variations, stepIndex: i}
		}
		cumulativeMinutes += float64(step.DurationMinutes)
		if elapsedMinutes < cumulativeMinutes {
			return activeRollout{variations: step.Variations, stepIndex: i}
		}
	}

	lastIdx := len(rollout.Schedule) - 1
	return activeRollout{
		variations: rollout.Schedule[lastIdx].Variations,
		stepIndex:  lastIdx,
	}
}

type rolloutResult struct {
	variation         SnapshotVariation
	variationKey      string
	matchedWeight     int
	bucketValue       int
	scheduleStepIndex int
}

func selectVariationFromRollout(activeVariations []SnapshotRolloutVariation, bucketValue int, variations map[string]SnapshotVariation, scheduleStepIndex int) *rolloutResult {
	cumulative := 0
	var matchedRV *SnapshotRolloutVariation

	for i := range activeVariations {
		cumulative += activeVariations[i].Weight
		if bucketValue < cumulative {
			matchedRV = &activeVariations[i]
			break
		}
	}

	if matchedRV == nil && len(activeVariations) > 0 {
		matchedRV = &activeVariations[len(activeVariations)-1]
	}

	if matchedRV == nil {
		return nil
	}

	variation, ok := variations[matchedRV.VariationKey]
	if !ok {
		return nil
	}

	return &rolloutResult{
		variation:         variation,
		variationKey:      matchedRV.VariationKey,
		matchedWeight:     matchedRV.Weight,
		bucketValue:       bucketValue,
		scheduleStepIndex: scheduleStepIndex,
	}
}

func evaluateCondition(condition SnapshotRuleCondition, context EvaluationContext, inputsUsed map[string]bool) bool {
	inputsUsed[condition.ContextKind+"."+condition.AttributeKey] = true

	var contextValue interface{}
	if ctx, ok := context[condition.ContextKind]; ok {
		contextValue = ctx[condition.AttributeKey]
	}

	switch condition.Operator {
	case OpEquals:
		return valuesEqual(contextValue, condition.Value)
	case OpNotEquals:
		return !valuesEqual(contextValue, condition.Value)
	case OpContains:
		return containsOp(contextValue, condition.Value)
	case OpNotContains:
		return !containsOp(contextValue, condition.Value)
	case OpStartsWith:
		cs, csOk := contextValue.(string)
		vs, vsOk := condition.Value.(string)
		if csOk && vsOk {
			return len(cs) >= len(vs) && cs[:len(vs)] == vs
		}
		return false
	case OpEndsWith:
		cs, csOk := contextValue.(string)
		vs, vsOk := condition.Value.(string)
		if csOk && vsOk {
			return len(cs) >= len(vs) && cs[len(cs)-len(vs):] == vs
		}
		return false
	case OpGreaterThan:
		return compareNumbers(contextValue, condition.Value, func(a, b float64) bool { return a > b })
	case OpLessThan:
		return compareNumbers(contextValue, condition.Value, func(a, b float64) bool { return a < b })
	case OpGreaterThanOrEqual:
		return compareNumbers(contextValue, condition.Value, func(a, b float64) bool { return a >= b })
	case OpLessThanOrEqual:
		return compareNumbers(contextValue, condition.Value, func(a, b float64) bool { return a <= b })
	case OpIn:
		return inOp(contextValue, condition.Value)
	case OpNotIn:
		return !inOp(contextValue, condition.Value)
	case OpExists:
		return contextValue != nil
	case OpNotExists:
		return contextValue == nil
	}
	return false
}

func valuesEqual(a, b interface{}) bool {
	// Handle numeric comparison (JSON numbers are float64)
	af, aOk := toFloat64(a)
	bf, bOk := toFloat64(b)
	if aOk && bOk {
		return af == bf
	}
	return fmt.Sprintf("%v", a) == fmt.Sprintf("%v", b)
}

func toFloat64(v interface{}) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int32:
		return float64(n), true
	case int64:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	}
	return 0, false
}

func compareNumbers(a, b interface{}, cmp func(float64, float64) bool) bool {
	af, aOk := toFloat64(a)
	bf, bOk := toFloat64(b)
	if aOk && bOk {
		return cmp(af, bf)
	}
	return false
}

func containsOp(contextValue, value interface{}) bool {
	cs, csOk := contextValue.(string)
	vs, vsOk := value.(string)
	if csOk && vsOk {
		for i := 0; i+len(vs) <= len(cs); i++ {
			if cs[i:i+len(vs)] == vs {
				return true
			}
		}
		return false
	}
	if arr, ok := contextValue.([]interface{}); ok {
		for _, item := range arr {
			if valuesEqual(item, value) {
				return true
			}
		}
	}
	return false
}

func inOp(contextValue, value interface{}) bool {
	arr, ok := value.([]interface{})
	if !ok {
		return false
	}
	for _, item := range arr {
		if valuesEqual(item, contextValue) {
			return true
		}
	}
	return false
}

func evaluateConditions(conditions []SnapshotRuleCondition, context EvaluationContext, inputsUsed map[string]bool) bool {
	for _, cond := range conditions {
		if !evaluateCondition(cond, context, inputsUsed) {
			return false
		}
	}
	return true
}

func matchesIndividual(entries []SnapshotIndividualEntry, context EvaluationContext, inputsUsed map[string]bool) bool {
	for _, entry := range entries {
		inputsUsed[entry.ContextKind+"."+entry.AttributeKey] = true
		if ctx, ok := context[entry.ContextKind]; ok {
			if val, ok := ctx[entry.AttributeKey]; ok {
				if fmt.Sprintf("%v", val) == entry.AttributeValue {
					return true
				}
			}
		}
	}
	return false
}

func evaluateSegment(segment SnapshotSegment, context EvaluationContext, inputsUsed map[string]bool) bool {
	if len(segment.Excluded) > 0 && matchesIndividual(segment.Excluded, context, inputsUsed) {
		return false
	}
	if len(segment.Included) > 0 && matchesIndividual(segment.Included, context, inputsUsed) {
		return true
	}
	if len(segment.Conditions) == 0 {
		return false
	}
	return evaluateConditions(segment.Conditions, context, inputsUsed)
}

func evaluateTarget(target SnapshotTarget, context EvaluationContext, segments map[string]SnapshotSegment, inputsUsed map[string]bool) bool {
	switch target.Type {
	case "individual":
		if target.ContextKind != "" && target.AttributeKey != "" {
			inputsUsed[target.ContextKind+"."+target.AttributeKey] = true
			if ctx, ok := context[target.ContextKind]; ok {
				if val, ok := ctx[target.AttributeKey]; ok {
					return fmt.Sprintf("%v", val) == target.AttributeValue
				}
			}
		}
		return false
	case "rule":
		if len(target.Conditions) > 0 {
			return evaluateConditions(target.Conditions, context, inputsUsed)
		}
		return false
	case "segment":
		if target.SegmentKey != "" {
			if segment, ok := segments[target.SegmentKey]; ok {
				return evaluateSegment(segment, context, inputsUsed)
			}
		}
		return false
	}
	return false
}

func resolveTargetVariation(target SnapshotTarget, flagKey string, context EvaluationContext, variations map[string]SnapshotVariation, inputsUsed map[string]bool, now time.Time) *rolloutResult {
	if target.Rollout != nil {
		active := resolveActiveRollout(target.Rollout, now)
		bucketValue := getBucketValue(flagKey, context, target.Rollout, inputsUsed)
		return selectVariationFromRollout(active.variations, bucketValue, variations, active.stepIndex)
	}

	if target.VariationKey != "" {
		if variation, ok := variations[target.VariationKey]; ok {
			return &rolloutResult{
				variation:         variation,
				variationKey:      target.VariationKey,
				matchedWeight:     100_000,
				bucketValue:       0,
				scheduleStepIndex: -1,
			}
		}
	}

	return nil
}

func resolveDefaultVariation(flag SnapshotFlag, context EvaluationContext, inputsUsed map[string]bool, now time.Time) *rolloutResult {
	if flag.DefaultRollout != nil {
		active := resolveActiveRollout(flag.DefaultRollout, now)
		bucketValue := getBucketValue(flag.Key, context, flag.DefaultRollout, inputsUsed)
		return selectVariationFromRollout(active.variations, bucketValue, flag.Variations, active.stepIndex)
	}

	if flag.DefaultVariationKey != "" {
		if variation, ok := flag.Variations[flag.DefaultVariationKey]; ok {
			return &rolloutResult{
				variation:         variation,
				variationKey:      flag.DefaultVariationKey,
				matchedWeight:     100_000,
				bucketValue:       0,
				scheduleStepIndex: -1,
			}
		}
	}

	return nil
}

func buildRuleMatchReason(target SnapshotTarget) Reason {
	return Reason{
		Type:     "rule_match",
		RuleID:   target.ID,
		RuleName: target.Name,
	}
}

func buildRolloutReason(result *rolloutResult) Reason {
	if result.scheduleStepIndex >= 0 {
		return Reason{
			Type:       "gradual_rollout",
			StepIndex:  result.scheduleStepIndex,
			Percentage: float64(result.matchedWeight) / 1000,
			Bucket:     result.bucketValue,
		}
	}
	return Reason{
		Type:       "percentage_rollout",
		Percentage: float64(result.matchedWeight) / 1000,
		Bucket:     result.bucketValue,
	}
}

// EvaluateFlag evaluates a feature flag against the given context.
// It is a pure function with no I/O.
func EvaluateFlag(flag SnapshotFlag, context EvaluationContext, segments map[string]SnapshotSegment, now ...time.Time) EvalOutput {
	if !flag.Enabled {
		var value interface{}
		if v, ok := flag.Variations[flag.OffVariationKey]; ok {
			value = v.Value
		}
		return EvalOutput{
			Value:        value,
			VariationKey: flag.OffVariationKey,
			Reasons:      []Reason{{Type: "off"}},
			InputsUsed:   []string{},
		}
	}

	evalNow := time.Now().UTC()
	if len(now) > 0 {
		evalNow = now[0]
	}

	inputsUsed := make(map[string]bool)

	// Sort targets by sortOrder
	sortedTargets := make([]SnapshotTarget, len(flag.Targets))
	copy(sortedTargets, flag.Targets)
	sort.Slice(sortedTargets, func(i, j int) bool {
		return sortedTargets[i].SortOrder < sortedTargets[j].SortOrder
	})

	for _, target := range sortedTargets {
		if evaluateTarget(target, context, segments, inputsUsed) {
			resolved := resolveTargetVariation(target, flag.Key, context, flag.Variations, inputsUsed, evalNow)
			if resolved != nil {
				reasons := []Reason{buildRuleMatchReason(target)}
				if target.Rollout != nil {
					reasons = append(reasons, buildRolloutReason(resolved))
				}

				return EvalOutput{
					Value:             resolved.variation.Value,
					VariationKey:      resolved.variationKey,
					Reasons:           reasons,
					MatchedTargetName: target.Name,
					InputsUsed:        mapKeys(inputsUsed),
				}
			}
		}
	}

	resolved := resolveDefaultVariation(flag, context, inputsUsed, evalNow)
	if resolved != nil {
		var reasons []Reason
		if flag.DefaultRollout != nil {
			reasons = append(reasons, buildRolloutReason(resolved))
		}
		reasons = append(reasons, Reason{Type: "default"})

		return EvalOutput{
			Value:        resolved.variation.Value,
			VariationKey: resolved.variationKey,
			Reasons:      reasons,
			InputsUsed:   mapKeys(inputsUsed),
		}
	}

	return EvalOutput{
		Value:      nil,
		Reasons:    []Reason{{Type: "default"}},
		InputsUsed: mapKeys(inputsUsed),
	}
}

func mapKeys(m map[string]bool) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}
