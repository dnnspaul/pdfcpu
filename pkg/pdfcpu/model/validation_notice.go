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
	"context"
	"errors"
	"iter"
)

// NoticeSeverity identifies the severity of an accepted validation divergence.
type NoticeSeverity string

const (
	// NoticeSeverityWarning identifies an accepted validation divergence.
	NoticeSeverityWarning NoticeSeverity = "warning"
)

// NoticeDisposition identifies how a validation divergence was handled.
type NoticeDisposition string

const (
	// NoticeDigested identifies an accepted specification violation.
	NoticeDigested NoticeDisposition = "digested"

	// NoticeRepaired identifies an accepted and repaired specification violation.
	NoticeRepaired NoticeDisposition = "repaired"

	// NoticeSkipped identifies an accepted and skipped specification violation.
	NoticeSkipped NoticeDisposition = "skipped"
)

// NoticePhase identifies the processing phase that accepted a validation divergence.
type NoticePhase string

const (
	// NoticePhaseParse identifies a parser notice.
	NoticePhaseParse NoticePhase = "parse"

	// NoticePhaseRead identifies a reader notice.
	NoticePhaseRead NoticePhase = "read"

	// NoticePhaseValidate identifies a semantic validation notice.
	NoticePhaseValidate NoticePhase = "validate"
)

// ValidationNotice describes an accepted validation divergence.
type ValidationNotice struct {
	Severity        NoticeSeverity
	Disposition     NoticeDisposition
	Phase           NoticePhase
	Message         string
	Cause           error
	ObjectNumber    int
	PageNumber      int
	Path            string
	ActualVersion   Version
	RequiredVersion Version
	HasVersions     bool
}

// NewValidationNotice returns a warning for an accepted validation divergence.
func NewValidationNotice(phase NoticePhase, disposition NoticeDisposition, message string, cause error) ValidationNotice {
	return ValidationNotice{
		Severity:     NoticeSeverityWarning,
		Disposition:  disposition,
		Phase:        phase,
		Message:      message,
		Cause:        cause,
		ObjectNumber: validationNoticeObjectNumber(cause),
	}
}

func validationNoticeObjectNumber(err error) int {
	var validationErr *ValidationError
	if errors.As(err, &validationErr) {
		return validationErr.ObjectNumber()
	}
	return 0
}

// ValidationReport contains the accepted validation divergences for one PDF context.
// A report returned by Context.ValidationReport is a snapshot: it does not change when
// the PDF context records further notices and may be read by several goroutines at once.
type ValidationReport struct {
	notices []ValidationNotice

	// dropped counts the notices not stored because of ResourceLimits.MaxRetainedNotices.
	dropped int

	// digested holds the (kind, object) pairs already recorded by MarkDigested.
	digested map[digestKey]struct{}

	// abort, if set, stops the running read or validation once more than maxNotices
	// notices have been accepted. See Context.EnforceNoticeLimit.
	abort      context.CancelCauseFunc
	maxNotices int
}

// digestKey identifies one kind of digested divergence at one object.
type digestKey struct {
	kind  string
	objNr int
}

// Empty reports whether the validation report contains no notices.
func (r ValidationReport) Empty() bool {
	return len(r.notices) == 0
}

// Notices returns the validation notices in collection order.
func (r ValidationReport) Notices() []ValidationNotice {
	return append([]ValidationNotice(nil), r.notices...)
}

// Len returns the number of stored validation notices.
func (r ValidationReport) Len() int {
	return len(r.notices)
}

// Since returns the notices that r holds beyond those of before, an earlier
// snapshot of the same PDF context taken with Context.ValidationReport. The
// dropped notices recorded since before are counted as well.
func (r ValidationReport) Since(before ValidationReport) ValidationReport {
	n := min(len(before.notices), len(r.notices))
	return ValidationReport{notices: r.notices[n:len(r.notices):len(r.notices)], dropped: r.dropped - before.dropped}
}

// Dropped returns the number of validation notices that were accepted but not stored
// because ResourceLimits.MaxRetainedNotices was reached.
// Len() + Dropped() is the number of accepted validation notices.
func (r ValidationReport) Dropped() int {
	return r.dropped
}

// First returns the first validation notice in collection order.
// It reports false if no notice is stored.
func (r ValidationReport) First() (ValidationNotice, bool) {
	if len(r.notices) == 0 {
		return ValidationNotice{}, false
	}
	return r.notices[0], true
}

// All returns an iterator over the stored validation notices in collection order.
// It does not copy the notices.
func (r ValidationReport) All() iter.Seq[ValidationNotice] {
	return func(yield func(ValidationNotice) bool) {
		for _, notice := range r.notices {
			if !yield(notice) {
				return
			}
		}
	}
}

