package wav_test

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"

	"github.com/partyzanex/a2text/pkg/audio/wav"
)

// buildWAV constructs a minimal RIFF/WAVE file in memory.
func buildWAV(sampleRate uint32, numChannels, bitDepth uint16, audioFormat uint16, samples []int16) []byte {
	dataSize := uint32(len(samples) * int(bitDepth/8))
	fmtSize := uint32(16)

	buf := &bytes.Buffer{}

	// RIFF header
	buf.WriteString("RIFF")
	_ = binary.Write(buf, binary.LittleEndian, 4+8+fmtSize+8+dataSize) // ChunkSize
	buf.WriteString("WAVE")

	// fmt chunk
	buf.WriteString("fmt ")
	_ = binary.Write(buf, binary.LittleEndian, fmtSize)
	_ = binary.Write(buf, binary.LittleEndian, audioFormat)                                       // AudioFormat
	_ = binary.Write(buf, binary.LittleEndian, numChannels)                                       // NumChannels
	_ = binary.Write(buf, binary.LittleEndian, sampleRate)                                        // SampleRate
	_ = binary.Write(buf, binary.LittleEndian, sampleRate*uint32(numChannels)*uint32(bitDepth/8)) // ByteRate
	_ = binary.Write(buf, binary.LittleEndian, numChannels*bitDepth/8)                            // BlockAlign
	_ = binary.Write(buf, binary.LittleEndian, bitDepth)                                          // BitsPerSample

	// data chunk
	buf.WriteString("data")
	_ = binary.Write(buf, binary.LittleEndian, dataSize)

	for _, s := range samples {
		_ = binary.Write(buf, binary.LittleEndian, s)
	}

	return buf.Bytes()
}

// validWAV returns a valid 16kHz mono PCM16 WAV with n silence samples.
func validWAV(n int) []byte {
	return buildWAV(16000, 1, 16, 1, make([]int16, n))
}

// sineWAV generates a 440 Hz sine wave for the given duration.
func sineWAV(duration time.Duration) []byte {
	const (
		sampleRate = uint32(16000)
		freq       = 440.0
	)

	n := int(duration.Seconds() * float64(sampleRate))

	samples := make([]int16, n)
	for i := range n {
		samples[i] = int16(math.Sin(2*math.Pi*freq*float64(i)/float64(sampleRate)) * 32767)
	}

	return buildWAV(sampleRate, 1, 16, 1, samples)
}

type DecoderSuite struct {
	suite.Suite
}

func TestDecoderSuite(t *testing.T) {
	suite.Run(t, new(DecoderSuite))
}

// --- NewDecoder: happy path ---

func (s *DecoderSuite) TestNewDecoder_ValidWAV_ReturnsDecoder() {
	r := bytes.NewReader(validWAV(100))
	dec, err := wav.NewDecoder(r)
	s.Require().NoError(err)
	s.NotNil(dec)
}

func (s *DecoderSuite) TestNewDecoder_HeaderParsedCorrectly() {
	r := bytes.NewReader(validWAV(100))
	dec, err := wav.NewDecoder(r)
	s.Require().NoError(err)
	s.Equal(uint32(16000), dec.Header.SampleRate)
	s.Equal(uint16(1), dec.Header.NumChannels)
	s.Equal(uint16(16), dec.Header.BitDepth)
	s.Equal(uint32(100), dec.Header.NumSamples)
}

func (s *DecoderSuite) TestNewDecoder_Duration_Correct() {
	r := bytes.NewReader(validWAV(16000)) // 1 секунда при 16kHz
	dec, err := wav.NewDecoder(r)
	s.Require().NoError(err)
	s.Equal(time.Second, dec.Header.Duration)
}

