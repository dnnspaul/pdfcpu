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
	"errors"
	"fmt"
	"io"
	"runtime"
	"strings"
	"testing"

	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
)

const (
	noticeExplosionPages   = 100
	noticeExplosionPerPage = 500
	noticeExplosionTotal   = noticeExplosionPages * noticeExplosionPerPage
)

// noticeExplosionPDF builds a PDF whose pages all share one annotation array of
// perPage link annotations with the same malformed destination. Validating the
// document accepts one notice per annotation visit, so pages*perPage notices.
func noticeExplosionPDF(pages, perPage int) []byte {
	kids := make([]string, pages)
	for i := range pages {
		kids[i] = fmt.Sprintf("%d 0 R", 5+i)
	}
	annots := strings.Repeat("3 0 R ", perPage)

	objects := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", strings.Join(kids, " "), pages),
		"<< /Type /Annot /Subtype /Link /Rect [0 0 10 10] /Dest [5 0 R /XYZ 0 0] >>",
		"[" + strings.TrimSpace(annots) + "]",
	}
	for range pages {
		objects = append(objects, "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 595 842] /Annots 4 0 R >>")
	}
	return failureTestPDF(objects...)
}

// readNoticeExplosionPDF builds a valid PDF followed by n objects that each cause one parse notice.
func readNoticeExplosionPDF(n int) []byte {
	objects := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 595 842] >>",
	}
	for range n {
		objects = append(objects, "<<@/X 1 >>")
	}
	return failureTestPDF(objects...)
}

func noticeLimitConfiguration(maxRetained, maxNotices int) *model.Configuration {
	conf := model.NewDefaultConfiguration()
	conf.ValidationMode = model.ValidationRelaxed
	conf.Limits.MaxRetainedNotices = maxRetained
	conf.Limits.MaxNotices = maxNotices
	return conf
}

// noticeLimitRun reads and validates data and reports the bytes allocated and the
// heap growth while the context is alive.
func noticeLimitRun(t *testing.T, data []byte, conf *model.Configuration) (ctx *model.Context, allocated, retained int64, err error) {
	t.Helper()
	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)

	ctx, err = ReadContext(context.Background(), bytes.NewReader(data), conf)
	if err == nil {
		err = ValidateContext(context.Background(), ctx)
	}

	runtime.GC()
	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	runtime.KeepAlive(ctx)
	return ctx, int64(after.TotalAlloc - before.TotalAlloc), int64(after.HeapAlloc) - int64(before.HeapAlloc), err
}

func assertNoticeLimitError(t *testing.T, err error, limit int64) {
	t.Helper()
	var limitErr *model.ResourceLimitError
	if !errors.As(err, &limitErr) {
		t.Fatalf("got %v, want *ResourceLimitError", err)
	}
	if limitErr.Kind != model.LimitNotices || limitErr.Limit != limit || limitErr.Value != limit+1 {
		t.Fatalf("limit error %+v, want kind notices, limit %d, value %d", limitErr, limit, limit+1)
	}
	if !errors.Is(err, model.ErrResourceLimit) {
		t.Fatalf("%v does not match ErrResourceLimit", err)
	}
	if errors.Is(err, model.ErrInvalidPDF) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("%v matches ErrInvalidPDF or a context error", err)
	}
	if !IsDeterministicFailure(err) {
		t.Fatalf("%v is not a deterministic failure", err)
	}
}

func TestNoticeExplosionRetentionLimit(t *testing.T) {
	data := noticeExplosionPDF(noticeExplosionPages, noticeExplosionPerPage)

	uncapped, uncappedAlloc, uncappedHeap, err := noticeLimitRun(t, data, noticeLimitConfiguration(0, 0))
	if err != nil {
		t.Fatalf("uncapped: %v", err)
	}
	uncappedReport := uncapped.ValidationReport()
	if uncappedReport.Len() != noticeExplosionTotal || uncappedReport.Dropped() != 0 {
		t.Fatalf("uncapped: Len %d Dropped %d, want %d and 0", uncappedReport.Len(), uncappedReport.Dropped(), noticeExplosionTotal)
	}

	capped, cappedAlloc, cappedHeap, err := noticeLimitRun(t, data, noticeLimitConfiguration(100, 0))
	if err != nil {
		t.Fatalf("capped: %v", err)
	}
	report := capped.ValidationReport()
	if report.Len() != 100 || report.Dropped() != noticeExplosionTotal-100 {
		t.Fatalf("capped: Len %d Dropped %d, want 100 and %d", report.Len(), report.Dropped(), noticeExplosionTotal-100)
	}
	first, ok := report.First()
	uncappedFirst, _ := uncappedReport.First()
	if !ok || first.Message != uncappedFirst.Message {
		t.Fatalf("capped First %q, want %q", first.Message, uncappedFirst.Message)
	}
	stored := 0
	for n := range report.All() {
		if n.Message != uncappedFirst.Message {
			t.Fatalf("capped notice %d: got %q, want %q", stored, n.Message, uncappedFirst.Message)
		}
		stored++
	}
	if stored != 100 {
		t.Fatalf("All yielded %d notices, want 100", stored)
	}

	t.Logf("uncapped %d KiB allocated, %d KiB retained; capped %d KiB allocated, %d KiB retained",
		uncappedAlloc>>10, uncappedHeap>>10, cappedAlloc>>10, cappedHeap>>10)
	if cappedHeap > 8<<20 || cappedHeap*3 > uncappedHeap {
		t.Fatalf("capped retained heap %d KiB is not bounded against uncapped %d KiB", cappedHeap>>10, uncappedHeap>>10)
	}
}

