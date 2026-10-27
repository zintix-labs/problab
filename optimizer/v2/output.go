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
	"context"
	"fmt"
	"path/filepath"

	"github.com/zintix-labs/problab/spec"
)

// newOutputPublisher is the only format-routing boundary. optimal_gacha is the
// only native format: every verified mode becomes one gacha pair published
// beneath <output.directory>/gacha. Publication consumes the same replay-verified
// MaterializedMode, so it cannot change collection, LP solving, alias
// construction, or semantic verification.
func newOutputPublisher(output OutputOptions) ArtifactPublisher {
	for _, format := range output.Format {
		if format == OutputOptimalGacha {
			return GachaArtifactWriter{Directory: filepath.Join(output.Directory, "gacha")}
		}
	}
	return noNativeOutput{}
}

// noNativeOutput rejects publication when the plan requests no native format.
type noNativeOutput struct{}

func (noNativeOutput) PublishMode(context.Context, spec.GID, string, []int, MaterializedMode) (PublishedArtifact, error) {
	return PublishedArtifact{}, fmt.Errorf("publish mode: no native output format configured")
}
