package channels

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/md5"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/oines/astral/internal/domain"
)

type weixinText struct {
	Text string `json:"text"`
}
type weixinMedia struct {
	Query       string `json:"encrypt_query_param,omitempty"`
	AESKey      string `json:"aes_key,omitempty"`
	EncryptType int    `json:"encrypt_type,omitempty"`
	URL         string `json:"full_url,omitempty"`
}
type weixinImage struct {
	Media *weixinMedia `json:"media,omitempty"`
	Key   string       `json:"aeskey,omitempty"`
	Size  int64        `json:"mid_size,omitempty"`
}
type weixinVoice struct {
	Media *weixinMedia `json:"media,omitempty"`
	Codec int          `json:"encode_type,omitempty"`
	Text  string       `json:"text,omitempty"`
}
type weixinFile struct {
	Media *weixinMedia `json:"media,omitempty"`
	Name  string       `json:"file_name,omitempty"`
	Size  weixinID     `json:"len,omitempty"`
	MD5   string       `json:"md5,omitempty"`
}
type weixinVideo struct {
	Media *weixinMedia `json:"media,omitempty"`
	Size  int64        `json:"video_size,omitempty"`
}
type weixinReference struct {
	ID    string      `json:"svr_id,omitempty"`
	Title string      `json:"title,omitempty"`
	Item  *weixinItem `json:"message_item,omitempty"`
}
type weixinItem struct {
	Type  int              `json:"type"`
	Text  *weixinText      `json:"text_item,omitempty"`
	Image *weixinImage     `json:"image_item,omitempty"`
	Voice *weixinVoice     `json:"voice_item,omitempty"`
	File  *weixinFile      `json:"file_item,omitempty"`
	Video *weixinVideo     `json:"video_item,omitempty"`
	Ref   *weixinReference `json:"ref_msg,omitempty"`
}

type weixinStoredMedia struct {
	Media weixinMedia `json:"media"`
	Key   string      `json:"key,omitempty"`
	MD5   string      `json:"md5,omitempty"`
}

