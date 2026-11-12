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
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
)

// pageTreeIndexTestPDF builds a PDF with a root page tree node holding inner nodes of
// perNode pages each, so the tree has 1+inner nodes and inner*perNode pages.
func pageTreeIndexTestPDF(inner, perNode int) []byte {
	objs := []string{"<< /Type /Catalog /Pages 2 0 R >>", ""}
	rootKids := make([]string, 0, inner)
	for i := 0; i < inner; i++ {
		nodeNr := len(objs) + 1
		rootKids = append(rootKids, fmt.Sprintf("%d 0 R", nodeNr))
		kids := make([]string, perNode)
		for j := range kids {
			kids[j] = fmt.Sprintf("%d 0 R", nodeNr+1+j)
		}
		objs = append(objs, fmt.Sprintf("<< /Type /Pages /Parent 2 0 R /Kids [%s] /Count %d >>", strings.Join(kids, " "), perNode))
		for range kids {
			objs = append(objs, fmt.Sprintf("<< /Type /Page /Parent %d 0 R /MediaBox [0 0 595 842] /Resources << >> >>", nodeNr))
		}
	}
	objs[1] = fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", strings.Join(rootKids, " "), inner*perNode)

	var buf bytes.Buffer
	buf.WriteString("%PDF-1.7\n")
	offsets := make([]int, len(objs))
	for i, body := range objs {
		offsets[i] = buf.Len()
		fmt.Fprintf(&buf, "%d 0 obj\n%s\nendobj\n", i+1, body)
	}
	xref := buf.Len()
	fmt.Fprintf(&buf, "xref\n0 %d\n0000000000 65535 f \n", len(objs)+1)
	for _, off := range offsets {
		fmt.Fprintf(&buf, "%010d 00000 n \n", off)
	}
	fmt.Fprintf(&buf, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objs)+1, xref)
	return buf.Bytes()
}

// TestAddPageTreeNodeObjNrsVisitsEachDictOnce checks that the walk that builds the page tree
// index dereferences each node and page dict at most once, so building the index is linear in
// the size of the page tree.
func TestAddPageTreeNodeObjNrsVisitsEachDictOnce(t *testing.T) {
	const inner, perNode = 40, 100
	conf := model.NewDefaultConfiguration()
	ctx, err := Read(t.Context(), bytes.NewReader(pageTreeIndexTestPDF(inner, perNode)), conf)
	if err != nil {
		t.Fatal(err)
	}
	root, err := ctx.Pages()
	if err != nil || root == nil {
		t.Fatalf("pages root: %v", err)
	}

	nodes := map[int]bool{}
	visits := 0
	if err := addPageTreeNodeObjNrs(t.Context(), ctx.XRefTable, *root, 0, nodes, &visits); err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1+inner {
		t.Fatalf("page tree nodes: %d, want %d", len(nodes), 1+inner)
	}
	if want := 1 + inner + inner*perNode; visits > want {
		t.Fatalf("walk dereferenced %d dicts, want at most %d", visits, want)
	}
}
