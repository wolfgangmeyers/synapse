package managed

import (
	"bytes"
	"image/png"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
)

func TestRenderProgressChartPNG_Empty(t *testing.T) {
	pngBytes, err := RenderProgressChartPNG(nil, 800, 400)
	require.NoError(t, err)
	require.NotEmpty(t, pngBytes)

	img, err := png.Decode(bytes.NewReader(pngBytes))
	require.NoError(t, err)
	require.Equal(t, 800, img.Bounds().Dx())
	require.Equal(t, 400, img.Bounds().Dy())
}

func TestRenderProgressChartPNG_SingleEntry(t *testing.T) {
	entries := []store.AgentProgressEntry{
		{
			Percent:   42,
			Note:      "halfway there",
			Timestamp: "2026-03-30 12:00:00",
		},
	}
	pngBytes, err := RenderProgressChartPNG(entries, 600, 300)
	require.NoError(t, err)
	require.NotEmpty(t, pngBytes)

	img, err := png.Decode(bytes.NewReader(pngBytes))
	require.NoError(t, err)
	require.Equal(t, 600, img.Bounds().Dx())
	require.Equal(t, 300, img.Bounds().Dy())
}

func TestRenderProgressChartPNG_MultipleEntries(t *testing.T) {
	now := time.Now()
	entries := []store.AgentProgressEntry{
		{
			Percent:   0,
			Note:      "start",
			Timestamp: now.Format(time.RFC3339),
		},
		{
			Percent:   25,
			Note:      "quarter",
			Timestamp: now.Add(10 * time.Minute).Format(time.RFC3339),
		},
		{
			Percent:   60,
			Note:      "over half",
			Timestamp: now.Add(25 * time.Minute).Format(time.RFC3339),
		},
		{
			Percent:   100,
			Note:      "done",
			Timestamp: now.Add(45 * time.Minute).Format(time.RFC3339),
		},
	}

	pngBytes, err := RenderProgressChartPNG(entries, 0, 0) // default dimensions
	require.NoError(t, err)
	require.NotEmpty(t, pngBytes)

	img, err := png.Decode(bytes.NewReader(pngBytes))
	require.NoError(t, err)
	require.Equal(t, 800, img.Bounds().Dx())
	require.Equal(t, 400, img.Bounds().Dy())
}

func TestRenderProgressChartPNG_CustomDimensions(t *testing.T) {
	entries := []store.AgentProgressEntry{
		{
			Percent:   10,
			Note:      "init",
			Timestamp: "2026-03-30 10:00:00",
		},
		{
			Percent:   90,
			Note:      "almost done",
			Timestamp: "2026-03-30 11:00:00",
		},
	}

	pngBytes, err := RenderProgressChartPNG(entries, 1024, 768)
	require.NoError(t, err)
	require.NotEmpty(t, pngBytes)

	img, err := png.Decode(bytes.NewReader(pngBytes))
	require.NoError(t, err)
	require.Equal(t, 1024, img.Bounds().Dx())
	require.Equal(t, 768, img.Bounds().Dy())
}

func TestRenderProgressChartPNG_SameTimestampsOrFallback(t *testing.T) {
	entries := []store.AgentProgressEntry{
		{
			Percent:   10,
			Note:      "step 1",
			Timestamp: "same-time",
		},
		{
			Percent:   50,
			Note:      "step 2",
			Timestamp: "same-time",
		},
		{
			Percent:   80,
			Note:      "step 3",
			Timestamp: "same-time",
		},
	}

	pngBytes, err := RenderProgressChartPNG(entries, 400, 200)
	require.NoError(t, err)
	require.NotEmpty(t, pngBytes)

	img, err := png.Decode(bytes.NewReader(pngBytes))
	require.NoError(t, err)
	require.Equal(t, 400, img.Bounds().Dx())
	require.Equal(t, 200, img.Bounds().Dy())
}
