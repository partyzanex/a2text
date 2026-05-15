package settings

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"

	"github.com/partyzanex/a2text/internal/i18n"
	"github.com/partyzanex/a2text/internal/infra/config"
	"github.com/partyzanex/a2text/pkg/gowhisper"
	"github.com/partyzanex/a2text/pkg/whispercpp"
)

// ModelDownloader abstracts whispercpp.Downloader so unit tests can
// supply a stub. Production wiring uses a *whispercpp.Downloader, which
// satisfies the interface structurally.
type ModelDownloader interface {
	Download(
		ctx context.Context,
		modelFile string,
		destDir string,
		progress whispercpp.ProgressFunc,
	) (string, error)
}

// buildSTTTab assembles the STT settings tab. Each logical group goes
// into a titled card so the visual structure mirrors config sections.
//
// Stores the three provider-specific cards on ff so applyProviderVisibility
// can later show only the card matching the current provider.
func (w *Window) buildSTTTab(ff *formFields) fyne.CanvasObject {
	general := rowsCard(i18n.T("card.general"),
		formRowWithHelp(i18n.T("label.stt_provider"), "help.stt_provider", ff.provider),
		formRowWithHelp(i18n.T("label.stt_language"), "help.stt_language", ff.language),
		formRowWithHelp(i18n.T("label.ui_language"), "help.ui_language", ff.uiLanguage),
	)

	ff.whisperCheckBtn.OnTapped = func() { w.onCheckGoWhisper(ff) }

	ff.goWhisperCard = rowsCard(i18n.T("card.go_whisper"),
		formRowValidatedWithTrailingButton(i18n.T("label.url"), "help.gw_url",
			ff.whisperURL, ff.whisperCheckBtn, ff.whisperCheckStatus,
			validateRequiredHTTPURL),
		formRowWithHelp(i18n.T("label.model"), "help.gw_model", ff.whisperModel),
		formRowValidatedWithHelp(i18n.T("label.timeout"), "help.gw_timeout",
			ff.whisperTimeout, validateDuration),
		formRowWithHelp(i18n.T("label.auto_download"), "help.gw_auto_download",
			leftAlign(ff.whisperAutoDownload)),
	)

	ff.whisperCppCard = rowsCard(i18n.T("card.whisper_cpp"),
		// SelectEntry is a composite widget — passing &ff.modelPath.Entry
		// to formRowValidated would split it (only the inner Entry would
		// be laid out) and the orphaned dropdown button would crash on
		// click because its canvas pointer is never wired. Use the
		// dedicated SelectEntry row helper, which places the full widget
		// and attaches the validator + error caption around it.
		formRowSelectEntryValidatedWithHelp(
			i18n.T("label.model_path"), "help.cpp_model_path",
			ff.modelPath, validateWhisperCppModelPath,
		),
		w.buildModelDownloadRow(ff),
	)

	ff.cloudCard = rowsCard(i18n.T("card.cloud"),
		formRowWithHelp(i18n.T("label.cloud_provider"), "help.cloud_provider", ff.cloudProvider),
		formRowWithHelp(i18n.T("label.api_key"), "help.cloud_api_key", ff.cloudAPIKey),
		formRowValidatedWithHelp(i18n.T("label.base_url"), "help.cloud_base_url",
			ff.cloudBaseURL, validateHTTPURL),
	)

	retry := rowsCard(i18n.T("card.stt_retry"),
		formRowWithHelp(i18n.T("label.retry_enabled"), "help.retry_enabled",
			leftAlign(ff.sttRetryEnabled)),
		formRowValidatedWithHelp(i18n.T("label.retry_initial_delay"), "help.retry_initial_delay",
			ff.sttRetryInitDelay, validateDuration),
		formRowValidatedWithHelp(i18n.T("label.retry_max_delay"), "help.retry_max_delay",
			ff.sttRetryMaxDelay, validateDuration),
		formRowValidatedWithHelp(i18n.T("label.retry_max_attempts"), "help.retry_max_attempts",
			ff.sttRetryMaxAttempts, validateNonNegativeInt),
	)

	return tabBody(general, ff.goWhisperCard, ff.whisperCppCard, ff.cloudCard, retry)
}

