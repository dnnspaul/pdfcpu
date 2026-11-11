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
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

func TestMergeXRefTablesReturnsCancellation(t *testing.T) {
	c, cancel := context.WithCancel(t.Context())
	cancel()

	if err := MergeXRefTables(c, "", nil, nil, false, false); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want context.Canceled", err)
	}
}

func testMergeContext(t *testing.T) *model.Context {
	t.Helper()
	conf := model.NewDefaultConfiguration()
	conf.ValidationMode = model.ValidationRelaxed
	ctx, err := ReadFile(t.Context(), filepath.Join("..", "samples", "create", "primitives", "textAndAlignment.pdf"), conf)
	if err != nil {
		t.Fatal(err)
	}
	return ctx
}

func TestMergeXRefTablesPageTreeErrorIncludesMergeContext(t *testing.T) {
	src := testMergeContext(t)
	dest := testMergeContext(t)
	dest.PageCount++

	err := MergeXRefTables(t.Context(), "source.pdf", src, dest, false, false)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "merge page tree: append source pages") {
		t.Fatalf("expected merge page tree context, got %q", err.Error())
	}
	if !strings.Contains(err.Error(), "corrupt page node") {
		t.Fatalf("expected lower page tree context, got %q", err.Error())
	}
}

func TestMergeXRefTablesZipPageTreeErrorIncludesMergeContext(t *testing.T) {
	src := testMergeContext(t)
	dest := testMergeContext(t)
	dest.RootDict = nil
	dest.Root = nil

	err := MergeXRefTables(t.Context(), "source.pdf", src, dest, true, false)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "merge page tree: zip source pages") {
		t.Fatalf("expected zip merge page tree context, got %q", err.Error())
	}
}

func TestMergeXRefTablesNilContextErrors(t *testing.T) {
	err := MergeXRefTables(t.Context(), "source.pdf", nil, testMergeContext(t), false, false)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "merge: missing context") {
		t.Fatalf("expected missing context error, got %q", err.Error())
	}

	src := testMergeContext(t)
	src.Root = nil
	err = MergeXRefTables(t.Context(), "source.pdf", src, testMergeContext(t), false, false)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "missing source root") {
		t.Fatalf("expected missing source root error, got %q", err.Error())
	}
}

func TestEnsureOutlinesNilContextReturnsError(t *testing.T) {
	err := EnsureOutlines(t.Context(), nil, "source.pdf", false)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "ensure outlines: missing context") {
		t.Fatalf("expected missing context error, got %q", err.Error())
	}
}

// TestRenameOrphanWidgetFieldIgnoresNonNameSubtype treats a dict whose Subtype is not a name as not a widget.
// The scan covers every source dict, eg. a Mac file information dict with an integer Subtype.
func TestRenameOrphanWidgetFieldIgnoresNonNameSubtype(t *testing.T) {
	ctx := testMergeContext(t)
	for _, subtype := range []types.Object{types.Integer(1413830740), types.StringLiteral("Widget")} {
		d := types.Dict{"Subtype": subtype, "T": types.StringLiteral("f")}
		if err := renameOrphanWidgetField(ctx, d, "0"); err != nil {
			t.Fatalf("Subtype %v: %v", subtype, err)
		}
		if d["T"] != types.StringLiteral("f") {
			t.Fatalf("Subtype %v: T renamed to %v", subtype, d["T"])
		}
	}

	d := types.Dict{"Subtype": types.Name("Widget"), "T": types.StringLiteral("f")}
	if err := renameOrphanWidgetField(ctx, d, "0"); err != nil {
		t.Fatal(err)
	}
	if d["T"] != types.StringLiteral("0.f") {
		t.Fatalf("widget T: got %v, want 0.f", d["T"])
	}
}

// TestMergeXRefTablesSourceWithIntegerSubtype merges a source holding a dict with an integer Subtype.
func TestMergeXRefTablesSourceWithIntegerSubtype(t *testing.T) {
	src, dest := testMergeContext(t), testMergeContext(t)
	// ReadFile leaves PageCount to validation. textAndAlignment.pdf has one page.
	src.PageCount, dest.PageCount = 1, 1
	if _, err := src.IndRefForNewObject(types.Dict{"Subtype": types.Integer(1413830740)}); err != nil {
		t.Fatal(err)
	}
	if err := MergeXRefTables(t.Context(), "source.pdf", src, dest, false, false); err != nil {
		t.Fatal(err)
	}
	if dest.PageCount != 2 {
		t.Fatalf("PageCount: got %d, want 2", dest.PageCount)
	}
}