func (s *DecoderSuite) TestNewDecoder_ExtraChunksBefore_DataStillParsed() {
	// WAV с LIST-чанком перед data — должен работать
	buf := &bytes.Buffer{}
	buf.WriteString("RIFF")

	listData := []byte("INFOtest    ")
	fmtSize := uint32(16)
	dataSize := uint32(10 * 2)
	totalSize := 4 + 8 + fmtSize + 8 + uint32(len(listData)) + 8 + dataSize
	_ = binary.Write(buf, binary.LittleEndian, totalSize)
	buf.WriteString("WAVE")

	buf.WriteString("fmt ")
	_ = binary.Write(buf, binary.LittleEndian, fmtSize)
	_ = binary.Write(buf, binary.LittleEndian, uint16(1))     // PCM
	_ = binary.Write(buf, binary.LittleEndian, uint16(1))     // mono
	_ = binary.Write(buf, binary.LittleEndian, uint32(16000)) // 16kHz
	_ = binary.Write(buf, binary.LittleEndian, uint32(32000)) // ByteRate
	_ = binary.Write(buf, binary.LittleEndian, uint16(2))     // BlockAlign
	_ = binary.Write(buf, binary.LittleEndian, uint16(16))    // 16-bit

	buf.WriteString("LIST")
	_ = binary.Write(buf, binary.LittleEndian, uint32(len(listData)))
	buf.Write(listData)

	buf.WriteString("data")
	_ = binary.Write(buf, binary.LittleEndian, dataSize)
	buf.Write(make([]byte, dataSize))

	dec, err := wav.NewDecoder(buf)
	s.Require().NoError(err)
	s.Equal(uint32(10), dec.Header.NumSamples)
}

// --- NewDecoder: ошибки формата ---

func (s *DecoderSuite) TestNewDecoder_InvalidRIFF_ReturnsErrNotWAV() {
	r := bytes.NewReader([]byte("XXXX\x00\x00\x00\x00WAVE"))
	_, err := wav.NewDecoder(r)
	s.ErrorIs(err, wav.ErrNotWAV)
}

func (s *DecoderSuite) TestNewDecoder_InvalidWAVE_ReturnsErrNotWAV() {
	r := bytes.NewReader([]byte("RIFF\x00\x00\x00\x00XXXX"))
	_, err := wav.NewDecoder(r)
	s.ErrorIs(err, wav.ErrNotWAV)
}

func (s *DecoderSuite) TestNewDecoder_NonPCMFormat_ReturnsErrUnsupportedFormat() {
	data := buildWAV(16000, 1, 16, 2, nil) // AudioFormat=2 (ADPCM)
	_, err := wav.NewDecoder(bytes.NewReader(data))
	s.ErrorIs(err, wav.ErrUnsupportedFormat)
}

func (s *DecoderSuite) TestNewDecoder_IEEEFloatFormat_ReturnsErrUnsupportedFormat() {
	data := buildWAV(16000, 1, 32, 3, nil) // AudioFormat=3 (IEEE float)
	_, err := wav.NewDecoder(bytes.NewReader(data))
	s.ErrorIs(err, wav.ErrUnsupportedFormat)
}

func (s *DecoderSuite) TestNewDecoder_8BitDepth_ReturnsErrUnsupportedDepth() {
	data := buildWAV(16000, 1, 8, 1, nil)
	_, err := wav.NewDecoder(bytes.NewReader(data))
	s.ErrorIs(err, wav.ErrUnsupportedDepth)
}

func (s *DecoderSuite) TestNewDecoder_24BitDepth_ReturnsErrUnsupportedDepth() {
	data := buildWAV(16000, 1, 24, 1, nil)
	_, err := wav.NewDecoder(bytes.NewReader(data))
	s.ErrorIs(err, wav.ErrUnsupportedDepth)
}

func (s *DecoderSuite) TestNewDecoder_WrongSampleRate_ReturnsErrUnsupportedFormat() {
	for _, rate := range []uint32{8000, 22050, 44100, 48000} {
		data := buildWAV(rate, 1, 16, 1, nil)
		_, err := wav.NewDecoder(bytes.NewReader(data))
		s.ErrorIs(err, wav.ErrUnsupportedFormat, "sample rate %d should fail", rate)
	}
}

func (s *DecoderSuite) TestNewDecoder_StereoChannels_ReturnsErrUnsupportedFormat() {
	data := buildWAV(16000, 2, 16, 1, nil)
	_, err := wav.NewDecoder(bytes.NewReader(data))
	s.ErrorIs(err, wav.ErrUnsupportedFormat)
}

