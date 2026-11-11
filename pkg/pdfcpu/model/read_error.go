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

import "io"

// readErrorRecorder wraps the input of a read context and records the first
// failure of the input reader. Read records every error other than io.EOF.
// Seek records only errors of seeks to a non-negative offset from the start,
// the only seeks whose failure is caused by the reader and not by malformed input.
// Seeks relative to the end or the current position, and seeks to negative offsets,
// fail on malformed input or while scanning and are not recorded.
type readErrorRecorder struct {
	rs  io.ReadSeeker
	err error
}

// Read implements io.Reader.
func (r *readErrorRecorder) Read(p []byte) (int, error) {
	n, err := r.rs.Read(p)
	if err != nil && err != io.EOF && r.err == nil {
		r.err = err
	}
	return n, err
}

// Seek implements io.Seeker.
func (r *readErrorRecorder) Seek(offset int64, whence int) (int64, error) {
	n, err := r.rs.Seek(offset, whence)
	if err != nil && whence == io.SeekStart && offset >= 0 && r.err == nil {
		r.err = err
	}
	return n, err
}

// ReaderErr returns the first failure of the input reader of rc, or nil if the reader has not failed.
// A read that fails after the reader has failed is a reader failure, not invalid input.
// rc may be nil: contexts built in memory by CreateContext have no read context and no reader.
func (rc *ReadContext) ReaderErr() error {
	if rc == nil || rc.rec == nil {
		return nil
	}
	return rc.rec.err
}
