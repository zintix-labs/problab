// Copyright 2026 Zintix Labs
// SPDX-License-Identifier: Apache-2.0

package v2

import (
	"github.com/zintix-labs/problab/sdk/tag"
)

// runTags follows the collected result through Prepare/Compile and every replay
// verifier. Neither a later Run nor the legacy registry can change its meaning.
type runTags struct {
	tagger     *tag.BitSet
	predicates []classPredicate
}

func newCollectionTagRegistry(custom map[string]tag.IsTag) (*tag.Registry, error) {
	return tag.NewRegistry(custom)
}

func (p PreparedProblem) replayTags() (*tag.BitSet, []classPredicate, error) {
	if p.tags != nil {
		return p.tags.tagger, p.tags.predicates, nil
	}
	// Hand-built mathematical fixtures may not come from Collector. They can
	// use no tag filters; named tags without run context fail closed, never use globals.
	r, err := newCollectionTagRegistry(nil)
	if err != nil {
		return nil, nil, err
	}
	return compileTagPredicates(p.Plan.Intent.Classes, r)
}
