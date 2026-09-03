// Copyright 2025 Zintix Labs
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package v2

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"slices"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// ConfigError identifies the canonical YAML path whose value violates a v2
// semantic rule. LoadConfig syntax errors are returned directly by yaml.v3;
// successfully decoded but contradictory documents return this error type.
type ConfigError struct {
	Path    string
	Problem string
}

// Error renders a stable path-first validation message suitable for a CLI and
// for conversion into an INFEASIBLE_CONFIG/ConfigInvalid diagnostic.
func (e *ConfigError) Error() string {
	if e.Path == "" {
		return "optimizer v2 config: " + e.Problem
	}
	return "optimizer v2 config " + e.Path + ": " + e.Problem
}

// DefaultEngineOptions returns the versioned numerical defaults described by
// the v2 contract. Loading does not silently apply these defaults: canonical
// YAML should spell out every effective value so reviews and reports are clear.
func DefaultEngineOptions() EngineOptions {
	return EngineOptions{
		FeasibilityTolerance:                           DefaultFeasibilityTolerance,
		OptimalityTolerance:                            DefaultOptimalityTolerance,
		QuantileEpsilon:                                DefaultQuantileEpsilon,
		ProfileTolerance:                               DefaultProfileTolerance,
		VisibilityTolerance:                            DefaultVisibilityTolerance,
		ProfileBisectionIterations:                     DefaultProfileBisectionIterations,
		OtherVisibilityBisectionIterations:             DefaultOtherVisibilityBisectionIterations,
		MainGroupInternalVisibilityBisectionIterations: DefaultMainGroupInternalVisibilityBisectionIterations,
	}
}

// LoadConfigFS reads one configuration file from fsys and then applies the same
// strict decoding and semantic validation as LoadConfig. Accepting fs.FS keeps
// cmd/opt compatible with go:embed without giving the optimizer filesystem policy.
func LoadConfigFS(fsys fs.FS, name string) (Config, error) {
	if fsys == nil {
		return Config{}, errors.New("optimizer v2 config: nil filesystem")
	}
	raw, err := fs.ReadFile(fsys, name)
	if err != nil {
		return Config{}, fmt.Errorf("read optimizer v2 config %q: %w", name, err)
	}
	return ParseConfig(raw)
}

// ParseConfig decodes a byte slice as exactly one strict YAML document and
// validates its cross-field semantics. It is a convenience for embedded files
// and tests; streaming callers can call LoadConfig directly.
func ParseConfig(raw []byte) (Config, error) {
	return LoadConfig(bytes.NewReader(raw))
}

// LoadConfig decodes and validates exactly one canonical v2 YAML document.
// KnownFields prevents misspellings from becoming zero values, while the second
// decode rejects document concatenation that could otherwise hide ignored input.
func LoadConfig(r io.Reader) (Config, error) {
	if r == nil {
		return Config{}, errors.New("optimizer v2 config: nil reader")
	}

	decoder := yaml.NewDecoder(r)
	decoder.KnownFields(true)

	var config Config
	if err := decoder.Decode(&config); err != nil {
		return Config{}, fmt.Errorf("decode optimizer v2 config: %w", err)
	}

	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return Config{}, errors.New("decode optimizer v2 config: multiple YAML documents are not allowed")
		}
		return Config{}, fmt.Errorf("decode trailing optimizer v2 YAML: %w", err)
	}

	if err := config.Validate(); err != nil {
		return Config{}, err
	}
	return config, nil
}

