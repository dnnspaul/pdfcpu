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
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

// pageHelpersInheritedFontPDF builds a two page PDF whose pages inherit a /Resources /Font
// dictionary with n entries from the page tree root. Every page clones the inherited dictionary.
func pageHelpersInheritedFontPDF(n int) []byte {
	var fonts strings.Builder
	for i := range n {
		fmt.Fprintf(&fonts, "/F%d 5 0 R ", i)
	}
	return failureTestPDF(
		"<< /Type /Catalog /Pages 2 0 R >>",
		fmt.Sprintf("<< /Type /Pages /Kids [3 0 R 4 0 R] /Count 2 /MediaBox [0 0 595 842] /Resources << /Font << %s>> >> >>", fonts.String()),
		"<< /Type /Page /Parent 2 0 R >>",
		"<< /Type /Page /Parent 2 0 R >>",
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
	)
}

// TestExtractPagesContextCancelWithLargeInheritedFont pins that a cancellation returns promptly
// while pages with a large inherited font dictionary are being extracted. Each extracted page
// clones the inherited dictionary, which must not take seconds per page.
func TestExtractPagesContextCancelWithLargeInheritedFont(t *testing.T) {
	if testing.Short() {
		t.Skip("large input")
	}
	ctx := pageHelpersRead(t, pageHelpersInheritedFontPDF(40000))

	c, cancel := context.WithCancel(context.Background())
	var mu sync.Mutex
	var cancelAt time.Time
	timer := time.AfterFunc(50*time.Millisecond, func() {
		mu.Lock()
		cancelAt = time.Now()
		mu.Unlock()
		cancel()
	})
	defer timer.Stop()

	var digested int
	err := ExtractPagesContext(c, ctx, nil, func(io.Reader, int) error {
		digested++
		return nil
	})
	returned := time.Now()

	mu.Lock()
	cancelled := !cancelAt.IsZero()
	mu.Unlock()
	if err == nil {
		// Extraction finished before the cancel: nothing to stop, and digested must cover all pages.
		if digested != 2 {
			t.Fatalf("extraction succeeded with %d pages digested, want 2", digested)
		}
		return
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want context.Canceled", err)
	}
	if !cancelled {
		t.Fatal("extraction failed before the cancel")
	}
	mu.Lock()
	after := returned.Sub(cancelAt)
	mu.Unlock()
	if after > time.Second {
		t.Errorf("ExtractPagesContext returned %v after cancel, want < 1s", after)
	}
}
