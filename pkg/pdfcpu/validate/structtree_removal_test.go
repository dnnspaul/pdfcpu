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
	"io"
	"strings"
	"testing"

	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

// TestTolerateInvalidStructTreeDecision verifies which StructTreeRoot failures tolerateInvalidStructTree tolerates, and whether it keeps or removes them.
func TestTolerateInvalidStructTreeDecision(t *testing.T) {
	structErr := model.WithValidationErrorEntry(errors.New("number tree StructTree Kids[0]: missing dict"), "StructTreeRoot")
	limitErr := model.WithValidationErrorEntry(
		&model.ResourceLimitError{Kind: model.LimitNotices, Limit: 1, Value: 2}, "StructTreeRoot",
	)
	plainErr := model.WithValidationErrorEntry(errors.New("page tree broken"), "Pages")

	cases := []struct {
		name          string
		keep          bool
		remove        bool
		strict        bool
		ctx           context.Context
		err           error
		wantTolerated bool
		wantCtxErr    bool
		readerFail    bool
	}{
		{name: "structure failure", remove: true, ctx: context.Background(), err: structErr, wantTolerated: true},
		{name: "reader failed", remove: true, ctx: context.Background(), err: structErr, readerFail: true},
		{name: "flag off", remove: false, ctx: context.Background(), err: structErr},
		{name: "strict mode", remove: true, strict: true, ctx: context.Background(), err: structErr},
		{name: "resource limit", remove: true, ctx: context.Background(), err: limitErr},
		{name: "not a structure tree failure", remove: true, ctx: context.Background(), err: plainErr},
		{name: "context done", remove: true, ctx: canceledStructTreeContext(), err: structErr, wantCtxErr: true},
		{name: "keep", keep: true, ctx: context.Background(), err: structErr, wantTolerated: true},
		{name: "keep and remove", keep: true, remove: true, ctx: context.Background(), err: structErr, wantTolerated: true},
		{name: "keep reader failed", keep: true, ctx: context.Background(), err: structErr, readerFail: true},
		{name: "keep strict mode", keep: true, strict: true, ctx: context.Background(), err: structErr},
		{name: "keep resource limit", keep: true, ctx: context.Background(), err: limitErr},
		{name: "keep context done", keep: true, ctx: canceledStructTreeContext(), err: structErr, wantCtxErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			conf := model.NewDefaultConfiguration()
			conf.KeepInvalidStructTree = tc.keep
			conf.RemoveInvalidStructTree = tc.remove
			var rs io.ReadSeeker = strings.NewReader("")
			if tc.readerFail {
				rs = failingReadSeeker{strings.NewReader("")}
			}
			ctx, err := model.NewContext(rs, conf)
			if err != nil {
				t.Fatal(err)
			}
			if tc.readerFail {
				if _, err := ctx.Read.RS.Read(make([]byte, 1)); err == nil {
					t.Fatal("read did not fail")
				}
			}
			ctx.XRefTable.Conf = conf
			ctx.XRefTable.ValidationMode = model.ValidationRelaxed
			if tc.strict {
				ctx.XRefTable.ValidationMode = model.ValidationStrict
			}
			rootDict := types.Dict{"StructTreeRoot": types.Dict{}}

			tolerated, ctxErr := tolerateInvalidStructTree(tc.ctx, ctx, rootDict, tc.err)
			if tolerated != tc.wantTolerated {
				t.Fatalf("tolerated: got %t, want %t", tolerated, tc.wantTolerated)
			}
			if ctx.XRefTable.StructTreeInvalid != tc.wantTolerated {
				t.Fatalf("StructTreeInvalid: got %t, want %t", ctx.XRefTable.StructTreeInvalid, tc.wantTolerated)
			}
			wantRemoved := tc.wantTolerated && !tc.keep
			if (ctxErr != nil) != tc.wantCtxErr {
				t.Fatalf("context error: got %v, want error %t", ctxErr, tc.wantCtxErr)
			}
			if tc.wantCtxErr && !errors.Is(ctxErr, context.Canceled) {
				t.Fatalf("got %v, want context.Canceled", ctxErr)
			}
			_, kept := rootDict["StructTreeRoot"]
			if kept == wantRemoved {
				t.Fatalf("StructTreeRoot kept: %t, want %t", kept, !wantRemoved)
			}
			if ctx.XRefTable.StructTreeRemoved != wantRemoved {
				t.Fatalf("StructTreeRemoved: got %t, want %t", ctx.XRefTable.StructTreeRemoved, wantRemoved)
			}
		})
	}
}

// failingReadSeeker seeks like its reader but every Read fails.
type failingReadSeeker struct {
	*strings.Reader
}

// Read implements io.Reader.
func (failingReadSeeker) Read([]byte) (int, error) {
	return 0, errors.New("disk read failed")
}

// canceledStructTreeContext returns a cancelled context.
func canceledStructTreeContext() context.Context {
	c, cancel := context.WithCancelCause(context.Background())
	cancel(errors.New("caller stopped"))
	return c
}