func (s *DecoderSuite) TestNewDecoder_OddDataSize_ReturnsError() {
	data := buildWAV(16000, 1, 16, 1, make([]int16, 10))
	// Испортить dataSize — сделать нечётным
	dataChunkSizeOffset := 12 + 8 + 16 + 4 // после RIFF header, fmt chunk, "data"
	data[dataChunkSizeOffset] = 0x01       // нечётный размер
	_, err := wav.NewDecoder(bytes.NewReader(data))
	s.Error(err)
}

func (s *DecoderSuite) TestNewDecoder_DataTooLarge_ReturnsErrDataTooLarge() {
	data := buildWAV(16000, 1, 16, 1, make([]int16, 10))
	// Подделать dataSize = 600MB
	dataChunkSizeOffset := 12 + 8 + 16 + 4
	binary.LittleEndian.PutUint32(data[dataChunkSizeOffset:], 600*1024*1024)
	_, err := wav.NewDecoder(bytes.NewReader(data))
	s.ErrorIs(err, wav.ErrDataTooLarge)
}

func (s *DecoderSuite) TestNewDecoder_TruncatedHeader_ReturnsError() {
	data := validWAV(10)
	_, err := wav.NewDecoder(bytes.NewReader(data[:6])) // обрезан RIFF header
	s.Error(err)
}

func (s *DecoderSuite) TestNewDecoder_TruncatedFmtChunk_ReturnsError() {
	data := validWAV(10)
	_, err := wav.NewDecoder(bytes.NewReader(data[:20])) // обрезан fmt chunk
	s.Error(err)
}

func (s *DecoderSuite) TestNewDecoder_EmptyReader_ReturnsError() {
	_, err := wav.NewDecoder(bytes.NewReader(nil))
	s.Error(err)
}

// --- Open ---

func (s *DecoderSuite) TestOpen_ValidFile_ReturnsDecoder() {
	path := filepath.Join(s.T().TempDir(), "test.wav")
	s.Require().NoError(os.WriteFile(path, validWAV(100), 0o644))

	dec, err := wav.Open(path)
	s.Require().NoError(err)
	s.Require().NoError(dec.Close())
}

func (s *DecoderSuite) TestOpen_NonExistentFile_ReturnsError() {
	_, err := wav.Open("/nonexistent/file.wav")
	s.Error(err)
}

// --- Read (streaming) ---

func (s *DecoderSuite) TestRead_AllSamplesInOneCall() {
	n := 100
	data := sineWAV(time.Duration(n) * time.Second / 16000)
	dec, err := wav.NewDecoder(bytes.NewReader(data))
	s.Require().NoError(err)

	buf := make([]float32, n)
	read, err := dec.Read(buf)
	s.Require().NoError(err)
	s.Equal(n, read)
}

func (s *DecoderSuite) TestRead_ReturnsEOFAtEnd() {
	data := validWAV(10)
	dec, err := wav.NewDecoder(bytes.NewReader(data))
	s.Require().NoError(err)

	buf := make([]float32, 100) // больше чем сэмплов
	n, err := dec.Read(buf)
	s.Equal(10, n)
	s.ErrorIs(err, io.EOF)
}

func (s *DecoderSuite) TestRead_ChunkedReading_TotalSamplesCorrect() {
	total := 1000
	data := sineWAV(time.Duration(total) * time.Second / 16000)
	dec, err := wav.NewDecoder(bytes.NewReader(data))
	s.Require().NoError(err)

	buf := make([]float32, 64)

	var got int

	for {
		n, err := dec.Read(buf)
		got += n

		if errors.Is(err, io.EOF) {
			break
		}

		s.Require().NoError(err)
	}

	s.Equal(total, got)
}

func (s *DecoderSuite) TestRead_SampleValues_NormalizedCorrectly() {
	// int16 max (32767) → должен быть близко к 1.0
	// int16 min (-32768) → должен быть близко к -1.0
	samples := []int16{32767, -32768, 0, 16384}
	data := buildWAV(16000, 1, 16, 1, samples)
	dec, err := wav.NewDecoder(bytes.NewReader(data))
	s.Require().NoError(err)

	buf := make([]float32, 4)
	n, _ := dec.Read(buf)
	s.Equal(4, n)

	s.InDelta(1.0, buf[0], 0.0001)
	s.InDelta(-1.0, buf[1], 0.0001)
	s.InDelta(0.0, buf[2], 0.0001)
	s.InDelta(0.5, buf[3], 0.001)
}

