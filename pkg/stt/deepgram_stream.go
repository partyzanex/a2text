package stt

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	msginterfaces "github.com/deepgram/deepgram-go-sdk/v3/pkg/api/listen/v1/websocket/interfaces"
	interfaces "github.com/deepgram/deepgram-go-sdk/v3/pkg/client/interfaces"
	listen "github.com/deepgram/deepgram-go-sdk/v3/pkg/client/listen"

	"github.com/partyzanex/a2text/pkg/sttx"
)

// DeepgramStreamSampleRate is the PCM sample rate the streaming transcriber
// declares to Deepgram. Must match what the audio source actually produces;
// callers feeding a different rate will get garbled output.
const DeepgramStreamSampleRate = 16000

// Smart-drain tuning. After the audio stream EOFs we wait for finals to
// stop arriving rather than for a fixed timeout — most cycles can ship
// the transcript within 100-200ms of stop because the last final was
// already in hand. The hard cap protects against runaway servers.
const (
	// deepgramStreamDrainQuiet is set equal to the hard cap so the drain
	// loop becomes a fixed wait window after stop: we always give
	// Deepgram exactly deepgramStreamDrainHardCap to deliver tail finals
	// for the last 1-2 seconds of speech. Anything arriving later is
	// dropped — predictable latency beats catching a stray late final.
	deepgramStreamDrainQuiet = 1000 * time.Millisecond

	// deepgramStreamDrainHardCap caps the drain window. 1s catches the
	// typical 500-800ms tail of finals for the last utterance without
	// making the user wait noticeably past stop.
	deepgramStreamDrainHardCap = 1000 * time.Millisecond
)

// WAV header byte counts. RIFF header is 12 bytes (RIFF/size/WAVE),
// each chunk has an 8-byte header (id/size).
const (
	wavRIFFHeaderBytes  = 12
	wavChunkHeaderBytes = 8
)

// DeepgramStreamTranscriber pushes PCM audio to Deepgram over a WebSocket
// via the official SDK and accumulates the final transcript until the
// stream closes.
//
// Implements transcribe.Transcriber for the existing file-based pipeline
// (Transcribe reads the WAV, strips the header, then streams the body);
// callers wanting true concurrent realtime should use Stream directly with
// a live PCM io.Reader sourced from the capture pkg.
type DeepgramStreamTranscriber struct {
	log     *slog.Logger
	apiKey  string
	baseURL string
	model   string
}

// NewDeepgramStreamTranscriber builds the streaming Deepgram transcriber.
// baseURL may be empty — the SDK falls back to its production host. model
// is the Deepgram model id (e.g. nova-2). log may be nil.
func NewDeepgramStreamTranscriber(apiKey, baseURL, model string, log *slog.Logger) *DeepgramStreamTranscriber {
	if log == nil {
		log = slog.Default()
	}

	return &DeepgramStreamTranscriber{
		log:     log,
		apiKey:  apiKey,
		baseURL: baseURL,
		model:   model,
	}
}

// Name identifies the transcriber in logs.
func (t *DeepgramStreamTranscriber) Name() string { return "deepgram-stream" }

// Close, LoadModel, ReloadModel, DetectLanguage exist solely to satisfy
// transcribe.Transcriber. The streaming provider has no long-lived state.
func (t *DeepgramStreamTranscriber) Close() error               { return nil }
func (t *DeepgramStreamTranscriber) LoadModel(_ string) error   { return nil }
func (t *DeepgramStreamTranscriber) ReloadModel(_ string) error { return nil }
func (t *DeepgramStreamTranscriber) DetectLanguage(_ context.Context, _ string) (string, error) {
	return "", errors.New("deepgram-stream: DetectLanguage not supported")
}

// Transcribe opens the WAV file at wavPath, strips the RIFF/WAVE container,
// and streams the raw PCM payload to Deepgram. Returns the accumulated
// final transcript.
//
// This is the adapter used by the existing voice.Cycle path; truly
// realtime callers should call Stream directly with a live PCM reader.
func (t *DeepgramStreamTranscriber) Transcribe(
	ctx context.Context, wavPath, lang string,
) (string, error) {
	if err := validateWavPath(wavPath); err != nil {
		return "", err
	}

	file, err := os.Open(filepath.Clean(wavPath))
	if err != nil {
		return "", fmt.Errorf("%w: open wav: %w", sttx.ErrTranscribeFailed, err)
	}

	defer func() {
		if closeErr := file.Close(); closeErr != nil {
			t.log.Debug("deepgram-stream: wav close failed", slog.Any("err", closeErr))
		}
	}()

	if err := skipWAVHeader(file); err != nil {
		return "", fmt.Errorf("%w: %w", sttx.ErrTranscribeFailed, err)
	}

	return t.Stream(ctx, file, lang)
}

