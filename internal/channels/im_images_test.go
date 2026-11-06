package channels

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"image"
	"image/color"
	"image/draw"
	"image/gif"
	"image/jpeg"
	"image/png"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	core "github.com/oines/astral-core"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/messages"
	"github.com/oines/astral/internal/testkit"
)

func observationPNG(t *testing.T, width, height int) []byte {
	t.Helper()
	return observationPNGWithCompression(t, width, height, png.DefaultCompression)
}

func observationPNGWithCompression(t *testing.T, width, height int, level png.CompressionLevel) []byte {
	t.Helper()
	canvas := image.NewNRGBA(image.Rect(0, 0, width, height))
	for y := height / 2; y < height; y++ {
		for x := width / 2; x < width; x++ {
			canvas.SetNRGBA(x, y, color.NRGBA{R: 230, A: 255})
		}
	}
	var data bytes.Buffer
	encoder := png.Encoder{CompressionLevel: level}
	if err := encoder.Encode(&data, canvas); err != nil {
		t.Fatal(err)
	}
	return data.Bytes()
}

func observationUncompressedPNG(t *testing.T, width, height int) []byte {
	t.Helper()
	canvas := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			canvas.SetRGBA(x, y, color.RGBA{
				R: byte(x / 5), G: byte(y / 4), B: byte((x + y) / 9), A: 255,
			})
		}
	}
	var data bytes.Buffer
	// The source is a valid, deliberately uncompressed PNG. Its bytes exceed
	// both image budgets while the pixels compress well, so processing must
	// encode WebP without needing to shrink the original dimensions.
	encoder := png.Encoder{CompressionLevel: png.NoCompression}
	if err := encoder.Encode(&data, canvas); err != nil {
		t.Fatal(err)
	}
	return data.Bytes()
}

func observationPreviewPixels(t *testing.T, part core.Content) image.Image {
	t.Helper()
	var encoded string
	if err := json.Unmarshal(part.Data, &encoded); err != nil {
		t.Fatal(err)
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatal(err)
	}
	pixels, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	return pixels
}

func observationPreviewBase64(t *testing.T, part core.Content) string {
	t.Helper()
	var encoded string
	if err := json.Unmarshal(part.Data, &encoded); err != nil {
		t.Fatal(err)
	}
	if _, err := base64.StdEncoding.DecodeString(encoded); err != nil {
		t.Fatalf("invalid preview base64: %v", err)
	}
	return encoded
}

func TestObservationPreviewCompressesHighNoiseWithoutResizing(t *testing.T) {
	canvas := image.NewRGBA(image.Rect(0, 0, 512, 512))
	state := uint32(1)
	for i := 0; i < len(canvas.Pix); i += 4 {
		for channel := 0; channel < 3; channel++ {
			state = state*1664525 + 1013904223
			canvas.Pix[i+channel] = byte(state >> 24)
		}
		canvas.Pix[i+3] = 255
	}
	var source bytes.Buffer
	if err := png.Encode(&source, canvas); err != nil {
		t.Fatal(err)
	}
	if base64.StdEncoding.EncodedLen(source.Len()) <= observationImageCompressAboveBytes {
		t.Fatal("fixture does not exceed the encoded preview budget")
	}
	result, err := processObservationImage(t.Context(), source.Bytes())
	if err != nil || result.IsError || len(result.Content) != 2 {
		t.Fatalf("prepare noisy preview: %+v, %v", result, err)
	}
	if encoded := observationPreviewBase64(t, result.Content[1]); len(encoded) >= base64.StdEncoding.EncodedLen(source.Len()) {
		t.Fatal("compressed preview did not reduce the original encoded size")
	}
	if result.Content[1].MediaType != "image/webp" {
		t.Fatalf("noisy image was not compressed to WebP: %q", result.Content[1].MediaType)
	}
	if bounds := observationPreviewPixels(t, result.Content[1]).Bounds(); bounds != canvas.Bounds() {
		t.Fatalf("preview was unexpectedly resized: %v", bounds)
	}
}