// buildSTTFieldWidgets creates STT-section widgets and returns an initialized formFields.
func (w *Window) buildSTTFieldWidgets() *formFields {
	apiKeyEntry := widget.NewEntry()
	apiKeyEntry.Password = true
	apiKeyEntry.SetText(w.cfg.CloudAPIKey)

	return &formFields{
		provider: widget.NewSelect(
			[]string{
				config.VoiceProviderGoWhisper,
				config.VoiceProviderWhisperCpp,
				config.VoiceProviderCloud,
			},
			nil,
		),
		language:            newLanguageSelect(sttLanguageCodes()),
		uiLanguage:          newLanguageSelect(i18n.SupportedLanguages),
		whisperURL:          entryWithText(w.cfg.GoWhisper.URL, "http://localhost:9081/api/whisper"),
		whisperModel:        newModelSelectEntry(w.cfg.GoWhisper.Model),
		whisperTimeout:      entryWithText(formatDuration(w.cfg.GoWhisper.Timeout), "30s"),
		whisperAutoDownload: widget.NewCheck("", nil),
		modelPath:           newWhisperCppModelPathEntry(w.cfg.ModelPath),
		modelDownloadBtn:    widget.NewButton(i18n.T("button.download_model"), nil),
		modelDownloadBar:    widget.NewProgressBar(),
		modelDownloadMsg:    widget.NewLabel(""),
		cloudProvider:       entryWithText(w.cfg.CloudProvider, "openai"),
		cloudAPIKey:         apiKeyEntry,
		cloudBaseURL:        entryWithText(w.cfg.CloudBaseURL, ""),
		sttRetryEnabled:     widget.NewCheck("", nil),
		sttRetryInitDelay:   entryWithText(formatDuration(w.cfg.STTRetry.InitialDelay), "200ms"),
		sttRetryMaxDelay:    entryWithText(formatDuration(w.cfg.STTRetry.MaxDelay), "5s"),
		sttRetryMaxAttempts: entryWithText(intOrEmpty(w.cfg.STTRetry.MaxAttempts), "2"),
		whisperCheckBtn:     widget.NewButton(i18n.T("button.check_connection"), nil),
		whisperCheckStatus:  newStatusText(),
	}
}

// applySTTFields writes STT-related form values back to the config.
func (w *Window) applySTTFields(ff *formFields) {
	w.cfg.Provider = ff.provider.Selected
	w.cfg.Language = ff.language.Selected
	w.cfg.UILanguage = ff.uiLanguage.Selected
	w.cfg.GoWhisper.URL = ff.whisperURL.Text
	w.cfg.GoWhisper.Model = ff.whisperModel.Text
	w.cfg.GoWhisper.Timeout = parseDuration(ff.whisperTimeout.Text)
	w.cfg.GoWhisper.AutoDownload = ff.whisperAutoDownload.Checked
	w.cfg.ModelPath = ff.modelPath.Text
	w.cfg.CloudProvider = ff.cloudProvider.Text
	w.cfg.CloudBaseURL = ff.cloudBaseURL.Text

	if ff.cloudAPIKey.Text != "" {
		w.cfg.CloudAPIKey = ff.cloudAPIKey.Text
	}

	w.cfg.STTRetry.Enabled = ff.sttRetryEnabled.Checked
	w.cfg.STTRetry.InitialDelay = parseDuration(ff.sttRetryInitDelay.Text)
	w.cfg.STTRetry.MaxDelay = parseDuration(ff.sttRetryMaxDelay.Text)
	w.cfg.STTRetry.MaxAttempts = parseIntEntry(ff.sttRetryMaxAttempts.Text)
}

// applyProviderVisibility shows the STT card matching ff.provider.Selected
// and hides the other two.
func applyProviderVisibility(ff *formFields) {
	cards := map[string]fyne.CanvasObject{
		config.VoiceProviderGoWhisper:  ff.goWhisperCard,
		config.VoiceProviderWhisperCpp: ff.whisperCppCard,
		config.VoiceProviderCloud:      ff.cloudCard,
	}

	selected := ff.provider.Selected

	for provider, card := range cards {
		if card == nil {
			continue
		}

		if provider == selected {
			card.Show()
		} else {
			card.Hide()
		}
	}
}

