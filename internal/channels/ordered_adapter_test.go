package channels

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/oines/astral/internal/domain"
)

func TestNapCatOrderedMentionNewlineImageAndTrailingText(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path != "/send_group_msg" {
			t.Errorf("unexpected route: %s", r.URL.Path)
		}
		var body struct {
			Message []napcatSegment `json:"message"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		parts := body.Message
		if len(parts) != 4 || parts[0].Type != "at" || parts[0].Data["qq"] != "123" || parts[1].Data["text"] != "\nResult\n" || parts[2].Type != "image" || parts[3].Data["text"] != "\nEnd" {
			t.Errorf("lost order or whitespace: %+v", parts)
			return
		}
		file, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(string(parts[2].Data["file"]), "base64://"))
		if err != nil || string(file) != "image" {
			t.Errorf("source bytes: %q %v", file, err)
		}
		fmt.Fprint(w, `{"status":"ok","retcode":0,"data":{"message_id":1234}}`)
	}))
	defer server.Close()
	parts := []SendPart{{Type: "mention", UserID: "123"}, {Type: "text", Text: "\nResult\n"}, {Type: "image", FileName: "chart.png", Size: 5, OpenFile: func(context.Context) (io.ReadCloser, error) { return io.NopCloser(strings.NewReader("image")), nil }}, {Type: "text", Text: "\nEnd"}}
	receipt, err := napcatTestAdapter(t, server).Send(context.Background(), Send{Target: domain.Conversation{ExternalID: "group:456"}, Parts: parts})
	if err != nil || len(receipt.MessageIDs) != 1 || requests != 1 {
		t.Fatalf("send: %+v, %v (%d requests)", receipt, err, requests)
	}
	_, err = napcatTestAdapter(t, server).Send(context.Background(), Send{Target: domain.Conversation{ExternalID: "private:456"}, Parts: parts})
	if err == nil || requests != 1 {
		t.Fatal("private chat mention was silently downgraded or sent")
	}
}

func TestFeishuOrderedPostUploadsImageAndPreservesMentionAndLines(t *testing.T) {
	imageUploaded, sent := false, false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/open-apis/auth/v3/tenant_access_token/internal":
			fmt.Fprint(w, `{"code":0,"tenant_access_token":"token","expire":7200}`)
		case "/open-apis/im/v1/images":
			if err := r.ParseMultipartForm(1 << 20); err != nil {
				t.Error(err)
				return
			}
			defer r.MultipartForm.RemoveAll()
			if r.FormValue("image_type") != "message" {
				t.Error("wrong image upload type")
			}
			file, _, err := r.FormFile("image")
			if err != nil {
				t.Error(err)
				return
			}
			defer file.Close()
			data, _ := io.ReadAll(file)
			if string(data) != "image" {
				t.Errorf("image source: %q", data)
			}
			imageUploaded = true
			fmt.Fprint(w, `{"code":0,"data":{"image_key":"img_v3_uploaded"}}`)
		case "/open-apis/im/v1/messages":
			if !imageUploaded {
				t.Error("post sent before media upload")
			}
			var body struct {
				MsgType string `json:"msg_type"`
				Content string `json:"content"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
				return
			}
			var content struct {
				ZhCN struct {
					Content [][]map[string]string `json:"content"`
				} `json:"zh_cn"`
			}
			if err := json.Unmarshal([]byte(body.Content), &content); err != nil {
				t.Error(err)
				return
			}
			rows := content.ZhCN.Content
			if len(rows) == 1 && len(rows[0]) == 1 && rows[0][0]["tag"] == "md" {
				if body.MsgType != "post" || rows[0][0]["text"] != "<at user_id=\"ou_alice\">&lt;Alice&gt;</at>\n**Result**\n\n\n![image](img_v3_uploaded)\n\n End" {
					t.Errorf("invalid Markdown post or lost formatting: %s", body.Content)
				}
			} else if body.MsgType != "post" || len(rows) != 4 || rows[0][0]["tag"] != "at" || rows[0][0]["user_id"] != "ou_alice" || rows[1][0]["text"] != "Result" || len(rows[2]) != 1 || rows[2][0]["tag"] != "img" || rows[2][0]["image_key"] != "img_v3_uploaded" || rows[3][0]["text"] != " End" {
				t.Errorf("post lost ordered content: %s", body.Content)
			}
			sent = true
			fmt.Fprint(w, `{"code":0,"data":{"message_id":"om_sent"}}`)
		default:
			t.Errorf("unexpected route: %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	a, err := NewFeishu(domain.Connection{Config: encoded(FeishuConfig{AppID: "cli_app", BaseURL: server.URL})}, "secret", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	parts := []SendPart{{Type: "mention", UserID: "ou_alice"}, {Type: "text", Text: "\nResult\n"}, {Type: "image", FileName: "chart.png", Size: 5, OpenFile: func(context.Context) (io.ReadCloser, error) { return io.NopCloser(strings.NewReader("image")), nil }}, {Type: "text", Text: " End"}}
	receipt, err := a.Send(context.Background(), Send{Target: domain.Conversation{ExternalID: "oc_chat"}, Parts: parts})
	if err != nil || !sent || len(receipt.MessageIDs) != 1 {
		t.Fatalf("send: %+v, %v", receipt, err)
	}
	parts[0].Name, parts[1].Text = "<Alice>", "\n**Result**\n"
	_, err = a.Send(context.Background(), Send{MessageOptions: domain.MessageOptions{Format: "markdown"}, Target: domain.Conversation{ExternalID: "oc_chat"}, Parts: parts})
	if err != nil {
		t.Fatalf("Markdown send: %v", err)
	}
}

func TestFeishuPostRejectsInvalidMentionBeforeOpeningMedia(t *testing.T) {
	f := &feishu{}
	opened := false
	_, err := f.partsContent(context.Background(), Send{Parts: []SendPart{
		{Type: "image", OpenFile: func(context.Context) (io.ReadCloser, error) {
			opened = true
			return io.NopCloser(strings.NewReader("image")), nil
		}},
		{Type: "mention", UserID: "all"},
	}})
	if err == nil || opened {
		t.Fatal("invalid post accessed media or accepted unsupported mention")
	}
}
