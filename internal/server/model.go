package server

import (
	"bufio"
	"bytes"
	"net/http"
)

type ResponseWriterProxy struct {
	writer *bufio.Writer
	buffer *bytes.Buffer

	StatusCode int
	header     http.Header
}

func NewResponseWriterProxy() *ResponseWriterProxy {
	buffer := bytes.NewBuffer(nil)
	return &ResponseWriterProxy{
		writer: bufio.NewWriter(buffer),
		buffer: buffer,
	}
}

func (w *ResponseWriterProxy) Write(b []byte) (int, error) {
	return w.writer.Write(b)
}

func (w *ResponseWriterProxy) Header() http.Header {
	if w.header == nil {
		w.header = http.Header{}
	}
	return w.header
}

func (w *ResponseWriterProxy) WriteHeader(statusCode int) {
	w.StatusCode = statusCode
}

func (w *ResponseWriterProxy) Flush() {
	_ = w.writer.Flush()
}

func (w *ResponseWriterProxy) IsError() bool {
	return w.StatusCode != 0 && w.StatusCode/100 != 2
}

func (w *ResponseWriterProxy) Body() []byte {
	w.Flush()
	return w.buffer.Bytes()
}
