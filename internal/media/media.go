package media

import (
	"bytes"
	"compress/zlib"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Supported media types.
const (
	MimePDF  = "application/pdf"
	MimePNG  = "image/png"
	MimeJPEG = "image/jpeg"
	MimeGIF  = "image/gif"
	MimeWEBP = "image/webp"
)

// MaxAttachmentBytes caps how large a file we'll read into memory as a data URI (25MB).
const MaxAttachmentBytes = 25 * 1024 * 1024

// DetectMime returns the MIME type and whether it is a supported media type (image or PDF).
func DetectMime(data []byte, filename string) (mimeType string, isSupported bool) {
	if len(data) >= 4 {
		switch {
		case bytes.HasPrefix(data, []byte("%PDF")):
			return MimePDF, true
		case bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")):
			return MimePNG, true
		case bytes.HasPrefix(data, []byte("\xff\xd8\xff")):
			return MimeJPEG, true
		case bytes.HasPrefix(data, []byte("GIF87a")) || bytes.HasPrefix(data, []byte("GIF89a")):
			return MimeGIF, true
		case len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP":
			return MimeWEBP, true
		}
	}

	ext := strings.ToLower(filepath.Ext(filename))
	switch ext {
	case ".pdf":
		return MimePDF, true
	case ".png":
		return MimePNG, true
	case ".jpg", ".jpeg":
		return MimeJPEG, true
	case ".gif":
		return MimeGIF, true
	case ".webp":
		return MimeWEBP, true
	default:
		return "", false
	}
}

// IsImage returns whether the mime type is a supported image.
func IsImage(mime string) bool {
	return strings.HasPrefix(mime, "image/")
}

// IsPDF returns whether the mime type is PDF.
func IsPDF(mime string) bool {
	return mime == MimePDF
}

// DataURIToMimeAndPayload parses "data:<mime>;base64,<payload>".
func DataURIToMimeAndPayload(dataURI string) (mimeType string, payload string, ok bool) {
	const prefix = "data:"
	if !strings.HasPrefix(dataURI, prefix) {
		return "", "", false
	}
	rest := dataURI[len(prefix):]
	comma := strings.IndexByte(rest, ',')
	if comma < 0 {
		return "", "", false
	}
	meta, data := rest[:comma], rest[comma+1:]
	meta, isBase64 := strings.CutSuffix(meta, ";base64")
	if !isBase64 || meta == "" || data == "" {
		return "", "", false
	}
	return meta, data, true
}

// BuildDataURI creates "data:<mime>;base64,<encoded>".
func BuildDataURI(mimeType string, raw []byte) string {
	return fmt.Sprintf("data:%s;base64,%s", mimeType, base64.StdEncoding.EncodeToString(raw))
}

// FileToDataURI reads a local file and encodes it to a Data URI, returning mime, name, dataURI.
func FileToDataURI(path string) (mimeType string, dataURI string, raw []byte, err error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", "", nil, err
	}
	if info.Size() > MaxAttachmentBytes {
		return "", "", nil, fmt.Errorf("file size %d exceeds limit of %d bytes", info.Size(), MaxAttachmentBytes)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", "", nil, err
	}
	mime, supported := DetectMime(data, filepath.Base(path))
	if !supported {
		return "", "", nil, fmt.Errorf("unsupported file format %q", filepath.Ext(path))
	}
	return mime, BuildDataURI(mime, data), data, nil
}

// StageAttachment saves raw bytes or a copied file into <dir>/.fastllm/attachments/<filename>.
func StageAttachment(workspaceDir, nameHint string, raw []byte) (string, error) {
	targetDir := filepath.Join(workspaceDir, ".fastllm", "attachments")
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return "", err
	}

	ext := filepath.Ext(nameHint)
	base := strings.TrimSuffix(filepath.Base(nameHint), ext)
	if base == "" || base == "." {
		base = "attachment"
	}
	if ext == "" {
		mime, _ := DetectMime(raw, "")
		switch mime {
		case MimePNG:
			ext = ".png"
		case MimeJPEG:
			ext = ".jpg"
		case MimePDF:
			ext = ".pdf"
		case MimeGIF:
			ext = ".gif"
		case MimeWEBP:
			ext = ".webp"
		default:
			ext = ".bin"
		}
	}

	ts := time.Now().Format("20060102-150405")
	fileName := fmt.Sprintf("%s-%s%s", base, ts, ext)
	destPath := filepath.Join(targetDir, fileName)

	if err := os.WriteFile(destPath, raw, 0o644); err != nil {
		return "", err
	}
	return destPath, nil
}

// ExtractPDFText extracts text content from raw PDF bytes.
// It parses content streams (including zlib FlateDecode streams) and extracts
// strings from text operators (Tj, TJ, ', \").
func ExtractPDFText(pdfBytes []byte) (string, error) {
	if !bytes.HasPrefix(pdfBytes, []byte("%PDF")) {
		return "", fmt.Errorf("not a valid PDF document")
	}

	streams := extractStreams(pdfBytes)
	var sb strings.Builder

	for _, stream := range streams {
		text := parseContentStream(stream)
		if strings.TrimSpace(text) != "" {
			if sb.Len() > 0 {
				sb.WriteString("\n\n")
			}
			sb.WriteString(text)
		}
	}

	result := strings.TrimSpace(sb.String())
	if result == "" {
		// Fallback: search for literal text blocks between BT and ET directly in pdfBytes
		result = strings.TrimSpace(extractDirectBTET(pdfBytes))
	}

	if result == "" {
		return "[PDF document: no extractable text found, or document contains scanned images only]", nil
	}
	return result, nil
}

