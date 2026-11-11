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
	"fmt"
	"math"
)

// ErrResourceLimit signals that processing stopped at a configured resource limit.
// For the same input and configuration the failure is deterministic.
var ErrResourceLimit = errors.New("resource limit exceeded")

// LimitKind identifies the resource limit reported by a ResourceLimitError.
type LimitKind int

const (
	// LimitObjectNumber identifies a limit on PDF object numbers.
	LimitObjectNumber LimitKind = iota + 1

	// LimitNotices identifies a limit on the number of validation notices.
	LimitNotices
)

// String returns a description of k.
func (k LimitKind) String() string {
	switch k {
	case LimitObjectNumber:
		return "object number"
	case LimitNotices:
		return "validation notice"
	}
	return fmt.Sprintf("LimitKind(%d)", int(k))
}

// ResourceLimitError reports processing stopped because Value exceeded Limit.
// Err optionally carries the original error; if set, it provides the message.
type ResourceLimitError struct {
	Kind  LimitKind
	Limit int64
	Value int64
	Err   error
}

// Error implements error.
func (e *ResourceLimitError) Error() string {
	if e.Err != nil {
		return e.Err.Error()
	}
	return fmt.Sprintf("%s limit %d exceeded: %d", e.Kind, e.Limit, e.Value)
}

// Unwrap returns the original error, if any.
func (e *ResourceLimitError) Unwrap() error {
	return e.Err
}

// Is classifies this error as ErrResourceLimit.
func (e *ResourceLimitError) Is(target error) bool {
	return target == ErrResourceLimit
}

// CheckObjectNumber returns a *ResourceLimitError of kind LimitObjectNumber if
// objNr exceeds l.MaxObjectNumber. A zero or negative MaxObjectNumber disables the check.
func (l ResourceLimits) CheckObjectNumber(objNr int64) error {
	if l.MaxObjectNumber > 0 && objNr > int64(l.MaxObjectNumber) {
		return &ResourceLimitError{Kind: LimitObjectNumber, Limit: int64(l.MaxObjectNumber), Value: objNr}
	}
	return nil
}

// CheckObjectRange checks the last object number of count objects starting at start
// against l.MaxObjectNumber. A count of zero or less checks nothing. start is expected to be
// zero or greater: callers reject a negative object number first, and the overflow guard is
// only applied for a positive start.
func (l ResourceLimits) CheckObjectRange(start, count int64) error {
	if count <= 0 {
		return nil
	}
	if start > 0 && count-1 > math.MaxInt64-start {
		return l.CheckObjectNumber(math.MaxInt64)
	}
	return l.CheckObjectNumber(start + count - 1)
}

// CheckXRefSize checks a cross-reference /Size, which is one greater than the
// highest object number, against l.MaxObjectNumber.
func (l ResourceLimits) CheckXRefSize(size int64) error {
	return l.CheckObjectRange(0, size)
}
