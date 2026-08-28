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
	"time"

	"github.com/zintix-labs/problab/spec"
)

const (
	// ConfigVersion is the only configuration version accepted by this package.
	// A hard version gate prevents an old binary from silently assigning new
	// fields their Go zero values, or a new binary from guessing old semantics.
	ConfigVersion = 2

	// ClassWeightBase is the fixed denominator used to convert a class Weight
	// into its unconditional probability. It is intentionally a system constant
	// rather than a designer-configurable normalization base.
	ClassWeightBase = 1_000_000

	// DefaultFeasibilityTolerance bounds original-row constraint replay error.
	DefaultFeasibilityTolerance = 1e-9
	// DefaultOptimalityTolerance bounds the backend's objective comparison error.
	DefaultOptimalityTolerance = 1e-9
	// DefaultQuantileEpsilon represents the strict side of the lower-median rule.
	DefaultQuantileEpsilon = 1e-9
	// DefaultProfileTolerance is added to the final feasible Main profile delta
	// before the relative-profile solution space is locked.
	DefaultProfileTolerance = 1e-8
	// DefaultVisibilityTolerance is subtracted from final feasible visibility
	// rho values before their solution spaces are locked.
	DefaultVisibilityTolerance = 1e-8
	// DefaultProfileBisectionIterations bounds Main profile probes.
	DefaultProfileBisectionIterations = 60
	// DefaultOtherVisibilityBisectionIterations bounds Other visibility probes.
	DefaultOtherVisibilityBisectionIterations = 60
	// DefaultMainGroupInternalVisibilityBisectionIterations bounds the neutral
	// refinement that protects supported sibling buckets inside Main Groups.
	DefaultMainGroupInternalVisibilityBisectionIterations = 60
)

// OptimizationStageID is the stable machine identity of a product-facing
// optimization substage. It deliberately describes semantics rather than an
// incidental phase number.
type OptimizationStageID string

const (
	StageProveHardFeasibility                OptimizationStageID = "prove-hard-feasibility"
	StageMinimizeMainProfileDeviation        OptimizationStageID = "minimize-main-profile-deviation"
	StageMaximizeOtherBucketVisibility       OptimizationStageID = "maximize-other-bucket-visibility"
	StageMaximizeMainGroupInternalVisibility OptimizationStageID = "maximize-main-group-internal-visibility"
	StageSelectCanonicalBucketProbabilities  OptimizationStageID = "select-canonical-bucket-probabilities"
)

// MainSemanticAxiomVersion names the versioned product meaning of a configured
// Main Experience Group. When supported Other buckets exist, every Main Group
// must carry at least the conditional mass that one Other bucket would receive
// if total Other mass were divided equally. A group below that threshold is,
// by definition, not a principal player experience and belongs in Others.
const MainSemanticAxiomVersion = "main-group-dominates-supported-other-average-v1"

// EngineName is a stable routing key recorded in configuration and reports.
// It is not a display name: changing it selects a different implementation
// contract and therefore changes reproducibility provenance.
type EngineName string

const (
	// EngineIntentLPV2 selects the v2 hard-LP, relative-profile, and relative
	// Other-visibility engine. Config validation rejects every other engine name
	// until another engine has an explicit v2 contract.
	EngineIntentLPV2 EngineName = "intent_lp_v2"
)

// Config is the canonical, versioned optimizer input document. Plans contain
// execution routing, Intents contain designer requirements, and EngineOptions
// contains bounded numerical controls shared by the plans in this document.
type Config struct {
	Version       int                   `yaml:"version" json:"version"`
	Plans         []RunPlan             `yaml:"plans" json:"plans"`
	Intents       map[string]MathIntent `yaml:"intents" json:"intents"`
	EngineOptions EngineOptions         `yaml:"engine_options" json:"engine_options"`
}

// RunPlan describes one repeatable optimizer invocation without embedding the
// referenced MathIntent. The indirection lets multiple games or output targets
// reuse the same designer-owned intent.
type RunPlan struct {
	// ID is the stable plan identifier used by RunRequest.PlanID and reports.
	ID string `yaml:"id" json:"id"`
	// Target selects the production game and bet modes whose outcomes are used.
	Target Target `yaml:"target" json:"target"`
	// Engine selects the registered optimizer implementation.
	Engine EngineName `yaml:"engine" json:"engine"`
	// Intent is the key of the MathIntent entry in Config.Intents.
	Intent string `yaml:"intent" json:"intent"`
	// Seed initializes worker zero's collection PRNG stream and the deterministic
	// sub-seed generator for additional workers. The exact value, including zero
	// or a negative int64, is valid and reportable.
	Seed int64 `yaml:"seed" json:"seed"`
	// Collection controls execution ownership and the finite spin budget.
	Collection CollectionOptions `yaml:"collection" json:"collection"`
	// CandidateSelection declares the bounded post-LP evaluator policy. v2
	// currently accepts only the canonical candidate with evaluator "none".
	CandidateSelection CandidateSelectionOptions `yaml:"candidate_selection" json:"candidate_selection"`
	// Output names the directory into which verified artifacts are published.
	Output OutputOptions `yaml:"output" json:"output"`
}