// onCheckGoWhisper handles a click on the check button: validates the
// URL syntactically before touching the network, then issues
// gowhisper.Check on a background goroutine. Returns immediately — the
// caption updates when the probe completes (or its timeout fires).
func (w *Window) onCheckGoWhisper(ff *formFields) {
	targetURL := strings.TrimSpace(ff.whisperURL.Text)
	if err := validateRequiredHTTPURL(targetURL); err != nil {
		w.log.Debug("settings: go-whisper check rejected — invalid URL",
			slog.String("url", targetURL),
			slog.Any("err", err),
		)
		setStatusText(ff.whisperCheckStatus, err.Error(), statusKindError)

		return
	}

	parent := w.rootCtx()

	const checkTimeout = 5 * time.Second

	w.log.Debug("settings: go-whisper check starting",
		slog.String("url", targetURL),
		slog.Duration("timeout", checkTimeout),
	)
	setStatusText(ff.whisperCheckStatus, i18n.T("check.status.in_progress"), statusKindNeutral)
	ff.whisperCheckBtn.Disable()

	go func() {
		defer fyne.Do(func() { ff.whisperCheckBtn.Enable() })

		res, err := gowhisper.Check(parent, targetURL, checkTimeout)

		fyne.Do(func() {
			if err != nil {
				w.log.Warn("settings: go-whisper check failed",
					slog.String("url", targetURL),
					slog.Any("err", err),
				)
				setStatusText(ff.whisperCheckStatus, i18n.T("check.status.failed"), statusKindError)

				return
			}

			w.log.Info("settings: go-whisper check ok",
				slog.String("url", targetURL),
				slog.Int("models", len(res.Models)),
				slog.Duration("elapsed", res.Elapsed.Truncate(time.Millisecond)),
				slog.String("status", res.Status),
			)

			msg := fmt.Sprintf("%s (%d %s, %s)",
				i18n.T("check.status.ok"),
				len(res.Models), i18n.T("check.models"),
				res.Elapsed.Truncate(time.Millisecond),
			)
			setStatusText(ff.whisperCheckStatus, msg, statusKindSuccess)
		})
	}()
}

// buildModelDownloadRow assembles the "download model" row sitting just
// under the model-path entry.
func (w *Window) buildModelDownloadRow(ff *formFields) *fyne.Container {
	ff.modelDownloadBar.Hide()
	ff.modelDownloadMsg.Hide()
	ff.modelDownloadBtn.OnTapped = func() { w.onDownloadModel(ff) }

	right := container.NewBorder(nil, nil, nil, ff.modelDownloadBtn, ff.modelDownloadBar)
	stack := container.NewVBox(right, ff.modelDownloadMsg)

	return formRow(" ", stack)
}

// onDownloadModel handles a click on the "Скачать модель" button.
// Clicking the button again while a download is in progress cancels it.
func (w *Window) onDownloadModel(ff *formFields) {
	w.downloadMu.Lock()

	if w.downloadCancel != nil {
		cancel := w.downloadCancel
		w.downloadCancel = nil
		w.downloadMu.Unlock()

		cancel()

		return
	}

	current := strings.TrimSpace(ff.modelPath.Text)
	if current == "" {
		w.downloadMu.Unlock()
		w.setDownloadMessage(ff, i18n.T("download.error.empty_path"), true)

		return
	}

	modelFile := filepath.Base(current)

	destDir := whisperCppModelsDir()
	if destDir == "" {
		destDir = filepath.Dir(current)
	}

	// Fyne's OnTapped callback takes no parameters, so we cannot accept
	// a ctx here — we read it from rootCtx, which the bootstrap wires via
	// SetRootContext. contextcheck cannot follow this UI-callback path.
	parent := w.rootCtx()

	ctx, cancel := context.WithCancel(parent)
	w.downloadCancel = cancel

	if w.downloader == nil {
		w.downloader = &whispercpp.Downloader{}
	}

	dl := w.downloader

	w.downloadMu.Unlock()

	ff.modelDownloadBar.SetValue(0)
	ff.modelDownloadBar.Show()
	ff.modelDownloadBtn.SetText(i18n.T("button.download_cancel"))
	w.setDownloadMessage(ff, i18n.T("download.status.starting"), false)

	go w.runDownload(ctx, ff, dl, modelFile, destDir)
}

