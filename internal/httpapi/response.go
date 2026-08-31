package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/MengStar-L/sub2api5hlimit/internal/secure"
	"github.com/MengStar-L/sub2api5hlimit/internal/store"
)

type apiError struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func writeData(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	var body apiError
	body.Error.Code = code
	body.Error.Message = secure.Redact(message)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeStoreError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "NOT_FOUND", "记录不存在")
	case errors.Is(err, store.ErrUsernameExists):
		writeError(w, http.StatusConflict, "USERNAME_EXISTS", "用户名已存在")
	case errors.Is(err, store.ErrKeyBound):
		writeError(w, http.StatusConflict, "KEY_ALREADY_BOUND", "该 Key 已绑定其他用户")
	default:
		writeError(w, http.StatusInternalServerError, "STORE_ERROR", "数据操作失败")
	}
}

func decodeJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	if !strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "application/json") {
		writeError(w, http.StatusUnsupportedMediaType, "JSON_REQUIRED", "请求必须使用 application/json")
		return false
	}
	decoder := json.NewDecoder(io.LimitReader(r.Body, maxBody+1))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_JSON", "请求内容格式错误")
		return false
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "INVALID_JSON", "请求只能包含一个 JSON 对象")
		return false
	}
	return true
}