// Target identifies a game and exactly one zero-based bet-mode index. The slice
// shape is retained for configuration compatibility, but validation rejects
// zero or multiple entries: each mode owns an independent collection, intent,
// solve, and pending-publication transaction.
type Target struct {
	Game     spec.GID `yaml:"game" json:"game"`
	BetModes []int    `yaml:"bet_modes" json:"bet_modes"`
}

// CollectionOptions contains execution policy, not designer math intent.
type CollectionOptions struct {
	// Workers is the positive number of independent raw Machines. Class quotas
	// and MaxSpins are statically partitioned by worker index, so a fixed Seed and
	// Workers value is reproducible without scheduler-dependent shared counters.
	Workers int `yaml:"workers" json:"workers"`
	// BatchSize is the aggregate progress/reporting cadence. It does not control
	// worker count or the deterministic quota partition.
	BatchSize uint64 `yaml:"batch_size" json:"batch_size"`
	// MaxSpins is the hard upper bound on produced spins for a target run. It
	// prevents an impossible or rare classification request from running forever.
	MaxSpins uint64 `yaml:"max_spins" json:"max_spins"`
}

// CandidateSelectionOptions makes the outer evaluation boundary explicit even
// while v2 only emits the deterministic canonical candidate. Adding another
// evaluator requires a separately versioned, bounded, deterministic contract.
type CandidateSelectionOptions struct {
	Evaluator     string `yaml:"evaluator" json:"evaluator"`
	MaxCandidates int    `yaml:"max_candidates" json:"max_candidates"`
}

// OutputFormat is the stable on-disk runtime representation routing key.
type OutputFormat string

const (
	// OutputOptimalArtifactV1 preserves the existing global alias table, seed
	// bank, and problab.optimal/v1 manifest consumed by production runtime.
	OutputOptimalArtifactV1 OutputFormat = "optimal_artifact_v1"
	// OutputOptimalGacha emits the legacy per-mode zstd-compressed JSON
	// AliasTableF64 and raw seed bank consumed through gachas/seed_bank config.
	OutputOptimalGacha OutputFormat = "optimal_gacha"
)

// OutputOptions describes where a successfully verified artifact is written.
// The writer owns path creation and overwrite policy; neither field is Designer
// math intent, but both participate in reproducibility/config provenance.
type OutputOptions struct {
	Format    []OutputFormat `yaml:"format" json:"format"`
	Directory string         `yaml:"directory" json:"directory"`
}

// MathIntent is the complete designer-authored mathematical contract reused by
// one or more RunPlans. It contains no seed, worker, solver, or filesystem data.
type MathIntent struct {
	Overall OverallIntent `yaml:"overall" json:"overall"`
	Classes []ClassIntent `yaml:"classes" json:"classes"`
}

// OverallIntent constrains the unconditional payout-multiplier shape that is
// not already implied by Class contracts. RTP is deliberately absent: fixed
// Class weights and exact Class Exp values derive it without another input.
type OverallIntent struct {
	CV NumericRange `yaml:"cv" json:"cv"`
}

// NumericRange is a finite inclusive [Min, Max] range encoded as a YAML map.
// It is used where named endpoints make the unit and hard-bound role explicit.
type NumericRange struct {
	Min float64 `yaml:"min" json:"min"`
	Max float64 `yaml:"max" json:"max"`
}

// ClosedInterval is an inclusive [lower, upper] pair encoded as a two-element
// YAML sequence. Atomic bucket intervals use these numbers as boundaries, but
// the classifier applies half-open semantics to every bucket except the last.
type ClosedInterval [2]float64

// Lower returns the declared lower endpoint. The accessor documents endpoint
// meaning at call sites and avoids scattering positional indexes through v2.
func (r ClosedInterval) Lower() float64 { return r[0] }

// Upper returns the declared upper endpoint. It does not reorder invalid input;
// validation must reject descending ranges rather than changing designer intent.
func (r ClosedInterval) Upper() float64 { return r[1] }

// ClassIntent fixes a class's unconditional weight, collection predicate, and
// conditional mathematical design. Class declaration order is semantic because
// collection uses deterministic first-match classification.
type ClassIntent struct {
	Name    string        `yaml:"name" json:"name"`
	Weight  int           `yaml:"weight" json:"weight"`
	Collect CollectIntent `yaml:"collect" json:"collect"`
	Design  ClassDesign   `yaml:"design" json:"design"`
}