// Validate checks every schema-independent invariant that can be proved before
// game collection. It does not inspect registered tags, collect outcomes, derive
// support/risk caps, compile LP rows, or call a solver.
func (c Config) Validate() error {
	if c.Version != ConfigVersion {
		return invalid("version", "must be %d, got %d", ConfigVersion, c.Version)
	}
	if err := validateEngineOptions(c.EngineOptions); err != nil {
		return err
	}
	if len(c.Intents) == 0 {
		return invalid("intents", "must contain at least one math intent")
	}

	// Map iteration is deliberately sorted so the same invalid document reports
	// the same first error on every process and Go runtime version.
	intentIDs := make([]string, 0, len(c.Intents))
	for id := range c.Intents {
		intentIDs = append(intentIDs, id)
	}
	sort.Strings(intentIDs)
	for _, id := range intentIDs {
		path := fmt.Sprintf("intents[%q]", id)
		if err := validateStableID(path, id); err != nil {
			return err
		}
		if err := validateMathIntent(path, c.Intents[id]); err != nil {
			return err
		}
	}

	if len(c.Plans) == 0 {
		return invalid("plans", "must contain at least one run plan")
	}
	seenPlans := make(map[string]struct{}, len(c.Plans))
	for i, plan := range c.Plans {
		path := fmt.Sprintf("plans[%d]", i)
		if err := validateRunPlan(path, plan, c.Intents); err != nil {
			return err
		}
		if _, duplicate := seenPlans[plan.ID]; duplicate {
			return invalid(path+".id", "duplicates plan ID %q", plan.ID)
		}
		seenPlans[plan.ID] = struct{}{}
	}
	return nil
}

// ResolvePlan validates the complete document, selects id, and returns a deep,
// self-contained copy of the plan, referenced intent, and engine options. Later
// stages can safely treat the returned value as immutable even if a repository
// caller subsequently mutates Config-owned slices or pointers.
func (c Config) ResolvePlan(id string) (ResolvedPlan, error) {
	if err := c.Validate(); err != nil {
		return ResolvedPlan{}, err
	}
	if err := validateStableID("plan_id", id); err != nil {
		return ResolvedPlan{}, err
	}
	for _, plan := range c.Plans {
		if plan.ID != id {
			continue
		}
		return ResolvedPlan{
			Version:       c.Version,
			Plan:          cloneRunPlan(plan),
			Intent:        cloneMathIntent(c.Intents[plan.Intent]),
			EngineOptions: c.EngineOptions,
		}, nil
	}
	return ResolvedPlan{}, invalid("plan_id", "plan %q was not found", id)
}

// WithOverrides returns a deep copy of p with the explicit RunRequest overrides
// applied. It never mutates the resolved plan stored in a report or repository;
// only non-negative bet-mode indexes require local validation at this layer.
func (p ResolvedPlan) WithOverrides(overrides RunOverrides) (ResolvedPlan, error) {
	resolved := ResolvedPlan{
		Version:       p.Version,
		Plan:          cloneRunPlan(p.Plan),
		Intent:        cloneMathIntent(p.Intent),
		EngineOptions: p.EngineOptions,
	}
	if overrides.Game != nil {
		resolved.Plan.Target.Game = *overrides.Game
	}
	if overrides.BetMode != nil {
		if *overrides.BetMode < 0 {
			return ResolvedPlan{}, invalid("run.overrides.bet_mode", "must be non-negative")
		}
		resolved.Plan.Target.BetModes = []int{*overrides.BetMode}
	}
	if overrides.Seed != nil {
		resolved.Plan.Seed = *overrides.Seed
	}
	return resolved, nil
}

// validateEngineOptions rejects absent, non-finite, or unbounded-work numerical
// controls. It intentionally accepts non-default positive values because an
// audited developer configuration may trade runtime for resolution.
func validateEngineOptions(options EngineOptions) error {
	positive := []struct {
		path  string
		value float64
	}{
		{"engine_options.feasibility_tolerance", options.FeasibilityTolerance},
		{"engine_options.optimality_tolerance", options.OptimalityTolerance},
		{"engine_options.quantile_epsilon", options.QuantileEpsilon},
		{"engine_options.profile_tolerance", options.ProfileTolerance},
		{"engine_options.visibility_tolerance", options.VisibilityTolerance},
	}
	for _, field := range positive {
		if !finite(field.value) || field.value <= 0 {
			return invalid(field.path, "must be finite and greater than zero")
		}
	}
	if options.QuantileEpsilon >= 0.5 {
		return invalid("engine_options.quantile_epsilon", "must be less than 0.5 so the strict lower-median side remains meaningful")
	}
	if options.ProfileBisectionIterations <= 0 {
		return invalid("engine_options.profile_bisection_iterations", "must be greater than zero")
	}
	if options.OtherVisibilityBisectionIterations <= 0 {
		return invalid("engine_options.other_visibility_bisection_iterations", "must be greater than zero")
	}
	if options.MainGroupInternalVisibilityBisectionIterations <= 0 {
		return invalid("engine_options.main_group_internal_visibility_bisection_iterations", "must be greater than zero")
	}
	return nil
}

