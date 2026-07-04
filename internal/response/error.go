package response

import (
	"context"
	"encoding/json"
	"net/http"
	"time"
)

// requestIDContextKey 用于在 context 中存储 request_id。
type requestIDContextKey struct{}

var requestIDKey = requestIDContextKey{}

// RequestIDFromContext 从 context 中提取 request_id。
func RequestIDFromContext(r *http.Request) string {
	if id, ok := r.Context().Value(requestIDKey).(string); ok && id != "" {
		return id
	}
	return "-"
}

// SetRequestID 将 request_id 注入 context 和响应 Header。
func SetRequestID(r *http.Request, id string) *http.Request {
	ctx := context.WithValue(r.Context(), requestIDKey, id)
	return r.WithContext(ctx)
}

// WriteErrorJSON 按 SPEC 第 5.2 节格式写入统一的 JSON 错误响应。
// 自动从 request context 中提取 request_id。
func WriteErrorJSON(w http.ResponseWriter, r *http.Request, statusCode int, errorCode, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(statusCode)

	resp := map[string]any{
		"error": map[string]any{
			"code":    errorCode,
			"message": message,
		},
		"request_id": RequestIDFromContext(r),
		"timestamp":  time.Now().UTC().Format(time.RFC3339),
	}

	json.NewEncoder(w).Encode(resp)
}
