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

package validate

import (
	"context"
	"errors"
	"fmt"

	"github.com/pdfcpu/pdfcpu/internal/contextutil"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

type structureTraversal struct {
	*model.StructureTreeVisit
	ctx context.Context
}

func newStructureTraversal(c context.Context) *structureTraversal {
	return &structureTraversal{StructureTreeVisit: model.NewStructureTreeVisit(), ctx: c}
}

// structureTreeState is shared by all structure tree traversals of one relaxed
// StructTreeRoot validation. Only validateStructTreeRootDict installs it.
type structureTreeState struct {
	// explored holds the elements whose cycle walk has completed. A later
	// ParentTree or IDTree value that reaches them skips them, so a subtree
	// shared by many values is not walked again for each value.
	explored map[int]bool
	deferred map[int]bool // elements handed to deferred semantic validation
	queue    []deferredStructElement
}

// deferredStructElement is a structure element validated after the current descent has unwound.
type deferredStructElement struct {
	objNr int
	d     types.Dict
}

type structureTreeStateKey struct{}

// withStructureTreeState returns c carrying a fresh structure tree state.
func withStructureTreeState(c context.Context) context.Context {
	return context.WithValue(c, structureTreeStateKey{}, &structureTreeState{
		explored: map[int]bool{},
		deferred: map[int]bool{},
	})
}

// structureTreeStateFrom returns the structure tree state carried by c, or nil.
func structureTreeStateFrom(c context.Context) *structureTreeState {
	state, _ := c.Value(structureTreeStateKey{}).(*structureTreeState)
	return state
}

func validateStructureHierarchy(c context.Context, x *model.XRefTable, root types.Dict) error {
	if err := contextutil.Check(c); err != nil {
		return err
	}
	if err := validateStructTreeRootType(x, root); err != nil {
		return err
	}
	// Index values are references, not additional ownership paths.
	return walkStructureHierarchy(c, x, root["K"], "structure tree root K", 1, true, map[int]bool{}, structureTreeStateFrom(c))
}

// hierarchyFrame is one pending node of walkStructureHierarchy. Children are not
// materialized: a K array is walked by index, and element names are built only
// when an error is wrapped.
type hierarchyFrame struct {
	raw        types.Object
	label      string // name of raw, unless inArray is set
	inArray    bool   // raw is element index of the K array named arrayLabel
	index      int
	arrayLabel string
	owner      int
	n          int // object number entered by this frame, 0 for a direct object
	depth      int
	allowArray bool
	started    bool
	isArray    bool         // children are the elements of kids
	kids       types.Array  // K array of this frame
	hasKid     bool         // children are the single K child kid
	kid        types.Object // single K child
	next       int          // index of the next child
}

// name returns the label of f used in error context.
func (f *hierarchyFrame) name() string {
	if f.inArray {
		return fmt.Sprintf("%s[%d]", f.arrayLabel, f.index)
	}
	return f.label
}

// childCount returns the number of children of f.
func (f *hierarchyFrame) childCount() int {
	switch {
	case f.isArray:
		return len(f.kids)
	case f.hasKid:
		return 1
	}
	return 0
}

// nextChild returns the child of f at index next and advances next.
func (f *hierarchyFrame) nextChild() *hierarchyFrame {
	i := f.next
	f.next++
	if f.isArray {
		return &hierarchyFrame{
			raw: f.kids[i], inArray: true, index: i, arrayLabel: f.label,
			owner: validationObjectNumber(0, f.kids[i]), depth: f.depth,
		}
	}
	return &hierarchyFrame{
		raw: f.kid, label: "structure element K", owner: validationObjectNumber(0, f.kid),
		depth: f.depth + 1, allowArray: true,
	}
}

// walkStructureHierarchy walks the structure hierarchy below raw with an explicit stack,
// so that a long chain of structure elements does not grow the Go stack.
// A non-nil seen walks the root hierarchy and detects duplicate elements. A nil seen
// walks for cycles only. A relaxed cycles-only walk skips elements the state has
// already explored and does not report failures other than cancellation, which the
// semantic pass reports with its own context.
func walkStructureHierarchy(c context.Context, x *model.XRefTable, raw types.Object, label string, depth int, allowArray bool, seen map[int]bool, state *structureTreeState) error {
	visit := newStructureTraversal(c)
	stack := []*hierarchyFrame{{raw: raw, label: label, owner: validationObjectNumber(0, raw), depth: depth, allowArray: allowArray}}
	for len(stack) > 0 {
		top := stack[len(stack)-1]
		if !top.started {
			top.started = true
			skip, err := startHierarchyFrame(c, x, visit, top, seen, state)
			if err != nil {
				return unwindHierarchyFrames(x, stack, err)
			}
			if skip {
				stack = stack[:len(stack)-1]
			}
			continue
		}
		if top.next < top.childCount() {
			stack = append(stack, top.nextChild())
			continue
		}
		visit.Leave(top.n)
		if state != nil && top.n != 0 {
			state.explored[top.n] = true
		}
		stack = stack[:len(stack)-1]
	}
	return nil
}

// startHierarchyFrame enters f and collects its children. It reports whether f is
// finished without being walked, because it is a relaxed cycle or already explored.
func startHierarchyFrame(c context.Context, x *model.XRefTable, visit *structureTraversal, f *hierarchyFrame, seen map[int]bool, state *structureTreeState) (bool, error) {
	if err := contextutil.Check(c); err != nil {
		return false, err
	}
	n, err := enterStructureTreeObject(visit, f.raw)
	if err != nil {
		if x.ValidationMode != model.ValidationRelaxed || !errors.Is(err, model.ErrStructureTreeCycle) {
			return false, err
		}
		recordStructureTreeCycleNotice(x, n, model.WrapRecursionError(objectContext(f.name(), f.raw), err))
		return true, nil
	}
	f.n = n
	if seen == nil && state != nil && state.explored[n] {
		visit.Leave(n)
		return true, nil
	}
	o, err := x.Dereference(f.raw)
	if err != nil {
		return false, hierarchyFailure(c, seen, err)
	}
	switch v := o.(type) {
	case types.Dict:
		return false, structureHierarchyDictChildren(c, x, v, f, seen)
	case types.Array:
		// Nested arrays are invalid K children; leave their handling to semantic validation.
		if f.allowArray {
			f.isArray = true
			f.kids = v
		}
	}
	return false, nil
}

// structureHierarchyDictChildren handles the StructElem dict d of frame f and queues its K child.
func structureHierarchyDictChildren(c context.Context, x *model.XRefTable, d types.Dict, f *hierarchyFrame, seen map[int]bool) error {
	typ, _, err := x.DereferenceNameEntry(d, "Type")
	if err != nil {
		return hierarchyFailure(c, seen, err)
	}
	if typ != nil && typ.Value() != "StructElem" {
		return nil
	}
	if x.ValidationMode == model.ValidationStrict {
		if err := checkValidationTree(c, x, "structure tree", f.depth); err != nil {
			return err
		}
	} else if err := contextutil.Check(c); err != nil {
		return err
	}
	if seen != nil && f.n != 0 {
		if seen[f.n] {
			err := fmt.Errorf("obj#%d: %w", f.n, model.ErrStructureTreeDuplicate)
			if x.ValidationMode == model.ValidationStrict {
				return err
			}
			model.ShowDigestedSpecViolationError(err)
			return nil
		}
		seen[f.n] = true
	}
	raw, found := d.Find("K")
	if !found {
		return nil
	}
	f.kid = raw
	f.hasKid = true
	return nil
}

// hierarchyFailure returns err for the root walk. A cycles-only walk returns only a
// cancellation and otherwise leaves the element to the semantic pass.
func hierarchyFailure(c context.Context, seen map[int]bool, err error) error {
	if seen != nil {
		return err
	}
	return contextutil.Check(c)
}

// unwindHierarchyFrames adds the context and object number of each frame to err, innermost
// first, as the recursive walk did. Only the innermost 2*MaxRecursionDepth+2 frames add
// context text, so a hostile chain cannot grow the message without bound. Every current
// working depth is below that bound and keeps its full context.
func unwindHierarchyFrames(x *model.XRefTable, stack []*hierarchyFrame, err error) error {
	limit := 2*x.MaxRecursionDepth() + 2
	for i := len(stack) - 1; i >= 0; i-- {
		f := stack[i]
		if len(stack)-1-i < limit {
			err = model.WrapRecursionError(objectContext(f.name(), f.raw), err)
		}
		err = model.WithValidationErrorObject(err, f.owner)
	}
	return err
}

// deferStructElement queues the structure element d, reached at depth, for validation
// from depth 1 instead of descending past the recursion limit. Each element is queued
// once per state. It reports whether d is deferred, which callers take as validated.
func deferStructElement(visit *structureTraversal, x *model.XRefTable, d types.Dict, objNr, depth int) bool {
	state := structureTreeStateFrom(visit.ctx)
	if state == nil || objNr <= 0 || depth <= x.MaxRecursionDepth() {
		return false
	}
	if state.deferred[objNr] {
		return true
	}
	state.deferred[objNr] = true
	state.queue = append(state.queue, deferredStructElement{objNr: objNr, d: d})
	return true
}

// drainDeferredStructElements validates the queued structure elements, including those
// queued while draining, each from depth 1 with one shared traversal.
func drainDeferredStructElements(c context.Context, x *model.XRefTable, useIDs bool) error {
	state := structureTreeStateFrom(c)
	if state == nil {
		return nil
	}
	visit := newStructureTraversal(c)
	for len(state.queue) > 0 {
		e := state.queue[0]
		state.queue = state.queue[1:]
		if err := validateStructElementDictDepth(x, e.d, e.objNr, useIDs, 1, visit); err != nil {
			return model.WithValidationErrorObject(fmt.Errorf("structure element obj#%d: %w", e.objNr, err), e.objNr)
		}
	}
	return nil
}

// digestKindStructureTreeCycle keys the relaxed mode notice for a structure tree cycle.
const digestKindStructureTreeCycle = "structureTreeCycle"

// recordStructureTreeCycleNotice digests a relaxed mode structure tree cycle once per cycle target object.
// The hierarchy pass and the IDTree traversal can both reach the same cycle.
func recordStructureTreeCycleNotice(x *model.XRefTable, objNr int, err error) {
	if objNr > 0 && !x.MarkDigested(digestKindStructureTreeCycle, objNr) {
		return
	}
	err = model.WithValidationErrorObject(err, objNr)
	x.AddValidationNotice(model.NewValidationNotice(model.NoticePhaseValidate, model.NoticeDigested, err.Error(), err))
}
