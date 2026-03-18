// Package gradual provides a feature flag SDK for Go.
package gradual

import "encoding/json"

// TargetingOperator defines the comparison operators for rule conditions.
type TargetingOperator string

const (
	OpEquals              TargetingOperator = "equals"
	OpNotEquals           TargetingOperator = "not_equals"
	OpContains            TargetingOperator = "contains"
	OpNotContains         TargetingOperator = "not_contains"
	OpStartsWith          TargetingOperator = "starts_with"
	OpEndsWith            TargetingOperator = "ends_with"
	OpGreaterThan         TargetingOperator = "greater_than"
	OpLessThan            TargetingOperator = "less_than"
	OpGreaterThanOrEqual  TargetingOperator = "greater_than_or_equal"
	OpLessThanOrEqual     TargetingOperator = "less_than_or_equal"
	OpIn                  TargetingOperator = "in"
	OpNotIn               TargetingOperator = "not_in"
	OpExists              TargetingOperator = "exists"
	OpNotExists           TargetingOperator = "not_exists"
)

// EvaluationContext is keyed by context kind (e.g. "user", "company")
// with arbitrary string-keyed attributes.
type EvaluationContext map[string]map[string]interface{}

// SnapshotRuleCondition represents a single condition in a targeting rule.
type SnapshotRuleCondition struct {
	ContextKind  string            `json:"contextKind"`
	AttributeKey string            `json:"attributeKey"`
	Operator     TargetingOperator `json:"operator"`
	Value        interface{}       `json:"value"`
}

// SnapshotIndividualEntry represents an individual targeting entry.
type SnapshotIndividualEntry struct {
	ContextKind    string `json:"contextKind"`
	AttributeKey   string `json:"attributeKey"`
	AttributeValue string `json:"attributeValue"`
}

// SnapshotSegment represents a user segment.
type SnapshotSegment struct {
	Key        string                    `json:"key"`
	Conditions []SnapshotRuleCondition   `json:"conditions"`
	Included   []SnapshotIndividualEntry `json:"included,omitempty"`
	Excluded   []SnapshotIndividualEntry `json:"excluded,omitempty"`
}

// SnapshotRolloutVariation is a variation with a weight for rollout.
type SnapshotRolloutVariation struct {
	VariationKey string `json:"variationKey"`
	Weight       int    `json:"weight"` // 0-100000 for 0.001% precision
}

// SnapshotScheduleStep is a step in a gradual rollout schedule.
type SnapshotScheduleStep struct {
	DurationMinutes int                        `json:"durationMinutes"`
	Variations      []SnapshotRolloutVariation `json:"variations"`
}

// SnapshotRollout defines a percentage-based or scheduled rollout.
type SnapshotRollout struct {
	Variations        []SnapshotRolloutVariation `json:"variations"`
	BucketContextKind string                     `json:"bucketContextKind"`
	BucketAttributeKey string                    `json:"bucketAttributeKey"`
	Seed              string                     `json:"seed,omitempty"`
	Schedule          []SnapshotScheduleStep     `json:"schedule,omitempty"`
	StartedAt         string                     `json:"startedAt,omitempty"`
}

// SnapshotTarget defines a targeting rule.
type SnapshotTarget struct {
	ID             string                  `json:"id,omitempty"`
	Type           string                  `json:"type"` // "rule", "individual", "segment"
	SortOrder      int                     `json:"sortOrder"`
	Name           string                  `json:"name,omitempty"`
	VariationKey   string                  `json:"variationKey,omitempty"`
	Rollout        *SnapshotRollout        `json:"rollout,omitempty"`
	Conditions     []SnapshotRuleCondition `json:"conditions,omitempty"`
	ContextKind    string                  `json:"contextKind,omitempty"`
	AttributeKey   string                  `json:"attributeKey,omitempty"`
	AttributeValue string                  `json:"attributeValue,omitempty"`
	SegmentKey     string                  `json:"segmentKey,omitempty"`
}

// SnapshotVariation represents a flag variation.
type SnapshotVariation struct {
	Key   string      `json:"key"`
	Value interface{} `json:"value"`
}

// SnapshotFlag represents a complete feature flag definition.
type SnapshotFlag struct {
	Key                 string                       `json:"key"`
	Type                string                       `json:"type"` // "boolean", "string", "number", "json"
	Enabled             bool                         `json:"enabled"`
	Variations          map[string]SnapshotVariation `json:"variations"`
	OffVariationKey     string                       `json:"offVariationKey"`
	Targets             []SnapshotTarget             `json:"targets"`
	DefaultVariationKey string                       `json:"defaultVariationKey,omitempty"`
	DefaultRollout      *SnapshotRollout             `json:"defaultRollout,omitempty"`
}

// EnvironmentSnapshot is the full snapshot of flags and segments.
type EnvironmentSnapshot struct {
	Version     int                        `json:"version"`
	GeneratedAt string                     `json:"generatedAt"`
	Meta        EnvironmentSnapshotMeta    `json:"meta"`
	Flags       map[string]SnapshotFlag    `json:"flags"`
	Segments    map[string]SnapshotSegment `json:"segments"`
}

// EnvironmentSnapshotMeta contains snapshot metadata.
type EnvironmentSnapshotMeta struct {
	ProjectID       string `json:"projectId"`
	OrganizationID  string `json:"organizationId"`
	EnvironmentSlug string `json:"environmentSlug"`
	EnvironmentID   string `json:"environmentId"`
}

// Reason describes why an evaluation produced a specific result.
type Reason struct {
	Type       string  `json:"type"`
	RuleID     string  `json:"ruleId,omitempty"`
	RuleName   string  `json:"ruleName,omitempty"`
	Percentage float64 `json:"percentage,omitempty"`
	Bucket     int     `json:"bucket,omitempty"`
	StepIndex  int     `json:"stepIndex,omitempty"`
	Detail     string  `json:"detail,omitempty"`
	ErrorCode  string  `json:"errorCode,omitempty"`
}

// MarshalJSON customizes JSON output to only include relevant fields per type.
func (r Reason) MarshalJSON() ([]byte, error) {
	m := map[string]interface{}{"type": r.Type}
	switch r.Type {
	case "rule_match":
		m["ruleId"] = r.RuleID
		if r.RuleName != "" {
			m["ruleName"] = r.RuleName
		}
	case "percentage_rollout":
		m["percentage"] = r.Percentage
		m["bucket"] = r.Bucket
	case "gradual_rollout":
		m["stepIndex"] = r.StepIndex
		m["percentage"] = r.Percentage
		m["bucket"] = r.Bucket
	case "error":
		m["detail"] = r.Detail
		if r.ErrorCode != "" {
			m["errorCode"] = r.ErrorCode
		}
	}
	return json.Marshal(m)
}

// EvalOutput is the internal evaluation result.
type EvalOutput struct {
	Value             interface{}
	VariationKey      string
	Reasons           []Reason
	MatchedTargetName string
	ErrorDetail       string
	InputsUsed        []string
}
