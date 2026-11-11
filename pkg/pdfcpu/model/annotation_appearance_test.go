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
	"errors"
	"testing"

	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

// TestCheckAnnotationAppearanceMissing verifies a missing PDF 2.0 appearance dictionary is classified by type.
func TestCheckAnnotationAppearanceMissing(t *testing.T) {
	v20 := V20
	xRefTable := &XRefTable{HeaderVersion: &v20}
	d := types.Dict{
		"Subtype": types.Name("Square"),
		"Rect":    types.Array{types.Integer(0), types.Integer(0), types.Integer(10), types.Integer(10)},
	}

	err := xRefTable.CheckAnnotationAppearance(d)
	if !errors.Is(err, ErrMissingAnnotationAppearance) {
		t.Fatalf("got %v, want ErrMissingAnnotationAppearance", err)
	}
	if err.Error() != "PDF 2.0 Square annotation requires an appearance dictionary (AP)" {
		t.Fatalf("got message %q", err.Error())
	}

	d["AP"] = types.Dict{"N": types.Name("Appearance")}
	if err := xRefTable.CheckAnnotationAppearance(d); err != nil {
		t.Fatalf("got %v, want nil with AP present", err)
	}
}
