package wav

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const maxDataBytes = 500 * 1024 * 1024 // 500 MB

const (
	// int16Range is 2^16, used for sign-extension of 16-bit PCM samples.
	int16Range = 1 << 16
)

const (
	expectedSampleRate = 16000
	expectedBitDepth   = 16
	bytesPerSample     = 2
	minFmtChunkSize    = 16
	float32Amplitude   = 32768.0
)

var (
	ErrNotWAV            = errors.New("not a WAV file")
	ErrUnsupportedFormat = errors.New("unsupported audio format")
	ErrUnsupportedDepth  = errors.New("unsupported bit depth")
	ErrDataTooLarge      = errors.New("audio data too large")
)

// Header contains audio metadata parsed from the WAV file.
type Header struct {
	SampleRate  uint32
	NumChannels uint16
	BitDepth    uint16
	NumSamples  uint32
	Duration    time.Duration
}

// Decoder reads PCM samples from a WAV file.
type Decoder struct {
	r         io.Reader
	closer    io.Closer
	Header    Header
	closeOnce sync.Once
	pos       uint32 // current sample index
	dataStart int64  // byte offset of first sample from stream start
}

// validatePath ensures the given file path is safe to open.
// It checks that the path is not a symlink and that the file exists as a regular file.
func validatePath(path string) error {
	if path == "" {
		return errors.New("empty file path")
	}

	// Use Lstat instead of Stat to detect symlinks without following them.
	fileInfo, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("stat path: %w", err)
	}

	// Reject symlinks to prevent symlink-based file inclusion attacks.
	if fileInfo.Mode()&os.ModeSymlink != 0 {
		return errors.New("file path is a symlink")
	}

	// Ensure it's a regular file, not a directory or device.
	if !fileInfo.Mode().IsRegular() {
		return errors.New("path is not a regular file")
	}

	// Ensure file has non-zero size to avoid opening empty/invalid files.
	if fileInfo.Size() == 0 {
		return errors.New("file is empty")
	}

	// Validate WAV extension as a simple sanity check.
	if !strings.HasSuffix(strings.ToLower(path), ".wav") {
		return fmt.Errorf("expected .wav extension, got: %s", filepath.Ext(path))
	}

	return nil
}

// Open opens a WAV file at path.
func Open(path string) (*Decoder, error) {
	if err := validatePath(path); err != nil {
		return nil, fmt.Errorf("wav: %w", err)
	}

	// Safe: path validated by validatePath above (not symlink, regular file, .wav ext, not empty).
	file, err := os.Open(path) //nolint:gosec // path validated above
	if err != nil {
		return nil, fmt.Errorf("wav: %w", err)
	}

	dec, err := NewDecoder(file)
	if err != nil {
		return nil, errors.Join(err, file.Close())
	}

	dec.closer = file

	return dec, nil
}

// NewDecoder creates a Decoder from an io.Reader.
func NewDecoder(r io.Reader) (*Decoder, error) {
	return parseHeader(r)
}

// validateWAVMagic checks RIFF and WAVE magic bytes.
func validateWAVMagic(readExact func([]byte) error) error {
	var riff [12]byte
	if err := readExact(riff[:]); err != nil {
		return fmt.Errorf("%w: %w", ErrNotWAV, err)
	}

	if string(riff[0:4]) != "RIFF" {
		return fmt.Errorf("%w: missing RIFF magic", ErrNotWAV)
	}

	if string(riff[8:12]) != "WAVE" {
		return fmt.Errorf("%w: missing WAVE magic", ErrNotWAV)
	}

	return nil
}

