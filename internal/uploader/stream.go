package uploader

import (
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// StreamFileHandler serves a file with HTTP Range request support,
// enabling native browser <video> and <audio> players to seek and buffer.
//
// GET /files/<bucket>/<key>/stream
//
// Behaviour:
//   - Always sets Accept-Ranges: bytes so browsers know seeking is possible.
//   - No Range header → 200 OK, full body.
//   - Range header present → 206 Partial Content, requested byte slice.
//   - Invalid / unsatisfiable range → 416 Range Not Satisfiable.
//   - Content-Disposition is "inline" so the browser renders the player
//     instead of triggering a file download.
func (a *App) StreamFileHandler(w http.ResponseWriter, r *http.Request, bucket, key string) {
	// Probe file metadata: total size + stored content-type.
	head, err := a.S3Client.HeadObject(r.Context(), &s3.HeadObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		if isObjectNotFound(err) {
			jsonError(w, "file not found", http.StatusNotFound)
			return
		}
		jsonError(w, fmt.Sprintf("failed to stat file: %v", err), http.StatusInternalServerError)
		return
	}

	totalSize := aws.ToInt64(head.ContentLength)

	contentType := "application/octet-stream"
	if head.ContentType != nil && *head.ContentType != "" {
		contentType = *head.ContentType
	}

	// These headers are always present, even for error responses that may
	// follow — headers written before WriteHeader are buffered and can be
	// overridden by jsonError.
	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", "inline")

	rangeHeader := r.Header.Get("Range")
	if rangeHeader == "" {
		// No Range header — serve complete file with 200 OK.
		a.serveFullStream(w, r, bucket, key, totalSize)
		return
	}

	// Validate and parse the byte range before touching S3.
	start, end, err := parseByteRange(rangeHeader, totalSize)
	if err != nil {
		// RFC 9110 §14.1.2: respond with 416 and a Content-Range indicating
		// the actual file length so the client can self-correct.
		w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", totalSize))
		w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
		return
	}

	// Fetch only the requested byte range from S3 using a server-side
	// range get — no need to download the entire object.
	rangeSpec := fmt.Sprintf("bytes=%d-%d", start, end)
	obj, err := a.S3Client.GetObject(r.Context(), &s3.GetObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
		Range:  aws.String(rangeSpec),
	})
	if err != nil {
		if isObjectNotFound(err) {
			jsonError(w, "file not found", http.StatusNotFound)
			return
		}
		jsonError(w, fmt.Sprintf("failed to retrieve file range: %v", err), http.StatusInternalServerError)
		return
	}
	defer obj.Body.Close()

	chunkSize := end - start + 1
	w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, totalSize))
	w.Header().Set("Content-Length", strconv.FormatInt(chunkSize, 10))
	w.WriteHeader(http.StatusPartialContent)
	io.Copy(w, obj.Body) //nolint:errcheck — client disconnect is expected
}

// serveFullStream fetches the complete object and writes it with 200 OK.
// Called only when the request carries no Range header.
func (a *App) serveFullStream(w http.ResponseWriter, r *http.Request, bucket, key string, totalSize int64) {
	obj, err := a.S3Client.GetObject(r.Context(), &s3.GetObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		if isObjectNotFound(err) {
			jsonError(w, "file not found", http.StatusNotFound)
			return
		}
		jsonError(w, fmt.Sprintf("failed to retrieve file: %v", err), http.StatusInternalServerError)
		return
	}
	defer obj.Body.Close()

	w.Header().Set("Content-Length", strconv.FormatInt(totalSize, 10))
	w.WriteHeader(http.StatusOK)
	io.Copy(w, obj.Body) //nolint:errcheck — client disconnect is expected
}

// parseByteRange parses a single "Range: bytes=<start>-<end>" header and
// returns the start and end byte positions (inclusive, zero-based).
//
// Supported forms (per RFC 9110 §14.1.2):
//
//	bytes=0-499          → bytes 0 through 499 inclusive
//	bytes=500-           → bytes 500 through EOF
//	bytes=-200           → last 200 bytes (suffix range)
//
// Multi-range requests (bytes=0-100,200-300) are intentionally not supported
// and return an error; callers should respond with 416.
func parseByteRange(header string, totalSize int64) (start, end int64, err error) {
	if !strings.HasPrefix(header, "bytes=") {
		return 0, 0, fmt.Errorf("unsupported range unit")
	}
	spec := strings.TrimPrefix(header, "bytes=")

	// Multi-range is out of scope — single-range only.
	if strings.Contains(spec, ",") {
		return 0, 0, fmt.Errorf("multi-range not supported")
	}

	parts := strings.SplitN(spec, "-", 2)
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("invalid range format")
	}
	startStr, endStr := parts[0], parts[1]

	if startStr == "" && endStr == "" {
		return 0, 0, fmt.Errorf("invalid range: both start and end are empty")
	}

	// Suffix range: bytes=-N → last N bytes.
	if startStr == "" {
		n, pErr := strconv.ParseInt(endStr, 10, 64)
		if pErr != nil || n <= 0 {
			return 0, 0, fmt.Errorf("invalid suffix range")
		}
		start = totalSize - n
		if start < 0 {
			start = 0
		}
		return start, totalSize - 1, nil
	}

	start, err = strconv.ParseInt(startStr, 10, 64)
	if err != nil || start < 0 {
		return 0, 0, fmt.Errorf("invalid range start")
	}
	if start >= totalSize {
		return 0, 0, fmt.Errorf("range start exceeds file size")
	}

	// Open-ended range: bytes=N- → N through EOF.
	if endStr == "" {
		return start, totalSize - 1, nil
	}

	end, err = strconv.ParseInt(endStr, 10, 64)
	if err != nil || end < start {
		return 0, 0, fmt.Errorf("invalid range end")
	}
	// Clamp to the last valid byte (RFC 9110 allows end to exceed file size).
	if end >= totalSize {
		end = totalSize - 1
	}
	return start, end, nil
}
