// Copyright 2026 Zintix Labs
// SPDX-License-Identifier: Apache-2.0

package dto

import "encoding/json"

// ResultConverter encodes one replayed result. The input is borrowed until the
// call returns; retain it only after copying mutable data. Implementations own
// their platform schema, including payout semantics and removal of PRNG state.
type ResultConverter func(SpinResult) (json.RawMessage, error)

// IdentityConverter is the default RGS converter. It includes the DTO's start
// and after PRNG snapshots and checkpoint: do not disclose it to parties that
// should not receive the underlying replay material.
func IdentityConverter(r SpinResult) (json.RawMessage, error) {
	return json.Marshal(r)
}
