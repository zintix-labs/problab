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

package dto

import (
	"encoding/json"
	"reflect"

	"github.com/zintix-labs/problab/errs"
	"github.com/zintix-labs/problab/spec"
)

type extrender struct {
	typeName string
	rend     func(any) any
}

var extendRenders = map[spec.LogicKey]extrender{}

// RegisterExtendRender 註冊遊戲 Extend 結果解析函數。
//
// IMPORTANT:
// - T must be the NON-pointer struct type of the extend payload.
// - Engine/buf may carry extend as `*T` (preferred; allows nil) or `T`.
// - This registry is DTO-boundary only (type assertion for JSON output).
func RegisterExtendRender[T any](lkey spec.LogicKey) error {
	rt := reflect.TypeOf((*T)(nil)).Elem()
	if rt.Kind() == reflect.Ptr {
		return errs.NewFatal("RegisterExtendRender: T must be a non-pointer struct type")
	}
	if rt.Kind() != reflect.Struct {
		return errs.NewFatal("RegisterExtendRender: T must be a struct type")
	}
	name := rt.PkgPath() + "." + rt.Name()
	// Duplicate-register: allow only if the type matches.
	if e, ok := extendRenders[lkey]; ok {
		if e.typeName != name {
			return errs.NewFatal("duplicate register err: extend type mismatch")
		}
		return nil
	}

	extendRenders[lkey] = extrender{
		typeName: name,
		rend: func(v any) any {
			if v == nil {
				return nil
			}
			if val, ok := v.(*T); ok {
				return val
			}
			if val, ok := v.(T); ok {
				return val
			}
			return v
		},
	}
	return nil
}

func renderExtendResult(lkey spec.LogicKey, v any) any {
	if er, ok := extendRenders[lkey]; ok {
		return er.rend(v)
	}
	return v
}

var cpCodecs = map[spec.LogicKey]codec{}

type codec struct {
	// typeName is used only for duplicate-register diagnostics.
	typeName string
	dec      func(json.RawMessage) (any, error)
	enc      func(any) (json.RawMessage, error)
}

// RegisterCheckpoint registers checkpoint (cp) codec for a given logic.
//
// IMPORTANT:
// - T must be the NON-pointer struct type of the checkpoint.
// - The engine/buf layer should carry the typed checkpoint as `*T` (or nil).
// - DTO boundary will decode/encode between `json.RawMessage` and `*T`.
//
// Example:
//
//	dto.RegisterCheckpoint[MyCheckpoint](spec.LogicFoo)
func RegisterCheckpoint[T any](lkey spec.LogicKey) error {
	// Registration-time validation only (NOT on the hot path).
	// If user accidentally passes a pointer type, reject it to keep allocation fast.
	rt := reflect.TypeOf((*T)(nil)).Elem()
	if rt.Kind() == reflect.Ptr {
		return errs.NewFatal("register failed: T must be a non-pointer struct type")
	}
	if rt.Kind() != reflect.Struct {
		return errs.NewFatal("register failed: T must be a struct type")
	}
	name := rt.PkgPath() + "." + rt.Name()

	// Duplicate-register: allow only if the type matches.
	if c, ok := cpCodecs[lkey]; ok {
		if c.typeName != name {
			return errs.NewFatal("duplicate register err: checkpoint type mismatch")
		}
		return nil
	}

	cpCodecs[lkey] = codec{
		typeName: name,
		dec: func(raw json.RawMessage) (any, error) {
			if len(raw) == 0 {
				return nil, nil
			}
			v := new(T) // *T, no reflection
			if err := json.Unmarshal(raw, v); err != nil {
				return nil, err
			}
			return v, nil
		},
		enc: func(v any) (json.RawMessage, error) {
			if v == nil {
				return nil, nil
			}
			vt, ok := v.(*T)
			if !ok {
				return nil, errs.NewWarn("checkpoint type mismatch")
			}
			b, err := json.Marshal(vt)
			if err != nil {
				return nil, err
			}
			return b, nil
		},
	}
	return nil
}

// DecodeCheckpoint decodes dto cp (json.RawMessage) into a typed checkpoint pointer (*T) registered by logic key.
// Returns (nil, nil) if data is empty.
func DecodeCheckpoint(key spec.LogicKey, data json.RawMessage) (any, error) {
	codecer, ok := cpCodecs[key]
	if !ok {
		return nil, errs.NewWarn("checkpoint type does not exist")
	}
	return codecer.dec(data)
}

// EncodeCheckpoint encodes a typed checkpoint pointer (*T) into dto cp (json.RawMessage) using the registered logic key.
// Returns (nil, nil) if checkpoint is nil.
func EncodeCheckpoint(key spec.LogicKey, checkpoint any) (json.RawMessage, error) {
	codecer, ok := cpCodecs[key]
	if !ok {
		return nil, errs.NewWarn("checkpoint type does not exist")
	}
	return codecer.enc(checkpoint)
}
