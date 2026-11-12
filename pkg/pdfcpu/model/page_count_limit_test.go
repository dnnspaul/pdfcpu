/*
Copyright 2026 The pdfcpu Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

	http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/
package model

import (
	"errors"
	"math"
	"testing"
)

// TestResourceLimitsCheckPageCount pins the page count check: a count at the limit passes, one past
// it is a LimitPages ResourceLimitError, and a zero or negative limit or a missing configuration is off.
func TestResourceLimitsCheckPageCount(t *testing.T) {
	on := ResourceLimits{MaxPageCount: 10}
	for _, tc := range []struct {
		name  string
		check func() error
		value int64 // 0: no error expected
	}{
		{"at limit", func() error { return on.CheckPageCount(10) }, 0},
		{"over limit", func() error { return on.CheckPageCount(11) }, 11},
		{"zero limit is off", func() error { return ResourceLimits{}.CheckPageCount(math.MaxInt) }, 0},
		{"negative limit is off", func() error { return ResourceLimits{MaxPageCount: -1}.CheckPageCount(11) }, 0},
		{"xref table over limit", func() error {
			return (&XRefTable{Conf: &Configuration{Limits: on}}).CheckPageCount(11)
		}, 11},
		{"xref table without configuration", func() error { return (&XRefTable{}).CheckPageCount(math.MaxInt) }, 0},
		{"nil xref table", func() error { return (*XRefTable)(nil).CheckPageCount(math.MaxInt) }, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.check()
			if tc.value == 0 {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			var limitErr *ResourceLimitError
			if !errors.As(err, &limitErr) || !errors.Is(err, ErrResourceLimit) {
				t.Fatalf("got %v, want ResourceLimitError", err)
			}
			want := ResourceLimitError{Kind: LimitPages, Limit: 10, Value: tc.value}
			if *limitErr != want {
				t.Fatalf("got %+v, want %+v", *limitErr, want)
			}
			if got := err.Error(); got != "page count limit 10 exceeded: 11" {
				t.Fatalf("got message %q", got)
			}
		})
	}
}
