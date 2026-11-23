package middleware

import "net/http"

// Unwrap lets http.ResponseController set deadlines on the actual connection.
// Without it, the error-body capture middleware hides Gin's underlying writer,
// so a slow SSE client could block a handler despite its query being cancelled.
func (w *respCaptureWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
