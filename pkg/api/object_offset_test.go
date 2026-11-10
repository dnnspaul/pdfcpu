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
	"fmt"
	"testing"

	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
)

// earlyObjectOffsetTestPDF returns a PDF whose xref entries for objects 2 and 3 point 3 bytes early,
// into the "bj\n" of the preceding endobj. startxref points at the "endobj" line before the xref
// section, which makes the reader repair xref by a fixed offset instead of rebuilding the table.
func earlyObjectOffsetTestPDF() []byte {
	var buf bytes.Buffer
	buf.WriteString("%PDF-1.7\n")

	objects := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 200 200] /Resources << >> >>",
	}
	offsets := make([]int, len(objects))
	for i, object := range objects {
		offsets[i] = appendValidationTestObject(&buf, i+1, object)
	}

	xrefOffset := buf.Len()
	fmt.Fprintf(&buf, "xref\n0 %d\n", len(objects)+1)
	buf.WriteString("0000000000 65535 f \n")
	for i, offset := range offsets {
		if i > 0 {
			offset -= 3
		}
		fmt.Fprintf(&buf, "%010d 00000 n \n", offset)
	}
	fmt.Fprintf(
		&buf,
		"trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n",
		len(objects)+1, xrefOffset-len("endobj\n"),
	)
	return buf.Bytes()
}

// TestValidateEarlyObjectOffsets accepts object offsets that land inside the preceding endobj keyword.
func TestValidateEarlyObjectOffsets(t *testing.T) {
	pdf := earlyObjectOffsetTestPDF()
	for _, tc := range []struct {
		name string
		mode int
	}{
		{"strict", model.ValidationStrict},
		{"relaxed", model.ValidationRelaxed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			conf := model.NewDefaultConfiguration()
			conf.ValidationMode = tc.mode
			if err := Validate(t.Context(), bytes.NewReader(pdf), conf, nil); err != nil {
				t.Fatal(err)
			}
		})
	}
}