// validateRunPlan checks routing and finite execution bounds while leaving game
// existence and the selected mode's runtime range to Problab at Run time. A v2
// plan owns exactly one mode so each mode may use an independent intent, seed,
// collection budget, and numerical-control document before cross-run assembly.
func validateRunPlan(path string, plan RunPlan, intents map[string]MathIntent) error {
	if err := validateStableID(path+".id", plan.ID); err != nil {
		return err
	}
	if len(plan.Target.BetModes) != 1 {
		return invalid(path+".target.bet_modes", "must contain exactly one bet-mode index; optimize sibling modes in separate Runs")
	}
	if plan.Target.BetModes[0] < 0 {
		return invalid(path+".target.bet_modes[0]", "must be non-negative")
	}
	if plan.Engine != EngineIntentLPV2 {
		return invalid(path+".engine", "must be %q, got %q", EngineIntentLPV2, plan.Engine)
	}
	if err := validateStableID(path+".intent", plan.Intent); err != nil {
		return err
	}
	if _, exists := intents[plan.Intent]; !exists {
		return invalid(path+".intent", "references unknown intent %q", plan.Intent)
	}
	if plan.Collection.Workers < 1 {
		return invalid(path+".collection.workers", "must be greater than zero")
	}
	if plan.Collection.BatchSize == 0 {
		return invalid(path+".collection.batch_size", "must be greater than zero; it is an aggregate reporting cadence, not a worker count")
	}
	if plan.Collection.MaxSpins == 0 {
		return invalid(path+".collection.max_spins", "must be greater than zero")
	}
	if plan.CandidateSelection.Evaluator != "none" {
		return invalid(path+".candidate_selection.evaluator", "must be %q until a bounded outer evaluator is registered", "none")
	}
	if plan.CandidateSelection.MaxCandidates != 1 {
		return invalid(path+".candidate_selection.max_candidates", "must be 1 when evaluator is %q", "none")
	}
	if len(plan.Output.Format) == 0 {
		return invalid(path+".output.format", "must contain at least one output format")
	}
	seenOutputFormats := make(map[OutputFormat]struct{}, len(plan.Output.Format))
	for i, format := range plan.Output.Format {
		switch format {
		case OutputOptimalArtifactV1, OutputOptimalGacha:
		default:
			return invalid(
				fmt.Sprintf("%s.output.format[%d]", path, i),
				"must be %q or %q, got %q",
				OutputOptimalArtifactV1,
				OutputOptimalGacha,
				format,
			)
		}
		if _, duplicate := seenOutputFormats[format]; duplicate {
			return invalid(fmt.Sprintf("%s.output.format[%d]", path, i), "duplicates output format %q", format)
		}
		seenOutputFormats[format] = struct{}{}
	}
	if strings.TrimSpace(plan.Output.Directory) == "" {
		return invalid(path+".output.directory", "must not be blank")
	}
	return nil
}

