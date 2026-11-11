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
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/pdfcpu/pdfcpu/internal/contextutil"
	"github.com/pdfcpu/pdfcpu/pkg/log"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
)

// checkCountingContext counts calls of Err and reports cancellation from call cancelAt on.
// A cancelAt of zero never reports cancellation.
type checkCountingContext struct {
	context.Context
	checks, cancelAt int
}

func (c *checkCountingContext) Err() error {
	c.checks++
	if c.cancelAt > 0 && c.checks >= c.cancelAt {
		return context.Canceled
	}
	return nil
}

// xrefRepairTestPDF builds a PDF of n objects without a cross-reference table.
func xrefRepairTestPDF(n int) []byte {
	var buf bytes.Buffer
	buf.WriteString("%PDF-1.7\n%\xFF\xFF\xFF\xFF\n")
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&buf, "%d 0 obj\n<< /K %d >>\nendobj\n", i, i)
	}
	return buf.Bytes()
}

// xrefRepairFillerPDF builds a PDF of n lines that are not objects, without a cross-reference table.
func xrefRepairFillerPDF(n int) []byte {
	var buf bytes.Buffer
	buf.WriteString("%PDF-1.7\n%\xFF\xFF\xFF\xFF\n")
	for i := 0; i < n; i++ {
		buf.WriteString("% filler\n")
	}
	return buf.Bytes()
}

func loadedObjects(ctx *model.Context) int {
	n := 0
	for _, e := range ctx.Table {
		if e != nil && e.Object != nil {
			n++
		}
	}
	return n
}

func TestXRefRepairStopsWhenCancelled(t *testing.T) {
	const objects = 4 * contextutil.BatchInterval
	pdf := xrefRepairTestPDF(objects)
	newContext := func(t *testing.T) *model.Context {
		t.Helper()
		ctx, err := model.NewContext(bytes.NewReader(pdf), model.NewStatelessConfiguration())
		if err != nil {
			t.Fatal(err)
		}
		return ctx
	}

	t.Run("register", func(t *testing.T) {
		c, cancel := context.WithCancel(t.Context())
		cancel()
		ctx := newContext(t)
		if err := registerXRefObjects(c, ctx, 1); !errors.Is(err, context.Canceled) {
			t.Fatalf("got %v, want context.Canceled", err)
		}
		if len(ctx.Table) >= objects {
			t.Fatalf("registered %d objects after cancellation, want fewer than %d", len(ctx.Table), objects)
		}
	})

	t.Run("scan", func(t *testing.T) {
		// Input without objects, so the only check that can stop the scan loop is its own batch check.
		filler := xrefRepairFillerPDF(4 * contextutil.BatchInterval)
		newFiller := func() *model.Context {
			ctx, err := model.NewContext(bytes.NewReader(filler), model.NewStatelessConfiguration())
			if err != nil {
				t.Fatal(err)
			}
			return ctx
		}
		counter := &checkCountingContext{Context: t.Context()}
		if err := registerXRefObjects(counter, newFiller(), 1); err != nil {
			t.Fatal(err)
		}
		c := &checkCountingContext{Context: t.Context(), cancelAt: counter.checks + 1}
		if err := bypassXrefSection(c, newFiller(), 0, nil, 1); !errors.Is(err, context.Canceled) {
			t.Fatalf("got %v, want context.Canceled", err)
		}
	})
}

// recordingLogger records read log lines.
type recordingLogger struct {
	lines []string
}

func (l *recordingLogger) Printf(format string, args ...any) {
	l.lines = append(l.lines, fmt.Sprintf(format, args...))
}

func (l *recordingLogger) Println(args ...any) {
	l.lines = append(l.lines, fmt.Sprintln(args...))
}

func (l *recordingLogger) Fatalf(format string, args ...any) {
	panic(fmt.Sprintf(format, args...))
}

func (l *recordingLogger) Fatalln(args ...any) {
	panic(fmt.Sprintln(args...))
}

// captureReadLog records the read log for the rest of t. The read logger is
// disabled again afterwards, which is the state of package tests.
func captureReadLog(t *testing.T) *recordingLogger {
	t.Helper()
	rec := &recordingLogger{}
	log.SetReadLogger(rec)
	t.Cleanup(func() { log.SetReadLogger(nil) })
	return rec
}

// skippedLines counts the skipped messages in the read log.
func skippedLines(rec *recordingLogger) int {
	n := 0
	for _, line := range rec.lines {
		if strings.Contains(line, "pdfcpu skipped:") {
			n++
		}
	}
	return n
}

// malformedSkips counts the "malformed object" skips in the read log.
func malformedSkips(rec *recordingLogger) int {
	n := 0
	for _, line := range rec.lines {
		if strings.Contains(line, "malformed object") {
			n++
		}
	}
	return n
}

func TestProcessObjectReturnsCancelWithoutSkipping(t *testing.T) {
	pdf := xrefRepairTestPDF(1)
	ctx, err := model.NewContext(bytes.NewReader(pdf), model.NewStatelessConfiguration())
	if err != nil {
		t.Fatal(err)
	}
	rec := captureReadLog(t)
	c, cancel := context.WithCancel(t.Context())
	cancel()

	start := int64(bytes.Index(pdf, []byte("1 0 obj")))
	offset := start
	var offsetPrev int64
	if _, _, err := processObject(c, ctx, "1 0 obj", &offset, 1, &offsetPrev); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want context.Canceled", err)
	}
	if n := malformedSkips(rec); n != 0 {
		t.Fatalf("%d malformed object skips for a cancelled context, want 0", n)
	}
	if offset != start {
		t.Fatalf("offset advanced from %d to %d after cancellation", start, offset)
	}
}

