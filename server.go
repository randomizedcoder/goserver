// Copyright (c) 2021-2023 Apple Inc. Licensed under MIT License.

package goserver

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"strconv"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

const (
	smallContentLength int64 = 1
	largeContentLength int64 = 8 * 1024 * 1024 * 1024
	chunkSize          int64 = 64 * 1024

	// ServiceType is the dns-sd service type for this service
	ServiceType = "_nq._tcp"

	quantileError    = 0.05
	summaryVecMaxAge = 5 * time.Minute
)

var (
	buffed []byte

	pC = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Subsystem: "counters",
			Name:      "networkQualityd",
			Help:      "networkQualityd counters",
		},
		[]string{"function", "variable", "type"},
	)
	pH = promauto.NewSummaryVec(
		prometheus.SummaryOpts{
			Subsystem: "histrograms",
			Name:      "networkQualityd",
			Help:      "networkQualityd historgrams",
			Objectives: map[float64]float64{
				0.1:  quantileError,
				0.5:  quantileError,
				0.9:  quantileError,
				0.99: quantileError,
			},
			MaxAge: summaryVecMaxAge,
		},
		[]string{"function", "variable", "type"},
	)
)

func init() {
	buffed = make([]byte, chunkSize)
	for i := range buffed {
		buffed[i] = 'x'
	}
}

// setCors makes it possible for wasm clients to connect to the server
// from a webclient that is not hosted on the same domain.
func setCors(h http.Header) {
	h.Set("Access-Control-Allow-Origin", "*")
	h.Set("Access-Control-Allow-Headers", "*")
}

type handlers struct {
	EnableCORS    bool
	BytesServed   *uint64
	BytesReceived *uint64
}

// BulkHandlers returns path, handler tuples with the provided prefix.
func BulkHandlers(prefix string, EnableCORS bool) map[string]http.HandlerFunc {
	h := &handlers{EnableCORS: EnableCORS}
	return map[string]http.HandlerFunc{
		prefix + "/small": h.smallHandler,
		prefix + "/large": h.largeHandler,
		prefix + "/slurp": h.slurpHandler,
	}
}

func CountingBulkHandlers(prefix string, EnableCORS bool, bytesServed, bytesReceived *uint64) map[string]http.HandlerFunc {
	h := &handlers{EnableCORS: EnableCORS, BytesServed: bytesServed, BytesReceived: bytesReceived}
	return map[string]http.HandlerFunc{
		prefix + "/small": h.smallHandler,
		prefix + "/large": h.largeHandler,
		prefix + "/slurp": h.slurpHandler,
	}
}

// A Server defines parameters for running a network quality server.
type Server struct {
	PublicPort     int
	PublicHostPort string
	ContextPath    string
	Scheme         string
	EnableCORS     bool
	EnableH3AltSvc bool
	BytesServed    uint64
	BytesReceived  uint64

	generatedConfig []byte
	once            sync.Once
}

func (m *Server) PrintStats() {
	startTime := time.Now()
	defer func() {
		pH.WithLabelValues("PrintStats", "complete", "count").Observe(time.Since(startTime).Seconds())
	}()
	pC.WithLabelValues("PrintStats", "start", "count").Inc()
	var lastBytesServed uint64
	var lastBytesReceived uint64
	for {
		var msg string
		x := atomic.LoadUint64(&m.BytesServed)
		y := atomic.LoadUint64(&m.BytesReceived)

		if x > lastBytesServed {
			dur := time.Second
			delta := x - lastBytesServed
			bps := float64(delta) / dur.Seconds()
			throughput := (bps / float64(1024*1024) * 8)

			msg += fmt.Sprintf("Sent: %0.2fMbps", throughput)
			lastBytesServed = x
		}

		if y > lastBytesReceived {
			dur := time.Second
			delta := y - lastBytesReceived
			bps := float64(delta) / dur.Seconds()
			throughput := (bps / float64(1024*1024) * 8)
			if len(msg) > 0 {
				msg += " "
			}
			msg += fmt.Sprintf("Received: %0.2fMbps", throughput)
			lastBytesReceived = y
		}

		if len(msg) > 0 {
			log.Printf("%s", msg)
		}
		time.Sleep(1 * time.Second)
	}
}