// add stores notice unless retain stored notices exist already, counts it
// and stops the running read or validation once the notice limit is exceeded.
func (r *ValidationReport) add(notice ValidationNotice, retain int) {
	if retain > 0 && len(r.notices) >= retain {
		r.dropped++
	} else {
		r.notices = append(r.notices, notice)
	}
	r.checkNoticeLimit()
}

// checkNoticeLimit stops the running read or validation if more than maxNotices notices were accepted.
func (r *ValidationReport) checkNoticeLimit() {
	if r.abort == nil {
		return
	}
	if total := len(r.notices) + r.dropped; total > r.maxNotices {
		r.abort(&ResourceLimitError{Kind: LimitNotices, Limit: int64(r.maxNotices), Value: int64(total)})
		r.abort = nil
	}
}

// retainedNoticeLimit returns conf's MaxRetainedNotices or 0 for a nil conf.
func retainedNoticeLimit(conf *Configuration) int {
	if conf == nil {
		return 0
	}
	return conf.Limits.MaxRetainedNotices
}

// AddValidationNotice adds notice to this PDF context's validation report.
func (ctx *Context) AddValidationNotice(notice ValidationNotice) {
	if ctx == nil {
		return
	}
	ctx.validationReport.add(notice, retainedNoticeLimit(ctx.Configuration))
}

// AddValidationNotice adds notice to the report owned by this cross-reference table's PDF context.
func (xRefTable *XRefTable) AddValidationNotice(notice ValidationNotice) {
	if xRefTable == nil || xRefTable.validationReport == nil {
		return
	}
	xRefTable.validationReport.add(notice, retainedNoticeLimit(xRefTable.Conf))
}

// MarkDigested records that the divergence of kind at objNr is being digested.
// It reports true only the first time kind is marked for objNr in this cross-reference
// table's PDF context, so callers can add one notice per object in constant time.
// Callers skip the call when objNr <= 0, because such a value names no object to key on.
func (xRefTable *XRefTable) MarkDigested(kind string, objNr int) bool {
	if xRefTable == nil || xRefTable.validationReport == nil {
		return false
	}
	r := xRefTable.validationReport
	key := digestKey{kind: kind, objNr: objNr}
	if _, ok := r.digested[key]; ok {
		return false
	}
	if r.digested == nil {
		r.digested = map[digestKey]struct{}{}
	}
	r.digested[key] = struct{}{}
	return true
}

// ValidationReport returns a snapshot of this PDF context's validation report.
// The snapshot shares the stored notices with ctx without copying them.
func (ctx *Context) ValidationReport() ValidationReport {
	if ctx == nil {
		return ValidationReport{}
	}
	r := &ctx.validationReport
	n := len(r.notices)
	return ValidationReport{notices: r.notices[:n:n], dropped: r.dropped}
}

// EnforceNoticeLimit applies ResourceLimits.MaxNotices of ctx's configuration to a read
// or validation of ctx that runs with c.
// It returns the context to run the work with, which is cancelled once more than
// MaxNotices validation notices have been accepted, and a function end that must be
// called with the work's result when the work has returned.
// end returns a *ResourceLimitError of kind LimitNotices if the limit was exceeded
// and c is not done; otherwise it returns its argument.
// Without a positive MaxNotices, EnforceNoticeLimit returns c and an end that returns its argument.
//
// Usage contract:
//   - end must be called exactly once, preferably with defer, so that it also runs
//     when the work panics.
//   - Nested calls must be ended in reverse order. Ending out of order restores the wrong state.
//   - Like the context itself, it is not safe for concurrent use.
//
// The notice limit is read from ctx.Configuration, while the storage cap of
// AddValidationNotice comes from the Conf of the cross-reference table. Both are the
// same object, because XRefTable.Conf is set from the PDF context's configuration.
func (ctx *Context) EnforceNoticeLimit(c context.Context) (context.Context, func(error) error) {
	if ctx == nil || ctx.Configuration == nil || ctx.Configuration.Limits.MaxNotices <= 0 {
		return c, func(err error) error { return err }
	}
	r := &ctx.validationReport
	prevAbort, prevMax := r.abort, r.maxNotices
	limited, cancel := context.WithCancelCause(c)
	r.abort, r.maxNotices = cancel, ctx.Configuration.Limits.MaxNotices
	r.checkNoticeLimit()
	return limited, func(err error) error {
		// Read the state before the re-check below, which may cancel c.
		stopped := limited.Err() != nil
		cause := context.Cause(limited)
		callerDone := c.Err() != nil
		cancel(nil)
		r.abort, r.maxNotices = prevAbort, prevMax
		// An inner call may have tripped the limit and had its error swallowed.
		// Re-check so that the outer call is cancelled as well.
		r.checkNoticeLimit()
		var limitErr *ResourceLimitError
		if stopped && !callerDone && errors.As(cause, &limitErr) {
			return limitErr
		}
		return err
	}
}