// Stream pushes raw 16-bit little-endian mono PCM from pcm to Deepgram and
// returns once pcm reaches EOF and the server flushes pending finals.
// Cancelling ctx tears the connection down.
//
// pcm must produce 16kHz s16le mono — Deepgram trusts the encoding query
// parameters declared in the LiveTranscriptionOptions below.
func (t *DeepgramStreamTranscriber) Stream(
	ctx context.Context, pcm io.Reader, lang string,
) (string, error) {
	handler := newDeepgramStreamHandler(t.log)

	cOpts := &interfaces.ClientOptions{
		APIKey: t.apiKey,
		Host:   extractHostForSDK(t.baseURL),
	}

	tOpts := &interfaces.LiveTranscriptionOptions{
		Model:          deepgramModelOrDefault(t.model),
		Language:       sdkLanguageOrEmpty(lang),
		Encoding:       "linear16",
		SampleRate:     DeepgramStreamSampleRate,
		Channels:       1,
		Punctuate:      true,
		SmartFormat:    true,
		InterimResults: true,
	}

	dgClient, err := listen.NewWSUsingChan(ctx, t.apiKey, cOpts, tOpts, handler)
	if err != nil {
		return "", fmt.Errorf("%w: ws client: %w", sttx.ErrTranscribeFailed, err)
	}

	if !dgClient.Connect() {
		return "", fmt.Errorf("%w: ws connect failed", sttx.ErrTranscribeFailed)
	}

	// Stream blocks until pcm EOFs or the connection errors. Ignore the
	// returned error here — the handler captures whatever finals already
	// arrived, which is what we ultimately need to return.
	streamErr := dgClient.Stream(pcm)
	if streamErr != nil {
		t.log.Debug("deepgram-stream: Stream returned error", slog.Any("err", streamErr))
	}

	// Tell the server we are done sending audio and wait for the close
	// response so any pending finals arrive before we walk away.
	dgClient.Finish()

	handler.drainUntilQuiet(deepgramStreamDrainQuiet, deepgramStreamDrainHardCap)

	dgClient.Stop()

	return handler.text(), nil
}

// deepgramModelOrDefault picks a sensible model when callers leave it
// blank. nova-2 is broadly capable and supports the languages users
// typically dictate in (RU/EN). Adjustable via settings.
func deepgramModelOrDefault(model string) string {
	if m := strings.TrimSpace(model); m != "" {
		return m
	}

	return "nova-2"
}

// sdkLanguageOrEmpty maps our "auto" sentinel to the SDK's empty-string
// representation (let Deepgram detect). Anything else is passed through.
func sdkLanguageOrEmpty(lang string) string {
	lang = strings.TrimSpace(lang)
	if lang == "" || lang == "auto" {
		return ""
	}

	return lang
}

// extractHostForSDK strips the URL scheme and path from baseURL so the SDK
// gets just the host. Stored config typically holds something like
// "https://api.deepgram.com/v1/listen" left over from the REST adapter;
// the SDK appends its own path and scheme, so we must not send our own.
func extractHostForSDK(baseURL string) string {
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		return ""
	}

	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Host == "" {
		// url.Parse returns Host="" when scheme is missing. Try again with
		// https:// prepended; if that still fails, fall back to the raw
		// string (let the SDK error out clearly rather than silently).
		if reparsed, reErr := url.Parse("https://" + baseURL); reErr == nil && reparsed.Host != "" {
			return reparsed.Host
		}

		return baseURL
	}

	return parsed.Host
}

// deepgramStreamHandler is the LiveMessageChan implementation required by
// the SDK. It collects final transcripts into an in-memory buffer and
// signals close on closeChan so the Stream method knows when to return.
type deepgramStreamHandler struct {
	log *slog.Logger

	openChan          chan *msginterfaces.OpenResponse
	messageChan       chan *msginterfaces.MessageResponse
	metadataChan      chan *msginterfaces.MetadataResponse
	speechStartedChan chan *msginterfaces.SpeechStartedResponse
	utteranceEndChan  chan *msginterfaces.UtteranceEndResponse
	closeChan         chan *msginterfaces.CloseResponse
	errorChan         chan *msginterfaces.ErrorResponse
	unhandledChan     chan *[]byte

	mu        sync.Mutex
	finals    []string
	closeOnce sync.Once
	closed    chan struct{}

	// drainEnded flips to true the moment drainUntilQuiet returns. Any
	// final arriving after this point is logged as "late" so the user can
	// see whether the tail was actually missed by the upstream model or
	// just cut by our drain window.
	drainEnded atomic.Bool

	// finalsDuringDrain counts finals appended after drain entry. Logged
	// at drain end to give visibility into tail behaviour.
	finalsDuringDrain atomic.Int32
	drainStarted      atomic.Bool

	// finalSignal pulses (non-blocking send) every time a final segment
	// arrives. drainUntilQuiet uses it to reset its idle timer — when no
	// new final shows up for the quiet period, we stop waiting and ship
	// the transcript. Buffer 1 so handleMessage never blocks if the
	// drain loop is not currently listening.
	finalSignal chan struct{}
}