// CollectIntent states how many replayable outcomes must be accepted by a
// class and which payout/tag predicate accepts them. Samples counts accepted
// outcomes, not attempted spins.
type CollectIntent struct {
	Samples  uint64         `yaml:"samples" json:"samples"`
	WinRange ClosedInterval `yaml:"win_range" json:"win_range"`
	Tags     TagFilters     `yaml:"tags" json:"tags"`
}

// TagFilters is a conjunction: every Matches tag must be present and every
// Mismatches tag must be absent. Runtime validation of registered tag names is
// delegated to the collector; schema validation detects empty, duplicate, and
// logically contradictory declarations.
type TagFilters struct {
	Matches    []string `yaml:"matches" json:"matches"`
	Mismatches []string `yaml:"mismatches" json:"mismatches"`
}

// ClassDesign contains hard class math, optional bucket-level collision risk,
// and the switch between empirical-uniform and LP-controlled distributions.
type ClassDesign struct {
	// Exp is the exact conditional payout-multiplier mean for this class.
	Exp float64 `yaml:"exp" json:"exp"`
	// Median is the allowed inclusive range for the weighted lower median.
	Median ClosedInterval `yaml:"median" json:"median"`
	// Subjective controls atomic buckets and group-level soft intent.
	Subjective SubjectiveIntent `yaml:"subjective" json:"subjective"`
	// Risk is nil when no collision constraint was requested. v2 deliberately
	// has no hidden risk default, including for empirical-uniform classes.
	Risk *RiskIntent `yaml:"risk,omitempty" json:"risk,omitempty"`
}

// SubjectiveIntent separates the required intent switch from fields that are
// legal only when it is enabled. Intent is a pointer so validation can tell an
// explicitly authored false value from a missing required YAML field.
type SubjectiveIntent struct {
	Intent         *bool           `yaml:"intent" json:"intent"`
	Buckets        []float64       `yaml:"buckets,omitempty" json:"buckets,omitempty"`
	MainExperience *MainExperience `yaml:"main_experience,omitempty" json:"main_experience,omitempty"`
}

// Enabled reports whether the designer explicitly enabled atomic-bucket LP
// control. A missing required Intent value returns false here but is rejected by
// Config.Validate before any engine is selected.
func (s SubjectiveIntent) Enabled() bool {
	return s.Intent != nil && *s.Intent
}

// MainExperience describes semantic groups of complete atomic buckets. Designer
// hard intent constrains only Group totals; a later system-neutral refinement
// protects supported siblings from arbitrary zeroing without inventing a target
// internal shape.
type MainExperience struct {
	Groups      []ClosedInterval `yaml:"groups" json:"groups"`
	Probability NumericRange     `yaml:"probability" json:"probability"`
	Prefer      []float64        `yaml:"prefer" json:"prefer"`
}

// RiskIntent supplies the assumptions for the per-bucket birthday/Poisson
// collision proxy. Risk caps are later derived from actual unique support;
// this type intentionally does not store a user-authored bucket probability.
type RiskIntent struct {
	Rounds    uint64          `yaml:"rounds" json:"rounds"`
	Collision CollisionIntent `yaml:"collision" json:"collision"`
}

// CollisionIntent is the maximum allowed approximate collision probability for
// each atomic bucket over RiskIntent.Rounds independent observations.
type CollisionIntent struct {
	Max float64 `yaml:"max" json:"max"`
}

// EngineOptions controls numerical tolerances and bounded search work. These
// values must never relax or rewrite MathIntent; every effective value is copied
// into ResolvedPlan and therefore available to provenance reports.
type EngineOptions struct {
	// FeasibilityTolerance is the maximum scaled violation accepted when the
	// original semantic constraints are replayed after a backend solve.
	FeasibilityTolerance float64 `yaml:"feasibility_tolerance" json:"feasibility_tolerance"`
	// OptimalityTolerance controls comparisons between solver objective values.
	OptimalityTolerance float64 `yaml:"optimality_tolerance" json:"optimality_tolerance"`
	// QuantileEpsilon represents strict "less than one half" in lower-median
	// constraints and must be smaller than 0.5.
	QuantileEpsilon float64 `yaml:"quantile_epsilon" json:"quantile_epsilon"`
	// ProfileTolerance is added to the final feasible Main-profile delta
	// before the relative Main-profile rows are locked for later stages.
	ProfileTolerance float64 `yaml:"profile_tolerance" json:"profile_tolerance"`
	// VisibilityTolerance is subtracted from final feasible dimensionless rho
	// values before fixed visibility rows are locked. It must never be interpreted
	// as an absolute bucket-probability allowance.
	VisibilityTolerance float64 `yaml:"visibility_tolerance" json:"visibility_tolerance"`
	// ProfileBisectionIterations is the fixed Main-profile probe budget for the common
	// worst relative Main-profile deviation delta.
	ProfileBisectionIterations int `yaml:"profile_bisection_iterations" json:"profile_bisection_iterations"`
	// OtherVisibilityBisectionIterations is the fixed Other-visibility probe budget for the
	// common relative Other-bucket visibility retention rho.
	OtherVisibilityBisectionIterations int `yaml:"other_visibility_bisection_iterations" json:"other_visibility_bisection_iterations"`
	// MainGroupInternalVisibilityBisectionIterations is the fixed probe budget
	// for the common relative visibility retention of supported siblings inside
	// every eligible Main Group.
	MainGroupInternalVisibilityBisectionIterations int `yaml:"main_group_internal_visibility_bisection_iterations" json:"main_group_internal_visibility_bisection_iterations"`
}