// runDownload is the goroutine body for an in-flight download. All UI
// mutations are marshalled to the Fyne goroutine via fyne.Do.
func (w *Window) runDownload(
	ctx context.Context,
	ff *formFields,
	dl ModelDownloader,
	modelFile, destDir string,
) {
	defer func() {
		w.downloadMu.Lock()
		w.downloadCancel = nil
		w.downloadMu.Unlock()

		fyne.Do(func() {
			ff.modelDownloadBar.Hide()
			ff.modelDownloadBtn.SetText(i18n.T("button.download_model"))
		})
	}()

	path, err := dl.Download(ctx, modelFile, destDir, func(progress whispercpp.Progress) {
		fyne.Do(func() {
			if progress.Total > 0 {
				ff.modelDownloadBar.SetValue(float64(progress.Done) / float64(progress.Total))
			}

			w.setDownloadMessage(ff, formatProgress(progress), false)
		})
	})
	if err != nil {
		if errors.Is(err, context.Canceled) {
			fyne.Do(func() { w.setDownloadMessage(ff, i18n.T("download.status.cancelled"), false) })

			return
		}

		w.log.Warn("settings: model download failed", slog.Any("err", err))
		fyne.Do(func() {
			msg := fmt.Sprintf("%s: %v", i18n.T("download.status.failed"), err)
			w.setDownloadMessage(ff, msg, true)
		})

		return
	}

	fyne.Do(func() {
		ff.modelPath.SetText(path)
		w.setDownloadMessage(ff, i18n.T("download.status.done"), false)

		if w.saver != nil {
			w.saver.Schedule()
		}
	})
}

// setDownloadMessage updates the status caption under the download
// progress bar.
func (w *Window) setDownloadMessage(ff *formFields, text string, isError bool) {
	_ = isError

	if text == "" {
		ff.modelDownloadMsg.SetText("")
		ff.modelDownloadMsg.Hide()

		return
	}

	ff.modelDownloadMsg.SetText(text)
	ff.modelDownloadMsg.Show()
}

// formatProgress renders a Progress value as a human-readable string
// for the status caption.
func formatProgress(progress whispercpp.Progress) string {
	if progress.Total <= 0 {
		return fmt.Sprintf("%s: %s", progress.Source, formatBytes(progress.Done))
	}

	return fmt.Sprintf("%s: %s / %s",
		progress.Source, formatBytes(progress.Done), formatBytes(progress.Total))
}

// formatBytes prints a byte count with a one-letter SI suffix.
func formatBytes(n int64) string {
	const (
		kib = 1024
		mib = kib * 1024
		gib = mib * 1024
	)

	switch {
	case n >= gib:
		return fmt.Sprintf("%.2f GiB", float64(n)/float64(gib))
	case n >= mib:
		return fmt.Sprintf("%.1f MiB", float64(n)/float64(mib))
	case n >= kib:
		return fmt.Sprintf("%.0f KiB", float64(n)/float64(kib))
	}

	return fmt.Sprintf("%d B", n)
}

// commonGoWhisperModels lists the well-known GGML model IDs that ship with
// go-whisper.
//
//nolint:gochecknoglobals // immutable lookup table for the model combobox
var commonGoWhisperModels = []string{
	"ggml-tiny", "ggml-tiny.en",
	"ggml-base", "ggml-base.en",
	"ggml-small", "ggml-small.en",
	"ggml-medium", "ggml-medium.en",
	"ggml-large-v1",
	"ggml-large-v2",
	"ggml-large-v3",
	"ggml-large-v3-turbo",
}

// newModelSelectEntry builds the "Модель" combobox.
func newModelSelectEntry(current string) *widget.SelectEntry {
	options := make([]string, len(commonGoWhisperModels), len(commonGoWhisperModels)+1)
	copy(options, commonGoWhisperModels)

	if current != "" && !slices.Contains(options, current) {
		options = append(options, current)
	}

	entry := widget.NewSelectEntry(options)
	entry.SetText(current)
	entry.SetPlaceHolder("ggml-small")

	return entry
}

// commonWhisperCppModels lists the well-known GGML model filenames for
// whisper.cpp.
//
//nolint:gochecknoglobals // immutable lookup table for the model-path combobox
var commonWhisperCppModels = []string{
	"ggml-tiny.bin", "ggml-tiny.en.bin",
	"ggml-base.bin", "ggml-base.en.bin",
	"ggml-small.bin", "ggml-small.en.bin",
	"ggml-medium.bin", "ggml-medium.en.bin",
	"ggml-large-v1.bin",
	"ggml-large-v2.bin",
	"ggml-large-v3.bin",
	"ggml-large-v3-turbo.bin",
}

// whisperCppModelsDir returns the conventional directory where users
// keep whisper.cpp .bin models on this machine.
func whisperCppModelsDir() string {
	if dir := strings.TrimSpace(os.Getenv("XDG_DATA_HOME")); dir != "" {
		return filepath.Join(dir, "a2text", "models")
	}

	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".local", "share", "a2text", "models")
	}

	return ""
}