func newDeepgramStreamHandler(log *slog.Logger) *deepgramStreamHandler {
	const chanBuf = 16

	handler := &deepgramStreamHandler{
		log:               log,
		openChan:          make(chan *msginterfaces.OpenResponse, 1),
		messageChan:       make(chan *msginterfaces.MessageResponse, chanBuf),
		metadataChan:      make(chan *msginterfaces.MetadataResponse, chanBuf),
		speechStartedChan: make(chan *msginterfaces.SpeechStartedResponse, chanBuf),
		utteranceEndChan:  make(chan *msginterfaces.UtteranceEndResponse, chanBuf),
		closeChan:         make(chan *msginterfaces.CloseResponse, 1),
		errorChan:         make(chan *msginterfaces.ErrorResponse, chanBuf),
		unhandledChan:     make(chan *[]byte, chanBuf),
		closed:            make(chan struct{}),
		finalSignal:       make(chan struct{}, 1),
	}

	go handler.run()

	return handler
}

// Get* methods implement msginterfaces.LiveMessageChan.
func (h *deepgramStreamHandler) GetOpen() []*chan *msginterfaces.OpenResponse {
	return []*chan *msginterfaces.OpenResponse{&h.openChan}
}

func (h *deepgramStreamHandler) GetMessage() []*chan *msginterfaces.MessageResponse {
	return []*chan *msginterfaces.MessageResponse{&h.messageChan}
}

func (h *deepgramStreamHandler) GetMetadata() []*chan *msginterfaces.MetadataResponse {
	return []*chan *msginterfaces.MetadataResponse{&h.metadataChan}
}

func (h *deepgramStreamHandler) GetSpeechStarted() []*chan *msginterfaces.SpeechStartedResponse {
	return []*chan *msginterfaces.SpeechStartedResponse{&h.speechStartedChan}
}

func (h *deepgramStreamHandler) GetUtteranceEnd() []*chan *msginterfaces.UtteranceEndResponse {
	return []*chan *msginterfaces.UtteranceEndResponse{&h.utteranceEndChan}
}

func (h *deepgramStreamHandler) GetClose() []*chan *msginterfaces.CloseResponse {
	return []*chan *msginterfaces.CloseResponse{&h.closeChan}
}

func (h *deepgramStreamHandler) GetError() []*chan *msginterfaces.ErrorResponse {
	return []*chan *msginterfaces.ErrorResponse{&h.errorChan}
}

func (h *deepgramStreamHandler) GetUnhandled() []*chan *[]byte {
	return []*chan *[]byte{&h.unhandledChan}
}

// run drains all SDK channels concurrently. Finals are appended to the
// in-memory buffer; close fires the signal channel exactly once.
func (h *deepgramStreamHandler) run() {
	for {
		if !h.dispatchOne() {
			return
		}
	}
}

// dispatchOne handles a single event from any of the SDK channels.
// Returns false when the SDK closes the message or error channels, which
// is our signal to exit the loop.
//
// contract requires us to drain all of them, so the branch count is
// driven by the SDK, not by our logic.
//
//nolint:cyclop // one select arm per SDK channel — the LiveMessageChan
func (h *deepgramStreamHandler) dispatchOne() bool {
	select {
	case msg, ok := <-h.messageChan:
		if !ok {
			return false
		}

		h.handleMessage(msg)
	case err, ok := <-h.errorChan:
		if !ok {
			return false
		}

		h.handleError(err)
	case <-h.closeChan:
		h.closeOnce.Do(func() { close(h.closed) })
	case <-h.openChan:
	case <-h.metadataChan:
	case <-h.speechStartedChan:
	case <-h.utteranceEndChan:
	case <-h.unhandledChan:
	}

	return true
}

func (h *deepgramStreamHandler) handleError(err *msginterfaces.ErrorResponse) {
	h.log.Warn("deepgram-stream: server error",
		slog.String("code", err.ErrCode),
		slog.String("msg", err.ErrMsg),
		slog.String("description", err.Description))
}