func (s *DecoderSuite) TestRead_EmptyDataChunk_ReturnsEOFImmediately() {
	data := buildWAV(16000, 1, 16, 1, nil)
	dec, err := wav.NewDecoder(bytes.NewReader(data))
	s.Require().NoError(err)

	buf := make([]float32, 10)
	n, err := dec.Read(buf)
	s.Equal(0, n)
	s.ErrorIs(err, io.EOF)
}

func (s *DecoderSuite) TestRead_BufferSmallerThanData_ReadsIncrementally() {
	data := validWAV(1000)
	dec, err := wav.NewDecoder(bytes.NewReader(data))
	s.Require().NoError(err)

	buf := make([]float32, 1) // по одному сэмплу

	var total int

	for {
		n, err := dec.Read(buf)
		total += n

		if errors.Is(err, io.EOF) {
			break
		}

		s.Require().NoError(err)
	}

	s.Equal(1000, total)
}

// --- Seek ---

func (s *DecoderSuite) TestSeek_FromStart_PositionsCorrectly() {
	data := sineWAV(100 * time.Millisecond)
	r := bytes.NewReader(data)
	dec, err := wav.NewDecoder(r)
	s.Require().NoError(err)

	pos, err := dec.Seek(100, io.SeekStart)
	s.Require().NoError(err)
	s.Equal(int64(100), pos)
}

func (s *DecoderSuite) TestSeek_ToBeginning_AllowsReread() {
	data := sineWAV(100 * time.Millisecond)
	r := bytes.NewReader(data)
	dec, err := wav.NewDecoder(r)
	s.Require().NoError(err)

	buf := make([]float32, 10)
	_, _ = dec.Read(buf)

	first := make([]float32, 10)
	copy(first, buf)

	_, err = dec.Seek(0, io.SeekStart)
	s.Require().NoError(err)

	_, _ = dec.Read(buf)
	s.Equal(first, buf)
}

func (s *DecoderSuite) TestSeek_FromCurrent_MovesForward() {
	data := sineWAV(100 * time.Millisecond)
	r := bytes.NewReader(data)
	dec, err := wav.NewDecoder(r)
	s.Require().NoError(err)

	_, err = dec.Seek(50, io.SeekCurrent)
	s.Require().NoError(err)

	buf := make([]float32, 1)
	n, _ := dec.Read(buf)
	s.Equal(1, n) // данные есть после позиции 50
}

func (s *DecoderSuite) TestSeek_FromEnd_PositionsFromEnd() {
	data := sineWAV(100 * time.Millisecond)
	numSamples := int64(d_numSamples(data))
	r := bytes.NewReader(data)
	dec, err := wav.NewDecoder(r)
	s.Require().NoError(err)

	pos, err := dec.Seek(-10, io.SeekEnd)
	s.Require().NoError(err)
	s.Equal(numSamples-10, pos)
}

func (s *DecoderSuite) TestSeek_BeyondEnd_ReturnsError() {
	data := validWAV(100)
	r := bytes.NewReader(data)
	dec, err := wav.NewDecoder(r)
	s.Require().NoError(err)

	_, err = dec.Seek(1000, io.SeekStart)
	s.Error(err)
}

func (s *DecoderSuite) TestSeek_Negative_ReturnsError() {
	data := validWAV(100)
	r := bytes.NewReader(data)
	dec, err := wav.NewDecoder(r)
	s.Require().NoError(err)

	_, err = dec.Seek(-1, io.SeekStart)
	s.Error(err)
}

func (s *DecoderSuite) TestSeek_NonSeekerReader_ReturnsError() {
	// io.Reader без Seek — pipe
	pipeReader, pipeWriter := io.Pipe()
	go func() {
		_, _ = pipeWriter.Write(validWAV(100))
		_ = pipeWriter.Close()
	}()

	dec, err := wav.NewDecoder(pipeReader)
	s.Require().NoError(err)

	_, err = dec.Seek(0, io.SeekStart)
	s.Error(err)
}

