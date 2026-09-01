package httpapi

import (
	"errors"
	"net/http"
	"strings"

	"github.com/MengStar-L/sub2api5hlimit/internal/releasecheck"
	"github.com/MengStar-L/sub2api5hlimit/internal/store"
)

func (s *Server) updateStatus(w http.ResponseWriter, r *http.Request) {
	if s.updates == nil {
		writeError(w, http.StatusServiceUnavailable, "UPDATER_UNAVAILABLE", "当前安装未启用程序更新")
		return
	}
	view, err := s.updates.View(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "UPDATE_STATE_ERROR", "无法读取程序更新状态")
		return
	}
	writeData(w, http.StatusOK, view)
}

func (s *Server) checkUpdate(w http.ResponseWriter, r *http.Request) {
	if s.updates == nil {
		writeError(w, http.StatusServiceUnavailable, "UPDATER_UNAVAILABLE", "当前安装未启用程序更新")
		return
	}
	view, err := s.updates.Check(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "UPDATE_CHECK_ERROR", "无法保存程序更新检查结果")
		return
	}
	writeData(w, http.StatusOK, view)
}

func (s *Server) applyUpdate(w http.ResponseWriter, r *http.Request) {
	if s.updates == nil {
		writeError(w, http.StatusServiceUnavailable, "UPDATER_UNAVAILABLE", "当前安装未启用程序更新")
		return
	}
	var request struct {
		TargetVersion string `json:"target_version"`
	}
	if !decodeJSON(w, r, &request) {
		return
	}
	request.TargetVersion = strings.TrimSpace(request.TargetVersion)
	if request.TargetVersion == "" {
		writeError(w, http.StatusBadRequest, "TARGET_VERSION_REQUIRED", "请选择要安装的版本")
		return
	}
	s.maintenanceMu.Lock()
	defer s.maintenanceMu.Unlock()
	actorID := currentSession(r).User.ID
	if s.store != nil {
		job, err := s.store.CurrentQuotaResetJob(r.Context())
		if err == nil && (job.Status == store.QuotaResetJobQueued || job.Status == store.QuotaResetJobRunning) {
			writeError(w, http.StatusConflict, "QUOTA_RESET_BATCH_ACTIVE", "请等待批量额度重置完成后再更新程序")
			return
		}
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusInternalServerError, "QUOTA_RESET_STATE_ERROR", "无法确认额度重置任务状态")
			return
		}
	}
	result, err := s.updates.Apply(r.Context(), request.TargetVersion, actorID)
	if err != nil {
		switch {
		case errors.Is(err, releasecheck.ErrInvalidTarget):
			writeError(w, http.StatusConflict, "UPDATE_TARGET_CHANGED", "最新稳定版已经变化，请重新检查")
		case errors.Is(err, releasecheck.ErrUpdatePending):
			writeError(w, http.StatusConflict, "UPDATE_IN_PROGRESS", "已有程序更新正在执行")
		case errors.Is(err, releasecheck.ErrUpdateUnavailable):
			writeError(w, http.StatusConflict, "AUTOMATIC_UPDATE_UNAVAILABLE", "该版本需要按部署文档手动升级")
		default:
			writeError(w, http.StatusInternalServerError, "UPDATE_REQUEST_FAILED", "无法提交程序更新请求")
		}
		return
	}
	writeData(w, http.StatusAccepted, result)
}