func TestObservationPreviewBoundsLargeAnimatedGIFWithoutShrinkingSmallFrame(t *testing.T) {
	palette := make(color.Palette, 256)
	for i := range palette {
		palette[i] = color.RGBA{R: byte(i), G: byte(i * 73), B: byte(i * 151), A: 255}
	}
	animation := gif.GIF{}
	state := uint32(1)
	for range 12 {
		frame := image.NewPaletted(image.Rect(0, 0, 96, 96), palette)
		for i := range frame.Pix {
			state = state*1664525 + 1013904223
			frame.Pix[i] = byte(state >> 24)
		}
		animation.Image = append(animation.Image, frame)
		animation.Delay = append(animation.Delay, 10)
	}
	var source bytes.Buffer
	if err := gif.EncodeAll(&source, &animation); err != nil {
		t.Fatal(err)
	}
	if base64.StdEncoding.EncodedLen(source.Len()) <= observationImageCompressAboveBytes {
		t.Fatal("small-frame animation does not exceed the encoded preview budget")
	}
	result, err := processObservationImage(t.Context(), source.Bytes())
	if err != nil || result.IsError || len(result.Content) != 2 {
		t.Fatalf("prepare animated preview: %+v, %v", result, err)
	}
	observationPreviewBase64(t, result.Content[1])
	if bounds := observationPreviewPixels(t, result.Content[1]).Bounds(); bounds.Dx() != 96 || bounds.Dy() != 96 || result.Content[1].MediaType != "image/webp" {
		t.Fatalf("large animation was not reduced to a bounded static preview: %v, %q", bounds, result.Content[1].MediaType)
	}
}

func TestObservationPreviewUsesIMQualityWithoutChangingReadDefault(t *testing.T) {
	source := observationUncompressedPNG(t, 1200, 800)
	if base64.StdEncoding.EncodedLen(len(source)) <= 150_000 {
		t.Fatal("fixture does not require ordinary image processing")
	}
	preview, err := processObservationImage(t.Context(), source)
	if err != nil || preview.IsError || len(preview.Content) != 2 {
		t.Fatalf("prepare IM image: %+v, %v", preview, err)
	}
	options := core.ImageOptions{CompressAboveBytes: observationImageCompressAboveBytes, Quality: 30}
	expected, err := core.ProcessImage(t.Context(), source, options)
	if err != nil || expected.IsError || len(expected.Content) != 2 {
		t.Fatalf("prepare reference IM image: %+v, %v", expected, err)
	}
	if preview.Content[1].MediaType != "image/webp" || !bytes.Equal(preview.Content[1].Data, expected.Content[1].Data) {
		t.Fatal("IM preview did not use Core's quality-30 WebP encoding")
	}
	if bounds := observationPreviewPixels(t, preview.Content[1]).Bounds(); bounds.Dx() != 1200 || bounds.Dy() != 800 {
		t.Fatalf("IM unnecessarily imposed a dimension limit despite fitting the encoded budget: %v", bounds)
	}
	previewBytes, err := base64.StdEncoding.DecodeString(observationPreviewBase64(t, preview.Content[1]))
	if err != nil {
		t.Fatal(err)
	}
	passthrough, err := processObservationImage(t.Context(), previewBytes)
	if err != nil || passthrough.IsError || len(passthrough.Content) != 2 || !bytes.Equal(passthrough.Content[1].Data, preview.Content[1].Data) {
		t.Fatal("a WebP already within the preview limits was recompressed:", err)
	}
	// The source exceeds the ordinary byte budget too, so the default must
	// encode instead of taking the original-byte passthrough path.
	readOptions := core.ImageOptions{}
	readDefault, err := core.ProcessImage(t.Context(), source, readOptions)
	if err != nil || readDefault.IsError || len(readDefault.Content) != 2 {
		t.Fatalf("prepare ordinary read image: %+v, %v", readDefault, err)
	}
	readOptions.Quality = 75
	readHigh, err := core.ProcessImage(t.Context(), source, readOptions)
	if err != nil || readHigh.IsError || len(readHigh.Content) != 2 {
		t.Fatalf("prepare explicit high-quality read image: %+v, %v", readHigh, err)
	}
	if readDefault.Content[1].MediaType != "image/webp" || !bytes.Equal(readDefault.Content[1].Data, readHigh.Content[1].Data) {
		t.Fatal("ordinary image processing no longer uses its quality-75 default")
	}
	if bytes.Equal(preview.Content[1].Data, readDefault.Content[1].Data) {
		t.Fatal("IM preview unexpectedly used ordinary read quality")
	}
}

