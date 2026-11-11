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
	"context"
	"runtime"
	"slices"
	"testing"
	"time"

	pdfcpuLog "github.com/pdfcpu/pdfcpu/pkg/log"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

// sparseTestContext returns a context whose trailer Size is far larger than its table,
// like a file with an object numbered 2^31-1.
func sparseTestContext(t *testing.T, size int) *model.Context {
	t.Helper()
	ctx, err := model.NewContext(bytes.NewReader(nil), model.NewDefaultConfiguration())
	if err != nil {
		t.Fatal(err)
	}
	version := model.V17
	ctx.HeaderVersion = &version
	ctx.Size = &size
	ctx.Table[0] = model.NewFreeHeadXRefTableEntry()
	ctx.Table[1] = model.NewXRefTableEntryGen0(types.Dict{})
	ctx.Table[2] = model.NewXRefTableEntryGen0(types.Dict{})
	ctx.Table[size-1] = model.NewXRefTableEntryGen0(types.Dict{})
	return ctx
}

// TestDeleteRedundantObjectsSparseTable walks the table instead of every object number below Size.
func TestDeleteRedundantObjectsSparseTable(t *testing.T) {
	ctx := sparseTestContext(t, 1<<31)
	ctx.Optimize.DuplicateInfoObjects[2] = true
	ctx.Optimize.DuplicateInfoObjects[1<<31-1] = true
	ctx.Write.SetWriteOffset(1<<31 - 1)

	c, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := deleteRedundantObjects(c, ctx); err != nil {
		t.Fatal(err)
	}
	if !ctx.Table[2].Free {
		t.Fatal("obj#2: duplicate info object not freed")
	}
	if ctx.Table[1].Free || ctx.Table[1<<31-1].Free {
		t.Fatal("unrelated object freed")
	}
	if ctx.Optimize.DuplicateInfoObjects[1<<31-1] {
		t.Fatal("written object still marked duplicate")
	}
}

// TestLogWriteStatsSkippedWithoutStats does no stats work unless stats are logged or written to a file.
func TestLogWriteStatsSkippedWithoutStats(t *testing.T) {
	// pkg/log exposes no getter for the stats logger, only StatsEnabled.
	// Restore the default stats logger if it was on, so other tests are not affected.
	wasEnabled := pdfcpuLog.StatsEnabled()
	pdfcpuLog.SetStatsLogger(nil)
	t.Cleanup(func() {
		if wasEnabled {
			pdfcpuLog.SetDefaultStatsLogger()
		}
	})

	ctx := sparseTestContext(t, 8)
	logWriteStats(ctx)
	if ctx.Optimize.NonReferencedObjs != nil {
		t.Fatalf("NonReferencedObjs: got %v, want nil", ctx.Optimize.NonReferencedObjs)
	}
}

// TestLogWriteStatsNonReferencedObjs records unwritten in-use objects below Size for the stats file.
func TestLogWriteStatsNonReferencedObjs(t *testing.T) {
	ctx := sparseTestContext(t, 8)
	ctx.StatsFileName = "stats.csv"
	ctx.Table[5] = &model.XRefTableEntry{Free: true}
	ctx.Table[9] = model.NewXRefTableEntryGen0(types.Dict{})
	ctx.Write.SetWriteOffset(2)
	logWriteStats(ctx)
	if want := []int{1, 7}; !slices.Equal(ctx.Optimize.NonReferencedObjs, want) {
		t.Fatalf("NonReferencedObjs: got %v, want %v", ctx.Optimize.NonReferencedObjs, want)
	}
}

// TestRemapSourceXRefTableSparseTable sizes the remapped table by its entries, not by the source Size.
func TestRemapSourceXRefTableSparseTable(t *testing.T) {
	const size = 1 << 22
	ctx := sparseTestContext(t, size)
	lookup := map[int]int{1: 11, 2: 12, size - 1: 13}

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	remapSourceXRefTable(ctx, lookup)
	runtime.ReadMemStats(&after)

	if got := after.TotalAlloc - before.TotalAlloc; got > 1<<20 {
		t.Fatalf("allocated %d bytes for a 4 entry table", got)
	}
	if len(ctx.Table) != 4 || ctx.Table[13] == nil || ctx.Table[0] == nil {
		t.Fatalf("remapped table: got %v", ctx.Table)
	}
}
