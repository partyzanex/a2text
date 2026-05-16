package settings

import (
	"testing"

	"fyne.io/fyne/v2/widget"
	"github.com/stretchr/testify/assert"
)

// newFFForSeedTest builds a minimal formFields with the widgets the seed
// and OnChanged hooks touch. modelPath is a plain Entry rather than a
// SelectEntry because the seed/wire helpers do not depend on the
// dropdown surface.
func newFFForSeedTest() *formFields {
	return &formFields{
		modelPath:           widget.NewSelectEntry(nil),
		whisperCppModelsDir: widget.NewEntry(),
		whisperCppModel:     widget.NewSelect(append([]string(nil), commonWhisperCppModels...), nil),
	}
}

// TestSeedWhisperCpp_EmptyModelPath_NoOp confirms that an empty ModelPath
// leaves all three widgets untouched.
func TestSeedWhisperCpp_EmptyModelPath_NoOp(t *testing.T) {
	ff := newFFForSeedTest()

	seedWhisperCppFromModelPath(ff, "")

	assert.Empty(t, ff.whisperCppModelsDir.Text)
	assert.Empty(t, ff.whisperCppModel.Selected)
}

// TestSeedWhisperCpp_FullPath_PopulatesDirAndSelection splits a full path
// into directory and filename and seeds both widgets.
func TestSeedWhisperCpp_FullPath_PopulatesDirAndSelection(t *testing.T) {
	ff := newFFForSeedTest()

	seedWhisperCppFromModelPath(ff, "/home/user/.local/share/a2text/models/ggml-small.bin")

	assert.Equal(t, "/home/user/.local/share/a2text/models", ff.whisperCppModelsDir.Text)
	assert.Equal(t, "ggml-small.bin", ff.whisperCppModel.Selected)
}

// TestSeedWhisperCpp_BareFilename_PreservesEmptyDir verifies that when
// ModelPath has no directory component, the dir entry stays empty (later
// resolved at runtime via whisperCppModelsDir()).
func TestSeedWhisperCpp_BareFilename_PreservesEmptyDir(t *testing.T) {
	ff := newFFForSeedTest()

	seedWhisperCppFromModelPath(ff, "ggml-tiny.bin")

	assert.Empty(t, ff.whisperCppModelsDir.Text)
	assert.Equal(t, "ggml-tiny.bin", ff.whisperCppModel.Selected)
}

// TestSeedWhisperCpp_DirAlreadySet_DoesNotOverwrite preserves a
// user-typed models directory that is not part of ModelPath.
func TestSeedWhisperCpp_DirAlreadySet_DoesNotOverwrite(t *testing.T) {
	ff := newFFForSeedTest()
	ff.whisperCppModelsDir.SetText("/opt/whisper")

	seedWhisperCppFromModelPath(ff, "/home/user/models/ggml-large.bin")

	assert.Equal(t, "/opt/whisper", ff.whisperCppModelsDir.Text)
	assert.Equal(t, "ggml-large.bin", ff.whisperCppModel.Selected)
}

// TestSeedWhisperCpp_CustomModelName_AppendedToOptions confirms a
// non-standard model filename is appended to the dropdown's option list
// (otherwise SetSelected would silently fail on Fyne).
func TestSeedWhisperCpp_CustomModelName_AppendedToOptions(t *testing.T) {
	ff := newFFForSeedTest()

	const custom = "ggml-finetune-ru.bin"

	seedWhisperCppFromModelPath(ff, "/tmp/"+custom)

	assert.Contains(t, ff.whisperCppModel.Options, custom)
	assert.Equal(t, custom, ff.whisperCppModel.Selected)
}

// TestWireWhisperCppModelChange_EmptyName_NoUpdate verifies the hook
// ignores the empty-string change Fyne emits when the option list is reset.
func TestWireWhisperCppModelChange_EmptyName_NoUpdate(t *testing.T) {
	ff := newFFForSeedTest()
	ff.modelPath.SetText("untouched")

	wireWhisperCppModelChange(ff)
	ff.whisperCppModel.OnChanged("")

	assert.Equal(t, "untouched", ff.modelPath.Text)
}

// TestWireWhisperCppModelChange_JoinsDirAndModel composes modelPath from
// dir + selected when both are present.
func TestWireWhisperCppModelChange_JoinsDirAndModel(t *testing.T) {
	ff := newFFForSeedTest()
	ff.whisperCppModelsDir.SetText("/opt/models")

	wireWhisperCppModelChange(ff)
	ff.whisperCppModel.OnChanged("ggml-medium.bin")

	assert.Equal(t, "/opt/models/ggml-medium.bin", ff.modelPath.Text)
}

// TestWireWhisperCppModelChange_BareNameWhenNoDir falls back to the model
// name alone when neither the dir entry nor the default models dir is set.
func TestWireWhisperCppModelChange_BareNameWhenNoDir(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("HOME", "")

	ff := newFFForSeedTest()
	ff.whisperCppModelsDir.SetText("")

	wireWhisperCppModelChange(ff)
	ff.whisperCppModel.OnChanged("ggml-base.bin")

	assert.Equal(t, "ggml-base.bin", ff.modelPath.Text)
}