func TestObservationPreviewKeepsTextResolutionWhenEncodedBudgetFits(t *testing.T) {
	canvas := image.NewRGBA(image.Rect(0, 0, 1600, 900))
	draw.Draw(canvas, canvas.Bounds(), image.White, image.Point{}, draw.Src)
	// A row of small high-contrast glyphs remains pixel-identical; this checks
	// resolution preservation, not an OCR or model recognition claim.
	for x := 16; x < 1560; x += 32 {
		for y, row := range []string{"00100", "01010", "10001", "11111", "10001", "10001", "10001"} {
			for column, pixel := range row {
				if pixel == '1' {
					draw.Draw(canvas, image.Rect(x+column*2, 16+y*2, x+column*2+2, 18+y*2), image.Black, image.Point{}, draw.Src)
				}
			}
		}
	}
	var source bytes.Buffer
	if err := png.Encode(&source, canvas); err != nil {
		t.Fatal(err)
	}
	if base64.StdEncoding.EncodedLen(source.Len()) > observationImageCompressAboveBytes {
		t.Fatal("text fixture does not fit the encoded preview budget")
	}
	result, err := processObservationImage(t.Context(), source.Bytes())
	if err != nil || result.IsError || len(result.Content) != 2 {
		t.Fatalf("prepare text preview: %+v, %v", result, err)
	}
	if frozen := observationPreviewBase64(t, result.Content[1]); frozen != base64.StdEncoding.EncodeToString(source.Bytes()) {
		t.Fatal("text pixels were unnecessarily recompressed or reduced by a total-pixel limit")
	}
	if bounds := observationPreviewPixels(t, result.Content[1]).Bounds(); bounds != canvas.Bounds() {
		t.Fatalf("text resolution changed: %v", bounds)
	}
}

func TestObservationImageKeepsOriginalDimensionsWhenEncodedBudgetFits(t *testing.T) {
	for _, test := range []struct {
		name                        string
		width, height, wantW, wantH int
	}{
		{"square", 1000, 1000, 1000, 1000},
		{"landscape", 1200, 800, 1200, 800},
		{"portrait", 800, 1200, 800, 1200},
		{"small", 200, 100, 200, 100},
		{"medium", 1024, 512, 1024, 512},
		{"narrow", 1, 100000, 1, 100000},
		{"wide", 100000, 1, 100000, 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			data := observationPNG(t, test.width, test.height)
			result, err := processObservationImage(context.Background(), data)
			if err != nil || result.IsError || len(result.Content) != 2 {
				t.Fatalf("prepare preview: %+v, %v", result, err)
			}
			bounds := observationPreviewPixels(t, result.Content[1]).Bounds()
			var frozen string
			if err := json.Unmarshal(result.Content[1].Data, &frozen); err != nil {
				t.Fatal(err)
			}
			if bounds.Dx() != test.wantW || bounds.Dy() != test.wantH || len(frozen) > observationImageCompressAboveBytes {
				t.Fatalf("preview=%v, want %dx%d; base64=%d limit=%d", bounds, test.wantW, test.wantH, len(frozen), observationImageCompressAboveBytes)
			}
			if frozen != base64.StdEncoding.EncodeToString(data) {
				t.Fatal("image that fit the encoded budget was unnecessarily re-encoded")
			}
		})
	}
}

