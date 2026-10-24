package s3api

import (
	"encoding/xml"
	"net/http"
)

type apiError struct {
	Status  int
	Code    string
	Message string
}

func (e *apiError) Error() string { return e.Code + ": " + e.Message }

func newErr(status int, code, msg string) *apiError { return &apiError{status, code, msg} }

// statusFor maps S3 error codes produced elsewhere (s3auth) to HTTP statuses.
func statusFor(code string) int {
	switch code {
	case "AccessDenied", "SignatureDoesNotMatch", "InvalidAccessKeyId", "RequestTimeTooSkewed":
		return http.StatusForbidden
	case "NoSuchKey", "NoSuchUpload", "NoSuchBucket":
		return http.StatusNotFound
	case "SlowDown":
		return http.StatusServiceUnavailable
	case "InternalError":
		return http.StatusInternalServerError
	case "NotImplemented":
		return http.StatusNotImplemented
	case "MethodNotAllowed":
		return http.StatusMethodNotAllowed
	case "OperationAborted":
		return http.StatusConflict
	}
	return http.StatusBadRequest
}

type errorBody struct {
	XMLName   xml.Name `xml:"Error"`
	Code      string
	Message   string
	Resource  string
	RequestID string `xml:"RequestId"`
}

func writeError(w http.ResponseWriter, r *http.Request, reqID string, e *apiError) {
	w.Header().Set("x-amz-request-id", reqID)
	if e.Status == http.StatusServiceUnavailable {
		w.Header().Set("Retry-After", "30")
	}
	if r.Method == http.MethodHead {
		w.WriteHeader(e.Status)
		return
	}
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(e.Status)
	writeXML(w, errorBody{Code: e.Code, Message: e.Message, Resource: r.URL.Path, RequestID: reqID})
}

func writeXML(w http.ResponseWriter, v any) {
	_, _ = w.Write([]byte(xml.Header))
	_ = xml.NewEncoder(w).Encode(v)
}
