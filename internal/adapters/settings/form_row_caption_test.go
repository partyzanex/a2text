package settings

import (
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/widget"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNewErrorCaption_Hidden_WordWrap verifies the caption starts hidden
// and is configured for word wrap so long messages flow onto multiple
// lines rather than stretching the form row.
func TestNewErrorCaption_Hidden_WordWrap(t *testing.T) {
	caption := newErrorCaption()

	assert.False(t, caption.Visible(), "caption must be hidden until error")
	assert.Equal(t, fyne.TextWrapWord, caption.Wrapping)
	assert.Empty(t, caption.Segments)
}

// TestSetErrorCaption_Empty_HidesAndClearsSegments resets the caption when
// validation passes — no leftover text, no occupied vertical space.
func TestSetErrorCaption_Empty_HidesAndClearsSegments(t *testing.T) {
	caption := newErrorCaption()
	setErrorCaption(caption, "previous error")
	require.True(t, caption.Visible())

	setErrorCaption(caption, "")

	assert.False(t, caption.Visible())
	assert.Empty(t, caption.Segments)
}

// TestSetErrorCaption_NonEmpty_ShowsTextSegment populates the rich-text
// caption with a single text segment carrying the supplied message.
func TestSetErrorCaption_NonEmpty_ShowsTextSegment(t *testing.T) {
	caption := newErrorCaption()

	msg := "URL must include scheme (http://) and host"
	setErrorCaption(caption, msg)

	assert.True(t, caption.Visible())
	require.Len(t, caption.Segments, 1)

	seg, ok := caption.Segments[0].(*widget.TextSegment)
	require.True(t, ok, "expected *widget.TextSegment, got %T", caption.Segments[0])
	assert.Equal(t, msg, seg.Text)
}

// TestSetErrorCaption_Replace overwrites the previous message instead of
// appending — repeated validator firings must not accumulate segments.
func TestSetErrorCaption_Replace(t *testing.T) {
	caption := newErrorCaption()

	setErrorCaption(caption, "first error")
	setErrorCaption(caption, "second error")

	require.Len(t, caption.Segments, 1)

	seg, ok := caption.Segments[0].(*widget.TextSegment)
	require.True(t, ok)
	assert.Equal(t, "second error", seg.Text)
}
