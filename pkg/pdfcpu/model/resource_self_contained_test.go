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
	"fmt"
	"testing"

	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

func TestNamedResourcesSelfContained(t *testing.T) {
	ref9 := *types.NewIndirectRef(9, 0)
	ref10 := *types.NewIndirectRef(10, 0)
	form := func(resources types.Object) types.StreamDict {
		sd := types.StreamDict{Dict: types.Dict{"Subtype": types.Name("Form"), "BBox": types.Array{}}}
		if resources != nil {
			sd.Dict["Resources"] = resources
		}
		return sd
	}
	// The soft mask group is object 10, a form without Resources.
	group := types.StreamDict{Dict: types.Dict{"Subtype": types.Name("Form"), "BBox": types.Array{}}}

	tests := []struct {
		name  string
		key   string
		obj9  types.Object
		obj10 types.Object
		want  bool
	}{
		{"form with resources", "XObject", form(types.Dict{}), nil, true},
		{"form without resources", "XObject", form(nil), nil, false},
		{"form with dangling resources", "XObject", form(*types.NewIndirectRef(99, 0)), nil, false},
		{"image", "XObject", types.StreamDict{Dict: types.Dict{"Subtype": types.Name("Image")}}, nil, true},
		{"tiling pattern without resources", "Pattern", types.StreamDict{Dict: types.Dict{"PatternType": types.Integer(1)}}, nil, false},
		{"shading pattern", "Pattern", types.Dict{"PatternType": types.Integer(2)}, nil, true},
		{"type 3 font without resources", "Font", types.Dict{"Subtype": types.Name("Type3")}, nil, false},
		{"type 3 font with resources", "Font", types.Dict{"Subtype": types.Name("Type3"), "Resources": types.Dict{}}, nil, true},
		{"type 1 font", "Font", types.Dict{"Subtype": types.Name("Type1")}, nil, true},
		{"soft mask group without resources", "ExtGState", types.Dict{"SMask": types.Dict{"G": ref10}}, group, false},
		{"soft mask none", "ExtGState", types.Dict{"SMask": types.Name("None")}, nil, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			xRefTable := newXRefTable(NewDefaultConfiguration())
			xRefTable.Table[9] = NewXRefTableEntryGen0(tc.obj9)
			if tc.obj10 != nil {
				xRefTable.Table[10] = NewXRefTableEntryGen0(tc.obj10)
			}
			d := types.Dict{tc.key: types.Dict{"R1": ref9}}
			prn := NewPageResourceNames()
			prn[tc.key]["R1"] = true
			if got := xRefTable.namedResourcesSelfContained(d, prn); got != tc.want {
				t.Fatalf("namedResourcesSelfContained() = %v, want %v", got, tc.want)
			}
		})
	}
}

// resourceTestTable returns an xref table with the objects of objs at their object numbers.
func resourceTestTable(conf *Configuration, objs map[int]types.Object) *XRefTable {
	xRefTable := newXRefTable(conf)
	for objNr, o := range objs {
		xRefTable.Table[objNr] = NewXRefTableEntryGen0(o)
	}
	return xRefTable
}

// resourceTestForm returns a form XObject with the Resources entry res, unless res is nil.
func resourceTestForm(res types.Object) types.StreamDict {
	sd := types.StreamDict{Dict: types.Dict{"Subtype": types.Name("Form"), "BBox": types.Array{}}}
	if res != nil {
		sd.Dict["Resources"] = res
	}
	return sd
}

// TestNamedResourcesSelfContainedSharedResources checks that forms sharing one indirect
// Resources dictionary are checked once. Checking it for each form would nest deeper than
// the recursion limit for more than 100 forms, which keeps all resources.
func TestNamedResourcesSelfContainedSharedResources(t *testing.T) {
	const forms = 150
	ref5 := *types.NewIndirectRef(5, 0)
	xobjs := types.Dict{}
	objs := map[int]types.Object{}
	for i := range forms {
		objNr := 100 + i
		xobjs[fmt.Sprintf("Fm%d", i)] = *types.NewIndirectRef(objNr, 0)
		objs[objNr] = resourceTestForm(ref5)
	}
	objs[5] = types.Dict{"XObject": xobjs}
	xRefTable := resourceTestTable(NewDefaultConfiguration(), objs)

	d := types.Dict{"XObject": types.Dict{"Fm0": *types.NewIndirectRef(100, 0)}}
	prn := NewPageResourceNames()
	prn["XObject"]["Fm0"] = true
	if !xRefTable.namedResourcesSelfContained(d, prn) {
		t.Fatalf("namedResourcesSelfContained() = false for %d forms sharing one Resources dictionary, want true", forms)
	}
}