func TestNoticeExplosionFatalLimit(t *testing.T) {
	data := noticeExplosionPDF(noticeExplosionPages, noticeExplosionPerPage)

	_, uncappedAlloc, _, err := noticeLimitRun(t, data, noticeLimitConfiguration(0, 0))
	if err != nil {
		t.Fatalf("uncapped: %v", err)
	}

	ctx, fatalAlloc, _, err := noticeLimitRun(t, data, noticeLimitConfiguration(100, 1000))
	assertNoticeLimitError(t, err, 1000)
	report := ctx.ValidationReport()
	if total := report.Len() + report.Dropped(); total <= 1000 || total > 1000+noticeExplosionPerPage {
		t.Fatalf("accepted notices %d, want within 1001..%d", total, 1000+noticeExplosionPerPage)
	}
	t.Logf("uncapped %d KiB allocated; stopped at %d notices after %d KiB allocated",
		uncappedAlloc>>10, report.Len()+report.Dropped(), fatalAlloc>>10)
	if fatalAlloc*10 > uncappedAlloc {
		t.Fatalf("limited run allocated %d KiB, uncapped %d KiB: not stopped promptly", fatalAlloc>>10, uncappedAlloc>>10)
	}
}

func TestNoticeLimitEntryPoints(t *testing.T) {
	const limit = 5
	validateData := noticeExplosionPDF(2, 10)
	readData := readNoticeExplosionPDF(20)
	valid := failureTestValidPDF()
	discard := func(io.Reader, int) error { return nil }

	type entry struct {
		name string
		run  func(c context.Context, data []byte, conf *model.Configuration) error
	}
	entries := []entry{
		{"ReadContext", func(c context.Context, data []byte, conf *model.Configuration) error {
			_, err := ReadContext(c, bytes.NewReader(data), conf)
			return err
		}},
		{"ReadAndValidate", func(c context.Context, data []byte, conf *model.Configuration) error {
			_, err := ReadAndValidate(c, bytes.NewReader(data), conf)
			return err
		}},
		{"ValidateContextWithReport", func(c context.Context, data []byte, conf *model.Configuration) error {
			ctx, err := ReadContext(c, bytes.NewReader(data), conf)
			if err != nil {
				return err
			}
			report, err := ValidateContextWithReport(c, ctx)
			if err != nil && report.Len()+report.Dropped() <= limit {
				return fmt.Errorf("limit error after %d notices: %v", report.Len()+report.Dropped(), err)
			}
			return err
		}},
		{"ValidateWithReport", func(c context.Context, data []byte, conf *model.Configuration) error {
			_, err := ValidateWithReport(c, bytes.NewReader(data), conf, nil)
			return err
		}},
		{"Optimize", func(c context.Context, data []byte, conf *model.Configuration) error {
			return Optimize(c, bytes.NewReader(data), io.Discard, conf, nil)
		}},
		{"MergeRaw", func(c context.Context, data []byte, conf *model.Configuration) error {
			return MergeRaw(c, []io.ReadSeeker{bytes.NewReader(valid), bytes.NewReader(data)}, io.Discard, false, conf)
		}},
		{"ExtractPages", func(c context.Context, data []byte, conf *model.Configuration) error {
			return ExtractPages(c, bytes.NewReader(data), nil, discard, conf)
		}},
	}

	inputs := []struct {
		name string
		data []byte
	}{
		{"validate", validateData},
		{"read", readData},
	}
	for _, input := range inputs {
		for _, e := range entries {
			t.Run(input.name+"/"+e.name, func(t *testing.T) {
				if err := e.run(context.Background(), input.data, noticeLimitConfiguration(0, 0)); err != nil {
					t.Fatalf("without a limit: %v", err)
				}
				err := e.run(context.Background(), input.data, noticeLimitConfiguration(0, limit))
				if input.name == "validate" && e.name == "ReadContext" {
					if err != nil {
						t.Fatalf("reading alone yields no validation notices: %v", err)
					}
					return
				}
				assertNoticeLimitError(t, err, limit)
			})
		}
	}
}

func TestNoticeLimitsKeepStrictErrors(t *testing.T) {
	data := noticeExplosionPDF(2, 10)
	strict := func(maxRetained, maxNotices int) error {
		conf := noticeLimitConfiguration(maxRetained, maxNotices)
		conf.ValidationMode = model.ValidationStrict
		_, err := ReadAndValidate(context.Background(), bytes.NewReader(data), conf)
		return err
	}

	base := strict(0, 0)
	limited := strict(1, 1)
	if base == nil || limited == nil {
		t.Fatalf("strict mode: want errors, got %v and %v", base, limited)
	}
	if base.Error() != limited.Error() {
		t.Fatalf("strict error changed with limits:\n%v\n%v", base, limited)
	}
	for _, err := range []error{base, limited} {
		if !errors.Is(err, model.ErrInvalidPDF) {
			t.Fatalf("%v does not match ErrInvalidPDF", err)
		}
	}
}