func (w *weixinAdapter) normalize(ctx context.Context, message weixinMessage) (Event, error) {
	conversation := w.conversation(message.From)
	id := string(message.ID)
	if id == "" && message.ClientID != "" {
		id = "client:" + message.ClientID
	}
	if id == "" && message.Sequence != "" {
		id = "seq:" + string(message.Sequence)
	}
	observation := domain.Observation{SchemaVersion: 1, ID: stableID("event", w.connection.ID, id), ConnectionID: w.connection.ID, ConversationID: conversation.ID,
		ExternalEventID: id, MessageID: id, Kind: "message.created", Sender: domain.Sender{ID: message.From, Name: message.From},
		SentAt: time.UnixMilli(message.Created).UTC(), ReceivedAt: time.Now().UTC(), Trigger: "private", Self: message.From == w.credential.BotID}
	if observation.Self {
		observation.Trigger = ""
	}
	if id == "" {
		return Event{Conversation: conversation, Observation: observation}, nil
	}
	var texts []string
	for index, item := range message.Items {
		if item.Ref != nil && observation.ReplyID == "" {
			observation.ReplyID, observation.ReplyPreview = item.Ref.ID, item.Ref.Title
			if item.Ref.Item != nil && item.Ref.Item.Text != nil {
				observation.ReplyPreview = item.Ref.Item.Text.Text
			}
			if preview := []rune(observation.ReplyPreview); len(preview) > 500 {
				observation.ReplyPreview = string(preview[:500])
			}
		}
		if item.Type == 1 && item.Text != nil {
			texts = append(texts, item.Text.Text)
		}
		if item.Type == 3 && item.Voice != nil && item.Voice.Text != "" {
			texts = append(texts, item.Voice.Text)
		}
		attachment := domain.Attachment{Ref: id + ":" + strconv.Itoa(index)}
		var media *weixinMedia
		var key, checksum string
		switch {
		case item.Type == 2 && item.Image != nil:
			media, key = item.Image.Media, item.Image.Key
			attachment.Kind, attachment.Name, attachment.MediaType = "image", "image-"+id+".jpg", "image/jpeg"
		case item.Type == 3 && item.Voice != nil:
			media = item.Voice.Media
			attachment.Kind, attachment.Name, attachment.MediaType = "audio", "voice-"+id+".bin", "application/octet-stream"
			// Preserve unsupported codecs as their original bytes; do not claim
			// they are an Ogg or PCM file and do not silently run a transcoder.
			switch item.Voice.Codec {
			case 5:
				attachment.Name, attachment.MediaType = "voice-"+id+".amr", "audio/amr"
			case 6:
				attachment.Name, attachment.MediaType = "voice-"+id+".silk", "audio/silk"
			case 7:
				attachment.Name, attachment.MediaType = "voice-"+id+".mp3", "audio/mpeg"
			case 8:
				attachment.Name, attachment.MediaType = "voice-"+id+".ogg", "audio/ogg"
			}
		case item.Type == 4 && item.File != nil:
			media, checksum = item.File.Media, item.File.MD5
			attachment.Kind, attachment.Name = "file", path.Base(strings.ReplaceAll(item.File.Name, "\\", "/"))
			if attachment.Name == "" || attachment.Name == "." || attachment.Name == "/" {
				attachment.Name = "file-" + id
			}
			attachment.MediaType = mime.TypeByExtension(path.Ext(attachment.Name))
			if attachment.MediaType == "" {
				attachment.MediaType = "application/octet-stream"
			}
			if size, err := strconv.ParseInt(string(item.File.Size), 10, 64); err == nil && size >= 0 {
				attachment.Size = size
			}
		case item.Type == 5 && item.Video != nil:
			media = item.Video.Media
			attachment.Kind, attachment.Name, attachment.MediaType = "video", "video-"+id+".mp4", "video/mp4"
		}
		if media == nil || media.Query == "" && media.URL == "" {
			continue
		}
		// Non-image items without an encryption key are not downloadable under
		// the current iLink wire behavior. Images may explicitly be plaintext.
		if item.Type != 2 && media.AESKey == "" {
			continue
		}
		attachment.ExternalID = stableID("weixin_attachment", w.connection.ID, id, strconv.Itoa(index))
		if err := w.putWire(ctx, "media", attachment.ExternalID, string(encoded(weixinStoredMedia{Media: *media, Key: key, MD5: checksum}))); err != nil {
			return Event{}, err
		}
		observation.Attachments = append(observation.Attachments, attachment)
	}
	observation.Text = strings.Join(texts, "\n")
	return Event{Conversation: conversation, Observation: observation}, nil
}

func (w *weixinAdapter) mediaURL(full, endpoint string, query url.Values) (string, error) {
	if full == "" {
		full = w.config.CDNBaseURL + "/" + endpoint + "?" + query.Encode()
	}
	u, err := url.Parse(full)
	if err != nil || u.Host == "" || u.User != nil || u.Fragment != "" {
		return "", domain.Invalid("invalid Weixin CDN URL")
	}
	base, _ := url.Parse(w.config.CDNBaseURL)
	host := strings.ToLower(u.Hostname())
	ip := net.ParseIP(host)
	if u.Scheme != "https" && !(u.Scheme == "http" && (host == "localhost" || ip != nil && ip.IsLoopback())) {
		return "", domain.Invalid("Weixin CDN requires HTTPS")
	}
	if !strings.EqualFold(u.Host, base.Host) && !strings.HasSuffix(host, ".weixin.qq.com") {
		return "", domain.Invalid("Weixin returned an untrusted CDN host")
	}
	return u.String(), nil
}

func weixinDecodeKey(value, preferredHex string) ([]byte, error) {
	if preferredHex != "" {
		key, err := hex.DecodeString(preferredHex)
		if err != nil || len(key) != aes.BlockSize {
			return nil, domain.Invalid("Weixin media has an invalid AES key")
		}
		return key, nil
	}
	if value == "" {
		return nil, nil
	}
	key, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		return nil, domain.Invalid("Weixin media has an invalid AES key")
	}
	if len(key) == 32 {
		key, err = hex.DecodeString(string(key))
	}
	if err != nil || len(key) != aes.BlockSize {
		return nil, domain.Invalid("Weixin media has an invalid AES key")
	}
	return key, nil
}