// ResolvedPlan is the immutable-by-convention, self-contained input to later
// stages. Config.ResolvePlan deep-copies all slices and optional values so a
// caller cannot accidentally alter other plans or the configuration repository.
type ResolvedPlan struct {
	Version       int           `json:"version"`
	Plan          RunPlan       `json:"plan"`
	Intent        MathIntent    `json:"intent"`
	EngineOptions EngineOptions `json:"engine_options"`
}

// RunRequest selects a configured plan and optionally applies explicit CLI or
// API overrides. Nil override fields mean "use the plan value". A BetMode
// override intentionally selects one mode, matching the existing cmd/opt CLI.
type RunRequest struct {
	PlanID    string       `json:"plan_id"`
	Overrides RunOverrides `json:"overrides,omitempty"`
}

// RunOverrides holds the small, auditable set of values that may be replaced at
// invocation time. Every applied override must be copied into the RunReport;
// it must never silently mutate the stored Config.
type RunOverrides struct {
	Game    *spec.GID `json:"game,omitempty"`
	BetMode *int      `json:"bet_mode,omitempty"`
	Seed    *int64    `json:"seed,omitempty"`
}

// Status is the stable, machine-readable final classification of one run.
// It answers what happened; DiagnosticCode separately explains why.
type Status string

const (
	StatusOptimal                  Status = "OPTIMAL"
	StatusInfeasibleConfig         Status = "INFEASIBLE_CONFIG"
	StatusInfeasibleSupport        Status = "INFEASIBLE_SUPPORT"
	StatusInfeasibleModel          Status = "INFEASIBLE_MODEL"
	StatusInfeasibleRepresentation Status = "INFEASIBLE_REPRESENTATION"
	StatusNumericalFailure         Status = "NUMERICAL_FAILURE"
	StatusArtifactInvalid          Status = "ARTIFACT_INVALID"
	StatusInternalError            Status = "INTERNAL_ERROR"
)

// Valid reports whether s is one of the versioned public run statuses. The zero
// value is intentionally invalid so an uninitialized result cannot look final.
func (s Status) Valid() bool {
	switch s {
	case StatusOptimal,
		StatusInfeasibleConfig,
		StatusInfeasibleSupport,
		StatusInfeasibleModel,
		StatusInfeasibleRepresentation,
		StatusNumericalFailure,
		StatusArtifactInvalid,
		StatusInternalError:
		return true
	default:
		return false
	}
}

// Success reports whether the selected mode was solved, materialized, verified,
// and durably staged. A successful mode can still be waiting for sibling modes
// before the writer publishes a complete runtime manifest.
func (s Status) Success() bool { return s == StatusOptimal }

// DiagnosticCode is a stable reason code shared by JSON and terminal reports.
// Contributors must use this closed set instead of inventing free-form strings.
type DiagnosticCode string

const (
	DiagnosticConfigInvalid                    DiagnosticCode = "ConfigInvalid"
	DiagnosticCollectionInsufficient           DiagnosticCode = "CollectionInsufficient"
	DiagnosticDuplicateReplayIdentity          DiagnosticCode = "DuplicateReplayIdentity"
	DiagnosticMeanSupportInfeasible            DiagnosticCode = "MeanSupportInfeasible"
	DiagnosticRiskCapacityInfeasible           DiagnosticCode = "RiskCapacityInfeasible"
	DiagnosticUniformClassRiskInfeasible       DiagnosticCode = "UniformClassRiskInfeasible"
	DiagnosticUniformClassMathInfeasible       DiagnosticCode = "UniformClassMathInfeasible"
	DiagnosticMainExperienceSupportInfeasible  DiagnosticCode = "MainExperienceSupportInfeasible"
	DiagnosticClassMeanInfeasible              DiagnosticCode = "ClassMeanInfeasible"
	DiagnosticMainProbabilityInfeasible        DiagnosticCode = "MainProbabilityInfeasible"
	DiagnosticMainGroupGuardrailInfeasible     DiagnosticCode = "MainGroupGuardrailInfeasible"
	DiagnosticMedianInfeasible                 DiagnosticCode = "MedianInfeasible"
	DiagnosticGlobalCVInfeasible               DiagnosticCode = "GlobalCVInfeasible"
	DiagnosticHardModelInfeasible              DiagnosticCode = "HardModelInfeasible"
	DiagnosticRepresentationInfeasible         DiagnosticCode = "RepresentationInfeasible"
	DiagnosticSolverNumericalFailure           DiagnosticCode = "SolverNumericalFailure"
	DiagnosticArtifactMaterializationViolation DiagnosticCode = "ArtifactMaterializationViolation"
	DiagnosticInternalModelError               DiagnosticCode = "InternalModelError"
)

