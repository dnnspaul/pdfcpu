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

package contextutil

import (
	"context"
	"errors"
	"testing"
)

// TestBatchChecksEveryInterval verifies that a Batch consults the context only on every BatchInterval-th call.
func TestBatchChecksEveryInterval(t *testing.T) {
	c, cancel := context.WithCancel(t.Context())
	cancel()
	var b Batch
	for round := range 3 {
		for i := 1; i < BatchInterval; i++ {
			if err := b.Check(c); err != nil {
				t.Fatalf("round %d call %d: got %v, want nil", round, i, err)
			}
		}
		if err := b.Check(c); !errors.Is(err, context.Canceled) {
			t.Fatalf("round %d call %d: got %v, want context.Canceled", round, BatchInterval, err)
		}
	}
}

// TestBatchReportsMissingContext verifies that a Batch reports a nil context like Check.
func TestBatchReportsMissingContext(t *testing.T) {
	var b Batch
	var err error
	for range BatchInterval {
		err = b.Check(nil)
	}
	if !errors.Is(err, ErrMissingContext) {
		t.Fatalf("got %v, want ErrMissingContext", err)
	}
}