// parseWAVChunks processes chunks from a WAV stream and returns the Decoder when data chunk is found.
func parseWAVChunks(
	r io.Reader,
	initialBytesRead int64,
	readExact func([]byte) error,
	skipN func(int64) error,
) (*Decoder, error) {
	var (
		hdr       Header
		hasFmt    bool
		bytesRead = initialBytesRead
	)

	var chunkHdr [8]byte
	for {
		if err := readExact(chunkHdr[:]); err != nil {
			break
		}

		bytesRead += 8

		chunkID := string(chunkHdr[0:4])
		size := binary.LittleEndian.Uint32(chunkHdr[4:])

		switch chunkID {
		case "fmt ":
			h, err := parseFmtChunk(size, readExact, skipN)
			if err != nil {
				return nil, err
			}

			hdr = h
			hasFmt = true
			bytesRead += chunkBytes(size)

		case "data":
			return buildDataDecoder(r, hdr, hasFmt, size, bytesRead)

		default:
			if err := skipUnknownChunk(chunkID, size, skipN); err != nil {
				return nil, err
			}

			bytesRead += chunkBytes(size)
		}
	}

	if !hasFmt {
		return nil, errors.New("missing fmt chunk")
	}

	return nil, errors.New("missing data chunk")
}

// chunkBytes returns the padded chunk size (even-aligned).
func chunkBytes(size uint32) int64 {
	n := int64(size)
	if size%2 != 0 {
		n++
	}

	return n
}

// buildDataDecoder validates the data chunk and returns a Decoder.
func buildDataDecoder(r io.Reader, hdr Header, hasFmt bool, size uint32, dataStart int64) (*Decoder, error) {
	if err := validateDataChunk(hasFmt, size); err != nil {
		return nil, err
	}

	numSamples := size / bytesPerSample

	hdr.NumSamples = numSamples
	if hdr.SampleRate > 0 {
		hdr.Duration = time.Duration(float64(numSamples) / float64(hdr.SampleRate) * float64(time.Second))
	}

	return &Decoder{r: r, Header: hdr, dataStart: dataStart}, nil
}

func parseHeader(r io.Reader) (*Decoder, error) {
	var bytesRead int64

	readExact := func(buf []byte) error {
		n, err := io.ReadFull(r, buf)
		bytesRead += int64(n)

		if err != nil {
			return fmt.Errorf("wav: %w", err)
		}

		return nil
	}

	skipN := func(n int64) error {
		m, err := io.CopyN(io.Discard, r, n)
		bytesRead += m

		if err != nil {
			return fmt.Errorf("wav: %w", err)
		}

		return nil
	}

	if err := validateWAVMagic(readExact); err != nil {
		return nil, err
	}

	decoder, err := parseWAVChunks(r, bytesRead, readExact, skipN)
	if err != nil {
		return nil, err
	}

	return decoder, nil
}

func parseFmtChunk(size uint32, readExact func([]byte) error, skipN func(int64) error) (Header, error) {
	if size < minFmtChunkSize {
		return Header{}, fmt.Errorf("fmt chunk too small: %d bytes", size)
	}

	fmtData := make([]byte, size)
	if err := readExact(fmtData); err != nil {
		return Header{}, fmt.Errorf("read fmt data: %w", err)
	}

	if size%2 != 0 {
		if err := skipN(1); err != nil {
			return Header{}, fmt.Errorf("skip fmt padding: %w", err)
		}
	}

	audioFmt := binary.LittleEndian.Uint16(fmtData[0:2])
	channels := binary.LittleEndian.Uint16(fmtData[2:4])
	sampleRate := binary.LittleEndian.Uint32(fmtData[4:8])
	bitDepth := binary.LittleEndian.Uint16(fmtData[14:16])

	if audioFmt != 1 {
		return Header{}, fmt.Errorf("%w: AudioFormat=%d, need 1 (PCM)", ErrUnsupportedFormat, audioFmt)
	}

	if channels != 1 {
		return Header{}, fmt.Errorf("%w: NumChannels=%d, need 1 (mono)", ErrUnsupportedFormat, channels)
	}

	if sampleRate != expectedSampleRate {
		return Header{}, fmt.Errorf("%w: SampleRate=%d, need %d", ErrUnsupportedFormat, sampleRate, expectedSampleRate)
	}

	if bitDepth != expectedBitDepth {
		return Header{}, fmt.Errorf("%w: BitsPerSample=%d, need %d", ErrUnsupportedDepth, bitDepth, expectedBitDepth)
	}

	return Header{
		SampleRate:  sampleRate,
		NumChannels: channels,
		BitDepth:    bitDepth,
	}, nil
}

