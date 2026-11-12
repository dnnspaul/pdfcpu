/*
Copyright 2022 The pdfcpu Authors.

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
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/pdfcpu/pdfcpu/internal/contextutil"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

var pParamMap = parameterMap[PageConfiguration]{
	"dimensions": parseDimensions,
	"formsize":   parsePageFormat,
	"papersize":  parsePageFormat,
}

// PageConfiguration represents the page config for the "pages insert" command.
type PageConfiguration struct {
	PageDim  *types.Dim        // page dimensions in display unit; nil inherits each selected page's effective MediaBox.
	PageSize string            // one of A0,A1,A2,A3,A4(=default),A5,A6,A7,A8,Letter,Legal,Ledger,Tabloid,Executive,ANSIC,ANSID,ANSIE.
	UserDim  bool              // true if one of dimensions or paperSize provided overriding the default.
	InpUnit  types.DisplayUnit // input display unit.
}

// DefaultPageConfiguration returns the default configuration.
func DefaultPageConfiguration() *PageConfiguration {
	return &PageConfiguration{
		PageDim:  types.PaperSize["A4"],
		PageSize: "A4",
		InpUnit:  types.POINTS,
	}
}

// String returns the string value of p.
func (p PageConfiguration) String() string {
	return fmt.Sprintf("Page config: %s %s\n", p.PageSize, p.PageDim)
}

func parsePageFormat(s string, p *PageConfiguration) (err error) {
	if p.UserDim {
		return errAmbiguousPageDim
	}
	p.PageDim, p.PageSize, err = types.ParsePageFormat(s)
	p.UserDim = true
	return err
}

func parseDimensions(s string, p *PageConfiguration) (err error) {
	if p.UserDim {
		return errAmbiguousPageDim
	}
	p.PageDim, p.PageSize, err = ParsePageDim(s, p.InpUnit)
	p.UserDim = true
	return err
}

// ParsePageConfiguration parses a page configuration string into an internal structure.
func ParsePageConfiguration(s string, u types.DisplayUnit) (*PageConfiguration, error) {

	if s == "" {
		return nil, nil
	}

	pageConf := DefaultPageConfiguration()
	pageConf.InpUnit = u

	for s := range strings.SplitSeq(s, ",") {

		ss1 := strings.Split(s, ":")
		if len(ss1) != 2 {
			return nil, errors.New("invalid page configuration string")
		}

		paramPrefix := strings.TrimSpace(ss1[0])
		paramValueStr := strings.TrimSpace(ss1[1])

		if err := handleParameter(pParamMap, paramPrefix, paramValueStr, pageConf); err != nil {
			return nil, err
		}
	}

	return pageConf, nil
}

func validateAddPagesInputs(ctxSrc, ctxDest *model.Context, pagesDict types.Dict, fieldsSrc, fieldsDest *types.Array, migrated map[int]int) error {
	if err := requireContextWithXRefTable(ctxSrc); err != nil {
		return fmt.Errorf("add pages: source context: %w", err)
	}
	if err := requireContextWithXRefTable(ctxDest); err != nil {
		return fmt.Errorf("add pages: destination context: %w", err)
	}
	if pagesDict == nil {
		return errors.New("add pages: missing destination page tree dict")
	}
	if fieldsSrc == nil {
		return errors.New("add pages: missing source form fields")
	}
	if fieldsDest == nil {
		return errors.New("add pages: missing destination form fields")
	}
	if migrated == nil {
		return errors.New("add pages: missing migration map")
	}
	return nil
}

func migratedPageDict(c context.Context, ctxSrc, ctxDest *model.Context, pageNr int, migrated map[int]int) (types.Dict, *types.IndirectRef, *model.InheritedPageAttrs, error) {
	if err := contextutil.Check(c); err != nil {
		return nil, nil, nil, err
	}
	d, pageIndRef, inhPAttrs, err := ctxSrc.PageDict(c, pageNr, true)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("read page dict: %w", err)
	}
	if d == nil {
		return nil, nil, nil, fmt.Errorf("unknown page number: %d", pageNr)
	}

	obj, err := migrateIndRef(c, pageIndRef, ctxSrc, ctxDest, migrated)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("migrate page object: %w", err)
	}

	pageDict, ok := obj.(types.Dict)
	if !ok {
		return nil, nil, nil, fmt.Errorf("migrated page object is %T, want types.Dict", obj)
	}
	return pageDict, pageIndRef, inhPAttrs, nil
}

func addPage(c context.Context, ctxSrc, ctxDest *model.Context, pageNr int, pagesIndRef types.IndirectRef, pagesDict types.Dict, migrated map[int]int, selection *formFieldSelection) (*types.IndirectRef, error) {
	d, pageIndRef, inhPAttrs, err := migratedPageDict(c, ctxSrc, ctxDest, pageNr, migrated)
	if err != nil {
		return nil, fmt.Errorf("page %d: %w", pageNr, err)
	}

	d["Resources"] = inhPAttrs.Resources.Clone()
	d["Parent"] = pagesIndRef
	d["MediaBox"] = inhPAttrs.MediaBox.Array()
	if _, found := d.Find("CropBox"); !found && inhPAttrs.CropBox != nil {
		d["CropBox"] = inhPAttrs.CropBox.Array()
	}
	if inhPAttrs.Rotate%360 > 0 {
		d["Rotate"] = types.Integer(inhPAttrs.Rotate)
	}

	if err := migratePageDict(c, d, *pageIndRef, ctxSrc, ctxDest, migrated, selection); err != nil {
		return nil, fmt.Errorf("page %d: migrate page dict: %w", pageNr, err)
	}

	if err := model.AppendPageTree(pageIndRef, 1, pagesDict); err != nil {
		return nil, fmt.Errorf("page %d: append page tree: %w", pageNr, err)
	}
	return pageIndRef, nil
}

func addPages(c context.Context, ctxSrc, ctxDest *model.Context, pageNrs []int, usePgCache bool, pagesIndRef types.IndirectRef, pagesDict types.Dict, fieldsSrc, fieldsDest *types.Array, migrated map[int]int) error {
	if err := contextutil.Check(c); err != nil {
		return err
	}
	if err := validateAddPagesInputs(ctxSrc, ctxDest, pagesDict, fieldsSrc, fieldsDest, migrated); err != nil {
		return err
	}
	// Used by collect, extractPages, split
	pageCache := map[int]*types.IndirectRef{}
	selection := newFormFieldSelection()

	for _, i := range pageNrs {
		if err := contextutil.Check(c); err != nil {
			return err
		}
		if usePgCache {
			if indRef, ok := pageCache[i]; ok {
				if err := model.AppendPageTree(indRef, 1, pagesDict); err != nil {
					return fmt.Errorf("page %d: append cached page tree: %w", i, err)
				}
				continue
			}
		}

		pageIndRef, err := addPage(c, ctxSrc, ctxDest, i, pagesIndRef, pagesDict, migrated, selection)
		if err != nil {
			return err
		}

		if usePgCache {
			pageCache[i] = pageIndRef
		}
	}

	if len(selection.widgets) == 0 || len(*fieldsSrc) == 0 {
		return nil
	}
	if err := contextutil.Check(c); err != nil {
		return err
	}
	if err := migrateFields(c, fieldsSrc, fieldsDest, ctxSrc, ctxDest, migrated, selection); err != nil {
		return fmt.Errorf("migrate fields: %w", err)
	}
	return nil
}

func destinationPageMigrated(arr types.Array, migrated map[int]int) bool {
	indRef, ok := arr[0].(types.IndirectRef)
	return !ok || migrated[indRef.ObjectNumber.Value()] > 0
}

// migrateNamedDestArray returns a copy of the destination array arr pointing
// to the migrated page, or false if arr does not point to a migrated page.
func migrateNamedDestArray(arr types.Array, migrated map[int]int) (types.Array, bool) {
	if len(arr) == 0 || !destinationPageMigrated(arr, migrated) {
		return nil, false
	}
	arr = arr.Clone().(types.Array)
	arr[0] = patchObject(arr[0], migrated)
	return arr, true
}

// migrateNamedDestValue returns a copy of the named destination v, a
// destination array or a dict with a D array, pointing to the migrated page,
// or false if v does not point to a migrated page. v is not changed.
func migrateNamedDestValue(xRefTable *model.XRefTable, v types.Object, migrated map[int]int) (types.Object, bool, error) {
	// destination array
	arr, err := xRefTable.DereferenceArray(v)
	if err == nil {
		arr, ok := migrateNamedDestArray(arr, migrated)
		if !ok {
			return nil, false, nil
		}
		return arr, true, nil
	}

	// destination dict with a D array.
	d, err := xRefTable.DereferenceDict(v)
	if err != nil {
		return nil, false, err
	}

	arr, ok := migrateNamedDestArray(d.ArrayEntry("D"), migrated)
	if !ok {
		return nil, false, nil
	}
	d = d.Clone().(types.Dict)
	d["D"] = arr
	return d, true, nil
}

// migrateNamedDests returns a copy of the named destinations n of ctxSrc
// holding the destinations that point to migrated pages, patched to point to
// the migrated page objects. n and the objects of ctxSrc are not changed, so
// later extractions from ctxSrc see all of its named destinations.
func migrateNamedDests(c context.Context, ctxSrc *model.Context, n *model.Node, migrated map[int]int) (*model.Node, error) {
	if err := requireContextWithXRefTable(ctxSrc); err != nil {
		return nil, fmt.Errorf("source context: %w", err)
	}
	if n == nil {
		return nil, errors.New("missing named destinations")
	}
	if migrated == nil {
		return nil, errors.New("missing migration map")
	}

	keep := func(k string, v types.Object) (types.Object, bool, error) {
		if v == nil {
			// Keep a corrupt entry as is.
			return nil, true, nil
		}
		v, ok, err := migrateNamedDestValue(ctxSrc.XRefTable, v, migrated)
		if err != nil {
			return nil, false, fmt.Errorf("named destination %q: %w", k, err)
		}
		return v, ok, nil
	}

	dests, err := n.FilteredCopy(c, keep)
	if err != nil {
		return nil, fmt.Errorf("process named destinations: %w", err)
	}
	return dests, nil
}

func finishAddedPages(c context.Context, ctxSrc, ctxDest *model.Context, fieldsDest types.Array, migrated map[int]int) error {
	if err := contextutil.Check(c); err != nil {
		return err
	}
	if ctxSrc.Form != nil && len(fieldsDest) > 0 {
		d := ctxSrc.Form.Clone().(types.Dict)
		if err := migrateFormDict(c, d, fieldsDest, ctxSrc, ctxDest, migrated); err != nil {
			return fmt.Errorf("add pages: migrate form: %w", err)
		}
		ctxDest.RootDict["AcroForm"] = d
	}

	if err := contextutil.Check(c); err != nil {
		return err
	}
	if n, ok := ctxSrc.Names["Dests"]; ok {
		// Carry over used named destinations.
		dests, err := migrateNamedDests(c, ctxSrc, n, migrated)
		if err != nil {
			return fmt.Errorf("add pages: migrate named destinations: %w", err)
		}
		ctxDest.Names = map[string]*model.Node{"Dests": dests}
	}

	carryOCProperties(ctxSrc, ctxDest, migrated)
	return nil
}

// AddPages adds pages and corresponding resources from ctxSrc to ctxDest and supports cancellation.
func AddPages(c context.Context, ctxSrc, ctxDest *model.Context, pageNrs []int, usePgCache bool) error {
	if err := contextutil.Check(c); err != nil {
		return err
	}
	if err := requireContextWithXRefTable(ctxSrc); err != nil {
		return fmt.Errorf("add pages: source context: %w", err)
	}
	if err := requireContextWithXRefTable(ctxDest); err != nil {
		return fmt.Errorf("add pages: destination context: %w", err)
	}

	pagesIndRef, err := ctxDest.Pages()
	if err != nil {
		return fmt.Errorf("add pages: read destination page tree: %w", err)
	}
	if pagesIndRef == nil {
		return errors.New("add pages: missing destination page tree")
	}

	pagesDict, err := ctxDest.DereferenceDict(*pagesIndRef)
	if err != nil {
		return fmt.Errorf("add pages: dereference destination page tree: %w", err)
	}

	fieldsSrc, fieldsDest := types.Array{}, types.Array{}

	if ctxSrc.Form != nil {
		o, _ := ctxSrc.Form.Find("Fields")
		fieldsSrc, err = ctxSrc.DereferenceArray(o)
		if err != nil {
			return fmt.Errorf("add pages: read source form fields: %w", err)
		}
	}

	migrated := map[int]int{}

	if err := addPages(
		c, ctxSrc, ctxDest, pageNrs, usePgCache, *pagesIndRef, pagesDict, &fieldsSrc, &fieldsDest, migrated,
	); err != nil {
		return fmt.Errorf("add pages: %w", err)
	}

	if err := recordInputPageTreeNodes(c, ctxSrc, ctxDest, migrated); err != nil {
		return err
	}

	return finishAddedPages(c, ctxSrc, ctxDest, fieldsDest, migrated)
}

// recordInputPageTreeNodes records in ctxDest the copies of the page tree nodes of
// ctxSrc that AddPages migrated. The output has its own page tree, so the writer
// skips these copies too. Only the migrated objects are checked against the index
// of the input page tree.
func recordInputPageTreeNodes(c context.Context, ctxSrc, ctxDest *model.Context, migrated map[int]int) error {
	idx, err := inputPageTreeIndex(c, ctxSrc.XRefTable)
	if err != nil {
		return fmt.Errorf("add pages: page tree nodes: %w", err)
	}
	for objNr, objNrDest := range migrated {
		if !idx.Nodes[objNr] {
			continue
		}
		if ctxDest.PageTreeNodes == nil {
			ctxDest.PageTreeNodes = map[int]bool{}
		}
		ctxDest.PageTreeNodes[objNrDest] = true
	}
	return nil
}

// inputPageTreeIndex returns the index of the page tree nodes reachable from the root of
// xRefTable. The index is built by one walk of the page tree and cached in xRefTable, so
// a later AddPages call on the same source costs no walk. A node is reachable when the
// walk reaches it through Kids, whatever its /Parent says. A /Pages dict that no Kids entry
// reaches is an orphan and is not in the index. A missing or broken root gives an empty index.
func inputPageTreeIndex(c context.Context, xRefTable *model.XRefTable) (*model.PageTreeIndex, error) {
	if xRefTable.InputPageTree != nil {
		return xRefTable.InputPageTree, nil
	}
	idx := &model.PageTreeIndex{Nodes: map[int]bool{}}
	if rootIndRef, err := xRefTable.Pages(); err == nil && rootIndRef != nil {
		if err := addPageTreeNodeObjNrs(c, xRefTable, *rootIndRef, 0, idx.Nodes, nil); err != nil {
			return nil, err
		}
	}
	xRefTable.InputPageTree = idx
	return idx, nil
}