// Representation identifies the mathematical decision space to which a result
// or diagnostic applies. It prevents bucket-level infeasibility from being
// overstated as proof about every possible raw per-outcome distribution.
type Representation string

const (
	// RepresentationAtomicBuckets means the engine controls atomic-bucket mass
	// and expands it uniformly over unique replayable outcomes in each bucket.
	RepresentationAtomicBuckets Representation = "configured_atomic_buckets_uniform_within_bucket"
)

// Bound records an inclusive numerical interval used in diagnostic requested
// versus achievable comparisons. A pointer to Bound is nil when a comparison
// is not meaningful for that diagnostic.
type Bound struct {
	Min float64 `json:"min"`
	Max float64 `json:"max"`
}

// Cause is one structured component of a diagnostic explanation. Metrics uses
// a slice rather than a map so report ordering is deterministic.
type Cause struct {
	Summary     string       `json:"summary"`
	SourcePaths []string     `json:"source_paths,omitempty"`
	Metrics     []NamedValue `json:"metrics,omitempty"`
}

// NamedValue attaches a stable name and optional unit to a diagnostic or report
// number without forcing renderers to parse a formatted sentence.
type NamedValue struct {
	Name  string  `json:"name"`
	Value float64 `json:"value"`
	Unit  string  `json:"unit,omitempty"`
}

// Diagnostic is the typed explanation for an expected run outcome. Constraint
// IDs and source paths preserve provenance without exposing backend row indexes
// as the only explanation available to users.
type Diagnostic struct {
	Code           DiagnosticCode `json:"code"`
	Status         Status         `json:"status"`
	Message        string         `json:"message"`
	SourcePaths    []string       `json:"source_paths,omitempty"`
	ConstraintIDs  []string       `json:"constraint_ids,omitempty"`
	Requested      *Bound         `json:"requested,omitempty"`
	Achievable     *Bound         `json:"achievable,omitempty"`
	Deficit        float64        `json:"deficit,omitempty"`
	Representation Representation `json:"representation,omitempty"`
	Causes         []Cause        `json:"causes,omitempty"`
}

// AdvisoryCode identifies a non-fatal static configuration topology risk. An
// advisory never relaxes or adds mathematical constraints; it exposes a fact
// that can change first-match collection ownership or leave a predicate-local
// payout interval uncovered.
type AdvisoryCode string

const (
	AdvisoryClassCollectionOverlap AdvisoryCode = "ClassCollectionOverlap"
	AdvisoryClassCollectionGap     AdvisoryCode = "ClassCollectionGap"
)

// Advisory is structured separately from Diagnostic because it never carries
// a Run Status and must not stop a valid configuration or optimization.
type Advisory struct {
	Code        AdvisoryCode `json:"code"`
	Message     string       `json:"message"`
	SourcePaths []string     `json:"source_paths,omitempty"`
}

// StopsRun reports whether this diagnostic carries a non-success final status.
// Diagnostics with a zero Status may be informational and do not stop a run.
func (d Diagnostic) StopsRun() bool {
	return d.Status.Valid() && d.Status != StatusOptimal
}

// Diagnostics preserves deterministic diagnostic order while providing a
// common stage-gate predicate for collection, preparation, solve, and verify.
type Diagnostics []Diagnostic

// StopsRun reports whether any diagnostic in declaration order terminates the
// current run. It does not choose or rewrite the run's final Status.
func (d Diagnostics) StopsRun() bool {
	for _, diagnostic := range d {
		if diagnostic.StopsRun() {
			return true
		}
	}
	return false
}

// RunResult is the complete value result of Tuner.Run. Expected infeasibility
// still returns a populated RunResult and a nil Go error. ArtifactPaths points
// to verified pending-mode files or, once complete, the published manifest and
// bundle payloads described by Report.Publication.
type RunResult struct {
	Status        Status      `json:"status"`
	Diagnostics   Diagnostics `json:"diagnostics,omitempty"`
	Report        RunReport   `json:"report"`
	ArtifactPaths []string    `json:"artifact_paths,omitempty"`
}

// Succeeded reports whether r represents a verified, durably staged optimal
// result for its one selected mode.
func (r RunResult) Succeeded() bool { return r.Status.Success() }