// newWhisperCppModelPathEntry builds the "Путь к модели" combobox.
func newWhisperCppModelPathEntry(current string) *widget.SelectEntry {
	dir := whisperCppModelsDir()

	options := make([]string, 0, len(commonWhisperCppModels)+1)
	for _, name := range commonWhisperCppModels {
		if dir == "" {
			options = append(options, name)

			continue
		}

		options = append(options, filepath.Join(dir, name))
	}

	if current != "" && !slices.Contains(options, current) {
		options = append(options, current)
	}

	entry := widget.NewSelectEntry(options)
	entry.SetText(current)

	if dir != "" {
		entry.SetPlaceHolder(filepath.Join(dir, "ggml-small.bin"))
	} else {
		entry.SetPlaceHolder("/path/to/ggml-small.bin")
	}

	return entry
}

// validateWhisperCppModelPath checks that a user-supplied model path
// points to an actual readable GGML/GGUF file.
func validateWhisperCppModelPath(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}

	if err := validateModelFileMeta(value); err != nil {
		return err
	}

	return validateModelFileMagic(value)
}

// validateModelFileMeta checks the stat-level properties of value.
func validateModelFileMeta(value string) error {
	const minModelSizeBytes int64 = 1 << 20 // 1 MiB

	info, err := os.Stat(value)
	if err != nil {
		return fmt.Errorf("%s: %w", i18n.T("validation.model_path_stat_failed"), err)
	}

	if info.IsDir() {
		return errors.New(i18n.T("validation.model_path_is_dir"))
	}

	if !info.Mode().IsRegular() {
		return errors.New(i18n.T("validation.model_path_not_regular"))
	}

	if info.Size() < minModelSizeBytes {
		return errors.New(i18n.T("validation.model_path_too_small"))
	}

	return nil
}

// validateModelFileMagic opens value, reads its first 4 bytes, and
// returns nil iff they match a known whisper-model magic.
func validateModelFileMagic(value string) (retErr error) {
	const magicSize = 4

	file, err := os.Open(filepath.Clean(value))
	if err != nil {
		return fmt.Errorf("%s: %w", i18n.T("validation.model_path_open_failed"), err)
	}

	defer func() {
		if closeErr := file.Close(); closeErr != nil && retErr == nil {
			retErr = fmt.Errorf("close model file: %w", closeErr)
		}
	}()

	magic := make([]byte, magicSize)
	if _, err := io.ReadFull(file, magic); err != nil {
		return fmt.Errorf("%s: %w", i18n.T("validation.model_path_read_failed"), err)
	}

	if !isWhisperModelMagic(magic) {
		return errors.New(i18n.T("validation.model_path_bad_magic"))
	}

	return nil
}

// isWhisperModelMagic returns true when magic matches one of the file
// signatures whisper.cpp recognises.
func isWhisperModelMagic(magic []byte) bool {
	const magicLen = 4

	if len(magic) < magicLen {
		return false
	}

	switch string(magic[:magicLen]) {
	case "lmgg", "ggml", "GGUF":
		return true
	}

	return false
}

// sttLanguageAuto is the magic string written to cfg.Language to ask
// whisper to auto-detect the spoken language.
const sttLanguageAuto = "auto"

// sttLanguageCodes returns the STT-language dropdown options.
func sttLanguageCodes() []string {
	codes := make([]string, 0, 1+len(i18n.SupportedLanguages))
	codes = append(codes, sttLanguageAuto)
	codes = append(codes, i18n.SupportedLanguages...)

	return codes
}

// newLanguageSelect builds a widget.Select pre-populated with the given
// language codes.
func newLanguageSelect(codes []string) *widget.Select {
	return widget.NewSelect(codes, nil)
}

// sttLanguageOrDefault canonicalises a stored STT language value to one
// of the dropdown options.
func sttLanguageOrDefault(code string) string {
	code = strings.TrimSpace(code)
	if code == "" {
		return sttLanguageAuto
	}

	if slices.Contains(sttLanguageCodes(), code) {
		return code
	}

	return sttLanguageAuto
}

// uiLanguageOrDefault canonicalises a stored UI language code to one of
// the bundled locales.
func uiLanguageOrDefault(code string) string {
	code = strings.TrimSpace(code)
	if code == "" {
		return i18n.DefaultLanguage
	}

	if slices.Contains(i18n.SupportedLanguages, code) {
		return code
	}

	return i18n.DefaultLanguage
}
