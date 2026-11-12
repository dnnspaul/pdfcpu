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
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

// filteredCopyLeaf returns a name tree leaf holding keys, each with the value [k].
func filteredCopyLeaf(keys ...string) *Node {
	n := &Node{Kmin: keys[0], Kmax: keys[len(keys)-1], D: types.Dict{"Names": types.Array{}}}
	for _, k := range keys {
		n.Names = append(n.Names, entry{k: k, v: types.Array{types.StringLiteral(k)}})
	}
	return n
}

// filteredCopyTree returns the name tree [[a b] [c] [[d] [e]]]; key b has raw PDF bytes that differ from its text.
func filteredCopyTree() *Node {
	ab, c, d, e := filteredCopyLeaf("a", "b"), filteredCopyLeaf("c"), filteredCopyLeaf("d"), filteredCopyLeaf("e")
	raw := "b\x00"
	ab.Names[1].raw = &raw
	de := &Node{Kids: []*Node{d, e}, Kmin: "d", Kmax: "e", D: types.Dict{"Kids": types.Array{}}}
	return &Node{Kids: []*Node{ab, c, de}, Kmin: "a", Kmax: "e", D: types.Dict{"Kids": types.Array{}}}
}

// TestNameTreeFilteredCopy pins that FilteredCopy keeps the selected entries with the values keep returns,
// their key bytes and the node structure without emptied nodes, gives every node a new dict, and leaves the
// source tree unchanged.
func TestNameTreeFilteredCopy(t *testing.T) {
	src := filteredCopyTree()
	want := filteredCopyTree()
	var seen []string
	cp, err := src.FilteredCopy(context.Background(), func(k string, v types.Object) (types.Object, bool, error) {
		seen = append(seen, k)
		if k != "b" && k != "e" {
			return nil, false, nil
		}
		return types.Integer(len(seen)), true, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(src, want) {
		t.Fatalf("source tree changed: %s", src)
	}
	if !reflect.DeepEqual(seen, []string{"a", "b", "c", "d", "e"}) {
		t.Errorf("keep saw %v, want the keys in order", seen)
	}
	keys, err := cp.KeyList(context.Background())
	if err != nil || !reflect.DeepEqual(keys, []string{"b 2", "e 5"}) {
		t.Fatalf("copy holds %v, %v", keys, err)
	}
	if len(cp.Kids) != 2 || len(cp.Kids[0].Names) != 1 || len(cp.Kids[1].Kids) != 1 {
		t.Fatalf("copy structure %s, want [[b] [[e]]]", cp)
	}
	if cp.Kids[0].Names[0].key() != "b\x00" {
		t.Errorf("key bytes %q, want %q", cp.Kids[0].Names[0].key(), "b\x00")
	}
	if cp.Kmin != "b" || cp.Kmax != "e" || cp.Kids[1].Kmin != "e" || cp.Kids[1].Kids[0].Kmax != "e" {
		t.Errorf("limits root %s..%s, kid %s..%s", cp.Kmin, cp.Kmax, cp.Kids[1].Kmin, cp.Kids[1].Kmax)
	}
	for _, n := range []*Node{cp, cp.Kids[0], cp.Kids[1], cp.Kids[1].Kids[0]} {
		if n.D == nil || len(n.D) != 0 {
			t.Errorf("copy node %s: dict %v, want a new empty dict", n, n.D)
		}
	}

	empty, err := src.FilteredCopy(context.Background(), func(string, types.Object) (types.Object, bool, error) { return nil, false, nil })
	if err != nil || empty == nil || len(empty.Kids) != 0 || len(empty.Names) != 0 || empty.D == nil {
		t.Fatalf("empty copy %v, %v: want an empty root with a dict", empty, err)
	}

	keepErr := errors.New("bad value")
	if _, err := src.FilteredCopy(context.Background(), func(string, types.Object) (types.Object, bool, error) { return nil, false, keepErr }); !errors.Is(err, keepErr) {
		t.Errorf("keep error: got %v", err)
	}
	c, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := src.FilteredCopy(c, func(string, types.Object) (types.Object, bool, error) { return nil, true, nil }); !errors.Is(err, context.Canceled) {
		t.Errorf("canceled: got %v", err)
	}
}