func validateDataChunk(hasFmt bool, size uint32) error {
	if !hasFmt {
		return errors.New("data chunk before fmt chunk")
	}

	if uint64(size) > maxDataBytes {
		return fmt.Errorf("%w: %d bytes", ErrDataTooLarge, size)
	}

	if size%2 != 0 {
		return fmt.Errorf("odd data chunk size: %d", size)
	}

	return nil
}

func skipUnknownChunk(_ string, size uint32, skipN func(int64) error) error {
	skip := int64(size)
	if size%2 != 0 {
		skip++
	}

	if skip > 0 {
		if err := skipN(skip); err != nil {
			return err
		}
	}

	return nil
}

// Read fills buf with float32 samples normalized to [-1, 1].
// Returns io.EOF when all samples have been read.
func (d *Decoder) Read(buf []float32) (int, error) {
	if len(buf) == 0 {
		return 0, nil
	}

	if d.pos >= d.Header.NumSamples {
		return 0, io.EOF
	}

	remaining := int64(d.Header.NumSamples - d.pos)

	toRead := min(int64(len(buf)), remaining)

	if toRead == 0 {
		return 0, io.EOF
	}

	rawLen := toRead * int64(bytesPerSample)
	raw := make([]byte, rawLen)

	if _, err := io.ReadFull(d.r, raw); err != nil {
		return 0, fmt.Errorf("read samples: %w", err)
	}

	toReadInt := int(toRead)
	for i := range toReadInt {
		offset := int64(i) * int64(bytesPerSample)

		sample := int32(binary.LittleEndian.Uint16(raw[offset : offset+2]))
		if sample >= 1<<15 {
			sample -= int16Range
		}

		buf[i] = float32(sample) / float32Amplitude
	}

	// toRead is capped by remaining = NumSamples - pos.
	// Both NumSamples and pos are uint32, so remaining ≤ uint32 max.
	if toRead > 0 && toRead <= 0xffffffff {
		d.pos += uint32(toRead)
	}

	if toRead < int64(len(buf)) {
		return int(toRead), io.EOF
	}

	return int(toRead), nil
}

// Seek sets the sample position. Works only if the underlying reader implements io.Seeker.
func (d *Decoder) Seek(sampleOffset int64, whence int) (int64, error) {
	seeker, ok := d.r.(io.Seeker)
	if !ok {
		return 0, errors.New("underlying reader does not support seeking")
	}

	var absPos int64

	switch whence {
	case io.SeekStart:
		absPos = sampleOffset
	case io.SeekCurrent:
		absPos = int64(d.pos) + sampleOffset
	case io.SeekEnd:
		absPos = int64(d.Header.NumSamples) + sampleOffset
	default:
		return 0, fmt.Errorf("invalid whence: %d", whence)
	}

	if absPos < 0 || absPos > int64(d.Header.NumSamples) {
		return 0, fmt.Errorf("seek out of range: %d not in [0, %d]", absPos, d.Header.NumSamples)
	}

	if absPos > 1<<32-1 {
		return 0, fmt.Errorf("seek: position %d exceeds uint32 range", absPos)
	}

	byteOffset := d.dataStart + absPos*int64(bytesPerSample)
	if _, err := seeker.Seek(byteOffset, io.SeekStart); err != nil {
		return 0, fmt.Errorf("seek: %w", err)
	}

	d.pos = uint32(absPos)

	return absPos, nil
}

// ReadAll reads all remaining samples.
func (d *Decoder) ReadAll() ([]float32, error) {
	remaining := d.Header.NumSamples - d.pos
	if remaining == 0 {
		return nil, nil
	}

	buf := make([]float32, remaining)

	n, err := d.Read(buf)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}

	return buf[:n], nil
}

// Close releases resources. Safe to call multiple times.
func (d *Decoder) Close() error {
	var err error

	d.closeOnce.Do(func() {
		if d.closer != nil {
			err = d.closer.Close()
		}
	})

	if err != nil {
		return fmt.Errorf("wav: %w", err)
	}

	return nil
}
