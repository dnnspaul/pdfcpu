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
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
)

func noticeLimitTestContext(t *testing.T, maxRetained, maxNotices int) *Context {
	t.Helper()
	conf := NewStatelessConfiguration()
	conf.Limits.MaxRetainedNotices = maxRetained
	conf.Limits.MaxNotices = maxNotices
	ctx, err := NewContext(bytes.NewReader(nil), conf)
	if err != nil {
		t.Fatal(err)
	}
	return ctx
}

func addNotices(ctx *Context, n int) {
	for i := range n {
		ctx.XRefTable.AddValidationNotice(NewValidationNotice(NoticePhaseValidate, NoticeDigested, fmt.Sprintf("notice %d", i), nil))
	}
}

func TestValidationReportAccessors(t *testing.T) {
	ctx := noticeLimitTestContext(t, 0, 0)
	r := ctx.ValidationReport()
	if r.Len() != 0 || r.Dropped() != 0 {
		t.Fatalf("empty report: Len %d Dropped %d", r.Len(), r.Dropped())
	}
	if _, ok := r.First(); ok {
		t.Fatal("empty report: First reports a notice")
	}
	for range r.All() {
		t.Fatal("empty report: All yields a notice")
	}

	addNotices(ctx, 3)
	r = ctx.ValidationReport()
	if r.Len() != 3 || r.Dropped() != 0 {
		t.Fatalf("3 notices: Len %d Dropped %d", r.Len(), r.Dropped())
	}
	first, ok := r.First()
	if !ok || first.Message != "notice 0" {
		t.Fatalf("First: got %q, %v", first.Message, ok)
	}

	var got []string
	for n := range r.All() {
		got = append(got, n.Message)
		if len(got) == 2 {
			break
		}
	}
	if len(got) != 2 || got[0] != "notice 0" || got[1] != "notice 1" {
		t.Fatalf("All with break: got %v", got)
	}
}

func TestValidationReportRetentionLimit(t *testing.T) {
	ctx := noticeLimitTestContext(t, 2, 0)
	addNotices(ctx, 5)
	ctx.AddValidationNotice(NewValidationNotice(NoticePhaseValidate, NoticeDigested, "context", nil))

	r := ctx.ValidationReport()
	if r.Len() != 2 || r.Dropped() != 4 {
		t.Fatalf("Len %d Dropped %d, want 2 and 4", r.Len(), r.Dropped())
	}
	notices := r.Notices()
	if len(notices) != 2 || notices[0].Message != "notice 0" || notices[1].Message != "notice 1" {
		t.Fatalf("Notices: got %+v", notices)
	}
	if r.Empty() {
		t.Fatal("Empty reports a capped report with stored notices")
	}
}

func TestMarkDigestedAfterRetentionLimit(t *testing.T) {
	ctx := noticeLimitTestContext(t, 1, 0)
	for range 3 {
		for objNr := 1; objNr <= 4; objNr++ {
			if ctx.XRefTable.MarkDigested("kind", objNr) {
				ctx.XRefTable.AddValidationNotice(NewValidationNotice(NoticePhaseValidate, NoticeDigested, "digested", nil))
			}
		}
	}

	r := ctx.ValidationReport()
	if r.Len() != 1 || r.Dropped() != 3 {
		t.Fatalf("Len %d Dropped %d, want 1 and 3", r.Len(), r.Dropped())
	}
	if got := len(ctx.validationReport.digested); got != r.Len()+r.Dropped() {
		t.Fatalf("digested entries: got %d, want %d", got, r.Len()+r.Dropped())
	}
}

func TestValidationReportSnapshotDoesNotCopy(t *testing.T) {
	ctx := noticeLimitTestContext(t, 0, 0)
	addNotices(ctx, 100)
	allocs := testing.AllocsPerRun(10, func() {
		r := ctx.ValidationReport()
		_ = r.Len()
		_, _ = r.First()
	})
	if allocs != 0 {
		t.Fatalf("snapshot allocations: got %v, want 0", allocs)
	}
}

func TestValidationReportSnapshotIsStableUnderConcurrentReads(t *testing.T) {
	ctx := noticeLimitTestContext(t, 0, 0)
	addNotices(ctx, 10)
	snapshot := ctx.ValidationReport()

	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 200 {
				i := 0
				for n := range snapshot.All() {
					if n.Message != fmt.Sprintf("notice %d", i) {
						t.Errorf("snapshot notice %d: got %q", i, n.Message)
						return
					}
					i++
				}
				if i != 10 {
					t.Errorf("snapshot length: got %d, want 10", i)
					return
				}
			}
		}()
	}
	addNotices(ctx, 1000)
	wg.Wait()

	if live := ctx.ValidationReport().Len(); live != 1010 {
		t.Fatalf("live Len: got %d, want 1010", live)
	}
}

func TestEnforceNoticeLimitDisabled(t *testing.T) {
	for _, max := range []int{0, -1} {
		ctx := noticeLimitTestContext(t, 0, max)
		c := context.Background()
		limited, end := ctx.EnforceNoticeLimit(c)
		if limited != c {
			t.Fatalf("MaxNotices %d: got a derived context", max)
		}
		addNotices(ctx, 10)
		sentinel := errors.New("sentinel")
		if err := end(sentinel); err != sentinel {
			t.Fatalf("MaxNotices %d: end(sentinel) = %v", max, err)
		}
		if err := end(nil); err != nil {
			t.Fatalf("MaxNotices %d: end(nil) = %v", max, err)
		}
	}

	var nilCtx *Context
	_, end := nilCtx.EnforceNoticeLimit(context.Background())
	sentinel := errors.New("sentinel")
	if err := end(sentinel); err != sentinel {
		t.Fatalf("nil context: end(sentinel) = %v", err)
	}
}