func (h *deepgramStreamHandler) handleMessage(msg *msginterfaces.MessageResponse) {
	if msg == nil || len(msg.Channel.Alternatives) == 0 {
		return
	}

	transcript := strings.TrimSpace(msg.Channel.Alternatives[0].Transcript)
	if transcript == "" {
		return
	}

	if msg.IsFinal {
		h.handleFinal(transcript)

		return
	}

	h.log.Debug("deepgram-stream: interim", slog.String("text", transcript))
}

// handleFinal appends a final transcript segment, updates drain-stats
// counters, pulses the drain wake channel, and logs the segment. Split
// out of handleMessage so neither function tips over cyclop.
func (h *deepgramStreamHandler) handleFinal(transcript string) {
	late := h.drainEnded.Load()

	h.mu.Lock()
	if !late {
		h.finals = append(h.finals, transcript)
	}
	h.mu.Unlock()

	if h.drainStarted.Load() && !late {
		h.finalsDuringDrain.Add(1)
	}

	// Non-blocking pulse so drainUntilQuiet resets its idle timer.
	// Buffer 1 + default case means we never block handleMessage on
	// a slow drain consumer.
	select {
	case h.finalSignal <- struct{}{}:
	default:
	}

	if late {
		h.log.Warn("deepgram-stream: final arrived AFTER drain window — dropped",
			slog.String("text", transcript))

		return
	}

	h.log.Debug("deepgram-stream: final segment", slog.String("text", transcript))
}

// drainUntilQuiet waits for finals to stop arriving, then returns. The
// quiet period restarts every time a new final is appended; the hard cap
// is an upper bound so a runaway server cannot stall the cycle.
//
// Returns as soon as the SDK delivers CloseResponse (rare in practice —
// the SDK is inconsistent about emitting it) or the quiet period elapses
// without a new final.
//
// Empirically tail finals arrive within ~150ms of stop, so quiet=250ms
// captures them while letting fast paths exit in ~250ms instead of 1s.
func (h *deepgramStreamHandler) drainUntilQuiet(quiet, hardCap time.Duration) {
	drainStart := time.Now()

	h.drainStarted.Store(true)
	h.finalsDuringDrain.Store(0)

	defer func() {
		h.drainEnded.Store(true)
		h.log.Debug("deepgram-stream: drain stats",
			slog.Duration("elapsed", time.Since(drainStart)),
			slog.Int("finals_during_drain", int(h.finalsDuringDrain.Load())))
	}()

	hardDeadline := time.NewTimer(hardCap)
	defer hardDeadline.Stop()

	idleTimer := time.NewTimer(quiet)
	defer idleTimer.Stop()

	for {
		select {
		case <-h.closed:
			h.log.Debug("deepgram-stream: drain done via CloseResponse")

			return
		case <-hardDeadline.C:
			h.log.Debug("deepgram-stream: drain done via hard cap",
				slog.Duration("cap", hardCap))

			return
		case <-idleTimer.C:
			h.log.Debug("deepgram-stream: drain done via quiet period",
				slog.Duration("quiet", quiet))

			return
		case <-h.finalSignal:
			// New final arrived — restart the idle timer.
			if !idleTimer.Stop() {
				select {
				case <-idleTimer.C:
				default:
				}
			}

			idleTimer.Reset(quiet)
		}
	}
}

func (h *deepgramStreamHandler) text() string {
	h.mu.Lock()
	defer h.mu.Unlock()

	return strings.Join(h.finals, " ")
}

// skipWAVHeader advances file past the RIFF/WAVE container so subsequent
// reads return raw PCM. Callers must guarantee 16kHz s16le mono.
func skipWAVHeader(file *os.File) error {
	header := make([]byte, wavRIFFHeaderBytes)
	if _, err := io.ReadFull(file, header); err != nil {
		return fmt.Errorf("read riff header: %w", err)
	}

	if string(header[0:4]) != "RIFF" || string(header[8:12]) != "WAVE" {
		return errors.New("not a RIFF/WAVE file")
	}

	chunk := make([]byte, wavChunkHeaderBytes)

	for {
		if _, err := io.ReadFull(file, chunk); err != nil {
			return fmt.Errorf("read chunk header: %w", err)
		}

		if string(chunk[0:4]) == "data" {
			return nil
		}

		size := binary.LittleEndian.Uint32(chunk[4:8])
		if _, err := file.Seek(int64(size), io.SeekCurrent); err != nil {
			return fmt.Errorf("skip chunk %q: %w", chunk[0:4], err)
		}
	}
}
