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
	"io"
	"runtime"
	"testing"
	"time"

	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
)

// panicReadSeeker panics on Read. Seek works, so the panic happens inside
// readContext, after the notice limit context has been derived.
type panicReadSeeker struct {
	io.ReadSeeker
}

func (panicReadSeeker) Read([]byte) (int, error) {
	panic("read panics")
}

// notDoneParent is a cancellable parent that is not a standard context type.
// context.WithCancelCause therefore starts a goroutine that waits for the
// derived context, and that goroutine exits only when the derived context is
// cancelled.
type notDoneParent struct {
	context.Context
}

var neverClosed = make(chan struct{})

func (notDoneParent) Done() <-chan struct{} {
	return neverClosed
}

func TestReadReleasesNoticeLimitContextOnPanic(t *testing.T) {
	conf := model.NewDefaultConfiguration()
	conf.Limits.MaxNotices = 5

	before := runtime.NumGoroutine()
	panicked := false
	func() {
		defer func() {
			panicked = recover() != nil
		}()
		rs := panicReadSeeker{bytes.NewReader([]byte("%PDF-1.7\n%%EOF\n"))}
		_, _ = Read(notDoneParent{context.Background()}, rs, conf)
	}()
	if !panicked {
		t.Fatal("Read did not panic")
	}

	// The released goroutine exits asynchronously, so poll briefly.
	deadline := time.Now().Add(2 * time.Second)
	for runtime.NumGoroutine() > before && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if n := runtime.NumGoroutine(); n > before {
		t.Fatalf("goroutines after recovered panic: %d, before: %d; notice limit context not released", n, before)
	}
}