// --- ReadAll ---

func (s *DecoderSuite) TestReadAll_ReturnsSameAsRead() {
	data := sineWAV(50 * time.Millisecond)

	dec1, _ := wav.NewDecoder(bytes.NewReader(data))
	all, err := dec1.ReadAll()
	s.Require().NoError(err)

	dec2, _ := wav.NewDecoder(bytes.NewReader(data))
	buf := make([]float32, len(all))
	n, _ := dec2.Read(buf)

	s.Equal(len(all), n)
	s.Equal(all, buf[:n])
}

func (s *DecoderSuite) TestReadAll_CountMatchesHeader() {
	n := 800
	data := validWAV(n)
	dec, err := wav.NewDecoder(bytes.NewReader(data))
	s.Require().NoError(err)

	samples, err := dec.ReadAll()
	s.Require().NoError(err)
	s.Len(samples, n)
	s.Equal(uint32(n), dec.Header.NumSamples)
}

func (s *DecoderSuite) TestReadAll_AfterPartialRead_ReturnsRemaining() {
	data := validWAV(100)
	dec, err := wav.NewDecoder(bytes.NewReader(data))
	s.Require().NoError(err)

	first := make([]float32, 30)
	_, _ = dec.Read(first)

	rest, err := dec.ReadAll()
	s.Require().NoError(err)
	s.Len(rest, 70)
}

// --- Close ---

func (s *DecoderSuite) TestClose_CalledTwice_NoError() {
	path := filepath.Join(s.T().TempDir(), "test.wav")
	s.Require().NoError(os.WriteFile(path, validWAV(10), 0o644))

	dec, err := wav.Open(path)
	s.Require().NoError(err)
	s.Require().NoError(dec.Close())
	s.NoError(dec.Close()) // второй Close не должен паниковать
}

func (s *DecoderSuite) TestClose_NewDecoder_NoError() {
	dec, err := wav.NewDecoder(bytes.NewReader(validWAV(10)))
	s.Require().NoError(err)
	s.NoError(dec.Close()) // Reader не имеет Close — должен работать без ошибки
}

// --- NewDecoder: расширенный fmt чанк ---

func (s *DecoderSuite) TestNewDecoder_ExtendedFmtChunkSize18_ParsedCorrectly() {
	// WAVEFORMATEX: fmt size = 18 (добавляет uint16 cbSize = 0)
	data := buildWAVWithExtendedFmt(2, []int16{100, 200, 300})
	dec, err := wav.NewDecoder(bytes.NewReader(data))
	s.Require().NoError(err)
	s.Equal(uint32(3), dec.Header.NumSamples)
	s.Equal(uint32(16000), dec.Header.SampleRate)
}

func (s *DecoderSuite) TestNewDecoder_ExtendedFmtChunkSize40_ParsedCorrectly() {
	// WAVEFORMATEXTENSIBLE: fmt size = 40 (расширенный заголовок)
	data := buildWAVWithExtendedFmt(24, []int16{1, 2})
	dec, err := wav.NewDecoder(bytes.NewReader(data))
	s.Require().NoError(err)
	s.Equal(uint32(2), dec.Header.NumSamples)
}

func (s *DecoderSuite) TestNewDecoder_FmtChunkTooSmall_ReturnsError() {
	// fmt chunk size < 16 — повреждённый файл
	buf := &bytes.Buffer{}
	buf.WriteString("RIFF")

	fmtSize := uint32(8) // меньше минимальных 16 байт
	_ = binary.Write(buf, binary.LittleEndian, 4+8+fmtSize+8)
	buf.WriteString("WAVE")

	buf.WriteString("fmt ")
	_ = binary.Write(buf, binary.LittleEndian, fmtSize)
	buf.Write(make([]byte, fmtSize))

	buf.WriteString("data")
	_ = binary.Write(buf, binary.LittleEndian, uint32(0))

	_, err := wav.NewDecoder(buf)
	s.Error(err)
}