// extractStreams locates all stream ... endstream blocks, decompressing FlateDecode if needed.
func extractStreams(pdfBytes []byte) [][]byte {
	var streams [][]byte
	pos := 0

	for {
		streamIdx := bytes.Index(pdfBytes[pos:], []byte("stream"))
		if streamIdx < 0 {
			break
		}
		actualStreamStart := pos + streamIdx + 6
		if actualStreamStart < len(pdfBytes) && pdfBytes[actualStreamStart] == '\r' {
			actualStreamStart++
		}
		if actualStreamStart < len(pdfBytes) && pdfBytes[actualStreamStart] == '\n' {
			actualStreamStart++
		}

		endIdx := bytes.Index(pdfBytes[actualStreamStart:], []byte("endstream"))
		if endIdx < 0 {
			break
		}
		streamData := pdfBytes[actualStreamStart : actualStreamStart+endIdx]
		pos = actualStreamStart + endIdx + 9

		// Try decompressing with zlib
		if zr, err := zlib.NewReader(bytes.NewReader(streamData)); err == nil {
			var decompressed bytes.Buffer
			if _, err := io.Copy(&decompressed, zr); err == nil {
				_ = zr.Close()
				streams = append(streams, decompressed.Bytes())
				continue
			}
			_ = zr.Close()
		}

		// If not compressed, store raw stream
		streams = append(streams, streamData)
	}

	return streams
}

// parseContentStream parses a PDF content stream between BT and ET and extracts text.
func parseContentStream(stream []byte) string {
	var out strings.Builder
	pos := 0

	for {
		btIdx := bytes.Index(stream[pos:], []byte("BT"))
		if btIdx < 0 {
			break
		}
		start := pos + btIdx + 2
		etIdx := bytes.Index(stream[start:], []byte("ET"))
		if etIdx < 0 {
			break
		}
		textBlock := stream[start : start+etIdx]
		pos = start + etIdx + 2

		extracted := extractTextFromBlock(textBlock)
		if extracted != "" {
			if out.Len() > 0 {
				out.WriteString("\n")
			}
			out.WriteString(extracted)
		}
	}

	return out.String()
}

func extractDirectBTET(data []byte) string {
	var out strings.Builder
	pos := 0

	for {
		btIdx := bytes.Index(data[pos:], []byte("BT"))
		if btIdx < 0 {
			break
		}
		start := pos + btIdx + 2
		etIdx := bytes.Index(data[start:], []byte("ET"))
		if etIdx < 0 {
			break
		}
		textBlock := data[start : start+etIdx]
		pos = start + etIdx + 2

		extracted := extractTextFromBlock(textBlock)
		if extracted != "" {
			if out.Len() > 0 {
				out.WriteString("\n")
			}
			out.WriteString(extracted)
		}
	}
	return out.String()
}

var tjRegex = regexp.MustCompile(`\((.*?)\)\s*Tj`)
var singleQuoteRegex = regexp.MustCompile(`\((.*?)\)\s*'`)
var doubleQuoteRegex = regexp.MustCompile(`\((.*?)\)\s*"`)
var arrayTjRegex = regexp.MustCompile(`\[(.*?)\]\s*TJ`)

func extractTextFromBlock(block []byte) string {
	var line strings.Builder
	str := string(block)

	// Array TJ: [(Hello) -10 (World)] TJ
	matchesTJ := arrayTjRegex.FindAllStringSubmatch(str, -1)
	for _, m := range matchesTJ {
		inner := m[1]
		// extract all (...) inside inner
		subMatches := regexp.MustCompile(`\((.*?)\)`).FindAllStringSubmatch(inner, -1)
		for _, sm := range subMatches {
			line.WriteString(unescapePDFString(sm[1]))
		}
		line.WriteString(" ")
	}

	// Single Tj: (Hello World) Tj
	matchesTj := tjRegex.FindAllStringSubmatch(str, -1)
	for _, m := range matchesTj {
		line.WriteString(unescapePDFString(m[1]))
		line.WriteString(" ")
	}

	// ' operator (move to next line and show text)
	for _, m := range singleQuoteRegex.FindAllStringSubmatch(str, -1) {
		line.WriteString("\n")
		line.WriteString(unescapePDFString(m[1]))
	}

	// " operator
	for _, m := range doubleQuoteRegex.FindAllStringSubmatch(str, -1) {
		line.WriteString("\n")
		line.WriteString(unescapePDFString(m[1]))
	}

	return strings.TrimSpace(line.String())
}

func unescapePDFString(s string) string {
	var res strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			i++
			switch s[i] {
			case 'n':
				res.WriteByte('\n')
			case 'r':
				res.WriteByte('\r')
			case 't':
				res.WriteByte('\t')
			case 'b':
				res.WriteByte('\b')
			case 'f':
				res.WriteByte('\f')
			case '(', ')', '\\':
				res.WriteByte(s[i])
			default:
				// Octal escape \ddd
				if s[i] >= '0' && s[i] <= '7' {
					oct := string(s[i])
					if i+1 < len(s) && s[i+1] >= '0' && s[i+1] <= '7' {
						i++
						oct += string(s[i])
						if i+1 < len(s) && s[i+1] >= '0' && s[i+1] <= '7' {
							i++
							oct += string(s[i])
						}
					}
					if val, err := strconv.ParseInt(oct, 8, 32); err == nil {
						res.WriteByte(byte(val))
					}
				} else {
					res.WriteByte(s[i])
				}
			}
		} else {
			res.WriteByte(s[i])
		}
	}
	return res.String()
}