func TestNamedResourcesSelfContainedNested(t *testing.T) {
	ref9 := *types.NewIndirectRef(9, 0)
	ref10 := *types.NewIndirectRef(10, 0)
	ref11 := *types.NewIndirectRef(11, 0)
	xobject := func(name string, r types.IndirectRef) types.Dict {
		return types.Dict{"XObject": types.Dict{name: r}}
	}
	type3 := types.Dict{"Subtype": types.Name("Type3")}

	tests := []struct {
		name string
		objs map[int]types.Object
		want bool
	}{
		{"nested form without resources", map[int]types.Object{
			9: resourceTestForm(xobject("Fm2", ref10)), 10: resourceTestForm(nil)}, false},
		{"nested form with resources", map[int]types.Object{
			9: resourceTestForm(xobject("Fm2", ref10)), 10: resourceTestForm(types.Dict{})}, true},
		{"nested image", map[int]types.Object{
			9: resourceTestForm(xobject("Im", ref10)), 10: types.StreamDict{Dict: types.Dict{"Subtype": types.Name("Image")}}}, true},
		{"nested type 3 font without resources", map[int]types.Object{
			9: resourceTestForm(types.Dict{"Font": types.Dict{"F": ref10}}), 10: type3}, false},
		{"nested form with dangling resources", map[int]types.Object{
			9: resourceTestForm(xobject("Fm2", ref10)), 10: resourceTestForm(*types.NewIndirectRef(99, 0))}, false},
		{"cycle back to the form", map[int]types.Object{
			9: resourceTestForm(xobject("Fm1", ref9))}, true},
		{"nested form without resources two levels down", map[int]types.Object{
			9: resourceTestForm(xobject("Fm2", ref10)), 10: resourceTestForm(xobject("Fm3", ref11)), 11: resourceTestForm(nil)}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			xRefTable := resourceTestTable(NewDefaultConfiguration(), tc.objs)
			d := types.Dict{"XObject": types.Dict{"R1": ref9}}
			prn := NewPageResourceNames()
			prn["XObject"]["R1"] = true
			if got := xRefTable.namedResourcesSelfContained(d, prn); got != tc.want {
				t.Fatalf("namedResourcesSelfContained() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestNamedResourcesSelfContainedDepthLimit checks that a chain of nested forms
// deeper than the recursion limit keeps all resources, since the chain is not read to its end.
func TestNamedResourcesSelfContainedDepthLimit(t *testing.T) {
	ref := func(n int) types.IndirectRef { return *types.NewIndirectRef(n, 0) }
	objs := map[int]types.Object{
		9:  resourceTestForm(types.Dict{"XObject": types.Dict{"F": ref(10)}}),
		10: resourceTestForm(types.Dict{"XObject": types.Dict{"F": ref(11)}}),
		11: resourceTestForm(types.Dict{"XObject": types.Dict{"F": ref(12)}}),
		12: resourceTestForm(types.Dict{}),
	}
	d := types.Dict{"XObject": types.Dict{"R1": ref(9)}}
	prn := NewPageResourceNames()
	prn["XObject"]["R1"] = true

	if got := resourceTestTable(NewDefaultConfiguration(), objs).namedResourcesSelfContained(d, prn); !got {
		t.Fatal("chain within the default limit: namedResourcesSelfContained() = false, want true")
	}
	conf := NewDefaultConfiguration()
	conf.Limits.MaxRecursionDepth = 2
	if got := resourceTestTable(conf, objs).namedResourcesSelfContained(d, prn); got {
		t.Fatal("chain beyond the limit: namedResourcesSelfContained() = true, want false")
	}
}

// TestAnnotationsUsePageNames checks the appearance streams of a page's annotations.
func TestAnnotationsUsePageNames(t *testing.T) {
	ref8 := *types.NewIndirectRef(8, 0)
	ref9 := *types.NewIndirectRef(9, 0)
	annot := func(ap types.Dict) types.Dict {
		return types.Dict{"Subtype": types.Name("Widget"), "AP": ap}
	}
	page := types.Dict{"Annots": types.Array{ref8}}

	tests := []struct {
		name string
		objs map[int]types.Object
		want bool
	}{
		{"normal appearance with resources", map[int]types.Object{
			8: annot(types.Dict{"N": ref9}), 9: resourceTestForm(types.Dict{})}, false},
		{"normal appearance without resources", map[int]types.Object{
			8: annot(types.Dict{"N": ref9}), 9: resourceTestForm(nil)}, true},
		{"down appearance without resources", map[int]types.Object{
			8: annot(types.Dict{"D": ref9}), 9: resourceTestForm(nil)}, true},
		{"state appearance without resources", map[int]types.Object{
			8: annot(types.Dict{"N": types.Dict{"On": ref9}}), 9: resourceTestForm(nil)}, true},
		{"state appearance with resources", map[int]types.Object{
			8: annot(types.Dict{"N": types.Dict{"On": ref9}}), 9: resourceTestForm(types.Dict{})}, false},
		{"annotation without appearance", map[int]types.Object{
			8: types.Dict{"Subtype": types.Name("Link")}}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			xRefTable := resourceTestTable(NewDefaultConfiguration(), tc.objs)
			if got := xRefTable.annotationsUsePageNames(page); got != tc.want {
				t.Fatalf("annotationsUsePageNames() = %v, want %v", got, tc.want)
			}
		})
	}
	if xRefTable := resourceTestTable(NewDefaultConfiguration(), nil); xRefTable.annotationsUsePageNames(types.Dict{}) {
		t.Fatal("page without annotations: annotationsUsePageNames() = true, want false")
	}
}