// RunReport captures deterministic inputs, numerical provenance, intent
// quality, verification, and hashes. Wall-clock measurements are included for
// operations but are deliberately excluded from solution hashing.
type RunReport struct {
	Plan                  ResolvedPlan              `json:"resolved_plan"`
	ExpectedRTP           float64                   `json:"expected_rtp"`
	Overrides             RunOverrides              `json:"overrides,omitempty"`
	ConfigHash            string                    `json:"config_hash"`
	Engine                EngineProvenance          `json:"engine"`
	Solver                SolverProvenance          `json:"solver"`
	StableOrderingVersion string                    `json:"stable_ordering_version"`
	Representation        Representation            `json:"representation"`
	Advisories            []Advisory                `json:"advisories,omitempty"`
	Stages                []StageDuration           `json:"stages,omitempty"`
	OptimizationStages    []OptimizationStageReport `json:"optimization_stages,omitempty"`
	Modes                 []ModeRunReport           `json:"modes,omitempty"`
	Candidates            CandidateReport           `json:"candidates"`
	Intent                IntentQualityReport       `json:"intent"`
	Verification          VerificationReport        `json:"verification"`
	Publication           *PublicationReport        `json:"publication,omitempty"`
	ModelHash             string                    `json:"model_hash,omitempty"`
	SolutionHash          string                    `json:"solution_hash,omitempty"`
	ArtifactHash          string                    `json:"artifact_hash,omitempty"`
}

// ModeRunReport preserves the selected mode's collection and solution evidence.
// The slice shape remains stable for report consumers, but a canonical v2 Run
// now contains exactly one entry.
type ModeRunReport struct {
	BetMode      int                      `json:"bet_mode"`
	BetUnit      int                      `json:"bet_unit"`
	Spins        uint64                   `json:"spins"`
	Solver       SolverProvenance         `json:"solver"`
	Intent       IntentQualityReport      `json:"intent"`
	Verification VerificationReport       `json:"verification"`
	Distribution BucketDistributionReport `json:"distribution"`
	ModelHash    string                   `json:"model_hash"`
	SolutionHash string                   `json:"solution_hash"`
}

// BucketDistributionReport exposes the verified runtime probability shape for
// one mode. CollisionProbability is the fixed display threshold used to derive
// DrawsAtCollisionProbability from actual alias marginals.
type BucketDistributionReport struct {
	BetMode              int                       `json:"bet_mode"`
	CollisionProbability float64                   `json:"collision_probability"`
	Classes              []ClassDistributionReport `json:"classes"`
}

// ClassDistributionReport retains declaration order and distinguishes the
// Class's fixed unconditional probability from conditional Bucket masses.
type ClassDistributionReport struct {
	Class       string                    `json:"class"`
	Probability float64                   `json:"probability"`
	Buckets     []BucketProbabilityReport `json:"buckets"`
}

// BucketProbabilityReport describes one configured atomic interval (or the
// single empirical-uniform interval). Seed probabilities are unconditional
// probabilities per game draw and use a min/max range because alias-table
// approximation can introduce tiny, verified differences between outcomes.
// DrawsAtCollisionProbability is zero only when the Bucket has zero runtime
// probability; otherwise it is the first whole draw count whose birthday/
// Poisson approximation reaches the report's CollisionProbability.
type BucketProbabilityReport struct {
	Index                       int     `json:"index"`
	Lower                       float64 `json:"lower"`
	Upper                       float64 `json:"upper"`
	UpperInclusive              bool    `json:"upper_inclusive"`
	SeedCount                   int     `json:"seed_count"`
	ConditionalProbability      float64 `json:"conditional_probability"`
	UnconditionalProbability    float64 `json:"unconditional_probability"`
	SeedProbabilityMin          float64 `json:"seed_probability_min"`
	SeedProbabilityMax          float64 `json:"seed_probability_max"`
	DrawsAtCollisionProbability float64 `json:"draws_at_collision_probability,omitempty"`
}

// PublicationState distinguishes a valid staged mode from newly published
// all-mode output packages without overloading mathematical Run Status.
type PublicationState string

const (
	// PublicationModeStaged means this mode is durable but at least one sibling
	// mode is still missing, so no new manifest was exposed.
	PublicationModeStaged PublicationState = "MODE_STAGED"
	// PublicationManifestPublished means all staged modes were compatible, the
	// complete bundle passed production-loader verification, and manifest.json
	// became visible in one atomic directory replacement.
	PublicationManifestPublished PublicationState = "MANIFEST_PUBLISHED"
	// PublicationOutputsPublished is the equivalent terminal state when all
	// requested formats are complete but none of them has a manifest.
	PublicationOutputsPublished PublicationState = "OUTPUTS_PUBLISHED"
)