func TestXRefRepairScanCancelIsNotSkipped(t *testing.T) {
	const objects = 4 * contextutil.BatchInterval
	pdf := xrefRepairTestPDF(objects)
	newContext := func(t *testing.T) *model.Context {
		t.Helper()
		ctx, err := model.NewContext(bytes.NewReader(pdf), model.NewStatelessConfiguration())
		if err != nil {
			t.Fatal(err)
		}
		return ctx
	}

	counter := &checkCountingContext{Context: t.Context()}
	if err := registerXRefObjects(counter, newContext(t), 1); err != nil {
		t.Fatal(err)
	}

	// The first check after registration is the one in processObject for the first object.
	rec := captureReadLog(t)
	c := &checkCountingContext{Context: t.Context(), cancelAt: counter.checks + 1}
	ctx := newContext(t)
	if err := bypassXrefSection(c, ctx, 0, nil, 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want context.Canceled", err)
	}
	if n := malformedSkips(rec); n != 0 {
		t.Fatalf("%d malformed object skips after cancellation, want 0", n)
	}
	if n := loadedObjects(ctx); n >= objects {
		t.Fatalf("loaded %d objects after cancellation, want fewer than %d", n, objects)
	}
}

// TestXRefRepairNoticeLimitIsResourceLimit checks that the notice limit cancel during
// repair surfaces as the ResourceLimitError and not as a skipped object.
func TestXRefRepairNoticeLimitIsResourceLimit(t *testing.T) {
	const limit = 5
	var buf bytes.Buffer
	buf.WriteString("%PDF-1.7\n%\xFF\xFF\xFF\xFF\n")
	fmt.Fprintf(&buf, "1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n")
	fmt.Fprintf(&buf, "2 0 obj\n<< /Type /Pages /Kids [3 0 R] /Count 1 >>\nendobj\n")
	fmt.Fprintf(&buf, "3 0 obj\n<< /Type /Page /Parent 2 0 R /MediaBox [0 0 595 842] >>\nendobj\n")
	for i := 4; i < 4+4*limit; i++ {
		fmt.Fprintf(&buf, "%d 0 obj\n<<@/X 1 >>\nendobj\n", i)
	}

	conf := model.NewStatelessConfiguration()
	conf.Limits.MaxNotices = limit
	rec := captureReadLog(t)
	_, err := Read(t.Context(), bytes.NewReader(buf.Bytes()), conf)

	var limitErr *model.ResourceLimitError
	if !errors.As(err, &limitErr) || limitErr.Kind != model.LimitNotices {
		t.Fatalf("got %v, want notice ResourceLimitError", err)
	}
	if !errors.Is(err, model.ErrResourceLimit) {
		t.Fatalf("%v does not match ErrResourceLimit", err)
	}
	if n := malformedSkips(rec); n != 0 {
		t.Fatalf("%d malformed object skips at the notice limit, want 0", n)
	}
}

// TestDereferenceAndLoadCancelIsNotSkipped checks that a cancellation while dereferencing an object in
// relaxed mode is returned and not logged as a skipped object.
func TestDereferenceAndLoadCancelIsNotSkipped(t *testing.T) {
	pdf := xrefRepairTestPDF(1)
	ctx, err := model.NewContext(bytes.NewReader(pdf), model.NewStatelessConfiguration())
	if err != nil {
		t.Fatal(err)
	}
	offset := int64(bytes.Index(pdf, []byte("1 0 obj")))
	generation := 0
	entry := &model.XRefTableEntry{Offset: &offset, Generation: &generation}

	rec := captureReadLog(t)
	c, cancel := context.WithCancel(t.Context())
	cancel()
	if err := dereferenceAndLoad(c, ctx, 1, entry); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want context.Canceled", err)
	}
	if entry.Object != nil {
		t.Fatalf("object loaded after cancellation: %v", entry.Object)
	}
	if n := skippedLines(rec); n != 0 {
		t.Fatalf("%d skipped messages for a cancelled context, want 0", n)
	}
}

// TestBypassXRefSectionContextErrorStartsNoRepair checks that a context error that triggered the repair
// is returned as is, without clearing the table or registering objects.
func TestBypassXRefSectionContextErrorStartsNoRepair(t *testing.T) {
	for _, wasErr := range []error{context.Canceled, context.DeadlineExceeded} {
		t.Run(wasErr.Error(), func(t *testing.T) {
			ctx, err := model.NewContext(bytes.NewReader(xrefRepairTestPDF(1)), model.NewStatelessConfiguration())
			if err != nil {
				t.Fatal(err)
			}
			before := len(ctx.Table)
			rec := captureReadLog(t)
			if err := bypassXrefSection(t.Context(), ctx, 0, wasErr, 1); !errors.Is(err, wasErr) {
				t.Fatalf("got %v, want %v", err, wasErr)
			}
			if len(ctx.Table) != before {
				t.Fatalf("table has %d entries after a context error, want %d", len(ctx.Table), before)
			}
			if n := skippedLines(rec); n != 0 {
				t.Fatalf("%d skipped messages for a context error, want 0", n)
			}
		})
	}
}