func (s *DecoderSuite) TestNewDecoder_MissingFmtChunk_ReturnsError() {
	// Только data чанк — нет fmt
	buf := &bytes.Buffer{}
	dataSize := uint32(10 * 2)

	buf.WriteString("RIFF")
	_ = binary.Write(buf, binary.LittleEndian, 4+8+dataSize)
	buf.WriteString("WAVE")
	buf.WriteString("data")
	_ = binary.Write(buf, binary.LittleEndian, dataSize)
	buf.Write(make([]byte, dataSize))

	_, err := wav.NewDecoder(buf)
	s.Error(err)
}

func (s *DecoderSuite) TestNewDecoder_MissingDataChunk_ReturnsError() {
	// Только fmt чанк — нет data
	buf := &bytes.Buffer{}
	fmtSize := uint32(16)

	buf.WriteString("RIFF")
	_ = binary.Write(buf, binary.LittleEndian, 4+8+fmtSize)
	buf.WriteString("WAVE")
	buf.WriteString("fmt ")
	_ = binary.Write(buf, binary.LittleEndian, fmtSize)
	_ = binary.Write(buf, binary.LittleEndian, uint16(1))
	_ = binary.Write(buf, binary.LittleEndian, uint16(1))
	_ = binary.Write(buf, binary.LittleEndian, uint32(16000))
	_ = binary.Write(buf, binary.LittleEndian, uint32(32000))
	_ = binary.Write(buf, binary.LittleEndian, uint16(2))
	_ = binary.Write(buf, binary.LittleEndian, uint16(16))

	_, err := wav.NewDecoder(buf)
	s.Error(err)
}

func (s *DecoderSuite) TestNewDecoder_OddSizeExtraChunk_SkippedWithPadding() {
	// По спецификации RIFF: чанк с нечётным размером дополняется одним байтом
	buf := &bytes.Buffer{}

	junkData := []byte("ABC") // 3 байта — нечётный размер
	fmtSize := uint32(16)
	dataSize := uint32(4 * 2)
	// RIFF size = "WAVE"(4) + junk header(8) + junk data(3) + padding(1) + fmt(8+16) + data(8+8)
	riffSize := 4 + 8 + 3 + 1 + 8 + fmtSize + 8 + dataSize

	buf.WriteString("RIFF")
	_ = binary.Write(buf, binary.LittleEndian, riffSize)
	buf.WriteString("WAVE")

	// junk чанк с нечётным размером + 1 байт padding
	buf.WriteString("junk")
	_ = binary.Write(buf, binary.LittleEndian, uint32(3))
	buf.Write(junkData)
	buf.WriteByte(0) // padding

	buf.WriteString("fmt ")
	_ = binary.Write(buf, binary.LittleEndian, fmtSize)
	_ = binary.Write(buf, binary.LittleEndian, uint16(1))
	_ = binary.Write(buf, binary.LittleEndian, uint16(1))
	_ = binary.Write(buf, binary.LittleEndian, uint32(16000))
	_ = binary.Write(buf, binary.LittleEndian, uint32(32000))
	_ = binary.Write(buf, binary.LittleEndian, uint16(2))
	_ = binary.Write(buf, binary.LittleEndian, uint16(16))

	buf.WriteString("data")
	_ = binary.Write(buf, binary.LittleEndian, dataSize)
	buf.Write(make([]byte, dataSize))

	dec, err := wav.NewDecoder(buf)
	s.Require().NoError(err)
	s.Equal(uint32(4), dec.Header.NumSamples)
}

// --- Read: дополнительные случаи ---

func (s *DecoderSuite) TestRead_ZeroLengthBuffer_ReturnsZero() {
	data := validWAV(10)
	dec, err := wav.NewDecoder(bytes.NewReader(data))
	s.Require().NoError(err)

	n, err := dec.Read([]float32{})
	s.Equal(0, n)
	s.NoError(err) // нулевой буфер — не ошибка и не EOF
}

