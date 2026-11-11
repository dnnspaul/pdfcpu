/*
	Copyright 2020 The pdfcpu Authors.

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
	"os"
	"path/filepath"

	"github.com/pdfcpu/pdfcpu/internal/contextutil"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/fault"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
)

func mergeSourceLabel(source string) string {
	if source == "" {
		return "source"
	}
	return fmt.Sprintf("source %s", source)
}

func wrapMergeCleanupError(context string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", context, err)
}

func mergeFileExists(filename string) bool {
	_, err := os.Stat(filename)
	return err == nil
}

// appendTo appends rs to ctxDest's page tree.
func appendTo(c context.Context, rs io.ReadSeeker, fName string, ctxDest *model.Context, dividerPage bool) error {
	source := mergeSourceLabel(fName)
	if err := contextutil.Check(c); err != nil {
		return err
	}
	if rs == nil {
		return fmt.Errorf("merge %s: read source: %w", source, ErrMissingPDFReadSeeker)
	}

	ctxSource, err := ReadAndValidate(c, rs, ctxDest.Configuration)
	if err != nil {
		return fmt.Errorf("merge %s: read and validate: %w", source, err)
	}

	if ctxDest.XRefTable.Version() < model.V20 && ctxSource.XRefTable.Version() == model.V20 {
		return fmt.Errorf("merge %s: validate version: %w", source, pdfcpu.ErrUnsupportedVersion)
	}

	// Merge source context into dest context.
	if err := pdfcpu.MergeXRefTables(c, fName, ctxSource, ctxDest, false, dividerPage); err != nil {
		return fmt.Errorf("merge %s: append pages: %w", source, err)
	}
	return nil
}

func appendFile(c context.Context, fName string, ctxDest *model.Context, dividerPage bool) (err error) {
	if err := contextutil.Check(c); err != nil {
		return err
	}
	f, err := os.Open(fName)
	if err != nil {
		return fmt.Errorf("merge source: open %s: %w", fName, err)
	}
	defer func() {
		closeErr := f.Close()
		if err != nil {
			return
		}
		err = wrapMergeCleanupError("merge source: close input", closeErr)
	}()

	return appendTo(c, f, filepath.Base(fName), ctxDest, dividerPage)
}

// MergeRaw merges a sequence of PDF streams, writes the result to w and supports cancellation.
func MergeRaw(c context.Context, rsc []io.ReadSeeker, w io.Writer, dividerPage bool, conf *model.Configuration) (err error) {
	defer fault.Catch(&err)

	if err := contextutil.Check(c); err != nil {
		return err
	}
	if len(rsc) == 0 {
		return fmt.Errorf("missing PDF inputs: %w", ErrMissingPDFInput)
	}

	if w == nil {
		return ErrMissingPDFWriter
	}

	conf = operationConfiguration(conf, model.MERGECREATE)
	conf.CreateBookmarks = false

	ctxDest, err := ReadAndValidate(c, rsc[0], conf)
	if err != nil {
		return fmt.Errorf("merge source 0: read and validate: %w", err)
	}

	ctxDest.EnsureVersionForWriting()

	for i, f := range rsc[1:] {
		if err = appendTo(c, f, fmt.Sprintf("%d", i+1), ctxDest, dividerPage); err != nil {
			return err
		}
	}

	if conf.OptimizeBeforeWriting {
		if err = OptimizeContext(c, ctxDest); err != nil {
			return fmt.Errorf("merge: optimize context: %w", err)
		}
	}

	if err = WriteContext(c, ctxDest, w); err != nil {
		return fmt.Errorf("merge: write output: %w", err)
	}
	return nil
}

func prepDestContext(c context.Context, destFile string, rs io.ReadSeeker, conf *model.Configuration) (*model.Context, error) {
	if err := contextutil.Check(c); err != nil {
		return nil, err
	}
	if rs == nil {
		return nil, ErrMissingPDFReadSeeker
	}

	ctxDest, err := ReadAndValidate(c, rs, conf)
	if err != nil {
		return nil, fmt.Errorf("merge destination %s: read and validate: %w", filepath.Base(destFile), err)
	}

	if conf.CreateBookmarks && conf.MergeBookmarkMode != model.MergeBookmarkModePreserve {
		if err := pdfcpu.EnsureOutlines(c, ctxDest, filepath.Base(destFile), conf.Cmd == model.MERGEAPPEND); err != nil {
			return nil, fmt.Errorf("merge destination %s: ensure outlines: %w", filepath.Base(destFile), err)
		}
	}

	if ctxDest.XRefTable.Version() < model.V20 {
		ctxDest.EnsureVersionForWriting()
	}
	return ctxDest, nil
}

func mergeDestFile(destFile string, inFiles []string) (string, []string, error) {
	if destFile != "" {
		return destFile, inFiles, nil
	}
	if len(inFiles) == 0 {
		return "", nil, ErrMissingPDFInput
	}
	return inFiles[0], inFiles[1:], nil
}

func mergeConfiguration(destFile string, conf *model.Configuration) *model.Configuration {
	cmd := model.MERGECREATE
	if destFile != "" {
		cmd = model.MERGEAPPEND
	}
	return operationConfiguration(conf, cmd)
}

// Merge concatenates inFiles and supports cancellation.
// if destFile is supplied it appends the result to destfile (=MERGEAPPEND)
// if no destFile supplied it writes the result to the first entry of inFiles (=MERGECREATE).
func Merge(c context.Context, destFile string, inFiles []string, w io.Writer, conf *model.Configuration, dividerPage bool) (err error) {
	if err := contextutil.Check(c); err != nil {
		return err
	}
	if w == nil {
		return ErrMissingPDFWriter
	}

	conf = mergeConfiguration(destFile, conf)
	destFile, inFiles, err = mergeDestFile(destFile, inFiles)
	if err != nil {
		return err
	}

	f, err := os.Open(destFile)
	if err != nil {
		return fmt.Errorf("merge destination: open %s: %w", destFile, err)
	}
	defer func() {
		closeErr := f.Close()
		if err != nil {
			return
		}
		err = wrapMergeCleanupError("merge destination: close input", closeErr)
	}()

	ctxDest, err := prepDestContext(c, destFile, f, conf)
	if err != nil {
		return err
	}

	for _, fName := range inFiles {
		if err := appendFile(c, fName, ctxDest, dividerPage); err != nil {
			return err
		}
	}

	if conf.OptimizeBeforeWriting {
		if err := OptimizeContext(c, ctxDest); err != nil {
			return fmt.Errorf("merge: optimize context: %w", err)
		}
	}

	if err := WriteContext(c, ctxDest, w); err != nil {
		return fmt.Errorf("merge: write output: %w", err)
	}
	return nil
}

// MergeCreateFile merges inFiles, writes the result to outFile and supports cancellation.
func MergeCreateFile(c context.Context, inFiles []string, outFile string, dividerPage bool, conf *model.Configuration) (err error) {
	if err := contextutil.Check(c); err != nil {
		return err
	}
	ok := false
	staged, err := openStagedOutput(nil, "", outFile, "merge")
	if err != nil {
		return fmt.Errorf("merge: create output: %w", err)
	}
	f := staged.output.file

	defer func() {
		if !ok {
			err = staged.cleanup(err)
			return
		}
		err = staged.commit()
	}()

	if err = Merge(c, "", inFiles, f, conf, dividerPage); err != nil {
		return err
	}
	if err = contextutil.Check(c); err != nil {
		return err
	}
	ok = true
	return nil
}

// MergeAppendFile appends inFiles to outFile and supports cancellation.
func MergeAppendFile(c context.Context, inFiles []string, outFile string, dividerPage bool, conf *model.Configuration) (err error) {
	if err := contextutil.Check(c); err != nil {
		return err
	}
	var f *os.File
	ok := false

	tmpFile := outFile
	destFile := ""

	if mergeFileExists(outFile) {
		destFile = outFile
	}
	staged, err := openStagedOutput(nil, "", tmpFile, "merge")
	if err != nil {
		return fmt.Errorf("merge: create output: %w", err)
	}
	f = staged.output.file

	defer func() {
		if !ok {
			err = staged.cleanup(err)
			return
		}
		err = staged.commit()
	}()

	if err = Merge(c, destFile, inFiles, f, conf, dividerPage); err != nil {
		return err
	}
	if err = contextutil.Check(c); err != nil {
		return err
	}

	ok = true
	return nil
}

// MergeCreateZip zips rs1 and rs2 into w and supports cancellation.
func MergeCreateZip(c context.Context, rs1, rs2 io.ReadSeeker, w io.Writer, conf *model.Configuration) (err error) {
	defer fault.Catch(&err)

	if err := contextutil.Check(c); err != nil {
		return err
	}
	if rs1 == nil {
		return fmt.Errorf("merge zip source 1: %w", ErrMissingPDFReadSeeker)
	}

	if rs2 == nil {
		return fmt.Errorf("merge zip source 2: %w", ErrMissingPDFReadSeeker)
	}

	if w == nil {
		return ErrMissingPDFWriter
	}

	conf = operationConfiguration(conf, model.MERGECREATEZIP)

	ctxDest, err := ReadAndValidate(c, rs1, conf)
	if err != nil {
		return fmt.Errorf("merge zip source 1: read and validate: %w", err)
	}
	if ctxDest.XRefTable.Version() == model.V20 {
		return fmt.Errorf("merge zip source 1: validate version: %w", pdfcpu.ErrUnsupportedVersion)
	}
	ctxDest.EnsureVersionForWriting()

	if _, err = pdfcpu.RemoveBookmarks(c, ctxDest); err != nil {
		return fmt.Errorf("merge zip source 1: remove bookmarks: %w", err)
	}

	ctxSrc, err := ReadAndValidate(c, rs2, conf)
	if err != nil {
		return fmt.Errorf("merge zip source 2: read and validate: %w", err)
	}
	if ctxSrc.XRefTable.Version() == model.V20 {
		return fmt.Errorf("merge zip source 2: validate version: %w", pdfcpu.ErrUnsupportedVersion)
	}

	if err := pdfcpu.MergeXRefTables(c, "", ctxSrc, ctxDest, true, false); err != nil {
		return fmt.Errorf("merge zip: append pages: %w", err)
	}

	if conf.OptimizeBeforeWriting {
		if err := OptimizeContext(c, ctxDest); err != nil {
			return fmt.Errorf("merge zip: optimize context: %w", err)
		}
	}

	if err := WriteContext(c, ctxDest, w); err != nil {
		return fmt.Errorf("merge zip: write output: %w", err)
	}
	return nil
}

// MergeCreateZipFile zips inFile1 and inFile2 into outFile and supports cancellation.
func MergeCreateZipFile(c context.Context, inFile1, inFile2, outFile string, conf *model.Configuration) (err error) {
	if err := contextutil.Check(c); err != nil {
		return err
	}
	var f1, f2, f *os.File
	ok := false

	if f1, err = os.Open(inFile1); err != nil {
		return fmt.Errorf("merge zip source 1: open %s: %w", inFile1, err)
	}

	if f2, err = os.Open(inFile2); err != nil {
		_ = f1.Close()
		return fmt.Errorf("merge zip source 2: open %s: %w", inFile2, err)
	}

	staged, err := openStagedOutput(f2, "", outFile, "merge zip")
	if err != nil {
		_ = f1.Close()
		_ = f2.Close()
		return fmt.Errorf("merge zip: create output %s: %w", outFile, err)
	}
	f = staged.output.file
	staged = staged.withInput(f1, "merge zip source 1: close")
	staged.inputs[0].context = "merge zip source 2: close"

	defer func() {
		if !ok {
			err = staged.cleanup(err)
			return
		}
		err = staged.commit()
	}()

	if err = MergeCreateZip(c, f1, f2, f, conf); err != nil {
		return err
	}
	if err = contextutil.Check(c); err != nil {
		return err
	}
	ok = true
	return nil
}

// MergeOptions controls MergeRawWithReport and MergeContexts.
// The zero value merges like MergeRaw.
type MergeOptions struct {
	// DividerPage inserts a blank page between merged inputs, see MergeRaw.
	DividerPage bool

	// AllowEncrypted merges encrypted inputs that open without a user password
	// (or with the configured passwords) instead of failing with ErrEncrypted.
	// They are decrypted in memory; the output is not encrypted and has no /Encrypt.
	AllowEncrypted bool

	// LowerPDF20 merges a PDF 2.0 input behind an input 0 older than PDF 2.0
	// instead of failing with ErrUnsupportedVersion. The output keeps the
	// version of input 0, raised to at least PDF 1.7, so a first input older
	// than 2.0 gives a PDF 1.7 output that may contain PDF 2.0 constructs of
	// the lowered input.
	LowerPDF20 bool
}

// MergeInputReport describes how MergeRawWithReport read and appended one input.
type MergeInputReport struct {
	ReadReport

	// LoweredFromPDF20 reports that the input is PDF 2.0 and was appended behind
	// an older first input with MergeOptions.LowerPDF20. Its PDF 2.0 content is
	// then in an output labelled with a version below 2.0.
	LoweredFromPDF20 bool
}

// MergeReport describes how MergeRawWithReport read and merged its inputs.
type MergeReport struct {
	// Inputs holds the report of every input, in input order.
	Inputs []MergeInputReport

	// Output holds the validation notices that the context of input 0 gained
	// after it was read: those recorded while the inputs were appended, the
	// output was optimized and it was written. A notice here can repeat a
	// notice of Inputs[0] for the same annotation, because writing checks the
	// annotations again. The notices of the inputs are in Inputs.
	Output model.ValidationReport
}

// MergeInputError reports a merge failure caused while reading or appending
// one input. Its message is the message of Err.
// It does not imply that the input is at fault: classify Err with errors.Is
// and errors.As, eg. for context.Canceled, ErrWrongPassword or ErrInvalidPDF.
type MergeInputError struct {
	// Index is the zero-based position of the input.
	Index int
	Err   error
}

// Error implements error.
func (e *MergeInputError) Error() string {
	return e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *MergeInputError) Unwrap() error {
	return e.Err
}

// errDuplicateMergeContext signals a context passed to MergeContexts more than once.
var errDuplicateMergeContext = errors.New("context passed more than once")

// MergeRawWithReport merges rsc in order into the document of rsc[0], writes
// the result to w and reports how every input was read. Every input is read
// and validated once, with conf: a nil conf selects
// model.NewTolerantConfiguration(), a non-nil conf is cloned. The output keeps
// the catalog and version of rsc[0]. With zero opts the result equals MergeRaw
// for the same conf.
//
// A failure while reading or appending input i is a *MergeInputError with
// Index i; a missing user password matches ErrWrongPassword. Input i is
// checked for encryption and version only after it was read, so the inputs
// before it were already appended and nothing is written. A recovered panic
// is a *PanicError. Write failures are not input errors, and w may have
// received part of the output. On error the report is zero.
func MergeRawWithReport(c context.Context, rsc []io.ReadSeeker, w io.Writer, conf *model.Configuration, opts MergeOptions) (report MergeReport, err error) {
	defer func() {
		if err != nil {
			report = MergeReport{}
		}
	}()
	defer catchPanic(&err)

	if err := contextutil.Check(c); err != nil {
		return MergeReport{}, err
	}
	if len(rsc) == 0 {
		return MergeReport{}, fmt.Errorf("missing PDF inputs: %w", ErrMissingPDFInput)
	}
	if w == nil {
		return MergeReport{}, ErrMissingPDFWriter
	}

	if conf == nil {
		conf = model.NewTolerantConfiguration()
	}
	conf = operationConfiguration(conf, model.MERGECREATE)
	conf.CreateBookmarks = false

	report.Inputs = make([]MergeInputReport, len(rsc))
	var ctxDest *model.Context
	var destRead model.ValidationReport
	for i, rs := range rsc {
		if i > 0 {
			if err := checkMergeSource(c, i, rs); err != nil {
				return MergeReport{}, err
			}
		}
		ctx, err := readMergeInput(c, rs, conf, opts.AllowEncrypted)
		if err != nil {
			return MergeReport{}, &MergeInputError{Index: i, Err: fmt.Errorf("merge source %d: read and validate: %w", i, err)}
		}
		report.Inputs[i].ReadReport = newReadReport(ctx)
		if i == 0 {
			if err := checkMergeEncryption(0, ctx, opts); err != nil {
				return MergeReport{}, err
			}
			ctxDest = ctx
			destRead = ctx.ValidationReport()
			prepareMergeDest(ctxDest)
			continue
		}
		lowered, err := appendMergeContext(c, i, ctx, ctxDest, opts)
		if err != nil {
			return MergeReport{}, err
		}
		report.Inputs[i].LoweredFromPDF20 = lowered
	}

	if err := writeMergedContext(c, ctxDest, w); err != nil {
		return MergeReport{}, err
	}
	report.Output = ctxDest.ValidationReport().Since(destRead)
	return report, nil
}

// MergeContexts merges ctxs in order into ctxs[0] and writes the result to w.
// The contexts must have been read and validated, eg. by
// ReadAndValidateWithReport. ctxs[0] becomes the merged document: its
// configuration is replaced by a clone set up for merging. The other contexts
// are consumed and must not be used afterwards. The contexts may be used by
// one goroutine at a time. No context may appear twice; contexts that share
// one XRefTable count as the same context.
//
// Errors are classified like those of MergeRawWithReport. Encrypted contexts
// were already decrypted while reading, so with opts.AllowEncrypted only the
// encryption of the output is dropped.
//
// All inputs are checked before anything is changed: nil and duplicate
// contexts, encryption unless opts.AllowEncrypted, and the version of PDF 2.0
// inputs unless opts.LowerPDF20. An error from these checks leaves all
// contexts unchanged. An error later, while appending or writing, leaves
// ctxs[0] partly merged and the other contexts consumed.
//
// Notices recorded while the output is optimized and written go to ctxs[0],
// see ctxs[0].ValidationReport.
func MergeContexts(c context.Context, ctxs []*model.Context, w io.Writer, opts MergeOptions) (err error) {
	defer catchPanic(&err)

	if err := contextutil.Check(c); err != nil {
		return err
	}
	if len(ctxs) == 0 {
		return fmt.Errorf("missing PDF inputs: %w", ErrMissingPDFInput)
	}
	if w == nil {
		return ErrMissingPDFWriter
	}
	if err := checkMergeContexts(ctxs, opts); err != nil {
		return err
	}

	ctxDest := ctxs[0]
	conf := ctxDest.Configuration.Clone()
	conf.Cmd = model.MERGECREATE
	conf.CreateBookmarks = false
	ctxDest.Configuration = conf
	ctxDest.XRefTable.Conf = conf

	prepareMergeDest(ctxDest)
	for i, ctx := range ctxs[1:] {
		if _, err := appendMergeContext(c, i+1, ctx, ctxDest, opts); err != nil {
			return err
		}
	}
	return writeMergedContext(c, ctxDest, w)
}

// checkMergeContexts checks all contexts of MergeContexts before any of them is changed.
func checkMergeContexts(ctxs []*model.Context, opts MergeOptions) error {
	seen := make(map[*model.XRefTable]bool, len(ctxs))
	for i, ctx := range ctxs {
		if ctx == nil || ctx.XRefTable == nil || ctx.Configuration == nil {
			return &MergeInputError{Index: i, Err: fmt.Errorf("merge source %d: %w", i, ErrMissingContext)}
		}
		if seen[ctx.XRefTable] {
			return &MergeInputError{Index: i, Err: fmt.Errorf("merge source %d: %w", i, errDuplicateMergeContext)}
		}
		seen[ctx.XRefTable] = true
	}
	for i, ctx := range ctxs {
		if err := checkMergeEncryption(i, ctx, opts); err != nil {
			return err
		}
		if i > 0 {
			if err := checkMergeVersion(i, ctx, ctxs[0], opts); err != nil {
				return err
			}
		}
	}
	return nil
}

// checkMergeSource checks the context and reader of input i > 0 before it is
// read, with the errors of appendTo.
func checkMergeSource(c context.Context, i int, rs io.ReadSeeker) error {
	if err := contextutil.Check(c); err != nil {
		return &MergeInputError{Index: i, Err: err}
	}
	if rs == nil {
		return &MergeInputError{Index: i, Err: fmt.Errorf("merge source %d: read source: %w", i, ErrMissingPDFReadSeeker)}
	}
	return nil
}

// readMergeInput reads and validates rs with conf, whose Cmd is MERGECREATE.
// With allowEncrypted the read runs as VALIDATE, because the reader refuses
// encrypted input for merge commands, and validation runs as MERGECREATE again.
func readMergeInput(c context.Context, rs io.ReadSeeker, conf *model.Configuration, allowEncrypted bool) (ctx *model.Context, err error) {
	defer fault.Catch(&err)

	if err := contextutil.Check(c); err != nil {
		return nil, err
	}
	if rs == nil {
		return nil, ErrMissingPDFReadSeeker
	}

	if allowEncrypted {
		conf.Cmd = model.VALIDATE
	}
	ctx, err = ReadContext(c, rs, conf)
	conf.Cmd = model.MERGECREATE
	if err != nil {
		return nil, fmt.Errorf("read context: %w", err)
	}
	return validateReadContext(c, ctx, ProgressOptions{})
}

// checkMergeEncryption refuses the encrypted input i of ctx unless opts.AllowEncrypted.
func checkMergeEncryption(i int, ctx *model.Context, opts MergeOptions) error {
	if ctx.Encrypt == nil || opts.AllowEncrypted {
		return nil
	}
	return &MergeInputError{Index: i, Err: fmt.Errorf("merge source %d: encryption status: %w", i, pdfcpu.ErrEncrypted)}
}

// followsOlderDest reports whether the PDF 2.0 input ctxSrc would follow the older destination ctxDest.
func followsOlderDest(ctxSrc, ctxDest *model.Context) bool {
	return ctxDest.XRefTable.Version() < model.V20 && ctxSrc.XRefTable.Version() == model.V20
}

// checkMergeVersion refuses the PDF 2.0 input i behind an older destination unless opts.LowerPDF20.
func checkMergeVersion(i int, ctxSrc, ctxDest *model.Context, opts MergeOptions) error {
	if !opts.LowerPDF20 && followsOlderDest(ctxSrc, ctxDest) {
		return &MergeInputError{Index: i, Err: fmt.Errorf("merge source %d: validate version: %w", i, pdfcpu.ErrUnsupportedVersion)}
	}
	return nil
}

// dropMergeEncryption drops the encryption of ctx, whose objects were decrypted
// while reading. This keeps the writer from encrypting them again and from
// writing /Encrypt (compare model.DECRYPT in pdfcpu.handleEncryption).
func dropMergeEncryption(ctx *model.Context) {
	if ctx.Encrypt != nil {
		ctx.Encrypt, ctx.E, ctx.EncKey = nil, nil, nil
	}
}

// prepareMergeDest prepares ctxDest, the context of input 0, like MergeRaw.
func prepareMergeDest(ctxDest *model.Context) {
	dropMergeEncryption(ctxDest)
	ctxDest.EnsureVersionForWriting()
}

// appendMergeContext appends ctxSrc, the context of input i, to ctxDest like
// appendTo. It reports whether the input was lowered from PDF 2.0.
func appendMergeContext(c context.Context, i int, ctxSrc, ctxDest *model.Context, opts MergeOptions) (bool, error) {
	if err := contextutil.Check(c); err != nil {
		return false, &MergeInputError{Index: i, Err: err}
	}
	if err := checkMergeEncryption(i, ctxSrc, opts); err != nil {
		return false, err
	}
	if err := checkMergeVersion(i, ctxSrc, ctxDest, opts); err != nil {
		return false, err
	}
	lowered := opts.LowerPDF20 && followsOlderDest(ctxSrc, ctxDest)
	dropMergeEncryption(ctxSrc)
	if err := pdfcpu.MergeXRefTables(c, fmt.Sprintf("%d", i), ctxSrc, ctxDest, false, opts.DividerPage); err != nil {
		return false, &MergeInputError{Index: i, Err: fmt.Errorf("merge source %d: append pages: %w", i, err)}
	}
	return lowered, nil
}

// writeMergedContext optimizes and writes the merged context like MergeRaw.
func writeMergedContext(c context.Context, ctxDest *model.Context, w io.Writer) error {
	if ctxDest.Configuration.OptimizeBeforeWriting {
		if err := OptimizeContext(c, ctxDest); err != nil {
			return fmt.Errorf("merge: optimize context: %w", err)
		}
	}
	if err := WriteContext(c, ctxDest, w); err != nil {
		return fmt.Errorf("merge: write output: %w", err)
	}
	return nil
}
