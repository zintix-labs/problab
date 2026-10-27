// Copyright 2026 Zintix Labs
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

package builtin

// Choose is a built-in way to choose at a choice point. Its zero value is unset:
// no choice is made.
type Choose uint8

const (
	ChooseUnset Choose = iota
	ChooseFirst        // position 0
	ChooseLast         // the last position
	ChooseIter         // positions in turn, see Selector.Pick
)

func (c Choose) valid() bool { return c <= ChooseIter }

// Selector picks positions for one player. It keeps that player's state (the
// ChooseIter counter), so every player's Strategy must own its own Selector; it
// must not live in a shared Factory. The zero value is ChooseUnset.
type Selector struct {
	Mode Choose
	next int // ChooseIter: the position to take next
}

// Pick returns a position in options, which must not be empty, or false when
// Mode is ChooseUnset.
//
// ChooseIter takes position 0 without moving the counter when there is a
// single option. Otherwise it takes the counter's position, or 0 when the
// counter is past the options, and moves the counter to the position after the
// one taken. All choice points with more than one option share the counter, so
// with differing option counts it does not promise equal use of each entry.
func (s *Selector) Pick(options []int) (int, bool) {
	switch s.Mode {
	case ChooseFirst:
		return 0, true
	case ChooseLast:
		return len(options) - 1, true
	case ChooseIter:
		if len(options) == 1 {
			return 0, true
		}
		i := s.next
		if i >= len(options) {
			i = 0
		}
		s.next = i + 1
		return i, true
	}
	return 0, false
}