// validateMathIntent enforces all designer-level invariants that do not depend
// on collected empirical support. Expected RTP is derived here from Class
// weights and exact Exp values rather than compared with a duplicate input.
func validateMathIntent(path string, intent MathIntent) error {
	if err := validateNumericRange(path+".overall.cv", intent.Overall.CV, true); err != nil {
		return err
	}
	if len(intent.Classes) == 0 {
		return invalid(path+".classes", "must contain at least one class")
	}

	seenClasses := make(map[string]struct{}, len(intent.Classes))
	totalWeight := 0
	totalSamples := uint64(0)
	for i, class := range intent.Classes {
		classPath := fmt.Sprintf("%s.classes[%d]", path, i)
		if err := validateClassIntent(classPath, class); err != nil {
			return err
		}
		if _, duplicate := seenClasses[class.Name]; duplicate {
			return invalid(classPath+".name", "duplicates class name %q", class.Name)
		}
		seenClasses[class.Name] = struct{}{}
		if ^uint64(0)-totalSamples < class.Collect.Samples {
			return invalid(classPath+".collect.samples", "makes total requested samples overflow uint64")
		}
		totalSamples += class.Collect.Samples
		totalWeight += class.Weight
		if totalWeight > ClassWeightBase {
			return invalid(classPath+".weight", "makes cumulative class weight exceed %d", ClassWeightBase)
		}
	}
	if totalWeight != ClassWeightBase {
		return invalid(path+".classes", "weights must sum to %d, got %d", ClassWeightBase, totalWeight)
	}
	expectedRTP := intent.ExpectedRTP()
	if !finite(expectedRTP) || expectedRTP <= 0 {
		return invalid(path+".classes", "class-weighted expected RTP must be finite and greater than zero: got %.17g", expectedRTP)
	}
	return nil
}

// ExpectedRTP derives the only possible unconditional payout mean from fixed
// Class weights and exact conditional Class Exp values. It is intentionally a
// method rather than a stored field so configuration can never carry a second,
// contradictory RTP source of truth.
func (intent MathIntent) ExpectedRTP() float64 {
	var total compensatedSum
	for _, class := range intent.Classes {
		total.Add(float64(class.Weight) * class.Design.Exp / float64(ClassWeightBase))
	}
	return total.Value()
}