func (m *Server) generateConfig() {
	urls := struct {
		SmallDownloadURL      string `json:"small_download_url"`
		LargeDownloadURL      string `json:"large_download_url"`
		UploadURL             string `json:"upload_url"`
		SmallHTTPSDownloadURL string `json:"small_https_download_url"`
		LargeHTTPSDownloadURL string `json:"large_https_download_url"`
		HTTPSUploadURL        string `json:"https_upload_url"`
	}{
		SmallDownloadURL:      m.generateSmallDownloadURL(),
		LargeDownloadURL:      m.generateLargeDownloadURL(),
		UploadURL:             m.generateUploadURL(),
		SmallHTTPSDownloadURL: m.generateSmallDownloadURL(),
		LargeHTTPSDownloadURL: m.generateLargeDownloadURL(),
		HTTPSUploadURL:        m.generateUploadURL(),
	}

	resp := struct {
		Version int         `json:"version"`
		Urls    interface{} `json:"urls"`
	}{
		Version: 1,
		Urls:    urls,
	}

	b, err := json.MarshalIndent(resp, "", "    ")
	if err != nil {
		log.Fatal(err)
	}

	m.generatedConfig = b
}

func (m *Server) ConfigHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	m.once.Do(func() { m.generateConfig() })

	w.Header().Set("Content-Type", "application/json")
	if m.EnableH3AltSvc {
		w.Header().Set("Alt-Svc", fmt.Sprintf("h3=\":%d\"", m.PublicPort))
	}

	if m.EnableCORS {
		setCors(w.Header())
	}

	_, err := w.Write(m.generatedConfig)
	if err != nil {
		log.Printf("could not write response: %s", err)
	}
}

func (m *Server) generateSmallDownloadURL() string {
	return fmt.Sprintf("%s://%s%s/small", m.Scheme, m.PublicHostPort, m.ContextPath)
}

func (m *Server) generateLargeDownloadURL() string {
	return fmt.Sprintf("%s://%s%s/large", m.Scheme, m.PublicHostPort, m.ContextPath)
}

func (m *Server) generateUploadURL() string {
	return fmt.Sprintf("%s://%s%s/slurp", m.Scheme, m.PublicHostPort, m.ContextPath)
}

func (h *handlers) smallHandler(w http.ResponseWriter, r *http.Request) {
	startTime := time.Now()
	defer func() {
		pH.WithLabelValues("smallHandler", "complete", "count").Observe(time.Since(startTime).Seconds())
	}()
	pC.WithLabelValues("smallHandler", r.Method, "count").Inc()
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		pC.WithLabelValues("smallHandler", "StatusMethodNotAllowed", "count").Inc()
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	w.Header().Set("Content-Length", strconv.FormatInt(smallContentLength, 10))
	w.Header().Set("Content-Type", "application/octet-stream")

	if h.EnableCORS {
		setCors(w.Header())
	}

	if err := h.chunkedBodyWriter(w, smallContentLength); !ignorableError(err) {
		pC.WithLabelValues("smallHandler", "chunkedBodyWriter", "error").Inc()
		log.Printf("Error writing content of length %d: %s", smallContentLength, err)
	}
}

func (h *handlers) largeHandler(w http.ResponseWriter, r *http.Request) {

	startTime := time.Now()
	defer func() {
		pH.WithLabelValues("largeHandler", "complete", "count").Observe(time.Since(startTime).Seconds())
	}()
	pC.WithLabelValues("largeHandler", r.Method, "count").Inc()

	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		pC.WithLabelValues("largeHandler", "StatusMethodNotAllowed", "count").Inc()
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	w.Header().Set("Content-Length", strconv.FormatInt(largeContentLength, 10))
	w.Header().Set("Content-Type", "application/octet-stream")

	if h.EnableCORS {
		setCors(w.Header())
	}

	if r.Method != http.MethodGet {
		return
	}

	if err := h.chunkedBodyWriter(w, largeContentLength); !ignorableError(err) {
		pC.WithLabelValues("smallHandler", "chunkedBodyWriter", "error").Inc()
		log.Printf("Error writing content of length %d: %s", largeContentLength, err)
	}
}