// PublicationReport makes cross-run assembly explicit in JSON results. Formats
// preserve config declaration order; Staged and Missing mode lists are ordered
// numerically. ManifestPath exists only when completed output includes Artifact
// v1, while ArtifactHash identifies any completed format set.
type PublicationReport struct {
	State            PublicationState `json:"state"`
	Formats          []OutputFormat   `json:"formats"`
	BetMode          int              `json:"bet_mode"`
	ExpectedModes    int              `json:"expected_modes"`
	StagedModes      []int            `json:"staged_modes"`
	MissingModes     []int            `json:"missing_modes,omitempty"`
	StagingDirectory string           `json:"staging_directory"`
	ManifestPath     string           `json:"manifest_path,omitempty"`
}

// EngineProvenance identifies the selected engine implementation independently
// of its stable EngineName routing key.
type EngineProvenance struct {
	Name           EngineName `json:"name"`
	Version        string     `json:"version"`
	SemanticAxioms []string   `json:"semantic_axioms"`
}

// SolverProvenance identifies the numerical backend and the canonical model
// transformation/scaling contract used before invoking it.
type SolverProvenance struct {
	Objective         string  `json:"objective,omitempty"`
	Backend           string  `json:"backend"`
	BackendVersion    string  `json:"backend_version"`
	BackendCode       string  `json:"backend_code"`
	TransformVersion  string  `json:"transform_version"`
	SemanticColumns   int     `json:"semantic_columns"`
	AuxiliaryColumns  int     `json:"auxiliary_columns"`
	Rows              int     `json:"rows"`
	MaxRowViolation   float64 `json:"max_row_violation"`
	MaxBoundViolation float64 `json:"max_bound_violation"`
}

// StageDuration records operational elapsed time for one named stage. Duration
// does not participate in model, candidate, solution, or artifact hashes.
type StageDuration struct {
	Stage    string        `json:"stage"`
	Duration time.Duration `json:"duration"`
}

// OptimizationStageReport records one terminal semantic optimization substage.
// It is operational provenance and never participates in deterministic hashes.
type OptimizationStageReport struct {
	Parent     string              `json:"parent"`
	Stage      OptimizationStageID `json:"stage"`
	BetMode    int                 `json:"bet_mode"`
	State      string              `json:"state"`
	Objective  string              `json:"objective,omitempty"`
	Metric     string              `json:"metric,omitempty"`
	Probes     int                 `json:"probes,omitempty"`
	Lower      *float64            `json:"lower,omitempty"`
	Upper      *float64            `json:"upper,omitempty"`
	FixedValue *float64            `json:"fixed_value,omitempty"`
	Duration   time.Duration       `json:"duration"`
}

// OptimizationStageEvent is the engine-to-application observation for one
// semantic optimization substage. Operational timing and progress never affect
// the mathematical model, solution identity, or artifact hashes.
type OptimizationStageEvent struct {
	Stage      OptimizationStageID
	State      string
	Objective  string
	Metric     string
	Probe      int
	Lower      *float64
	Upper      *float64
	FixedValue *float64
	Status     string
	Message    string
	Duration   time.Duration
}

// OptimizationStageObserver receives synchronous, declaration-ordered engine
// substage events. A nil observer disables operational reporting.
type OptimizationStageObserver func(OptimizationStageEvent)

// CandidateReport summarizes bounded candidate generation and deterministic
// reduction without claiming that canonical LP tie-breaking is player-optimal.
type CandidateReport struct {
	Budget                     int    `json:"budget"`
	Generated                  int    `json:"generated"`
	Evaluated                  int    `json:"evaluated"`
	CanonicalOnly              bool   `json:"canonical_only"`
	PlayerExperienceOptimality string `json:"player_experience_optimality"`
}

// IntentQualityReport records semantic optimization results and the class-level
// intent measurements replayed from the final primary bucket vector.
type IntentQualityReport struct {
	MainProfileOptimization                 BisectionReport        `json:"main_profile_optimization"`
	OtherBucketVisibilityOptimization       BisectionReport        `json:"other_bucket_visibility_optimization"`
	MainGroupInternalVisibilityOptimization BisectionReport        `json:"main_group_internal_visibility_optimization"`
	CanonicalBucketProbabilitySelection     CanonicalizationReport `json:"canonical_bucket_probability_selection"`
	Classes                                 []ClassIntentReport    `json:"classes,omitempty"`

	// Deprecated: use MainProfileOptimization.
	PhaseA BisectionReport `json:"phase_a_profile"`
	// Deprecated: use OtherBucketVisibilityOptimization.
	PhaseB BisectionReport `json:"phase_b_other_visibility"`
}

// BisectionReport records the final proved bracket of one semantic optimization.
type BisectionReport struct {
	Applicable         bool    `json:"applicable"`
	InapplicableReason string  `json:"inapplicable_reason,omitempty"`
	Objective          string  `json:"objective"`
	Metric             string  `json:"metric"`
	Direction          string  `json:"direction"`
	Lower              float64 `json:"lower"`
	Upper              float64 `json:"upper"`
	FixedValue         float64 `json:"fixed_value"`
	Probes             int     `json:"probes"`
}