// AnalyzeCollectionTopology reports deterministic, non-fatal overlap facts
// among Classes that have the same tag predicate. Different predicates are
// deliberately not compared: custom game tags may be mutually exclusive in
// ways the configuration layer cannot prove without executing game logic.
//
// Gaps are intentionally not reported. A collection range selects the outcomes
// that belong to a Class; the configuration does not require same-predicate
// Classes to exhaustively cover every payout value. Reporting a continuous
// interval gap would therefore warn about a valid design choice and cannot, by
// itself, prove that any reachable game outcome was omitted.
func AnalyzeCollectionTopology(intentID string, intent MathIntent) []Advisory {
	groups := make(map[string][]int)
	for index, class := range intent.Classes {
		key := tagFilterSignature(class.Collect.Tags)
		groups[key] = append(groups[key], index)
	}
	keys := make([]string, 0, len(groups))
	for key := range groups {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	advisories := make([]Advisory, 0)
	for _, key := range keys {
		indexes := append([]int(nil), groups[key]...)
		for left := 0; left < len(indexes); left++ {
			for right := left + 1; right < len(indexes); right++ {
				a, b := indexes[left], indexes[right]
				first, second := intent.Classes[a], intent.Classes[b]
				lower := math.Max(first.Collect.WinRange.Lower(), second.Collect.WinRange.Lower())
				upper := math.Min(first.Collect.WinRange.Upper(), second.Collect.WinRange.Upper())
				if lower > upper {
					continue
				}
				advisories = append(advisories, Advisory{
					Code: AdvisoryClassCollectionOverlap,
					Message: fmt.Sprintf(
						"classes %q and %q have the same tag predicate and overlapping inclusive win ranges [%.12g, %.12g]; declaration-order first match gives %q priority while its worker-local quota remains open",
						first.Name, second.Name, lower, upper, first.Name,
					),
					SourcePaths: []string{
						fmt.Sprintf("intents.%s.classes[%d].collect", intentID, a),
						fmt.Sprintf("intents.%s.classes[%d].collect", intentID, b),
					},
				})
			}
		}
	}
	return advisories
}

func tagFilterSignature(filters TagFilters) string {
	matches := append([]string(nil), filters.Matches...)
	mismatches := append([]string(nil), filters.Mismatches...)
	sort.Strings(matches)
	sort.Strings(mismatches)
	return "match=" + strings.Join(matches, "\x00") + "\x01mismatch=" + strings.Join(mismatches, "\x00")
}

// validateClassIntent validates one first-match collection class and the shape
// of its uniform or LP-controlled conditional distribution contract.
func validateClassIntent(path string, class ClassIntent) error {
	if err := validateStableID(path+".name", class.Name); err != nil {
		return err
	}
	if class.Weight <= 0 {
		return invalid(path+".weight", "must be a positive integer")
	}
	if class.Weight > ClassWeightBase {
		return invalid(path+".weight", "must not exceed the fixed ClassWeightBase %d", ClassWeightBase)
	}
	if class.Collect.Samples == 0 {
		return invalid(path+".collect.samples", "must be greater than zero")
	}
	if err := validateClosedInterval(path+".collect.win_range", class.Collect.WinRange); err != nil {
		return err
	}
	if err := validateTagFilters(path+".collect.tags", class.Collect.Tags); err != nil {
		return err
	}
	if !finite(class.Design.Exp) {
		return invalid(path+".design.exp", "must be finite")
	}
	if class.Design.Exp < class.Collect.WinRange.Lower() || class.Design.Exp > class.Collect.WinRange.Upper() {
		return invalid(path+".design.exp", "must lie inside collect.win_range")
	}
	if err := validateClosedInterval(path+".design.median", class.Design.Median); err != nil {
		return err
	}
	if class.Design.Median.Lower() < class.Collect.WinRange.Lower() || class.Design.Median.Upper() > class.Collect.WinRange.Upper() {
		return invalid(path+".design.median", "must lie inside collect.win_range")
	}
	if class.Design.Risk != nil {
		if err := validateRisk(path+".design.risk", *class.Design.Risk); err != nil {
			return err
		}
	}

	subjective := class.Design.Subjective
	if subjective.Intent == nil {
		return invalid(path+".design.subjective.intent", "is required and must be explicitly true or false")
	}
	if !subjective.Enabled() {
		if len(subjective.Buckets) != 0 {
			return invalid(path+".design.subjective.buckets", "must be omitted when intent is false")
		}
		if subjective.MainExperience != nil {
			return invalid(path+".design.subjective.main_experience", "must be omitted when intent is false")
		}
		return nil
	}
	return validateEnabledSubjective(path+".design.subjective", subjective, class.Collect.WinRange)
}

// validateEnabledSubjective checks the atomic-boundary representation and its
// Main groups without inventing or normalizing any designer-authored ranges.
func validateEnabledSubjective(path string, subjective SubjectiveIntent, winRange ClosedInterval) error {
	if len(subjective.Buckets) < 3 {
		return invalid(path+".buckets", "requires at least three boundaries (two atomic buckets) when intent is true")
	}
	for i, boundary := range subjective.Buckets {
		if !finite(boundary) {
			return invalid(fmt.Sprintf("%s.buckets[%d]", path, i), "must be finite")
		}
		if i > 0 && boundary <= subjective.Buckets[i-1] {
			return invalid(fmt.Sprintf("%s.buckets[%d]", path, i), "must be strictly greater than the previous boundary")
		}
	}
	if subjective.Buckets[0] != winRange.Lower() || subjective.Buckets[len(subjective.Buckets)-1] != winRange.Upper() {
		return invalid(path+".buckets", "first and last boundaries must exactly equal collect.win_range")
	}
	if subjective.MainExperience == nil {
		return invalid(path+".main_experience", "is required when intent is true")
	}
	return validateMainExperience(path+".main_experience", *subjective.MainExperience, subjective.Buckets)
}

// validateMainExperience ensures groups are complete, non-overlapping unions of
// atomic buckets and that their relative preference has a meaningful positive sum.
func validateMainExperience(path string, main MainExperience, boundaries []float64) error {
	if len(main.Groups) == 0 {
		return invalid(path+".groups", "must contain at least one group")
	}
	if err := validateNumericRange(path+".probability", main.Probability, false); err != nil {
		return err
	}
	if main.Probability.Min <= 0 || main.Probability.Max > 1 {
		return invalid(path+".probability", "must satisfy 0 < min <= max <= 1")
	}
	if len(main.Prefer) != len(main.Groups) {
		return invalid(path+".prefer", "length %d must equal groups length %d", len(main.Prefer), len(main.Groups))
	}
	preferenceTotal := 0.0
	for i, preference := range main.Prefer {
		if !finite(preference) || preference < 0 {
			return invalid(fmt.Sprintf("%s.prefer[%d]", path, i), "must be finite and non-negative")
		}
		preferenceTotal += preference
	}
	if preferenceTotal <= 0 || !finite(preferenceTotal) {
		return invalid(path+".prefer", "must have a finite positive sum")
	}

	mainBuckets := make([]bool, len(boundaries)-1)
	for i, group := range main.Groups {
		groupPath := fmt.Sprintf("%s.groups[%d]", path, i)
		if err := validateClosedInterval(groupPath, group); err != nil {
			return err
		}
		if group.Lower() == group.Upper() {
			return invalid(groupPath, "must span at least one atomic bucket")
		}
		start := slices.Index(boundaries, group.Lower())
		end := slices.Index(boundaries, group.Upper())
		if start < 0 || end < 0 {
			return invalid(groupPath, "endpoints must exactly match atomic bucket boundaries")
		}
		for bucket := start; bucket < end; bucket++ {
			if mainBuckets[bucket] {
				return invalid(groupPath, "overlaps another Main group at atomic bucket %d", bucket)
			}
			mainBuckets[bucket] = true
		}
	}
	for _, isMain := range mainBuckets {
		if !isMain && main.Probability.Max >= 1 {
			return invalid(path+".probability.max", "must be less than 1 when any atomic bucket lies outside Main groups")
		}
	}
	return nil
}

// validateRisk checks only explicit policy parameters. Collision capacity needs
// unique replay support and is therefore intentionally deferred until collection.
func validateRisk(path string, risk RiskIntent) error {
	if risk.Rounds < 2 {
		return invalid(path+".rounds", "must be at least 2")
	}
	if !finite(risk.Collision.Max) || risk.Collision.Max <= 0 || risk.Collision.Max >= 1 {
		return invalid(path+".collision.max", "must be finite and strictly between 0 and 1")
	}
	return nil
}

// validateTagFilters rejects predicates that are syntactically unstable or
// require the same tag to be both present and absent.
func validateTagFilters(path string, tags TagFilters) error {
	seenMatches := make(map[string]struct{}, len(tags.Matches))
	for i, tag := range tags.Matches {
		if err := validateTag(fmt.Sprintf("%s.matches[%d]", path, i), tag); err != nil {
			return err
		}
		if _, duplicate := seenMatches[tag]; duplicate {
			return invalid(fmt.Sprintf("%s.matches[%d]", path, i), "duplicates tag %q", tag)
		}
		seenMatches[tag] = struct{}{}
	}
	seenMismatches := make(map[string]struct{}, len(tags.Mismatches))
	for i, tag := range tags.Mismatches {
		if err := validateTag(fmt.Sprintf("%s.mismatches[%d]", path, i), tag); err != nil {
			return err
		}
		if _, duplicate := seenMismatches[tag]; duplicate {
			return invalid(fmt.Sprintf("%s.mismatches[%d]", path, i), "duplicates tag %q", tag)
		}
		if _, contradiction := seenMatches[tag]; contradiction {
			return invalid(fmt.Sprintf("%s.mismatches[%d]", path, i), "tag %q also appears in matches", tag)
		}
		seenMismatches[tag] = struct{}{}
	}
	return nil
}

// validateTag accepts exact, non-blank identifiers and rejects surrounding
// whitespace that would otherwise create visually indistinguishable tag names.
func validateTag(path, tag string) error {
	if strings.TrimSpace(tag) == "" {
		return invalid(path, "must not be blank")
	}
	if strings.TrimSpace(tag) != tag {
		return invalid(path, "must not contain leading or trailing whitespace")
	}
	return nil
}

// validateStableID enforces explicit non-blank routing identifiers while
// preserving case and punctuation as meaningful parts of the stable ID.
func validateStableID(path, id string) error {
	if strings.TrimSpace(id) == "" {
		return invalid(path, "must not be blank")
	}
	if strings.TrimSpace(id) != id {
		return invalid(path, "must not contain leading or trailing whitespace")
	}
	return nil
}

// validateClosedInterval rejects NaN, infinity, and descending inclusive pairs.
// Equality remains legal for fixed-payout collection and median ranges.
func validateClosedInterval(path string, interval ClosedInterval) error {
	if !finite(interval.Lower()) || !finite(interval.Upper()) {
		return invalid(path, "endpoints must be finite")
	}
	if interval.Lower() > interval.Upper() {
		return invalid(path, "lower endpoint must not exceed upper endpoint")
	}
	return nil
}

// validateNumericRange checks a map-shaped inclusive range and optionally
// requires its lower endpoint to be non-negative, as Overall CV does.
func validateNumericRange(path string, value NumericRange, nonNegative bool) error {
	if !finite(value.Min) || !finite(value.Max) {
		return invalid(path, "min and max must be finite")
	}
	if value.Min > value.Max {
		return invalid(path, "min must not exceed max")
	}
	if nonNegative && value.Min < 0 {
		return invalid(path+".min", "must be non-negative")
	}
	return nil
}

// finite centralizes the ban on NaN and infinity for every authored numerical
// value that participates in constraints, hashing, or deterministic ordering.
func finite(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}

// invalid constructs a typed path-aware ConfigError. Centralizing formatting
// keeps all semantic validation errors suitable for deterministic diagnostics.
func invalid(path, format string, args ...any) error {
	return &ConfigError{Path: path, Problem: fmt.Sprintf(format, args...)}
}

// cloneConfig gives Tuner an ownership-independent configuration repository.
// Callers may reuse or mutate their decoded Config after construction without
// changing future Run resolution, hashes, Class order, or Designer intent.
func cloneConfig(config Config) Config {
	cloned := config
	cloned.Plans = make([]RunPlan, len(config.Plans))
	for i, plan := range config.Plans {
		cloned.Plans[i] = cloneRunPlan(plan)
	}
	cloned.Intents = make(map[string]MathIntent, len(config.Intents))
	for id, intent := range config.Intents {
		cloned.Intents[id] = cloneMathIntent(intent)
	}
	return cloned
}

// cloneRunPlan copies every mutable slice in a routing plan while preserving
// scalar IDs, seed, collection limits, and output policy exactly.
func cloneRunPlan(plan RunPlan) RunPlan {
	cloned := plan
	cloned.Target.BetModes = slices.Clone(plan.Target.BetModes)
	cloned.Output.Format = slices.Clone(plan.Output.Format)
	return cloned
}

// cloneMathIntent deep-copies designer-owned slices and optional pointer values
// so a ResolvedPlan cannot alias Config or another resolved tuning session.
func cloneMathIntent(intent MathIntent) MathIntent {
	cloned := intent
	cloned.Classes = make([]ClassIntent, len(intent.Classes))
	for i, class := range intent.Classes {
		cloned.Classes[i] = cloneClassIntent(class)
	}
	return cloned
}

// cloneClassIntent copies one class and every reference-like child. Collection
// and preparation use it to retain the resolved designer contract without
// exposing mutable Config slices through a CollectedClass.
func cloneClassIntent(class ClassIntent) ClassIntent {
	cloned := class
	cloned.Collect.Tags.Matches = slices.Clone(class.Collect.Tags.Matches)
	cloned.Collect.Tags.Mismatches = slices.Clone(class.Collect.Tags.Mismatches)

	subjective := class.Design.Subjective
	cloned.Design.Subjective.Buckets = slices.Clone(subjective.Buckets)
	if subjective.Intent != nil {
		intentValue := *subjective.Intent
		cloned.Design.Subjective.Intent = &intentValue
	}
	if subjective.MainExperience != nil {
		main := *subjective.MainExperience
		main.Groups = slices.Clone(subjective.MainExperience.Groups)
		main.Prefer = slices.Clone(subjective.MainExperience.Prefer)
		cloned.Design.Subjective.MainExperience = &main
	}
	if class.Design.Risk != nil {
		risk := *class.Design.Risk
		cloned.Design.Risk = &risk
	}
	return cloned
}
