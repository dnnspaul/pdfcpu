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
	"fmt"
	"strings"
	"testing"

	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

var validationModes = []struct {
	name string
	mode int
}{
	{"strict", model.ValidationStrict},
	{"relaxed", model.ValidationRelaxed},
}

func validationModeTestContext(t *testing.T, mode int, objects map[int]types.Object) *model.Context {
	t.Helper()
	ctx, err := model.NewContext(strings.NewReader(""), model.NewDefaultConfiguration())
	if err != nil {
		t.Fatal(err)
	}
	version := model.V17
	ctx.HeaderVersion = &version
	ctx.XRefTable.ValidationMode = mode
	for objNr, o := range objects {
		ctx.Table[objNr] = model.NewXRefTableEntryGen0(o)
	}
	return ctx
}

// requireValidationModeOutcome checks that strict mode rejects with wantErr and
// relaxed mode accepts with a single digested notice carrying wantNotice.
func requireValidationModeOutcome(t *testing.T, ctx *model.Context, err error, wantErr, wantNotice string, wantObjNr int) {
	t.Helper()
	notices := ctx.ValidationReport().Notices()
	if ctx.XRefTable.ValidationMode == model.ValidationStrict {
		if err == nil || !strings.Contains(err.Error(), wantErr) {
			t.Fatalf("got %v, want error containing %q", err, wantErr)
		}
		if len(notices) != 0 {
			t.Fatalf("notices: got %+v, want none", notices)
		}
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	if len(notices) != 1 {
		t.Fatalf("notice count: got %d, want 1: %+v", len(notices), notices)
	}
	notice := notices[0]
	if notice.Message != wantNotice || notice.ObjectNumber != wantObjNr {
		t.Fatalf("notice: got %+v, want %q (obj#%d)", notice, wantNotice, wantObjNr)
	}
	if notice.Phase != model.NoticePhaseValidate || notice.Disposition != model.NoticeDigested {
		t.Fatalf("notice classification: got %+v", notice)
	}
}

// TestSetOCGStateMissingOperandsModes digests operators without OCG operands in relaxed mode only.
func TestSetOCGStateMissingOperandsModes(t *testing.T) {
	ocg := *types.NewIndirectRef(5, 0)
	for _, tc := range []struct {
		name   string
		state  types.Array
		want   string
		notice string
	}{
		{"preceding", types.Array{types.Name("OFF"), types.Name("ON"), ocg}, "SetOCGState.State[1]: preceding operator has no OCG operands", "SetOCGState.State[1]: 1 operator(s) without OCG operands"},
		{"trailing", types.Array{types.Name("OFF"), ocg, types.Name("ON")}, "SetOCGState.State[2]: operator has no OCG operands", "SetOCGState.State[2]: 1 operator(s) without OCG operands"},
	} {
		for _, m := range validationModes {
			t.Run(tc.name+"/"+m.name, func(t *testing.T) {
				ctx := validationModeTestContext(t, m.mode, map[int]types.Object{
					5: types.Dict{"Type": types.Name("OCG"), "Name": types.StringLiteral("Layer")},
				})
				d := types.Dict{"S": types.Name("SetOCGState"), "State": tc.state}
				err := validateSetOCGStateActionDict(ctx.XRefTable, d, 0, "SetOCGState")
				requireValidationModeOutcome(t, ctx, err, tc.want, tc.notice, 0)
			})
		}
	}
}

// TestSetOCGStateSharedActionDigestedOnce digests a shared SetOCGState action object with a dangling
// operator once per action object, however many page visits reference it. The notice names the action.
func TestSetOCGStateSharedActionDigestedOnce(t *testing.T) {
	const want = "SetOCGState.State[1]: 1 operator(s) without OCG operands"
	ctx := validationModeTestContext(t, model.ValidationRelaxed, map[int]types.Object{
		5: types.Dict{"Type": types.Name("OCG"), "Name": types.StringLiteral("Layer")},
		7: types.Dict{"S": types.Name("SetOCGState"), "State": types.Array{types.Name("OFF"), types.Name("ON"), *types.NewIndirectRef(5, 0)}},
	})
	action := ctx.Table[7].Object.(types.Dict)
	for i := 0; i < 100; i++ {
		if err := validateActionDictObject(t.Context(), ctx.XRefTable, action, *types.NewIndirectRef(7, 0), "shared SetOCGState"); err != nil {
			t.Fatal(err)
		}
	}
	notices := ctx.ValidationReport().Notices()
	if len(notices) != 1 || notices[0].Message != want || notices[0].ObjectNumber != 7 {
		t.Fatalf("notices: got %+v, want one notice %q for obj#7", notices, want)
	}
}

// TestSetOCGStateDistinctActionsDigestedSeparately digests each distinct shared action object once,
// so two action objects give two notices, each naming its own object.
func TestSetOCGStateDistinctActionsDigestedSeparately(t *testing.T) {
	const want = "SetOCGState.State[1]: 1 operator(s) without OCG operands"
	state := func() types.Array {
		return types.Array{types.Name("OFF"), types.Name("ON"), *types.NewIndirectRef(5, 0)}
	}
	ctx := validationModeTestContext(t, model.ValidationRelaxed, map[int]types.Object{
		5: types.Dict{"Type": types.Name("OCG"), "Name": types.StringLiteral("Layer")},
		7: types.Dict{"S": types.Name("SetOCGState"), "State": state()},
		8: types.Dict{"S": types.Name("SetOCGState"), "State": state()},
	})
	for _, objNr := range []int{7, 8, 7, 8} {
		action := ctx.Table[objNr].Object.(types.Dict)
		if err := validateActionDictObject(t.Context(), ctx.XRefTable, action, *types.NewIndirectRef(objNr, 0), "shared SetOCGState"); err != nil {
			t.Fatal(err)
		}
	}
	notices := ctx.ValidationReport().Notices()
	if len(notices) != 2 || notices[0].ObjectNumber != 7 || notices[1].ObjectNumber != 8 || notices[0].Message != want || notices[1].Message != want {
		t.Fatalf("notices: got %+v, want one notice each for obj#7 and obj#8", notices)
	}
}

// TestSetOCGStateInlineActionDigestedOncePerOwner digests an inline SetOCGState action validated many
// times under one owner object once. The action has no object number, so the notice names the owner.
// Inline actions are keyed on the object that holds them, so a second owner gets its own notice.
func TestSetOCGStateInlineActionDigestedOncePerOwner(t *testing.T) {
	const want = "SetOCGState.State[1]: 1 operator(s) without OCG operands"
	ctx := validationModeTestContext(t, model.ValidationRelaxed, map[int]types.Object{
		5: types.Dict{"Type": types.Name("OCG"), "Name": types.StringLiteral("Layer")},
	})
	action := types.Dict{"S": types.Name("SetOCGState"), "State": types.Array{types.Name("OFF"), types.Name("ON"), *types.NewIndirectRef(5, 0)}}
	origin := defaultActiveContentOrigin()
	origin.ownerObjNr = 9
	for i := 0; i < 100; i++ {
		if err := validateActionDictObjectWithOrigin(t.Context(), ctx.XRefTable, action, action, "inline SetOCGState", origin); err != nil {
			t.Fatal(err)
		}
	}
	origin.ownerObjNr = 10
	if err := validateActionDictObjectWithOrigin(t.Context(), ctx.XRefTable, action, action, "inline SetOCGState", origin); err != nil {
		t.Fatal(err)
	}
	notices := ctx.ValidationReport().Notices()
	if len(notices) != 2 || notices[0].Message != want || notices[1].Message != want || notices[0].ObjectNumber != 9 || notices[1].ObjectNumber != 10 {
		t.Fatalf("notices: got %+v, want one notice %q each for owners obj#9 and obj#10", notices, want)
	}
}

// TestSetOCGStateOperatorFloodNotice digests a State array of 1000 operators without operands
// as one notice in relaxed mode. Strict mode still rejects at the first offender.
func TestSetOCGStateOperatorFloodNotice(t *testing.T) {
	state := make(types.Array, 1000)
	for i := range state {
		state[i] = types.Name("ON")
	}
	const wantNotice = "SetOCGState.State[1]: 1000 operator(s) without OCG operands"
	for _, m := range validationModes {
		t.Run(m.name, func(t *testing.T) {
			ctx := validationModeTestContext(t, m.mode, nil)
			d := types.Dict{"S": types.Name("SetOCGState"), "State": state}
			err := validateSetOCGStateActionDict(ctx.XRefTable, d, 0, "SetOCGState")
			notices := ctx.ValidationReport().Notices()
			if m.mode == model.ValidationStrict {
				if err == nil || !strings.Contains(err.Error(), "SetOCGState.State[1]: preceding operator has no OCG operands") {
					t.Fatalf("got %v, want strict rejection at State[1]", err)
				}
				if len(notices) != 0 {
					t.Fatalf("notices: got %+v, want none", notices)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(notices) != 1 || notices[0].Message != wantNotice {
				t.Fatalf("notices: got %+v, want one notice %q", notices, wantNotice)
			}
		})
	}
}

// TestSetOCGStateInlineActionInSharedAdditionalActions digests an inline SetOCGState action held by one
// indirect /AA dict (obj#3) once, however many owners reach that dict. The notice anchors at obj#3.
func TestSetOCGStateInlineActionInSharedAdditionalActions(t *testing.T) {
	const want = "SetOCGState.State[1]: 1 operator(s) without OCG operands"
	ctx := validationModeTestContext(t, model.ValidationRelaxed, map[int]types.Object{
		3: types.Dict{"E": types.Dict{"S": types.Name("SetOCGState"), "State": types.Array{types.Name("OFF"), types.Name("ON"), *types.NewIndirectRef(5, 0)}}},
		5: types.Dict{"Type": types.Name("OCG"), "Name": types.StringLiteral("Layer")},
	})
	dict := types.Dict{"AA": *types.NewIndirectRef(3, 0)}
	for _, owner := range []int{10, 11, 10} {
		if err := validateAdditionalActionsWithOwner(t.Context(), ctx.XRefTable, dict, owner, "annot", "AA", OPTIONAL, model.V15, "fieldOrAnnot"); err != nil {
			t.Fatal(err)
		}
	}
	notices := ctx.ValidationReport().Notices()
	if len(notices) != 1 || notices[0].Message != want || notices[0].ObjectNumber != 3 {
		t.Fatalf("notices: got %+v, want one notice %q for obj#3", notices, want)
	}
}

// TestSetOCGStateIndirectNextArrayDigestedOnce digests an inline SetOCGState action in an indirect Next
// array (obj#8) once, however many owners reach that array. The notice anchors at obj#8.
func TestSetOCGStateIndirectNextArrayDigestedOnce(t *testing.T) {
	const want = "SetOCGState.State[1]: 1 operator(s) without OCG operands"
	ctx := validationModeTestContext(t, model.ValidationRelaxed, map[int]types.Object{
		5: types.Dict{"Type": types.Name("OCG"), "Name": types.StringLiteral("Layer")},
		8: types.Array{types.Dict{"S": types.Name("SetOCGState"), "State": types.Array{types.Name("OFF"), types.Name("ON"), *types.NewIndirectRef(5, 0)}}},
	})
	next := *types.NewIndirectRef(8, 0)
	for _, owner := range []int{10, 11, 12} {
		origin := defaultActiveContentOrigin()
		origin.ownerObjNr = owner
		if err := validateNextAction(t.Context(), ctx.XRefTable, next, 0, model.NewActionVisit(), origin); err != nil {
			t.Fatal(err)
		}
	}
	notices := ctx.ValidationReport().Notices()
	if len(notices) != 1 || notices[0].Message != want || notices[0].ObjectNumber != 8 {
		t.Fatalf("notices: got %+v, want one notice %q for obj#8", notices, want)
	}
}

// TestSetOCGStateInlineNextInSharedAdditionalActions digests an inline Next action under an inline action
// in one indirect /AA dict (obj#3) once, however many owners reach that dict. The notice anchors at obj#3.
func TestSetOCGStateInlineNextInSharedAdditionalActions(t *testing.T) {
	const want = "SetOCGState.State[1]: 1 operator(s) without OCG operands"
	ctx := validationModeTestContext(t, model.ValidationRelaxed, map[int]types.Object{
		3: types.Dict{"E": types.Dict{
			"S":     types.Name("SetOCGState"),
			"State": types.Array{types.Name("ON"), *types.NewIndirectRef(5, 0)},
			"Next":  types.Dict{"S": types.Name("SetOCGState"), "State": types.Array{types.Name("OFF"), types.Name("ON"), *types.NewIndirectRef(5, 0)}},
		}},
		5: types.Dict{"Type": types.Name("OCG"), "Name": types.StringLiteral("Layer")},
	})
	dict := types.Dict{"AA": *types.NewIndirectRef(3, 0)}
	for _, owner := range []int{10, 11} {
		if err := validateAdditionalActionsWithOwner(t.Context(), ctx.XRefTable, dict, owner, "annot", "AA", OPTIONAL, model.V15, "fieldOrAnnot"); err != nil {
			t.Fatal(err)
		}
	}
	notices := ctx.ValidationReport().Notices()
	if len(notices) != 1 || notices[0].Message != want || notices[0].ObjectNumber != 3 {
		t.Fatalf("notices: got %+v, want one notice %q for obj#3", notices, want)
	}
}

// TestSetOCGStateInlineNextUnderIndirectAction digests an inline Next action under an indirect action
// (obj#7) once, however many pages reference that action. The notice anchors at obj#7.
func TestSetOCGStateInlineNextUnderIndirectAction(t *testing.T) {
	const want = "SetOCGState.State[1]: 1 operator(s) without OCG operands"
	ctx := validationModeTestContext(t, model.ValidationRelaxed, map[int]types.Object{
		5: types.Dict{"Type": types.Name("OCG"), "Name": types.StringLiteral("Layer")},
		7: types.Dict{
			"S":     types.Name("SetOCGState"),
			"State": types.Array{types.Name("ON"), *types.NewIndirectRef(5, 0)},
			"Next":  types.Dict{"S": types.Name("SetOCGState"), "State": types.Array{types.Name("OFF"), types.Name("ON"), *types.NewIndirectRef(5, 0)}},
		},
	})
	action := ctx.Table[7].Object.(types.Dict)
	for i := 0; i < 3; i++ {
		if err := validateActionDictObject(t.Context(), ctx.XRefTable, action, *types.NewIndirectRef(7, 0), "shared SetOCGState"); err != nil {
			t.Fatal(err)
		}
	}
	notices := ctx.ValidationReport().Notices()
	if len(notices) != 1 || notices[0].Message != want || notices[0].ObjectNumber != 7 {
		t.Fatalf("notices: got %+v, want one notice %q for obj#7", notices, want)
	}
}

// TestSimpleFontWidthsCountModes digests any non-empty Widths count mismatch in relaxed mode only.
func TestSimpleFontWidthsCountModes(t *testing.T) {
	for _, count := range []int{190, 223, 230} {
		for _, m := range validationModes {
			t.Run(fmt.Sprintf("%d/%s", count, m.name), func(t *testing.T) {
				ctx := validationModeTestContext(t, m.mode, nil)
				d := types.Dict{
					"Type":      types.Name("Font"),
					"Subtype":   types.Name("TrueType"),
					"BaseFont":  types.Name("ShortWidths"),
					"FirstChar": types.Integer(32),
					"LastChar":  types.Integer(255),
					"Widths":    types.NewIntegerArray(make([]int, count)...),
				}
				_, err := validateTrueTypeFontDict(ctx.XRefTable, d, 2)
				want := fmt.Sprintf("trueTypeFontDict.Widths: invalid array length %d, expected 224 to match FirstChar 32 and LastChar 255", count)
				requireValidationModeOutcome(t, ctx, err, want, want, 2)
			})
		}
	}
}

func luminositySoftMaskGroup() *types.StreamDict {
	return &types.StreamDict{Dict: types.Dict{
		"Group": types.Dict{"S": types.Name("Transparency"), "CS": types.Name("DeviceRGB")},
	}}
}

// TestSoftMaskBackdropLengthModes digests a BC length mismatch in relaxed mode only.
func TestSoftMaskBackdropLengthModes(t *testing.T) {
	const want = "softMaskDict.BC: invalid array length 1, expected 3"
	for _, m := range validationModes {
		t.Run(m.name, func(t *testing.T) {
			ctx := validationModeTestContext(t, m.mode, nil)
			d := types.Dict{"S": types.Name("Luminosity"), "BC": types.NewIntegerArray(0)}
			err := validateSoftMaskBackdrop(t.Context(), ctx.XRefTable, d, luminositySoftMaskGroup(), 0, 0, true)
			requireValidationModeOutcome(t, ctx, err, want, want, 0)
		})
	}
}

// TestSoftMaskBackdropValuesRelaxed keeps rejecting non-numeric BC values after a digested length mismatch.
func TestSoftMaskBackdropValuesRelaxed(t *testing.T) {
	ctx := validationModeTestContext(t, model.ValidationRelaxed, nil)
	d := types.Dict{"S": types.Name("Luminosity"), "BC": types.Array{types.Name("Black")}}
	err := validateSoftMaskBackdrop(t.Context(), ctx.XRefTable, d, luminositySoftMaskGroup(), 0, 0, true)
	if err == nil || !strings.Contains(err.Error(), "softMaskDict.BC[0]: expected number") {
		t.Fatalf("got %v, want non-numeric BC error", err)
	}
}
