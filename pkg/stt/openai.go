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

	"github.com/partyzanex/a2text/pkg/sttx"
)

const langAuto = "auto"

// OpenAITranscriber sends audio to the OpenAI Whisper API for transcription.
type OpenAITranscriber struct {
	client  *http.Client
	log     *slog.Logger
	apiKey  string
	baseURL string
}

// NewOpenAITranscriber creates an OpenAITranscriber.
// baseURL defaults to "https://api.openai.com" if empty; pass a test server URL in tests.
// client defaults to http.DefaultClient if nil.
func NewOpenAITranscriber(apiKey, baseURL string, client *http.Client, log *slog.Logger) *OpenAITranscriber {
	if baseURL == "" {
		baseURL = "https://api.openai.com"
	}

	if client == nil {
		client = http.DefaultClient
	}

	if log == nil {
		log = slog.Default()
	}

	return &OpenAITranscriber{apiKey: apiKey, baseURL: baseURL, client: client, log: log}
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
	body, contentType, err := t.buildBody(wavPath, lang)
	if err != nil {
		return "", err
	}

	url := t.baseURL + "/v1/audio/transcriptions"

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, body)
	if err != nil {
		return "", fmt.Errorf("%w: create request: %w", sttx.ErrTranscribeFailed, err)
	}

	req.Header.Set("Authorization", "Bearer "+t.apiKey)
	req.Header.Set("Content-Type", contentType)

	resp, err := t.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("%w: http: %w", sttx.ErrTranscribeFailed, err)
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			t.log.Warn("close response body", slog.String("error", closeErr.Error()))
		}
	}()

	if resp.StatusCode != http.StatusOK {
		b, readErr := io.ReadAll(io.LimitReader(resp.Body, maxRespBodySize))
		if readErr != nil {
			return "", fmt.Errorf(
				"%w: api status %d (body unreadable: %w)",
				sttx.ErrTranscribeFailed, resp.StatusCode, readErr,
			)
		}

		return "", fmt.Errorf("%w: api status %d: %s", sttx.ErrTranscribeFailed, resp.StatusCode, string(b))
	}

	var result struct {
		Text string `json:"text"`
	}
	if err = json.NewDecoder(io.LimitReader(resp.Body, maxRespBodySize)).Decode(&result); err != nil {
		return "", fmt.Errorf("%w: decode response: %w", sttx.ErrTranscribeFailed, err)
	}

	t.log.Info("openai transcription complete", slog.Int("result_len", len(result.Text)))

	return result.Text, nil
}

// buildBody creates the multipart request body for the Whisper API.
func (t *OpenAITranscriber) buildBody(wavPath, lang string) (*bytes.Buffer, string, error) {
	if err := validateWavPath(wavPath); err != nil {
		return nil, "", err
	}

	// Safe: path validated by validateWavPath above (absolute, not symlink, regular file, .wav extension).
	file, err := os.Open(wavPath) //nolint:gosec // path validated above
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
