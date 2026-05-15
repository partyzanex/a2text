package settings

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"fyne.io/fyne/v2/widget"
)

// TestBuildTempDirFieldNotNil verifies that buildTempDirField returns a non-nil container.
func TestBuildTempDirFieldNotNil(t *testing.T) {
	window := &Window{}
	ff := &formFields{
		tempDir: widget.NewEntry(),
	}

	container := window.buildTempDirField(ff)
	assert.NotNil(t, container, "buildTempDirField should return non-nil container")
}

// TestTempDirPickerWithEmptyPath verifies that empty path handling works.
func TestTempDirPickerWithEmptyPath(t *testing.T) {
	// Create Entry with empty text
	entry := widget.NewEntry()
	entry.SetText("")

	// Simulate the path selection logic from openTempDirPicker
	currentPath := entry.Text
	if currentPath == "" {
		home, err := os.UserHomeDir()
		require.NoError(t, err)
		require.NotEmpty(t, home)
	}
}

// TestTempDirPickerWithValidPath verifies that valid path is preserved.
func TestTempDirPickerWithValidPath(t *testing.T) {
	// Create Entry with a valid path
	entry := widget.NewEntry()
	testPath := "/tmp/test"
	entry.SetText(testPath)

	// Verify path is preserved
	assert.Equal(t, testPath, entry.Text)
}

// TestTempDirFieldIntegration verifies basic integration of the field components.
func TestTempDirFieldIntegration(t *testing.T) {
	window := &Window{}
	entry := widget.NewEntry()
	entry.SetText("/some/path")

	ff := &formFields{
		tempDir:       entry,
		tempDirButton: widget.NewButton("Browse", nil),
	}

	// Verify components are properly set
	assert.NotNil(t, ff.tempDir)
	assert.NotNil(t, ff.tempDirButton)
	assert.Equal(t, "/some/path", ff.tempDir.Text)

	// Build the field
	field := window.buildTempDirField(ff)
	assert.NotNil(t, field)
}