func (s *DecoderSuite) TestRead_AfterEOF_ConsistentlyReturnsEOF() {
	data := validWAV(5)
	dec, err := wav.NewDecoder(bytes.NewReader(data))
	s.Require().NoError(err)

	buf := make([]float32, 100)
	_, _ = dec.Read(buf) // читаем всё до EOF

	// повторный Read должен стабильно возвращать (0, io.EOF)
	for range 3 {
		n, err := dec.Read(buf)
		s.Equal(0, n)
		s.ErrorIs(err, io.EOF)
	}
}

func (s *DecoderSuite) TestRead_BufferExactlyNumSamples_ReadsSamplesNoError() {
	data := validWAV(64)
	dec, err := wav.NewDecoder(bytes.NewReader(data))
	s.Require().NoError(err)

	buf := make([]float32, 64) // точно влезает
	n, err := dec.Read(buf)
	// Допустимо: (64, nil) или (64, io.EOF)
	s.Equal(64, n)
	s.True(err == nil || errors.Is(err, io.EOF))
}

func (s *DecoderSuite) TestRead_SingleSample_Works() {
	samples := []int16{16384}
	data := buildWAV(16000, 1, 16, 1, samples)
	dec, err := wav.NewDecoder(bytes.NewReader(data))
	s.Require().NoError(err)

	buf := make([]float32, 1)
	n, err := dec.Read(buf)
	s.Equal(1, n)
	s.True(err == nil || errors.Is(err, io.EOF))
	s.InDelta(0.5, buf[0], 0.001)
}

// --- Seek: дополнительные случаи ---

func (s *DecoderSuite) TestSeek_ToExactEnd_ReadReturnsEOF() {
	data := validWAV(50)
	r := bytes.NewReader(data)
	dec, err := wav.NewDecoder(r)
	s.Require().NoError(err)

	pos, err := dec.Seek(50, io.SeekStart) // ровно в конец
	s.Require().NoError(err)
	s.Equal(int64(50), pos)

	buf := make([]float32, 10)
	n, err := dec.Read(buf)
	s.Equal(0, n)
	s.ErrorIs(err, io.EOF)
}

func (s *DecoderSuite) TestSeek_FromEnd_ToMiddle() {
	data := validWAV(100)
	r := bytes.NewReader(data)
	dec, err := wav.NewDecoder(r)
	s.Require().NoError(err)

	// SeekEnd с отрицательным смещением — позиция 90 от начала
	pos, err := dec.Seek(-10, io.SeekEnd)
	s.Require().NoError(err)
	s.Equal(int64(90), pos)

	buf := make([]float32, 10)
	n, err := dec.Read(buf)
	s.Equal(10, n)
	s.True(err == nil || errors.Is(err, io.EOF))
}

func (s *DecoderSuite) TestSeek_CurrentNegative_MovesBackward() {
	samples := []int16{0, 100, 200, 300, 400}
	data := buildWAV(16000, 1, 16, 1, samples)
	r := bytes.NewReader(data)
	dec, err := wav.NewDecoder(r)
	s.Require().NoError(err)

	// Читаем 3 сэмпла, находимся на позиции 3
	buf := make([]float32, 3)
	_, _ = dec.Read(buf)

	// Двигаемся назад на 2 сэмпла — должны оказаться на позиции 1
	pos, err := dec.Seek(-2, io.SeekCurrent)
	s.Require().NoError(err)
	s.Equal(int64(1), pos)

	// Читаем 1 сэмпл — должны получить samples[1] = 100
	one := make([]float32, 1)
	_, _ = dec.Read(one)
	s.InDelta(float32(100)/32768.0, one[0], 0.0001)
}

func (s *DecoderSuite) TestSeek_InvalidWhence_ReturnsError() {
	data := validWAV(100)
	r := bytes.NewReader(data)
	dec, err := wav.NewDecoder(r)
	s.Require().NoError(err)

	_, err = dec.Seek(0, 99)
	s.Error(err)
}

