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
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
)

const (
	cancelLatencyAfter    = 50 * time.Millisecond
	cancelLatencyMax      = 250 * time.Millisecond
	cancelLatencyMaxAlloc = 32 << 20
)

// cancelLatencyRepairPDF builds a one-page PDF followed by n filler objects and no cross-reference table.
// With startXRef, the trailer points startxref at the first filler object, so relaxed reading rebuilds the table
// by registering the objects and then scanning the whole file. Without, reading first scans the whole file
// backwards for startxref. At 1M objects the 50 ms cancel lands in the registration pass. The scan loop is
// covered by TestXRefRepairStopsWhenCancelled/scan in pkg/pdfcpu, which uses input without objects.
func cancelLatencyRepairPDF(n int, startXRef bool) []byte {
	var buf bytes.Buffer
	buf.Grow(n * 32)
	buf.WriteString("%PDF-1.7\n%\xFF\xFF\xFF\xFF\n")
	appendValidationTestObject(&buf, 1, "<< /Type /Catalog /Pages 2 0 R >>")
	appendValidationTestObject(&buf, 2, "<< /Type /Pages /Kids [3 0 R] /Count 1 >>")
	appendValidationTestObject(&buf, 3, "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 595 842] >>")
	firstFiller := buf.Len()
	b := buf.AvailableBuffer()
	for i := 4; i < n+4; i++ {
		b = strconv.AppendInt(b, int64(i), 10)
		b = append(b, " 0 obj\n<< /K "...)
		b = strconv.AppendInt(b, int64(i), 10)
		b = append(b, " >>\nendobj\n"...)
	}
	buf.Write(b)
	fmt.Fprintf(&buf, "trailer\n<< /Size %d /Root 1 0 R >>\n", n+4)
	if startXRef {
		fmt.Fprintf(&buf, "startxref\n%d\n", firstFiller)
	}
	buf.WriteString("%%EOF\n")
	return buf.Bytes()
}

// cancelLatencyContentPDF builds a one-page PDF whose unfiltered content stream has n text operator groups.
func cancelLatencyContentPDF(n int) []byte {
	content := strings.Repeat("/F1 12 Tf 1 0 0 1 0 0 Tm (ab) Tj\n", n)
	return failureTestPDF(
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 595 842] /Resources << /Font << /F1 5 0 R >> >> /Contents 4 0 R >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(content), content),
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
	)
}

// cancelLatencyRun runs work with a context that is cancelled after cancelLatencyAfter. It returns the time from
// the cancellation until work returned and the bytes allocated in that time.
func cancelLatencyRun(t *testing.T, work func(context.Context) error) (time.Duration, uint64, error) {
	t.Helper()
	type cancelPoint struct {
		at         time.Time
		totalAlloc uint64
	}
	runtime.GC()
	c, cancel := context.WithCancel(t.Context())
	defer cancel()
	points := make(chan cancelPoint, 1)
	timer := time.AfterFunc(cancelLatencyAfter, func() {
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		points <- cancelPoint{at: time.Now(), totalAlloc: m.TotalAlloc}
		cancel()
	})

	err := work(c)
	end := time.Now()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	if timer.Stop() {
		t.Fatalf("finished within %v, before the cancellation: %v", cancelLatencyAfter, err)
	}
	p := <-points
	return end.Sub(p.at), m.TotalAlloc - p.totalAlloc, err
}

// TestCancellationLatencyOnHostileInputs verifies that work on inputs with long loops stops soon after
// cancellation and allocates little after it.
func TestCancellationLatencyOnHostileInputs(t *testing.T) {
	relaxed := func() *model.Configuration {
		conf := model.NewDefaultConfiguration()
		conf.ValidationMode = model.ValidationRelaxed
		return conf
	}
	readAndValidate := func(data []byte) func(context.Context) error {
		return func(c context.Context) error {
			_, err := ReadAndValidate(c, bytes.NewReader(data), relaxed())
			return err
		}
	}
	cases := []struct {
		name string
		// setup returns the work to cancel. Work done by setup itself is not measured.
		setup func(t *testing.T) func(context.Context) error
	}{
		{
			name: "startxref scan",
			setup: func(t *testing.T) func(context.Context) error {
				return readAndValidate(cancelLatencyRepairPDF(1_000_000, false))
			},
		},
		{
			name: "xref repair register",
			setup: func(t *testing.T) func(context.Context) error {
				return readAndValidate(cancelLatencyRepairPDF(1_000_000, true))
			},
		},
		{
			name: "page content",
			setup: func(t *testing.T) func(context.Context) error {
				ctx, err := ReadAndValidate(t.Context(), bytes.NewReader(cancelLatencyContentPDF(2_000_000)), relaxed())
				if err != nil {
					t.Fatal(err)
				}
				return func(c context.Context) error {
					_, err := ExtractPage(c, ctx, 1)
					return err
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			latency, allocated, err := cancelLatencyRun(t, tc.setup(t))
			t.Logf("returned %v after cancellation, %.1f MiB allocated after cancellation", latency, float64(allocated)/(1<<20))
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("got %v, want context.Canceled", err)
			}
			if latency > cancelLatencyMax {
				t.Errorf("returned %v after cancellation, want at most %v", latency, cancelLatencyMax)
			}
			if allocated > cancelLatencyMaxAlloc {
				t.Errorf("allocated %d MiB after cancellation, want at most %d MiB", allocated>>20, cancelLatencyMaxAlloc>>20)
			}
		})
	}
}