func weixinEncrypt(plain, key []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	padding := aes.BlockSize - len(plain)%aes.BlockSize
	data := make([]byte, len(plain)+padding)
	copy(data, plain)
	for i := len(plain); i < len(data); i++ {
		data[i] = byte(padding)
	}
	for offset := 0; offset < len(data); offset += aes.BlockSize {
		block.Encrypt(data[offset:offset+aes.BlockSize], data[offset:offset+aes.BlockSize])
	}
	return data, nil
}

func weixinDecrypt(ciphertext, key []byte) ([]byte, error) {
	if len(ciphertext) == 0 || len(ciphertext)%aes.BlockSize != 0 {
		return nil, domain.Invalid("Weixin media ciphertext has an invalid length")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, domain.Invalid("Weixin media has an invalid AES key")
	}
	for offset := 0; offset < len(ciphertext); offset += aes.BlockSize {
		block.Decrypt(ciphertext[offset:offset+aes.BlockSize], ciphertext[offset:offset+aes.BlockSize])
	}
	padding := int(ciphertext[len(ciphertext)-1])
	if padding < 1 || padding > aes.BlockSize || len(ciphertext) < padding {
		return nil, domain.Invalid("Weixin media has invalid PKCS#7 padding")
	}
	for _, value := range ciphertext[len(ciphertext)-padding:] {
		if value != byte(padding) {
			return nil, domain.Invalid("Weixin media has invalid PKCS#7 padding")
		}
	}
	return ciphertext[:len(ciphertext)-padding], nil
}

func (w *weixinAdapter) Download(ctx context.Context, attachment domain.Attachment) (Download, error) {
	if attachment.Size > weixinMediaLimit {
		return Download{}, domain.Invalid("Weixin attachment exceeds Astral's 50 MiB transfer limit")
	}
	value, err := w.getWire(ctx, "media", attachment.ExternalID)
	if err != nil {
		return Download{}, err
	}
	var stored weixinStoredMedia
	if value == "" || json.Unmarshal([]byte(value), &stored) != nil {
		return Download{}, domain.Missing("Weixin attachment", attachment.ExternalID)
	}
	key, err := weixinDecodeKey(stored.Media.AESKey, stored.Key)
	if err != nil {
		return Download{}, err
	}
	endpoint, err := w.mediaURL(stored.Media.URL, "download", url.Values{"encrypted_query_param": {stored.Media.Query}})
	if err != nil {
		return Download{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return Download{}, domain.Invalid("invalid Weixin media request")
	}
	response, err := w.client.Do(request)
	if err != nil {
		return Download{}, domain.Offline("Weixin attachment download could not start")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return Download{}, domain.Offline(fmt.Sprintf("Weixin attachment download returned HTTP %d", response.StatusCode))
	}
	if response.ContentLength > weixinMediaLimit+aes.BlockSize {
		return Download{}, domain.Invalid("Weixin attachment exceeds Astral's transfer limit")
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, weixinMediaLimit+aes.BlockSize+1))
	if err != nil {
		return Download{}, domain.Offline("Weixin attachment download was interrupted")
	}
	if int64(len(data)) > weixinMediaLimit+aes.BlockSize {
		return Download{}, domain.Invalid("Weixin attachment exceeds Astral's transfer limit")
	}
	if key != nil {
		data, err = weixinDecrypt(data, key)
		if err != nil {
			return Download{}, err
		}
	}
	if int64(len(data)) > weixinMediaLimit {
		return Download{}, domain.Invalid("Weixin attachment exceeds Astral's transfer limit")
	}
	if stored.MD5 != "" {
		checksum := md5.Sum(data) // The iLink media protocol uses plaintext MD5.
		expected, decodeErr := hex.DecodeString(stored.MD5)
		if decodeErr != nil || subtle.ConstantTimeCompare(checksum[:], expected) != 1 {
			return Download{}, domain.Invalid("Weixin attachment checksum did not match")
		}
	}
	return Download{Body: io.NopCloser(bytes.NewReader(data)), Size: int64(len(data)), Name: attachment.Name}, nil
}

