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
	"strings"
	"testing"

	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

func TestPrepareCoreFontsForWritingStrictFailsWithoutReconstructibleMetrics(t *testing.T) {
	ctx, err := CreateContextWithXRefTable(model.NewStatelessConfiguration(), types.PaperSize["A4"])
	if err != nil {
		t.Fatal(err)
	}
	v := model.V20
	ctx.HeaderVersion = &v
	ctx.XRefTable.ValidationMode = model.ValidationStrict
	font := types.Dict{
		"Type":     types.Name("Font"),
		"Subtype":  types.Name("Type1"),
		"BaseFont": types.Name("Helvetica"),
		"Encoding": types.Name("MacRomanEncoding"),
	}
	if _, err := ctx.IndRefForNewObject(font); err != nil {
		t.Fatal(err)
	}
	before := ctx.ValidationReport()
	err = prepareCoreFontsForWriting(t.Context(), ctx)
	if err == nil || !strings.Contains(err.Error(), "MacRomanEncoding") {
		t.Fatalf("expected strict mode to fail on the MacRomanEncoding font, got %v", err)
	}
	if !ctx.ValidationReport().Since(before).Empty() {
		t.Fatal("strict mode must not digest the failure")
	}
}
