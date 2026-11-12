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

package pdfcpu

import (
	"errors"
	"fmt"
	"slices"

	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

var ocConfigScalarKeys = []string{"Name", "Creator", "BaseState", "Intent", "ListMode"}
var ocConfigRefArrayKeys = []string{"ON", "OFF", "Locked"}

// ocMigration copies optional content properties of a source context to a
// destination context, keeping only the groups that pages carried over.
type ocMigration struct {
	ctxSrc   *model.Context
	migrated map[int]int
}

// ref returns the destination reference for the group reference o, or false
// if o is no reference or its group was not migrated.
func (m ocMigration) ref(o types.Object) (types.Object, bool) {
	ir, ok := o.(types.IndirectRef)
	if !ok {
		return nil, false
	}
	objNr := m.migrated[ir.ObjectNumber.Value()]
	if objNr == 0 {
		return nil, false
	}
	return *types.NewIndirectRef(objNr, 0), true
}

func (m ocMigration) refs(o types.Object) (types.Array, error) {
	a, err := m.ctxSrc.DereferenceArray(o)
	if err != nil {
		return nil, err
	}
	res := types.Array{}
	for _, v := range a {
		if r, ok := m.ref(v); ok {
			res = append(res, r)
		}
	}
	return res, nil
}

// order filters an Order array: group references, nested arrays and their
// text string labels. A nested array left without a group is dropped.
func (m ocMigration) order(o types.Object, depth int) (types.Array, bool, error) {
	if err := m.ctxSrc.CheckRecursionDepth("optional content order", depth); err != nil {
		return nil, false, err
	}
	a, err := m.ctxSrc.DereferenceArray(o)
	if err != nil {
		return nil, false, err
	}
	res, hasGroup := types.Array{}, false
	for _, v := range a {
		if r, ok := m.ref(v); ok {
			res = append(res, r)
			hasGroup = true
			continue
		}
		v, err := m.ctxSrc.Dereference(v)
		if err != nil {
			return nil, false, err
		}
		switch v := v.(type) {
		case types.StringLiteral, types.HexLiteral:
			res = append(res, v)
		case types.Array:
			sub, ok, err := m.order(v, depth+1)
			if err != nil {
				return nil, false, err
			}
			if ok {
				res = append(res, sub)
				hasGroup = true
			}
		}
	}
	return res, hasGroup, nil
}

func (m ocMigration) usageApplications(o types.Object) (types.Array, error) {
	a, err := m.ctxSrc.DereferenceArray(o)
	if err != nil {
		return nil, err
	}
	res := types.Array{}
	for _, v := range a {
		d, err := m.ctxSrc.DereferenceDict(v)
		if err != nil {
			return nil, err
		}
		if d == nil {
			continue
		}
		ocgs, err := m.refs(d["OCGs"])
		if err != nil {
			return nil, err
		}
		if len(ocgs) == 0 {
			continue
		}
		app := types.Dict{"OCGs": ocgs}
		for _, k := range []string{"Event", "Category"} {
			v, err := m.ctxSrc.Dereference(d[k])
			if err != nil {
				return nil, err
			}
			if v != nil {
				app[k] = v.Clone()
			}
		}
		res = append(res, app)
	}
	return res, nil
}

// config filters the optional content configuration dictionary o. An entry
// present in o is present in the result, possibly as an empty array.
func (m ocMigration) config(o types.Object) (types.Dict, error) {
	d, err := m.ctxSrc.DereferenceDict(o)
	if err != nil || d == nil {
		return nil, err
	}
	res := types.Dict{}
	for _, k := range ocConfigScalarKeys {
		v, err := m.ctxSrc.Dereference(d[k])
		if err != nil {
			return nil, fmt.Errorf("%s: %w", k, err)
		}
		if v != nil {
			res[k] = v.Clone()
		}
	}
	for _, k := range ocConfigRefArrayKeys {
		if _, found := d.Find(k); !found {
			continue
		}
		if res[k], err = m.refs(d[k]); err != nil {
			return nil, fmt.Errorf("%s: %w", k, err)
		}
	}
	if _, found := d.Find("Order"); found {
		if res["Order"], _, err = m.order(d["Order"], 0); err != nil {
			return nil, fmt.Errorf("Order: %w", err)
		}
	}
	if _, found := d.Find("RBGroups"); found {
		groups, err := m.ctxSrc.DereferenceArray(d["RBGroups"])
		if err != nil {
			return nil, fmt.Errorf("RBGroups: %w", err)
		}
		rb := types.Array{}
		for _, g := range groups {
			a, err := m.refs(g)
			if err != nil {
				return nil, fmt.Errorf("RBGroups: %w", err)
			}
			if len(a) > 0 {
				rb = append(rb, a)
			}
		}
		res["RBGroups"] = rb
	}
	if _, found := d.Find("AS"); found {
		if res["AS"], err = m.usageApplications(d["AS"]); err != nil {
			return nil, fmt.Errorf("AS: %w", err)
		}
	}
	return res, nil
}

// migratedOCProperties returns the optional content properties of ctxSrc for
// the groups in migrated, or nil if none of its groups was migrated.
func migratedOCProperties(ctxSrc *model.Context, migrated map[int]int) (types.Dict, error) {
	rootDict, err := ctxSrc.Catalog()
	if err != nil {
		return nil, err
	}
	o, found := rootDict.Find("OCProperties")
	if !found {
		return nil, nil
	}
	d, err := ctxSrc.DereferenceDict(o)
	if err != nil || d == nil {
		return nil, err
	}
	m := ocMigration{ctxSrc: ctxSrc, migrated: migrated}
	ocgs, err := m.refs(d["OCGs"])
	if err != nil {
		return nil, fmt.Errorf("OCGs: %w", err)
	}
	if len(ocgs) == 0 {
		return nil, nil
	}
	res := types.Dict{"OCGs": ocgs}
	cfg, err := m.config(d["D"])
	if err != nil {
		return nil, fmt.Errorf("D: %w", err)
	}
	if cfg != nil {
		res["D"] = cfg
	}
	if _, found := d.Find("Configs"); found {
		configs, err := ctxSrc.DereferenceArray(d["Configs"])
		if err != nil {
			return nil, fmt.Errorf("Configs: %w", err)
		}
		a := types.Array{}
		for _, c := range configs {
			cfg, err := m.config(c)
			if err != nil {
				return nil, fmt.Errorf("Configs: %w", err)
			}
			if cfg != nil {
				a = append(a, cfg)
			}
		}
		res["Configs"] = a
	}
	return res, nil
}

const ocPropertiesCarryKind = "optional content properties not carried over"

// carryOCProperties sets the optional content properties of ctxDest from
// ctxSrc after pages were added. Properties that cannot be read are not
// carried; the source gets one notice instead of failing the extraction.
func carryOCProperties(ctxSrc, ctxDest *model.Context, migrated map[int]int) {
	d, err := migratedOCProperties(ctxSrc, migrated)
	if err == nil {
		if d != nil {
			ctxDest.RootDict["OCProperties"] = d
		}
		return
	}
	if ctxSrc.Root != nil && !ctxSrc.XRefTable.MarkDigested(ocPropertiesCarryKind, ctxSrc.Root.ObjectNumber.Value()) {
		return
	}
	err = fmt.Errorf("optional content properties not carried over: %w", err)
	notice := model.NewValidationNotice(model.NoticePhaseValidate, model.NoticeSkipped, err.Error(), err)
	notice.Path = "OCProperties"
	ctxSrc.AddValidationNotice(notice)
}

func ocBaseState(xRefTable *model.XRefTable, cfg types.Dict) (string, error) {
	n, _, err := xRefTable.DereferenceNameEntry(cfg, "BaseState")
	if err != nil || n == nil || n.Value() != "OFF" {
		return "ON", err
	}
	return "OFF", nil
}

func appendArrayEntry(xRefTable *model.XRefTable, d types.Dict, key string, elems types.Array) error {
	if len(elems) == 0 {
		return nil
	}
	a, err := xRefTable.DereferenceArray(d[key])
	if err != nil {
		return fmt.Errorf("%s: %w", key, err)
	}
	d[key] = append(a.Clone().(types.Array), elems...)
	return nil
}

func containsRef(a types.Array, o types.Object) bool {
	return slices.ContainsFunc(a, func(v types.Object) bool { return v == o })
}

// mergeOCOrder appends the Order of cfgSrc to the Order of cfgDest. When only
// one side has an Order, the other side's groups are listed by its OCGs
// array, so the groups of both inputs stay in the layer panel.
func mergeOCOrder(xRefTable *model.XRefTable, ocgsDest, ocgsSrc types.Array, cfgSrc, cfgDest types.Dict) error {
	_, srcHas := cfgSrc.Find("Order")
	_, destHas := cfgDest.Find("Order")
	if !srcHas && !destHas {
		return nil
	}
	orderSrc := ocgsSrc
	if srcHas {
		a, err := xRefTable.DereferenceArray(cfgSrc["Order"])
		if err != nil {
			return fmt.Errorf("Order: %w", err)
		}
		orderSrc = a
	}
	if !destHas {
		cfgDest["Order"] = ocgsDest.Clone()
	}
	return appendArrayEntry(xRefTable, cfgDest, "Order", orderSrc)
}

// mergeOCDefaultConfig merges the default configuration cfgSrc into cfgDest.
// Every source group keeps the state cfgSrc gives it, whatever the base state
// of cfgDest.
func mergeOCDefaultConfig(xRefTable *model.XRefTable, ocgsDest, ocgsSrc types.Array, cfgSrc, cfgDest types.Dict) error {
	baseSrc, err := ocBaseState(xRefTable, cfgSrc)
	if err != nil {
		return err
	}
	baseDest, err := ocBaseState(xRefTable, cfgDest)
	if err != nil {
		return err
	}
	on, err := xRefTable.DereferenceArray(cfgSrc["ON"])
	if err != nil {
		return fmt.Errorf("ON: %w", err)
	}
	off, err := xRefTable.DereferenceArray(cfgSrc["OFF"])
	if err != nil {
		return fmt.Errorf("OFF: %w", err)
	}
	var addOn, addOff types.Array
	for _, g := range ocgsSrc {
		state := baseSrc
		if containsRef(on, g) {
			state = "ON"
		}
		if containsRef(off, g) {
			state = "OFF"
		}
		if state == baseDest {
			continue
		}
		if state == "ON" {
			addOn = append(addOn, g)
		} else {
			addOff = append(addOff, g)
		}
	}
	if err := appendArrayEntry(xRefTable, cfgDest, "ON", addOn); err != nil {
		return err
	}
	if err := appendArrayEntry(xRefTable, cfgDest, "OFF", addOff); err != nil {
		return err
	}
	if err := mergeOCOrder(xRefTable, ocgsDest, ocgsSrc, cfgSrc, cfgDest); err != nil {
		return err
	}
	for _, k := range []string{"RBGroups", "Locked", "AS"} {
		a, err := xRefTable.DereferenceArray(cfgSrc[k])
		if err != nil {
			return fmt.Errorf("%s: %w", k, err)
		}
		if err := appendArrayEntry(xRefTable, cfgDest, k, a); err != nil {
			return err
		}
	}
	return nil
}

// mergedOCProperties returns a copy of the optional content properties o2
// of the merge destination with those of the merged input o1 merged in, and
// the input's dictionary and default configuration. xRefTable holds the
// objects of both.
func mergedOCProperties(xRefTable *model.XRefTable, o1, o2 types.Object) (types.Dict, types.Dict, types.Dict, error) {
	dSrc, err := xRefTable.DereferenceDict(o1)
	if err != nil || dSrc == nil {
		return nil, nil, nil, err
	}
	dDest, err := xRefTable.DereferenceDict(o2)
	if err != nil {
		return nil, nil, nil, err
	}
	if dDest == nil {
		return dSrc, dSrc, nil, nil
	}
	merged := dDest.Clone().(types.Dict)
	ocgsSrc, err := xRefTable.DereferenceArray(dSrc["OCGs"])
	if err != nil {
		return nil, nil, nil, fmt.Errorf("OCGs: %w", err)
	}
	ocgsDest, err := xRefTable.DereferenceArray(dDest["OCGs"])
	if err != nil {
		return nil, nil, nil, fmt.Errorf("OCGs: %w", err)
	}
	cfgSrc, err := xRefTable.DereferenceDict(dSrc["D"])
	if err != nil {
		return nil, nil, nil, fmt.Errorf("D: %w", err)
	}
	if cfgSrc == nil {
		cfgSrc = types.Dict{}
	}
	cfgDest, err := xRefTable.DereferenceDict(dDest["D"])
	if err != nil {
		return nil, nil, nil, fmt.Errorf("D: %w", err)
	}
	if cfgDest == nil {
		cfgDest = types.Dict{}
	}
	cfgDest = cfgDest.Clone().(types.Dict)
	merged["D"] = cfgDest
	if err := appendArrayEntry(xRefTable, merged, "OCGs", ocgsSrc); err != nil {
		return nil, nil, nil, err
	}
	if err := mergeOCDefaultConfig(xRefTable, ocgsDest, ocgsSrc, cfgSrc, cfgDest); err != nil {
		return nil, nil, nil, fmt.Errorf("D: %w", err)
	}
	return merged, dSrc, cfgSrc, nil
}

// mergeOCProperties merges the optional content properties of ctxSrc into
// those of ctxDest, so that hidden layers of every input stay hidden. What
// cannot be merged gets a notice; it never fails the merge.
func mergeOCProperties(ctxSrc, ctxDest *model.Context) error {
	rootDictSrc, rootDictDest, err := rootDicts(ctxSrc, ctxDest)
	if err != nil {
		return fmt.Errorf("root dicts: %w", err)
	}
	o1, found := rootDictSrc.Find("OCProperties")
	if !found {
		return nil
	}
	o2, found := rootDictDest.Find("OCProperties")
	if !found {
		rootDictDest["OCProperties"] = o1
		return nil
	}
	xRefTable := ctxDest.XRefTable
	merged, dSrc, cfgSrc, err := mergedOCProperties(xRefTable, o1, o2)
	if err != nil {
		addOCMergeNotice(ctxDest, "", fmt.Sprintf("optional content properties of a merged input are not carried over: %v", err))
		return nil
	}
	if merged == nil {
		return nil
	}
	rootDictDest["OCProperties"] = merged
	if cfgSrc == nil {
		return nil
	}
	if _, found := dSrc.Find("Configs"); found {
		addOCMergeNotice(ctxDest, "/Configs", "alternate configurations of a merged input are not carried over")
	}
	intentSrc, err1 := ocIntent(xRefTable, cfgSrc)
	intentDest, err2 := ocIntent(xRefTable, merged["D"].(types.Dict))
	if err1 != nil || err2 != nil || intentSrc != intentDest {
		addOCMergeNotice(ctxDest, "/D/Intent", "the intent of a merged input's default configuration is not carried over")
	}
	return nil
}

func addOCMergeNotice(ctxDest *model.Context, path, msg string) {
	err := errors.New(msg)
	notice := model.NewValidationNotice(model.NoticePhaseValidate, model.NoticeSkipped, err.Error(), err)
	notice.Path = "OCProperties" + path
	ctxDest.AddValidationNotice(notice)
}

// ocIntent returns the Intent of the configuration d in PDF syntax. An
// absent Intent is View.
func ocIntent(xRefTable *model.XRefTable, d types.Dict) (string, error) {
	o, err := xRefTable.Dereference(d["Intent"])
	if err != nil {
		return "", err
	}
	if o == nil {
		return types.Name("View").PDFString(), nil
	}
	return o.PDFString(), nil
}