func (s *DecoderSuite) TestSeek_CorrectSampleValuesAtKnownOffsets() {
	// Создаём WAV с заранее известными значениями в каждой позиции
	samples := []int16{0, 16384, 32767, -32768, -16384}
	data := buildWAV(16000, 1, 16, 1, samples)
	r := bytes.NewReader(data)
	dec, err := wav.NewDecoder(r)
	s.Require().NoError(err)

	buf := make([]float32, 1)

	// samples[2] = 32767 → ≈ 1.0
	_, err = dec.Seek(2, io.SeekStart)
	s.Require().NoError(err)

	_, _ = dec.Read(buf)
	s.InDelta(1.0, buf[0], 0.0001)

	// samples[3] = -32768 → ≈ -1.0
	_, err = dec.Seek(3, io.SeekStart)
	s.Require().NoError(err)

	_, _ = dec.Read(buf)
	s.InDelta(-1.0, buf[0], 0.0001)

	// SeekCurrent -1 → назад на samples[3]
	_, err = dec.Seek(-1, io.SeekCurrent)
	s.Require().NoError(err)

	_, _ = dec.Read(buf)
	s.InDelta(-1.0, buf[0], 0.0001)

	// SeekEnd -len(samples) → самое начало
	_, err = dec.Seek(-int64(len(samples)), io.SeekEnd)
	s.Require().NoError(err)

	_, _ = dec.Read(buf)
	s.InDelta(0.0, buf[0], 0.0001) // samples[0] = 0
}

// --- ReadAll: дополнительные случаи ---

func (s *DecoderSuite) TestReadAll_EmptyData_ReturnsEmptySlice() {
	data := buildWAV(16000, 1, 16, 1, nil) // 0 сэмплов
	dec, err := wav.NewDecoder(bytes.NewReader(data))
	s.Require().NoError(err)

	samples, err := dec.ReadAll()
	s.Require().NoError(err)
	s.Empty(samples)
}

func (s *DecoderSuite) TestReadAll_TruncatedDataBytes_ReturnsError() {
	// dataSize в заголовке завышен: заявляет 100 сэмплов, реально только 10
	data := buildWAV(16000, 1, 16, 1, make([]int16, 10))
	dataChunkSizeOffset := 12 + 8 + 16 + 4
	binary.LittleEndian.PutUint32(data[dataChunkSizeOffset:], 200) // 100 сэмплов = 200 байт

	dec, err := wav.NewDecoder(bytes.NewReader(data))
	if err != nil {
		// Декодер может обнаружить несоответствие при парсинге — тоже OK
		return
	}

	// Если конструктор прошёл, ReadAll должен вернуть ошибку (неожиданный EOF)
	_, err = dec.ReadAll()
	s.Error(err)
}

// helpers

func d_numSamples(data []byte) uint32 {
	// dataChunkSize находится после RIFF(12) + fmt chunk header(8) + fmt data(16) + "data"(4)
	offset := 12 + 8 + 16 + 4

	return binary.LittleEndian.Uint32(data[offset:]) / 2
}

// buildWAVWithExtendedFmt создаёт WAV с расширенным fmt чанком (size > 16).
// extraBytes добавляются после стандартных 16 байт fmt (например, cbSize для WAVEFORMATEX).
func buildWAVWithExtendedFmt(extraBytes int, samples []int16) []byte {
	fmtSize := uint32(16 + extraBytes)
	dataSize := uint32(len(samples) * 2)

	buf := &bytes.Buffer{}
	buf.WriteString("RIFF")
	_ = binary.Write(buf, binary.LittleEndian, 4+8+fmtSize+8+dataSize)
	buf.WriteString("WAVE")

	buf.WriteString("fmt ")
	_ = binary.Write(buf, binary.LittleEndian, fmtSize)
	_ = binary.Write(buf, binary.LittleEndian, uint16(1))
	_ = binary.Write(buf, binary.LittleEndian, uint16(1))
	_ = binary.Write(buf, binary.LittleEndian, uint32(16000))
	_ = binary.Write(buf, binary.LittleEndian, uint32(32000))
	_ = binary.Write(buf, binary.LittleEndian, uint16(2))
	_ = binary.Write(buf, binary.LittleEndian, uint16(16))
	buf.Write(make([]byte, extraBytes))

	buf.WriteString("data")
	_ = binary.Write(buf, binary.LittleEndian, dataSize)

	for _, s := range samples {
		_ = binary.Write(buf, binary.LittleEndian, s)
	}

	return buf.Bytes()
}
