package channels

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"image"
	"image/color"
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
	canvas := image.NewNRGBA(image.Rect(0, 0, width, height))
	for y := height / 2; y < height; y++ {
		for x := width / 2; x < width; x++ {
			canvas.SetNRGBA(x, y, color.NRGBA{R: 230, A: 255})
		}
	}
	var data bytes.Buffer
	if err := png.Encode(&data, canvas); err != nil {
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

func TestObservationImageLimitsAreaWithoutUpscaling(t *testing.T) {
	for _, test := range []struct {
		name                        string
		width, height, wantW, wantH int
	}{
		{"square", 1000, 1000, 273, 273},
		{"landscape", 1200, 800, 335, 223},
		{"portrait", 800, 1200, 223, 335},
		{"small", 200, 100, 200, 100},
		{"at-limit", 300, 250, 300, 250},
		{"narrow", 1, 100000, 1, 75000},
		{"wide", 100000, 1, 75000, 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			data := observationPNG(t, test.width, test.height)
			result, err := processObservationImage(context.Background(), data)
			if err != nil || result.IsError || len(result.Content) != 2 {
				t.Fatalf("prepare preview: %+v, %v", result, err)
			}
			bounds := observationPreviewPixels(t, result.Content[1]).Bounds()
			if bounds.Dx() != test.wantW || bounds.Dy() != test.wantH || bounds.Dx()*bounds.Dy() > observationImageMaxPixels {
				t.Fatalf("preview=%v, want %dx%d within %d pixels", bounds, test.wantW, test.wantH, observationImageMaxPixels)
			}
			if test.width*test.height <= observationImageMaxPixels {
				var frozen string
				_ = json.Unmarshal(result.Content[1].Data, &frozen)
				if frozen != base64.StdEncoding.EncodeToString(data) {
					t.Fatal("small image was unnecessarily re-encoded")
				}
			}
		})
	}
}

func TestObservationImagePreservesTransparencyAndEXIFOrientation(t *testing.T) {
	result, err := processObservationImage(context.Background(), observationPNG(t, 1200, 800))
	if err != nil || result.IsError {
		t.Fatalf("prepare transparent image: %+v %v", result, err)
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
	if bounds := observationPreviewPixels(t, result.Content[1]).Bounds(); bounds.Dx() != 223 || bounds.Dy() != 335 {
		t.Fatalf("EXIF orientation was lost: %v", bounds)
	}
}

func TestIMPreviewIsFrozenBeforeMainAndProbeWithoutChangingOriginalDownload(t *testing.T) {
	bot := newBot(t)
	data := observationPNG(t, 1200, 800)
	var probes atomic.Int64
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
				payload, decodeErr := base64.StdEncoding.DecodeString(encodedImage)
				config, _, configErr := image.DecodeConfig(bytes.NewReader(payload))
				if !ok || decodeErr != nil || configErr != nil || config.Width*config.Height > observationImageMaxPixels {
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
			if bounds.Dx() != 335 || bounds.Dy() != 223 || part.URL != messages.AttachmentDownloadPath(b.ConversationID, "10", "10:0") {
				t.Fatalf("frozen preview dimensions or original reference changed: %v, %q", bounds, part.URL)
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
