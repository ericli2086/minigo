package middlewares

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/andybalholm/brotli"
	"github.com/gin-gonic/gin"
)

type BrotliConfig struct {
	Quality   int
	MinLength int
	Types     []string
	// 新增配置项，用于控制是否跳过特定路径
	ExcludePaths []string
}

func DefaultBrotliConfig() *BrotliConfig {
	return &BrotliConfig{
		Quality:   4,
		MinLength: 1024,
		Types: []string{
			"text/plain",
			"text/html",
			"text/css",
			"application/json",
			"application/javascript",
			"application/x-javascript",
			"application/xml",
			"application/x-httpd-php",
			"application/x-yaml",
		},
		// 默认跳过相关路径
		ExcludePaths: []string{},
	}
}

type brotliWriter struct {
	gin.ResponseWriter
	config     *BrotliConfig
	buffer     *bytes.Buffer
	statusCode int
}

func (w *brotliWriter) WriteHeader(code int) {
	w.statusCode = code
}

func (w *brotliWriter) Write(data []byte) (int, error) {
	return w.buffer.Write(data)
}

func (w *brotliWriter) WriteString(s string) (int, error) {
	return w.buffer.WriteString(s)
}

func shouldSkipPath(path string, excludePaths []string) bool {
	for _, prefix := range excludePaths {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return false
}

func Brotli(config *BrotliConfig) gin.HandlerFunc {
	if config == nil {
		config = DefaultBrotliConfig()
	}

	return func(c *gin.Context) {
		// 跳过特定路径
		if shouldSkipPath(c.Request.URL.Path, config.ExcludePaths) {
			c.Next()
			return
		}

		// 检查客户端是否支持 br 压缩
		if !strings.Contains(c.Request.Header.Get("Accept-Encoding"), "br") {
			c.Next()
			return
		}

		writer := &brotliWriter{
			ResponseWriter: c.Writer,
			config:         config,
			buffer:         &bytes.Buffer{},
			statusCode:     200,
		}

		c.Writer = writer
		c.Next()

		// 检查是否需要压缩
		contentType := writer.Header().Get("Content-Type")
		shouldCompress := false
		for _, t := range config.Types {
			if strings.Contains(strings.ToLower(contentType), strings.ToLower(t)) {
				shouldCompress = true
				break
			}
		}

		data := writer.buffer.Bytes()
		compressedData := &bytes.Buffer{}

		if shouldCompress && len(data) >= config.MinLength {
			// 预压缩数据以获取压缩后的大小
			br := brotli.NewWriterLevel(compressedData, config.Quality)
			br.Write(data)
			br.Close()

			if compressedData.Len() > 0 {
				writer.Header().Set("Content-Encoding", "br")
				writer.Header().Set("Content-Length", fmt.Sprintf("%d", compressedData.Len()))
				writer.ResponseWriter.WriteHeader(writer.statusCode)
				writer.ResponseWriter.Write(compressedData.Bytes())
				return
			}
		}

		// 如果不压缩或压缩失败，写入原始数据
		writer.Header().Set("Content-Length", fmt.Sprintf("%d", len(data)))
		writer.ResponseWriter.WriteHeader(writer.statusCode)
		writer.ResponseWriter.Write(data)
	}
}
