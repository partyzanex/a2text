package stt

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/partyzanex/a2text/pkg/sttx"
)

const (
	langAuto = "auto"

	// openaiHTTPTimeout is the maximum time for an OpenAI API request.
	// Transcription of long audio can take minutes, so this is generous.
	openaiHTTPTimeout = 10 * time.Minute
)

// OpenAITranscriber sends audio to the OpenAI Whisper API for transcription.
type OpenAITranscriber struct {
	client  *http.Client
	log     *slog.Logger
	audit   AuditLogger
	apiKey  string
	baseURL string
	model   string
}

// NewOpenAITranscriber creates an OpenAITranscriber.
// baseURL defaults to "https://api.openai.com" if empty; pass a test server URL in tests.
// client defaults to an http.Client with a 10-minute timeout if nil.
func NewOpenAITranscriber(apiKey, baseURL string, client *http.Client, log *slog.Logger) *OpenAITranscriber {
	if baseURL == "" {
		baseURL = "https://api.openai.com"
	}

	if client == nil {
		client = &http.Client{Timeout: openaiHTTPTimeout}
	}

	if log == nil {
		log = slog.Default()
	}

	return &OpenAITranscriber{
		apiKey:  apiKey,
		baseURL: baseURL,
		client:  client,
		log:     log,
		audit:   NoopAuditLogger{},
	}
}

// WithAudit attaches an AuditLogger so every Transcribe call records a
// metadata-only event in the audit trail. Returns the receiver for
// chaining. Passing nil restores the no-op default.
func (t *OpenAITranscriber) WithAudit(audit AuditLogger) *OpenAITranscriber {
	if audit == nil {
		audit = NoopAuditLogger{}
	}

	t.audit = audit

	return t
}

// WithModel records the model name (e.g. "whisper-1") in audit events.
// OpenAI's audio.transcriptions endpoint binds the model in the request
// body via buildBody; the constructor does not need it directly. Audit
// only cares for traceability.
func (t *OpenAITranscriber) WithModel(model string) *OpenAITranscriber {
	t.model = model

	return t
}

// LoadModel is a no-op — no local model needed for the cloud transcriber.
func (t *OpenAITranscriber) LoadModel(_ string) error { return nil }

// ReloadModel is a no-op — cloud transcriber doesn't support model reload.
func (t *OpenAITranscriber) ReloadModel(_ string) error { return nil }

// DetectLanguage returns "auto" since OpenAI Whisper API auto-detects language.
func (t *OpenAITranscriber) DetectLanguage(_ context.Context, _ string) (string, error) {
	return langAuto, nil
}

// Close is a no-op — no resources to release.
func (t *OpenAITranscriber) Close() error { return nil }

// Transcribe uploads wavPath to the OpenAI Whisper API and returns the transcription.
func (t *OpenAITranscriber) Transcribe(ctx context.Context, wavPath, lang string) (string, error) {
	url := t.baseURL + "/v1/audio/transcriptions"

	event, finish := t.startAudit(url, wavPath, lang)
	defer finish()

	text, err := t.transcribeOnce(ctx, url, wavPath, lang, event)
	if err == nil {
		classifyOutcome(event, text, nil)
		t.log.Info("openai transcription complete", slog.Int("result_len", len(text)))
	}

	return text, err
}

// startAudit captures the request metadata and returns a finaliser that
// records ElapsedMs and emits the event into the audit log.
func (t *OpenAITranscriber) startAudit(url, wavPath, lang string) (event *AuditEvent, finish func()) {
	audioBytes, audioDur, audioHash := captureAudioMetrics(wavPath)

	event = &AuditEvent{
		Timestamp:     time.Now().UTC(),
		Provider:      AuditProviderOpenAI,
		Endpoint:      url,
		Model:         t.model,
		Lang:          lang,
		AudioBytes:    audioBytes,
		AudioDuration: audioDur,
		AudioSHA256:   audioHash,
	}

	started := time.Now()

	finish = func() {
		event.ElapsedMs = time.Since(started).Milliseconds()
		t.audit.LogEvent(event)
	}

	return event, finish
}

