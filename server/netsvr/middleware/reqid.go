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

package middleware

import (
	"net/http"
	"strings"

	chimid "github.com/go-chi/chi/v5/middleware"
)

func RequestID(next http.Handler) http.Handler {
	return chimid.RequestID(next)
}

func GetReqId(r *http.Request) string {
	return chimid.GetReqID(r.Context())
}

func GetReqIdNumPart(r *http.Request) string {
	str := chimid.GetReqID(r.Context())
	if len(str) == 0 {
		return ""
	}
	i := strings.LastIndex(str, "-")
	if i < 0 || i+1 >= len(str) {
		return str
	}
	return str[i+1:]
}
