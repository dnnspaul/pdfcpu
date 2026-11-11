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
