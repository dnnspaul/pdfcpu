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

package types

import (
	"fmt"
	"testing"
	"time"
)

// dictCloneTestDict returns a dict with n keys: plain names, nested dicts and arrays every
// 100th key, and nil values every 1000th key.
func dictCloneTestDict(n int) Dict {
	d := make(Dict, n)
	for i := range n {
		k := fmt.Sprintf("F%d", i)
		switch {
		case i%1000 == 999:
			d[k] = nil
		case i%100 == 99:
			d[k] = Dict{"Name": Name("nested"), "Width": Integer(i)}
		default:
			d[k] = Integer(i)
		}
	}
	return d
}

// TestDictCloneKeepsAllKeys pins that Clone keeps every key and value, including a name and its
// #-escaped twin, which both decode to F1.
func TestDictCloneKeepsAllKeys(t *testing.T) {
	src := Dict{
		"A":       Integer(1),
		"B":       nil,
		"C#28#29": Name("encoded"),
		"C()":     Name("decoded"),
		"F#31":    Integer(2),
		"F1":      Integer(3),
		"Arr":     Array{Integer(4), Dict{"X": Integer(5)}},
		"Sub":     Dict{"Y": Name("z")},
	}
	got, ok := src.Clone().(Dict)
	if !ok {
		t.Fatalf("Clone returned %T, want Dict", src.Clone())
	}
	if len(got) != len(src) {
		t.Fatalf("clone has %d keys, want %d: %v", len(got), len(src), got)
	}
	for k, want := range src {
		v, found := got[k]
		if !found {
			t.Errorf("key %q missing in clone", k)
			continue
		}
		if want == nil {
			if v != nil {
				t.Errorf("key %q: got %v, want nil", k, v)
			}
			continue
		}
		if v == nil || v.String() != want.String() {
			t.Errorf("key %q: got %v, want %v", k, v, want)
		}
	}
	if !got.HasEntry("F1") || !got.HasEntry("F#31") || !got.HasEntry("C()") || !got.HasEntry("C#28#29") {
		t.Errorf("twin or encoded keys not found in clone: %v", got)
	}
}

// TestDictCloneIsDeep pins that mutating the clone, at any level, does not change the source.
func TestDictCloneIsDeep(t *testing.T) {
	src := Dict{
		"Arr": Array{Integer(1), Dict{"X": Integer(2)}},
		"Sub": Dict{"Y": Integer(3)},
	}
	got := src.Clone().(Dict)

	got["Top"] = Integer(4)
	got["Sub"].(Dict)["Y"] = Integer(5)
	got["Sub"].(Dict)["New"] = Integer(6)
	got["Arr"].(Array)[0] = Integer(7)
	got["Arr"].(Array)[1].(Dict)["X"] = Integer(8)
	delete(got, "Arr")

	if _, found := src["Top"]; found {
		t.Error("top level insert leaked into source")
	}
	if v := src["Sub"].(Dict)["Y"]; v != Integer(3) {
		t.Errorf("nested dict value changed in source: %v", v)
	}
	if _, found := src["Sub"].(Dict)["New"]; found {
		t.Error("nested dict insert leaked into source")
	}
	arr := src["Arr"].(Array)
	if arr[0] != Integer(1) || arr[1].(Dict)["X"] != Integer(2) {
		t.Errorf("array changed in source: %v", arr)
	}
}

// TestDictCloneLargeDict pins that Clone of 80k keys finishes well within a second. The former
// Insert based copy took about 38 s for this dict, so the bound catches a return to quadratic
// cost and leaves a wide margin for slow or race-instrumented runs.
func TestDictCloneLargeDict(t *testing.T) {
	if testing.Short() {
		t.Skip("timing test")
	}
	d := dictCloneTestDict(80000)
	start := time.Now()
	_ = d.Clone()
	if el := time.Since(start); el > time.Second {
		t.Errorf("Clone of 80k keys took %v, want well under 1s", el)
	}
}

// BenchmarkDictClone measures Clone of dicts with 20k and 80k keys.
func BenchmarkDictClone(b *testing.B) {
	for _, n := range []int{20000, 80000} {
		d := dictCloneTestDict(n)
		b.Run(fmt.Sprintf("keys=%d", n), func(b *testing.B) {
			b.ReportAllocs()
			for range b.N {
				_ = d.Clone()
			}
		})
	}
}
