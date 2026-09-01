package httpapi

import (
	"context"
	"net/http"

	"github.com/MengStar-L/sub2api5hlimit/internal/codexforecast"
	"github.com/MengStar-L/sub2api5hlimit/internal/store"
)

// CodexForecastProvider 由 codexforecast.Manager 实现；抽成接口便于测试替换。
type CodexForecastProvider interface {
	View(ctx context.Context) (store.CodexForecastState, error)
}

// GET /api/codex-forecast —— 用户与管理员都可读的第三方预测数值
func (s *Server) codexForecast(w http.ResponseWriter, r *http.Request) {
	if s.codex == nil {
		writeError(w, http.StatusServiceUnavailable, "CODEX_FORECAST_DISABLED", "预测数据源未启用")
		return
	}
	state, err := s.codex.View(r.Context())
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeData(w, http.StatusOK, map[string]any{
		"forecast":   state,
		"source_url": codexforecast.SourceURL,
		// 前端据此提示「预测值，不可全信」
		"disclaimer": "该数值由第三方站点依据公开信号推算，属于预测而非官方公告，仅供参考。",
	})
}