func TestEnforceNoticeLimitStopsWork(t *testing.T) {
	for _, failWork := range []bool{false, true} {
		ctx := noticeLimitTestContext(t, 2, 3)
		limited, end := ctx.EnforceNoticeLimit(context.Background())

		addNotices(ctx, 3)
		if limited.Err() != nil {
			t.Fatalf("failWork %v: cancelled at the limit", failWork)
		}
		addNotices(ctx, 2)
		if !errors.Is(limited.Err(), context.Canceled) {
			t.Fatalf("failWork %v: limited.Err() = %v, want context.Canceled", failWork, limited.Err())
		}

		var workErr error
		if failWork {
			workErr = fmt.Errorf("catalog: %w", limited.Err())
		}
		err := end(workErr)
		var limitErr *ResourceLimitError
		if !errors.As(err, &limitErr) {
			t.Fatalf("failWork %v: end: got %v, want *ResourceLimitError", failWork, err)
		}
		if limitErr.Kind != LimitNotices || limitErr.Limit != 3 || limitErr.Value != 4 {
			t.Fatalf("failWork %v: limit error %+v", failWork, limitErr)
		}
		if !errors.Is(err, ErrResourceLimit) {
			t.Fatalf("failWork %v: error does not match ErrResourceLimit", failWork)
		}
		if errors.Is(err, ErrInvalidPDF) || errors.Is(err, context.Canceled) {
			t.Fatalf("failWork %v: limit error matches ErrInvalidPDF or context.Canceled", failWork)
		}
		if err.Error() != "validation notice limit 3 exceeded: 4" {
			t.Fatalf("failWork %v: message %q", failWork, err.Error())
		}

		r := ctx.ValidationReport()
		if r.Len() != 2 || r.Dropped() != 3 {
			t.Fatalf("failWork %v: Len %d Dropped %d, want 2 and 3", failWork, r.Len(), r.Dropped())
		}
	}
}

func TestEnforceNoticeLimitAlreadyExceeded(t *testing.T) {
	ctx := noticeLimitTestContext(t, 0, 0)
	addNotices(ctx, 5)
	ctx.Configuration.Limits.MaxNotices = 3

	limited, end := ctx.EnforceNoticeLimit(context.Background())
	if limited.Err() == nil {
		t.Fatal("work over the limit at install time is not cancelled")
	}
	if err := end(nil); !errors.Is(err, ErrResourceLimit) {
		t.Fatalf("end(nil): got %v, want ErrResourceLimit", err)
	}
}

func TestEnforceNoticeLimitCallerCancellationWins(t *testing.T) {
	ctx := noticeLimitTestContext(t, 0, 1)
	callerCause := errors.New("caller cancelled")
	c, cancel := context.WithCancelCause(context.Background())
	limited, end := ctx.EnforceNoticeLimit(c)

	addNotices(ctx, 2)
	cancel(callerCause)

	workErr := fmt.Errorf("work: %w", limited.Err())
	if err := end(workErr); err != workErr {
		t.Fatalf("end: got %v, want the work error unchanged", err)
	}
	if cause := context.Cause(c); cause != callerCause {
		t.Fatalf("caller cause: got %v, want %v", cause, callerCause)
	}
}

func TestEnforceNoticeLimitReleasesContext(t *testing.T) {
	ctx := noticeLimitTestContext(t, 0, 2)
	outer, endOuter := ctx.EnforceNoticeLimit(context.Background())
	inner, endInner := ctx.EnforceNoticeLimit(outer)

	if err := endInner(nil); err != nil {
		t.Fatalf("endInner(nil) = %v", err)
	}
	if inner.Err() == nil {
		t.Fatal("inner context not released by end")
	}

	addNotices(ctx, 3)
	if outer.Err() == nil {
		t.Fatal("outer limit not restored after inner end")
	}
	if err := endOuter(nil); !errors.Is(err, ErrResourceLimit) {
		t.Fatalf("endOuter(nil): got %v, want ErrResourceLimit", err)
	}

	addNotices(ctx, 3)
	if n := ctx.ValidationReport().Len(); n != 6 {
		t.Fatalf("Len after end: got %d, want 6", n)
	}
}

func TestEnforceNoticeLimitNestedTripIsRechecked(t *testing.T) {
	ctx := noticeLimitTestContext(t, 0, 2)
	outer, endOuter := ctx.EnforceNoticeLimit(context.Background())
	inner, endInner := ctx.EnforceNoticeLimit(outer)

	addNotices(ctx, 3)
	if inner.Err() == nil {
		t.Fatal("inner limit not tripped at 3 notices")
	}
	// The work error is a cancellation caused by the inner trip. The inner end must
	// return the limit error, not that cancellation.
	if err := endInner(fmt.Errorf("work: %w", inner.Err())); !errors.Is(err, ErrResourceLimit) {
		t.Fatalf("endInner: got %v, want ErrResourceLimit", err)
	}

	if outer.Err() == nil {
		t.Fatal("outer context not cancelled by the inner end although 3 > 2 notices were accepted")
	}
	if err := endOuter(nil); !errors.Is(err, ErrResourceLimit) {
		t.Fatalf("endOuter(nil): got %v, want ErrResourceLimit", err)
	}
}