func (h *handlers) chunkedBodyWriter(w http.ResponseWriter, contentLength int64) error {

	startTime := time.Now()
	defer func() {
		pH.WithLabelValues("chunkedBodyWriter", "complete", "count").Observe(time.Since(startTime).Seconds())
	}()
	pC.WithLabelValues("chunkedBodyWriter", "start", "count").Inc()

	w.WriteHeader(http.StatusOK)

	n := contentLength
	for n > 0 {
		if n >= chunkSize {
			n -= chunkSize
			atomic.AddUint64(h.BytesServed, uint64(chunkSize))
			pC.WithLabelValues("chunkedBodyWriter", "chunkSize", "count").Add(float64(chunkSize))

			if _, err := w.Write(buffed); err != nil {
				pC.WithLabelValues("chunkedBodyWriter", "chunkedWrite", "error").Inc()
				return err
			}
			continue
		}

		atomic.AddUint64(h.BytesServed, uint64(n))
		pC.WithLabelValues("chunkedBodyWriter", "n", "count").Add(float64(n))
		if _, err := w.Write(buffed[:n]); err != nil {
			pC.WithLabelValues("chunkedBodyWriter", "Write", "error").Inc()
			return err
		}
		break
	}

	return nil
}

// setNoPublicCache tells the proxy to cache the content and the user
// that it can't be cached. It requires the proxy cache to be configured
// to use the Proxy-Cache-Control header
func setNoPublicCache(h http.Header) {
	h.Set("Proxy-Cache-Control", "max-age=604800, public")
	h.Set("Cache-Control", "no-store, must-revalidate, private, max-age=0")
}

// slurpHandler reads the post request and returns JSON with bytes
// read and how long it took
func (h *handlers) slurpHandler(w http.ResponseWriter, r *http.Request) {

	startTime := time.Now()
	defer func() {
		pH.WithLabelValues("slurpHandler", "complete", "count").Observe(time.Since(startTime).Seconds())
	}()
	pC.WithLabelValues("slurpHandler", "start", "count").Inc()

	w.Header().Set("Content-Type", "application/octet-stream")
	setNoPublicCache(w.Header())

	if h.EnableCORS {
		setCors((w.Header()))
	}

	_, err := io.Copy(countingDiscard{byteCounter: h.BytesReceived}, r.Body)
	if err != nil {
		pC.WithLabelValues("slurpHandler", "Copy", "error").Inc()
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	w.WriteHeader(http.StatusOK)
}

// countingDiscard implements ReaderFrom as an optimization so Copy to
// io.Discard can avoid doing unnecessary work.
// Modified from go's io/io.go discard to count the number of bytes discarded
type countingDiscard struct {
	byteCounter *uint64
}

func (cb countingDiscard) Write(p []byte) (int, error) {
	x := len(p)
	return x, nil
}

func (cb countingDiscard) WriteString(s string) (int, error) {
	x := len(s)
	return x, nil
}

var blackHolePool = sync.Pool{
	New: func() any {
		b := make([]byte, 8192)
		return &b
	},
}

func (cb countingDiscard) ReadFrom(r io.Reader) (n int64, err error) {
	bufp := blackHolePool.Get().(*[]byte)
	var readSize int
	for {
		readSize, err = r.Read(*bufp)
		n += int64(readSize)
		atomic.AddUint64(cb.byteCounter, uint64(readSize))
		if err != nil {
			blackHolePool.Put(bufp)
			if err == io.EOF {
				return n, nil
			}
			return
		}
	}
}

// ignorableError returns true if error does not effect results of clients accessing server
func ignorableError(err error) bool {
	if err == nil {
		return true
	}
	if errors.Is(err, syscall.EPIPE) {
		return true
	}
	if errors.Is(err, syscall.ECONNRESET) {
		return true
	}

	switch err.Error() {
	case "client disconnected": // from http.http2errClientDisconnected
		return true
	}
	return false
}
