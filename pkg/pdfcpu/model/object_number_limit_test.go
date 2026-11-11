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

func TestResourceLimitsObjectNumberChecks(t *testing.T) {
	on := ResourceLimits{MaxObjectNumber: 10}
	for _, tc := range []struct {
		name  string
		l     ResourceLimits
		check func(ResourceLimits) error
		value int64 // 0: no error expected
	}{
		{"number at limit", on, func(l ResourceLimits) error { return l.CheckObjectNumber(10) }, 0},
		{"number over limit", on, func(l ResourceLimits) error { return l.CheckObjectNumber(11) }, 11},
		{"zero limit is off", ResourceLimits{}, func(l ResourceLimits) error { return l.CheckObjectNumber(math.MaxInt64) }, 0},
		{"negative limit is off", ResourceLimits{MaxObjectNumber: -1}, func(l ResourceLimits) error { return l.CheckObjectNumber(11) }, 0},
		{"range ending at limit", on, func(l ResourceLimits) error { return l.CheckObjectRange(5, 6) }, 0},
		{"range over limit", on, func(l ResourceLimits) error { return l.CheckObjectRange(5, 7) }, 11},
		{"empty range", on, func(l ResourceLimits) error { return l.CheckObjectRange(100, 0) }, 0},
		{"overflowing range", on, func(l ResourceLimits) error { return l.CheckObjectRange(5, math.MaxInt64) }, math.MaxInt64},
		{"negative start range under limit", on, func(l ResourceLimits) error { return l.CheckObjectRange(-5, 3) }, 0},
		{"negative start overflowing range", on, func(l ResourceLimits) error { return l.CheckObjectRange(-5, math.MaxInt64) }, math.MaxInt64 - 6},
		{"xref size at limit", on, func(l ResourceLimits) error { return l.CheckXRefSize(11) }, 0},
		{"xref size over limit", on, func(l ResourceLimits) error { return l.CheckXRefSize(12) }, 11},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.check(tc.l)
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
			want := ResourceLimitError{Kind: LimitObjectNumber, Limit: 10, Value: tc.value}
			if *limitErr != want {
				t.Fatalf("got %+v, want %+v", *limitErr, want)
			}
		})
	}
}
