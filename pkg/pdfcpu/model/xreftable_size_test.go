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
	"slices"
	"strings"
	"testing"
)

func sizeTestXRefTable(size int, objNrs ...int) *XRefTable {
	xRefTable := newXRefTable(NewDefaultConfiguration())
	xRefTable.Size = &size
	for _, objNr := range objNrs {
		xRefTable.Table[objNr] = NewXRefTableEntryGen0(nil)
	}
	return xRefTable
}

// TestMissingObjects lists the object numbers below Size that have no table entry.
func TestMissingObjects(t *testing.T) {
	for _, tc := range []struct {
		name      string
		size      int
		objNrs    []int
		wantCount int
		want      string
	}{
		{"none missing", 3, []int{0, 1, 2}, 0, ""},
		{"gaps and tail", 7, []int{0, 1, 3, 5}, 3, "2,4,6"},
		{"head", 3, []int{2}, 2, "0,1"},
		{"entries beyond Size ignored", 3, []int{0, 1, 2, 9}, 0, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			count, s := sizeTestXRefTable(tc.size, tc.objNrs...).MissingObjects()
			got := ""
			if s != nil {
				got = *s
			}
			if count != tc.wantCount || got != tc.want {
				t.Fatalf("got %d %q, want %d %q", count, got, tc.wantCount, tc.want)
			}
		})
	}
}

// TestMissingObjectsSparseTable keeps the cost of MissingObjects bounded by the table, not by Size.
// A trailer Size of 2^31 for a file with a handful of objects used to build one string per missing number.
func TestMissingObjectsSparseTable(t *testing.T) {
	const size = 1 << 20
	xRefTable := sizeTestXRefTable(size, 0, 1, 2, size-1)
	var count int
	var s *string
	allocs := testing.AllocsPerRun(1, func() {
		count, s = xRefTable.MissingObjects()
	})
	if count != size-4 {
		t.Fatalf("count: got %d, want %d", count, size-4)
	}
	if s == nil || !strings.HasPrefix(*s, "3,4,5,") || !strings.HasSuffix(*s, ",...") {
		t.Fatalf("missing objects string not truncated")
	}
	if n := strings.Count(*s, ","); n != maxListedMissingObjects {
		t.Fatalf("listed: got %d separators, want %d", n, maxListedMissingObjects)
	}
	if allocs > 10*maxListedMissingObjects {
		t.Fatalf("allocations: got %.0f, want at most %d", allocs, 10*maxListedMissingObjects)
	}
}

// TestSortedObjNrsBelowSize returns the table keys below Size in ascending order.
func TestSortedObjNrsBelowSize(t *testing.T) {
	got := sizeTestXRefTable(10, 9, 0, 4, 10, 2147483647).SortedObjNrsBelowSize()
	if want := []int{0, 4, 9}; !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}
