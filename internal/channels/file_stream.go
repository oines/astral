package channels

import (
	"bufio"
	"bytes"
	"crypto/aes"
	"encoding/base64"
	"io"

	"github.com/oines/astral/internal/domain"
)

// JSON-based protocols still carry file bytes as Base64 strings. Encode only
// the small metadata envelope up front and stream the file through that string.
type fileJSONBody struct {
	*io.PipeReader
	source io.ReadCloser
	length int64
}

func (r *fileJSONBody) Close() error {
	_ = r.PipeReader.Close()
	return r.source.Close()
}

func streamFileJSON(body any, placeholder string, source io.ReadCloser, size int64) *fileJSONBody {
	raw := encoded(body)
	index := bytes.Index(raw, []byte(placeholder))
	prefix, suffix := raw[:index], raw[index+len(placeholder):]
	reader, writer := io.Pipe()
	go func() {
		_, err := writer.Write(prefix)
		if err == nil {
			encoder := base64.NewEncoder(base64.StdEncoding, writer)
			_, err = io.Copy(encoder, source)
			if err == nil {
				err = encoder.Close()
			}
		}
		if err == nil {
			_, err = writer.Write(suffix)
		}
		_ = writer.CloseWithError(err)
	}()
	length := int64(-1)
	if size > 0 {
		length = int64(len(prefix)+len(suffix)) + int64(base64.StdEncoding.EncodedLen(int(size)))
	}
	return &fileJSONBody{PipeReader: reader, source: source, length: length}
}

func filePlaceholder() string { return domain.ID("file_body") }

type transformedStream struct {
	*io.PipeReader
	source io.ReadCloser
}

func (r *transformedStream) Close() error {
	_ = r.PipeReader.Close()
	return r.source.Close()
}

func transformStream(source io.ReadCloser, transform func(io.Writer, io.Reader) error) io.ReadCloser {
	reader, writer := io.Pipe()
	go func() {
		defer source.Close()
		out := bufio.NewWriterSize(writer, 32<<10)
		err := transform(out, bufio.NewReaderSize(source, 32<<10))
		if err == nil {
			err = out.Flush()
		}
		_ = writer.CloseWithError(err)
	}()
	return &transformedStream{PipeReader: reader, source: source}
}

func weixinEncryptStream(dst io.Writer, src io.Reader, key []byte) error {
	block, err := aes.NewCipher(key)
	if err != nil {
		return err
	}
	var data [aes.BlockSize]byte
	for {
		n, readErr := io.ReadFull(src, data[:])
		if readErr != nil && readErr != io.EOF && readErr != io.ErrUnexpectedEOF {
			return readErr
		}
		if n < len(data) {
			for i := n; i < len(data); i++ {
				data[i] = byte(len(data) - n)
			}
		}
		block.Encrypt(data[:], data[:])
		if _, err = dst.Write(data[:]); err != nil {
			return err
		}
		if readErr != nil {
			return nil
		}
	}
}

func weixinDecryptStream(dst io.Writer, src io.Reader, key []byte) error {
	block, err := aes.NewCipher(key)
	if err != nil {
		return err
	}
	var ciphertext, plain [aes.BlockSize]byte
	haveBlock := false
	for {
		n, readErr := io.ReadFull(src, ciphertext[:])
		if n == 0 && readErr == io.EOF {
			if !haveBlock {
				return domain.Invalid("Weixin media has invalid ciphertext")
			}
			padding := int(plain[len(plain)-1])
			if padding < 1 || padding > aes.BlockSize {
				return domain.Invalid("Weixin media has invalid PKCS#7 padding")
			}
			for _, value := range plain[len(plain)-padding:] {
				if value != byte(padding) {
					return domain.Invalid("Weixin media has invalid PKCS#7 padding")
				}
			}
			_, err = dst.Write(plain[:len(plain)-padding])
			return err
		}
		if readErr != nil {
			return readErr
		}
		if haveBlock {
			if _, err = dst.Write(plain[:]); err != nil {
				return err
			}
		}
		block.Decrypt(plain[:], ciphertext[:])
		haveBlock = true
	}
}
