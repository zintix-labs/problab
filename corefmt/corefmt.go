package corefmt

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"

	"github.com/zintix-labs/problab/errs"
)

func EncodeBase64(b []byte) string {
	return base64.StdEncoding.EncodeToString(b)
}

func DecodeBase64(s string) ([]byte, error) {
	fmt.Println(s)
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil, errs.Wrap(err, "decode base64 failed")
	}
	return b, err
}

func EncodeBase64URL(b []byte) string {
	return base64.RawURLEncoding.EncodeToString(b)
}

func DecodeBase64URL(s string) ([]byte, error) {
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return nil, errs.Wrap(err, "decode base64url failed")
	}
	return b, err
}

func EncodeHex(b []byte) string {
	return hex.EncodeToString(b)
}

func DecodeHex(s string) ([]byte, error) {
	b, err := hex.DecodeString(s)
	if err != nil {
		return nil, errs.Wrap(err, "decode hex failed")
	}
	return b, err
}

// EncodeBlobFrame encodes raw bytes into a length-prefixed binary frame.
//
// This is the most "BLOB-native" transport for files/streams:
//
//	frame := uvarint(len(payload)) || payload
//
// Notes:
//   - This format is NOT JSON-friendly. If you need JSON/HTTP text transport, use Base64/Base64URL.
//   - The length prefix uses unsigned varint (encoding/binary).
func EncodeBlobFrame(payload []byte) []byte {
	var hdr [binary.MaxVarintLen64]byte
	n := binary.PutUvarint(hdr[:], uint64(len(payload)))

	out := make([]byte, 0, n+len(payload))
	out = append(out, hdr[:n]...)
	out = append(out, payload...)
	return out
}

// DecodeBlobFrame decodes a length-prefixed binary frame produced by EncodeBlobFrame.
// It returns an error if the frame is malformed or truncated.
func DecodeBlobFrame(frame []byte) ([]byte, error) {
	n, size := binary.Uvarint(frame)
	if size <= 0 {
		return nil, errs.NewWarn("decode blob frame failed: invalid varint length")
	}
	if uint64(len(frame)-size) < n {
		return nil, errs.NewWarn("decode blob frame failed: truncated payload")
	}
	payload := frame[size : size+int(n)]
	// Return a copy to avoid retaining the entire frame backing array.
	out := make([]byte, len(payload))
	copy(out, payload)
	return out, nil
}

// WriteBlobFrame writes a length-prefixed binary frame into w.
//
// This is useful for writing snapshots to disk or piping them through a binary channel.
func WriteBlobFrame(w io.Writer, payload []byte) error {
	var hdr [binary.MaxVarintLen64]byte
	n := binary.PutUvarint(hdr[:], uint64(len(payload)))
	if _, err := w.Write(hdr[:n]); err != nil {
		return errs.Wrap(err, "write blob frame header failed")
	}
	if _, err := w.Write(payload); err != nil {
		return errs.Wrap(err, "write blob frame payload failed")
	}
	return nil
}

// ReadBlobFrame reads a length-prefixed binary frame from r.
//
// maxBytes is a safety cap to prevent unbounded allocations when reading untrusted input.
// If you read only trusted local files, you can pass a large maxBytes.
func ReadBlobFrame(r io.Reader, maxBytes uint64) ([]byte, error) {
	br := bufio.NewReader(r)
	ln, err := binary.ReadUvarint(br)
	if err != nil {
		return nil, errs.Wrap(err, "read blob frame header failed")
	}
	if maxBytes > 0 && ln > maxBytes {
		return nil, errs.NewWarn("read blob frame failed: payload exceeds maxBytes")
	}
	buf := make([]byte, ln)
	if _, err := io.ReadFull(br, buf); err != nil {
		return nil, errs.Wrap(err, "read blob frame payload failed")
	}
	return buf, nil
}

// EncodeBlob is an explicit "no-op" helper for cases where you want to document intent:
// when your storage/transport supports binary (DB BLOB/BYTEA, file, application/octet-stream),
// you should store the snapshot as-is.
func EncodeBlob(b []byte) []byte {
	if b == nil {
		return nil
	}
	out := make([]byte, len(b))
	copy(out, b)
	return out
}

// DecodeBlob is the counterpart of EncodeBlob.
func DecodeBlob(b []byte) []byte {
	return EncodeBlob(b)
}

// EncodeJSONBytes is a convenience wrapper to make it explicit that JSON transport must be text-safe.
// Note: Go's encoding/json already marshals []byte as standard Base64, but this keeps your code explicit.
func EncodeJSONBytes(b []byte) string {
	return EncodeBase64(b)
}

// DecodeJSONBytes is the counterpart of EncodeJSONBytes.
func DecodeJSONBytes(s string) ([]byte, error) {
	return DecodeBase64(s)
}

// EncodeTextBytes is a best-effort helper for logs/debugging where you want stable, copyable text.
// Hex is larger than Base64 but is very human-friendly.
func EncodeTextBytes(b []byte) string {
	return EncodeHex(b)
}

// DecodeTextBytes decodes text produced by EncodeTextBytes.
func DecodeTextBytes(s string) ([]byte, error) {
	return DecodeHex(s)
}

// EncodeBlobFrameToBytes is a small helper to build a frame into a bytes.Buffer.
// Kept here to avoid repeated boilerplate in callers.
func EncodeBlobFrameToBytes(payload []byte) *bytes.Buffer {
	buf := bytes.NewBuffer(make([]byte, 0, binary.MaxVarintLen64+len(payload)))
	_ = WriteBlobFrame(buf, payload)
	return buf
}