// CanonicalizationReport describes deterministic tie-breaking after every
// higher-priority mathematical and neutral-preference lock is installed.
type CanonicalizationReport struct {
	Objective        string `json:"objective"`
	Metric           string `json:"metric"`
	Direction        string `json:"direction"`
	PrimaryVariables int    `json:"primary_variables"`
	Solves           int    `json:"solves"`
}

// ClassIntentReport describes group-level Main profile quality and supported
// bucket visibility. It distinguishes a system-neutral internal visibility
// floor from any Designer-authored Main Group shape.
type ClassIntentReport struct {
	Class                     string                           `json:"class"`
	MainTotal                 float64                          `json:"main_total"`
	WantedMainProfile         []float64                        `json:"wanted_main_profile,omitempty"`
	ActualMainProfile         []float64                        `json:"actual_main_profile,omitempty"`
	MainRelativeDeviation     float64                          `json:"main_relative_deviation"`
	MainProfileRetention      float64                          `json:"main_profile_retention"`
	OtherVisibility           OtherVisibilityReport            `json:"other_visibility"`
	MainGroupVisibility       []MainGroupVisibilityReport      `json:"main_group_visibility,omitempty"`
	RemainingDegreesOfFreedom []RemainingDegreeOfFreedomReport `json:"remaining_degrees_of_freedom,omitempty"`
	// Deprecated: use RemainingDegreesOfFreedom and select only state=unconstrained.
	UnconstrainedDimensions []string `json:"unconstrained_dimensions,omitempty"`
}

// MainGroupVisibilityReport measures the selected primary vector inside one
// configured Main Group without treating unsupported buckets as denominator mass.
type MainGroupVisibilityReport struct {
	GroupIndex          int                               `json:"group_index"`
	Applicable          bool                              `json:"applicable"`
	InapplicableReason  string                            `json:"inapplicable_reason,omitempty"`
	SupportedCount      int                               `json:"supported_count"`
	GroupTotal          float64                           `json:"group_total,omitempty"`
	PerfectUniformShare float64                           `json:"perfect_uniform_share,omitempty"`
	MinimumShare        float64                           `json:"minimum_share,omitempty"`
	Retention           float64                           `json:"retention,omitempty"`
	Buckets             []MainGroupBucketVisibilityReport `json:"buckets,omitempty"`
}

// MainGroupBucketVisibilityReport preserves configured order and explicitly
// distinguishes unsupported replay buckets from supported buckets with zero mass.
type MainGroupBucketVisibilityReport struct {
	Index         int     `json:"index"`
	Mass          float64 `json:"mass"`
	RelativeShare float64 `json:"relative_share,omitempty"`
	Supported     bool    `json:"supported"`
}

// RemainingDegreeOfFreedomReport describes what a neutral visibility lock does
// and does not determine about one Main Group's internal atomic shape.
type RemainingDegreeOfFreedomReport struct {
	Path       string `json:"path"`
	State      string `json:"state"`
	Constraint string `json:"constraint,omitempty"`
}

// OtherVisibilityReport is inapplicable when a class has no supported Other
// buckets. Ratios must not be fabricated when Applicable is false.
type OtherVisibilityReport struct {
	Applicable                bool                `json:"applicable"`
	OtherTotal                float64             `json:"other_total,omitempty"`
	ClassRetention            float64             `json:"class_retention,omitempty"`
	PerfectUniformShare       float64             `json:"perfect_uniform_share,omitempty"`
	UniformityRetentionReport float64             `json:"uniformity_retention_report_only,omitempty"`
	Buckets                   []OtherBucketReport `json:"buckets,omitempty"`
}

// OtherBucketReport gives both conditional class mass and normalized share of
// the class's Other total so the scale-independent visibility meaning is auditable.
type OtherBucketReport struct {
	Index         int     `json:"index"`
	Mass          float64 `json:"mass"`
	RelativeShare float64 `json:"relative_share"`
	RiskCap       float64 `json:"risk_cap,omitempty"`
}

// VerificationReport summarizes semantic replay of the selected solution and
// the materialized runtime artifact.
type VerificationReport struct {
	Pass   bool                `json:"pass"`
	Checks []VerificationCheck `json:"checks,omitempty"`
}

// VerificationCheck is one stable, machine-readable replay assertion. Actual
// and Required stay textual because checks may compare hashes or statuses as
// well as floating-point values; Tolerance is populated for numerical checks.
type VerificationCheck struct {
	Name      string  `json:"name"`
	Pass      bool    `json:"pass"`
	Actual    string  `json:"actual"`
	Required  string  `json:"required"`
	Tolerance float64 `json:"tolerance,omitempty"`
}