func (w *weixinAdapter) upload(ctx context.Context, send Send) (weixinItem, error) {
	if send.Size < 0 || send.Size > weixinMediaLimit {
		return weixinItem{}, domain.Invalid("Weixin file exceeds Astral's 50 MiB transfer limit")
	}
	// Closing a remote Computer stream unblocks ReadAll when the request is
	// cancelled, before its owner dispatches any chat message.
	stopClose := context.AfterFunc(ctx, func() { _ = send.File.Close() })
	defer stopClose()
	plain, err := io.ReadAll(io.LimitReader(send.File, weixinMediaLimit+1))
	if err != nil {
		return weixinItem{}, domain.Offline("Weixin source file could not be read")
	}
	if int64(len(plain)) > weixinMediaLimit {
		return weixinItem{}, domain.Invalid("Weixin file exceeds Astral's 50 MiB transfer limit")
	}
	key := make([]byte, aes.BlockSize)
	if _, err = rand.Read(key); err != nil {
		return weixinItem{}, domain.Offline("could not generate Weixin media key")
	}
	ciphertext, err := weixinEncrypt(plain, key)
	if err != nil {
		return weixinItem{}, err
	}
	mediaType := 3
	if strings.HasPrefix(send.MediaType, "image/") {
		mediaType = 1
	} else if strings.HasPrefix(send.MediaType, "video/") {
		mediaType = 2
	}
	fileKey := domain.ID("astral_weixin_file")
	checksum := md5.Sum(plain)
	var upload struct {
		URL   string `json:"upload_full_url"`
		Query string `json:"upload_param"`
	}
	err = w.call(ctx, "getuploadurl", map[string]any{"filekey": fileKey, "media_type": mediaType, "to_user_id": send.Target.ExternalID,
		"rawsize": len(plain), "rawfilemd5": hex.EncodeToString(checksum[:]), "filesize": len(ciphertext), "no_need_thumb": true, "aeskey": hex.EncodeToString(key)}, &upload)
	if err != nil {
		return weixinItem{}, err
	}
	if upload.URL == "" && upload.Query == "" {
		return weixinItem{}, domain.Offline("Weixin did not return a CDN upload URL")
	}
	endpoint, err := w.mediaURL(upload.URL, "upload", url.Values{"encrypted_query_param": {upload.Query}, "filekey": {fileKey}})
	if err != nil {
		return weixinItem{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(ciphertext))
	if err != nil {
		return weixinItem{}, domain.Invalid("invalid Weixin CDN upload URL")
	}
	request.Header.Set("Content-Type", "application/octet-stream")
	response, err := w.client.Do(request)
	if err != nil {
		return weixinItem{}, domain.Offline("Weixin CDN upload could not be completed")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return weixinItem{}, domain.Offline(fmt.Sprintf("Weixin CDN upload returned HTTP %d", response.StatusCode))
	}
	downloadParam := response.Header.Get("x-encrypted-param")
	if downloadParam == "" {
		return weixinItem{}, domain.Offline("Weixin CDN upload returned no download reference")
	}
	media := &weixinMedia{Query: downloadParam, AESKey: base64.StdEncoding.EncodeToString([]byte(hex.EncodeToString(key))), EncryptType: 1}
	switch mediaType {
	case 1:
		return weixinItem{Type: 2, Image: &weixinImage{Media: media, Size: int64(len(ciphertext))}}, nil
	case 2:
		return weixinItem{Type: 5, Video: &weixinVideo{Media: media, Size: int64(len(ciphertext))}}, nil
	default:
		name := path.Base(strings.ReplaceAll(send.FileName, "\\", "/"))
		if name == "" || name == "." || name == "/" {
			name = "attachment"
		}
		return weixinItem{Type: 4, File: &weixinFile{Media: media, Name: name, Size: weixinID(strconv.Itoa(len(plain)))}}, nil
	}
}