func TestObservationImagePreservesTransparencyAndEXIFOrientation(t *testing.T) {
	result, err := processObservationImage(context.Background(), observationPNGWithCompression(t, 1200, 800, png.NoCompression))
	if err != nil || result.IsError {
		t.Fatalf("prepare transparent image: %+v %v", result, err)
	}
	if result.Content[1].MediaType != "image/webp" {
		t.Fatalf("transparent fixture did not exercise WebP encoding: %q", result.Content[1].MediaType)
	}
	pixels := observationPreviewPixels(t, result.Content[1])
	if _, _, _, alpha := pixels.At(0, 0).RGBA(); alpha != 0 {
		t.Fatal("preview lost its transparent background")
	}
	if _, _, _, alpha := pixels.At(pixels.Bounds().Dx()-1, pixels.Bounds().Dy()-1).RGBA(); alpha != 0xffff {
		t.Fatal("preview lost its opaque foreground")
	}

	var jpegData bytes.Buffer
	if err := jpeg.Encode(&jpegData, image.NewRGBA(image.Rect(0, 0, 1200, 800)), nil); err != nil {
		t.Fatal(err)
	}
	tiff := make([]byte, 26)
	copy(tiff, "II")
	binary.LittleEndian.PutUint16(tiff[2:], 42)
	binary.LittleEndian.PutUint32(tiff[4:], 8)
	binary.LittleEndian.PutUint16(tiff[8:], 1)
	binary.LittleEndian.PutUint16(tiff[10:], 0x112)
	binary.LittleEndian.PutUint16(tiff[12:], 3)
	binary.LittleEndian.PutUint32(tiff[14:], 1)
	binary.LittleEndian.PutUint16(tiff[18:], 6)
	data := append([]byte{0xff, 0xd8, 0xff, 0xe1, 0, 34}, []byte("Exif\x00\x00")...)
	data = append(data, tiff...)
	data = append(data, jpegData.Bytes()[2:]...)
	result, err = processObservationImage(context.Background(), data)
	if err != nil || result.IsError {
		t.Fatalf("prepare rotated image: %+v %v", result, err)
	}
	if bounds := observationPreviewPixels(t, result.Content[1]).Bounds(); bounds.Dx() != 800 || bounds.Dy() != 1200 {
		t.Fatalf("EXIF orientation was lost: %v", bounds)
	}
}

