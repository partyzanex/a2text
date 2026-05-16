package settings

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"fyne.io/fyne/v2/widget"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestUpdateWhisperCppModelSelect_NoDir_KeepsHardcodedModels verifies that
// an empty directory yields exactly the hardcoded common-model list — no
// duplicates, no truncation, no loss of the canonical options.
func TestUpdateWhisperCppModelSelect_NoDir_KeepsHardcodedModels(t *testing.T) {
	w := &Window{log: slog.Default()}

	ff := &formFields{
		whisperCppModel:     widget.NewSelect([]string{}, nil),
		whisperCppModelsDir: widget.NewEntry(),
	}

	w.updateWhisperCppModelSelect(ff, "")

	assert.Equal(t, commonWhisperCppModels, ff.whisperCppModel.Options)
}

// TestUpdateWhisperCppModelSelect_DirWithExistingModel_NoDuplicates verifies
// that on-disk .bin files already present in commonWhisperCppModels are not
// added twice.
func TestUpdateWhisperCppModelSelect_DirWithExistingModel_NoDuplicates(t *testing.T) {
	tmpDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, ggmlBaseBin), []byte("x"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, ggmlSmallBin), []byte("x"), 0o600))

	w := &Window{log: slog.Default()}
	ff := &formFields{
		whisperCppModel:     widget.NewSelect([]string{}, nil),
		whisperCppModelsDir: widget.NewEntry(),
	}

	w.updateWhisperCppModelSelect(ff, tmpDir)

	// Length unchanged: the two .bin files were already in the hardcoded list.
	assert.Len(t, ff.whisperCppModel.Options, len(commonWhisperCppModels))

	// Each name appears exactly once.
	counts := map[string]int{}
	for _, name := range ff.whisperCppModel.Options {
		counts[name]++
	}

	assert.Equal(t, 1, counts[ggmlBaseBin])
	assert.Equal(t, 1, counts[ggmlSmallBin])
}

// TestUpdateWhisperCppModelSelect_DirWithCustomModel_AppendsAfterCommon
// verifies that a .bin file not in the hardcoded list is appended at the end.
func TestUpdateWhisperCppModelSelect_DirWithCustomModel_AppendsAfterCommon(t *testing.T) {
	tmpDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "ggml-custom-finetune.bin"), []byte("x"), 0o600))

	w := &Window{log: slog.Default()}
	ff := &formFields{
		whisperCppModel:     widget.NewSelect([]string{}, nil),
		whisperCppModelsDir: widget.NewEntry(),
	}

	w.updateWhisperCppModelSelect(ff, tmpDir)

	assert.Len(t, ff.whisperCppModel.Options, len(commonWhisperCppModels)+1)
	assert.Equal(t, "ggml-custom-finetune.bin",
		ff.whisperCppModel.Options[len(ff.whisperCppModel.Options)-1])
}

// TestUpdateWhisperCppModelSelect_PreservesExistingSelection verifies that
// when the user has already picked a model, the function does not silently
// switch their selection to the first entry. Select must be initialised with
// the option set first — Fyne's SetSelected only accepts values that are in
// Options.
func TestUpdateWhisperCppModelSelect_PreservesExistingSelection(t *testing.T) {
	w := &Window{log: slog.Default()}
	sel := widget.NewSelect(append([]string(nil), commonWhisperCppModels...), nil)
	sel.SetSelected(ggmlMediumBin)

	ff := &formFields{
		whisperCppModel:     sel,
		whisperCppModelsDir: widget.NewEntry(),
	}

	w.updateWhisperCppModelSelect(ff, "")

	assert.Equal(t, ggmlMediumBin, ff.whisperCppModel.Selected)
}

// TestUpdateWhisperCppModelSelect_NoSelection_AutoSelectsFirst verifies the
// fallback: when nothing is selected, the first option becomes the default.
func TestUpdateWhisperCppModelSelect_NoSelection_AutoSelectsFirst(t *testing.T) {
	w := &Window{log: slog.Default()}
	ff := &formFields{
		whisperCppModel:     widget.NewSelect([]string{}, nil),
		whisperCppModelsDir: widget.NewEntry(),
	}

	w.updateWhisperCppModelSelect(ff, "")

	require.NotEmpty(t, ff.whisperCppModel.Options)
	assert.Equal(t, ff.whisperCppModel.Options[0], ff.whisperCppModel.Selected)
}