// transcribeOnce performs the multipart upload + JSON decode. event.Outcome
// is populated on the failure paths; the success outcome is set by the
// caller via classifyOutcome.
func (t *OpenAITranscriber) transcribeOnce(
	ctx context.Context, url, wavPath, lang string, event *AuditEvent,
) (text string, err error) {
	body, contentType, err := t.buildBody(wavPath, lang)
	if err != nil {
		event.Outcome = AuditOutcomeBuildErr

		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, body)
	if err != nil {
		event.Outcome = AuditOutcomeBuildErr

		return "", fmt.Errorf("%w: create request: %w", sttx.ErrTranscribeFailed, err)
	}

	req.Header.Set("Authorization", "Bearer "+t.apiKey)
	req.Header.Set("Content-Type", contentType)

	resp, err := t.client.Do(req)
	if err != nil {
		event.Outcome = AuditOutcomeNetworkErr

		return "", fmt.Errorf("%w: http: %w", sttx.ErrTranscribeFailed, err)
	}

	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			t.log.Warn("close response body", slog.String("error", closeErr.Error()))
		}
	}()

	event.HTTPStatus = resp.StatusCode
	event.RequestID = resp.Header.Get("X-Request-Id")

	if resp.StatusCode != http.StatusOK {
		event.Outcome = httpStatusBucket(resp.StatusCode)

		return "", readOpenAIErrorBody(resp)
	}

	var result struct {
		Text string `json:"text"`
	}
	if decErr := json.NewDecoder(io.LimitReader(resp.Body, maxRespBodySize)).Decode(&result); decErr != nil {
		event.Outcome = AuditOutcomeDecodeErr

		return "", fmt.Errorf("%w: decode response: %w", sttx.ErrTranscribeFailed, decErr)
	}

	return result.Text, nil
}

// readOpenAIErrorBody packages the non-2xx response body into a typed error.
func readOpenAIErrorBody(resp *http.Response) error {
	b, readErr := io.ReadAll(io.LimitReader(resp.Body, maxRespBodySize))
	if readErr != nil {
		return fmt.Errorf(
			"%w: api status %d (body unreadable: %w)",
			sttx.ErrTranscribeFailed, resp.StatusCode, readErr,
		)
	}

	return fmt.Errorf("%w: api status %d: %s", sttx.ErrTranscribeFailed, resp.StatusCode, string(b))
}

// buildBody creates the multipart request body for the Whisper API.
func (t *OpenAITranscriber) buildBody(wavPath, lang string) (*bytes.Buffer, string, error) {
	if err := validateWavPath(wavPath); err != nil {
		return nil, "", err
	}

	// Safe: path validated by validateWavPath above (absolute, not symlink, regular file, .wav extension).
	file, err := os.Open(filepath.Clean(wavPath))
	if err != nil {
		return nil, "", fmt.Errorf("%w: open wav: %w", sttx.ErrTranscribeFailed, err)
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil {
			t.log.Warn("close wav file", slog.String("error", closeErr.Error()))
		}
	}()

	var body bytes.Buffer

	multipartWriter := multipart.NewWriter(&body)

	err = writeOpenAIFormFields(multipartWriter, file, wavPath, lang)
	if err != nil {
		return nil, "", err
	}

	if err = multipartWriter.Close(); err != nil {
		return nil, "", fmt.Errorf("%w: close multipart writer: %w", sttx.ErrTranscribeFailed, err)
	}

	return &body, multipartWriter.FormDataContentType(), nil
}

// writeOpenAIFormFields writes the file, model, language, and response_format
// fields to the multipart writer for the OpenAI Whisper API.
func writeOpenAIFormFields(w *multipart.Writer, file *os.File, wavPath, lang string) error {
	part, err := w.CreateFormFile("file", filepath.Base(wavPath))
	if err != nil {
		return fmt.Errorf("%w: create form file: %w", sttx.ErrTranscribeFailed, err)
	}

	if _, err = io.Copy(part, file); err != nil {
		return fmt.Errorf("%w: copy file: %w", sttx.ErrTranscribeFailed, err)
	}

	if err = w.WriteField("model", "whisper-1"); err != nil {
		return fmt.Errorf("%w: write model field: %w", sttx.ErrTranscribeFailed, err)
	}

	if lang != "" && lang != langAuto {
		if err = w.WriteField("language", lang); err != nil {
			return fmt.Errorf("%w: write language field: %w", sttx.ErrTranscribeFailed, err)
		}
	}

	if err = w.WriteField("response_format", "json"); err != nil {
		return fmt.Errorf("%w: write response_format field: %w", sttx.ErrTranscribeFailed, err)
	}

	return nil
}