func TestIMPreviewIsFrozenBeforeMainAndProbeWithoutChangingOriginalDownload(t *testing.T) {
	bot := newBot(t)
	data := observationUncompressedPNG(t, 1200, 800)
	var probes atomic.Int64
	var frozenPreview atomic.Value
	p := testkit.New(t, 1, func(_ context.Context, _ int, request map[string]any) any {
		if !strings.Contains(string(encoded(request["messages"])), "compressed preview") {
			t.Error("model request omitted the IM preview explanation")
		}
		images := 0
		for _, raw := range request["messages"].([]any) {
			message, _ := raw.(map[string]any)
			content, _ := message["content"].([]any)
			for _, raw := range content {
				part, _ := raw.(map[string]any)
				if part["type"] != "image_url" {
					continue
				}
				images++
				value, _ := part["image_url"].(map[string]any)
				url, _ := value["url"].(string)
				_, encodedImage, ok := strings.Cut(url, ",")
				if !strings.HasPrefix(url, "data:image/webp;base64,") {
					t.Errorf("model received a recompressed image in an unexpected format: %q", strings.SplitN(url, ",", 2)[0])
				}
				if !frozenPreview.CompareAndSwap(nil, encodedImage) && frozenPreview.Load() != encodedImage {
					t.Error("main model and Probe received different frozen preview bytes")
				}
				payload, decodeErr := base64.StdEncoding.DecodeString(encodedImage)
				config, _, configErr := image.DecodeConfig(bytes.NewReader(payload))
				if !ok || decodeErr != nil || configErr != nil || config.Width != 1200 || config.Height != 800 {
					t.Errorf("model received an invalid or oversized preview: %+v, %v, %v", config, decodeErr, configErr)
				}
			}
		}
		if images != 1 {
			t.Errorf("model received %d images, expected one preview", images)
		}
		if strings.Contains(string(encoded(request["tools"])), `"name":"decide"`) {
			probes.Add(1)
			return testkit.Tool("decide", map[string]any{"participate": false})
		}
		return testkit.Text("received image preview")
	})
	s := New(p.Store, p.Harness)
	source := &stickerImageAdapter{data: data, expected: "original"}
	s.Registry["telegram.bot"] = func(c domain.Connection, token string, client *http.Client) (Adapter, error) {
		base, err := NewTelegram(c, token, client)
		source.Adapter = base
		return source, err
	}
	account := bot.connection(t, s, p.Agent.ID)
	b := createBinding(t, p, s, account, "-100", "")
	attachment := domain.Attachment{Ref: "10:0", ExternalID: "original", Kind: "image", MediaType: "image/png", Name: "original.png", Size: int64(len(data))}
	event := domain.Observation{SchemaVersion: 1, ID: "im-preview", MessageID: "10", ConnectionID: account.ID, ConversationID: b.ConversationID, Trigger: "mention", Attachments: []domain.Attachment{attachment}}
	if _, err := p.Store.Archive(p.Context, event); err != nil {
		t.Fatal(err)
	}
	if err := s.deliver(p.Context, b, event); err != nil {
		t.Fatal(err)
	}
	p.Idle(t, b.SessionID)
	view, err := p.Harness.InspectContext(p.Context, b.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, message := range view.Request.Messages {
		if message.ContextKind != "im.observation" {
			continue
		}
		found = true
		body := string(encoded(message))
		if !strings.Contains(body, "compressed preview") || !strings.Contains(body, "download the original") || !strings.Contains(body, "read the downloaded file") {
			t.Fatal("observation did not explain preview resolution and original retrieval")
		}
		var metadata messages.Metadata
		if err := json.Unmarshal(message.Metadata, &metadata); err != nil || len(metadata.Attachments) != 1 || metadata.Attachments[0] != attachment || len(metadata.Files) != 0 {
			t.Fatalf("original attachment metadata changed or created a Computer file: %+v, %v", metadata, err)
		}
		for _, part := range message.Content {
			if part.Type != "image" {
				continue
			}
			bounds := observationPreviewPixels(t, part).Bounds()
			if bounds.Dx() != 1200 || bounds.Dy() != 800 || part.URL != messages.AttachmentDownloadPath(b.ConversationID, "10", "10:0") {
				t.Fatalf("frozen preview dimensions or original reference changed: %v, %q", bounds, part.URL)
			}
			if part.MediaType != "image/webp" || frozenPreview.Load() != observationPreviewBase64(t, part) {
				t.Fatal("model request changed the admitted WebP preview")
			}
		}
	}
	if !found {
		t.Fatal("IM observation was not frozen into Core")
	}
	participate, err := s.shouldParticipate(p.Context, b, view)
	if err != nil || participate || probes.Load() != 1 || source.downloads.Load() != 1 {
		t.Fatalf("Probe did not reuse the frozen preview: participate=%v, probes=%d, downloads=%d, err=%v", participate, probes.Load(), source.downloads.Load(), err)
	}
	files, err := list[domain.FileRef](p.Context, p.Store, "files")
	if err != nil || len(files) != 0 {
		t.Fatal("IM preview admission created a Computer file")
	}
	caller, err := p.Hub.Bind(p.Context, p.Agent.ID, b.SessionID, p.Daemons[0].Credentials.ComputerID, "")
	if err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(caller.CWD, "explicit-original.png")
	if _, err := s.DownloadAttachment(p.Context, caller, b.ConversationID, event.ID, attachment.Ref, destination); err != nil {
		t.Fatal(err)
	}
	downloaded, err := os.ReadFile(destination)
	if err != nil || !bytes.Equal(downloaded, data) {
		t.Fatal("explicit download did not preserve original bytes:", err)
	}
	// The ordinary image path used by read keeps Core's normal resolution;
	// this bound belongs only to newly admitted IM observations.
	full, err := core.ProcessImage(p.Context, downloaded, core.ImageOptions{})
	if err != nil || full.IsError {
		t.Fatalf("process downloaded original: %+v %v", full, err)
	}
	if bounds := observationPreviewPixels(t, full.Content[1]).Bounds(); bounds.Dx() != 1200 || bounds.Dy() != 800 {
		t.Fatalf("ordinary image processing inherited IM preview limits: %v", bounds)
	}
	before := source.downloads.Load()
	source.fail = true
	if err := s.deliver(p.Context, b, event); err != nil || source.downloads.Load() != before {
		t.Fatal("replayed admission tried to regenerate committed preview:", err)
	}
}
