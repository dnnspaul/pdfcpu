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

package api

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"slices"
	"testing"

	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

func optionalContentTestStream(entries, content string) string {
	return fmt.Sprintf("<< %s /Length %d >>\nstream\n%s\nendstream", entries, len(content), content)
}

// optionalContentPDF has two pages. Page 1 draws its text in layer oc1 (object
// 5, "Off"), page 2 in layer oc2 (object 6, "On"). d is the default
// configuration of the catalog's /OCProperties.
func optionalContentPDF(d string) []byte {
	const content = "/OC /%s BDC BT /F1 24 Tf 20 100 Td (Hidden) Tj ET EMC"
	return failureTestPDF(
		fmt.Sprintf("<< /Type /Catalog /Pages 2 0 R /OCProperties << /OCGs [5 0 R 6 0 R] /D %s >> >>", d),
		"<< /Type /Pages /Kids [3 0 R 8 0 R] /Count 2 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 300 200] /Contents 4 0 R /Resources << /Font << /F1 7 0 R >> /Properties << /oc1 5 0 R >> >> >>",
		optionalContentTestStream("", fmt.Sprintf(content, "oc1")),
		"<< /Type /OCG /Name (Off) >>",
		"<< /Type /OCG /Name (On) >>",
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /WinAnsiEncoding >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 300 200] /Contents 9 0 R /Resources << /Font << /F1 7 0 R >> /Properties << /oc2 6 0 R >> >> >>",
		optionalContentTestStream("", fmt.Sprintf(content, "oc2")),
	)
}

// optionalContentSummary lists the optional content groups of a PDF and the
// group names in the default configuration's ON and OFF arrays.
type optionalContentSummary struct {
	ocgs, on, off []string
}

func optionalContentNames(t *testing.T, ctx *model.Context, a types.Array) []string {
	t.Helper()
	var names []string
	for _, o := range a {
		d, err := ctx.DereferenceDict(o)
		if err != nil {
			t.Fatal(err)
		}
		name, _ := d["Name"].(types.StringLiteral)
		names = append(names, string(name))
	}
	return names
}

// optionalContentStateOf reads pdf back and summarizes its optional content.
func optionalContentStateOf(t *testing.T, pdf []byte) optionalContentSummary {
	t.Helper()
	ctx, err := ReadAndValidate(t.Context(), bytes.NewReader(pdf), model.NewDefaultConfiguration())
	if err != nil {
		t.Fatal(err)
	}
	rootDict, err := ctx.Catalog()
	if err != nil {
		t.Fatal(err)
	}
	ocp, err := ctx.DereferenceDict(rootDict["OCProperties"])
	if err != nil {
		t.Fatal(err)
	}
	ocgs, err := ctx.DereferenceArray(ocp["OCGs"])
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := ctx.DereferenceDict(ocp["D"])
	if err != nil {
		t.Fatal(err)
	}
	on, err := ctx.DereferenceArray(cfg["ON"])
	if err != nil {
		t.Fatal(err)
	}
	off, err := ctx.DereferenceArray(cfg["OFF"])
	if err != nil {
		t.Fatal(err)
	}
	return optionalContentSummary{
		ocgs: optionalContentNames(t, ctx, ocgs),
		on:   optionalContentNames(t, ctx, on),
		off:  optionalContentNames(t, ctx, off),
	}
}

func TestExtractPagesKeepsHiddenLayerHidden(t *testing.T) {
	var pages [][]byte
	digest := func(r io.Reader, _ int) error {
		b, err := io.ReadAll(r)
		pages = append(pages, b)
		return err
	}
	pdf := optionalContentPDF("<< /OFF [5 0 R] >>")
	if err := ExtractPages(t.Context(), bytes.NewReader(pdf), nil, digest, model.NewDefaultConfiguration()); err != nil {
		t.Fatal(err)
	}
	if len(pages) != 2 {
		t.Fatalf("expected 2 extracted pages, got %d", len(pages))
	}
	want := []optionalContentSummary{
		{ocgs: []string{"Off"}, off: []string{"Off"}},
		{ocgs: []string{"On"}},
	}
	for i, page := range pages {
		got := optionalContentStateOf(t, page)
		if !slices.Equal(got.ocgs, want[i].ocgs) || !slices.Equal(got.on, want[i].on) || !slices.Equal(got.off, want[i].off) {
			t.Fatalf("page %d: got %+v, want %+v", i+1, got, want[i])
		}
	}
}

func TestMergeKeepsHiddenLayerOfLaterInput(t *testing.T) {
	tests := []struct {
		name            string
		first, second   []byte
		wantOCGs        int
		wantON, wantOFF int
	}{
		{"hidden layer of second input", failureTestValidPDF(), optionalContentPDF("<< /OFF [5 0 R] >>"), 2, 0, 1},
		{"hidden layers of both inputs", optionalContentPDF("<< /OFF [5 0 R] >>"), optionalContentPDF("<< /OFF [5 0 R] >>"), 4, 0, 2},
		{"second input with other base state", optionalContentPDF("<< /OFF [5 0 R] >>"), optionalContentPDF("<< /BaseState /OFF /ON [6 0 R] >>"), 4, 0, 2},
		{"first input with other base state", optionalContentPDF("<< /BaseState /OFF /ON [6 0 R] >>"), optionalContentPDF("<< /OFF [5 0 R] >>"), 4, 2, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			rsc := []io.ReadSeeker{bytes.NewReader(tc.first), bytes.NewReader(tc.second)}
			if err := MergeRaw(t.Context(), rsc, &out, false, model.NewDefaultConfiguration()); err != nil {
				t.Fatal(err)
			}
			got := optionalContentStateOf(t, out.Bytes())
			if len(got.ocgs) != tc.wantOCGs || len(got.on) != tc.wantON || len(got.off) != tc.wantOFF {
				t.Fatalf("got %d OCGs, %d ON, %d OFF; want %d, %d, %d", len(got.ocgs), len(got.on), len(got.off), tc.wantOCGs, tc.wantON, tc.wantOFF)
			}
		})
	}
}

func TestMergeReportsUncarriedOptionalContentConfigs(t *testing.T) {
	// The second input has an alternate configuration and a non-default intent,
	// neither of which the merge carries over.
	second := failureTestPDF(
		"<< /Type /Catalog /Pages 2 0 R /OCProperties << /OCGs [4 0 R] /D << /Intent /Design >> /Configs [<< /Name (Alt) /OFF [4 0 R] >>] >> >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 595 842] >>",
		"<< /Type /OCG /Name (Layer) >>",
	)
	rsc := []io.ReadSeeker{
		bytes.NewReader(optionalContentPDF("<< >>")),
		bytes.NewReader(second),
	}
	report, err := MergeRawWithReport(context.Background(), rsc, io.Discard, model.NewDefaultConfiguration(), MergeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	paths := map[string]bool{}
	for _, n := range report.Output.Notices() {
		if n.Disposition != model.NoticeSkipped {
			continue
		}
		paths[n.Path] = true
	}
	for _, want := range []string{"OCProperties/Configs", "OCProperties/D/Intent"} {
		if !paths[want] {
			t.Fatalf("missing skipped notice %s, got %v", want, paths)
		}
	}
}
