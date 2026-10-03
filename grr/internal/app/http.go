package app

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/automotive/grr/internal/msa"
	"github.com/automotive/grr/internal/store"
	"github.com/gin-gonic/gin"
)

// Handler exposes the HTTP API.
type Handler struct {
	svc *Service
}

func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

// Register wires the routes.
func (h *Handler) Register(r gin.IRouter) {
	api := r.Group("/api/v1")

	api.GET("/health", h.health)

	api.POST("/studies", h.createStudy)
	api.GET("/studies", h.listStudies)
	api.GET("/studies/:id", h.getStudy)
	api.PUT("/studies/:id", h.updateStudy)

	api.GET("/studies/:id/versions", h.listVersions)
	api.GET("/studies/:id/draft", h.currentDraft)
	api.POST("/studies/:id/versions", h.newVersion)
	api.POST("/versions/:id/finalize", h.finalize)
	api.GET("/versions/:id", h.getVersion)

	api.GET("/versions/:id/measurements", h.listMeasurements)
	api.POST("/versions/:id/measurements", h.addMeasurement)
	api.POST("/versions/:id/measurements/import", h.importMeasurements)
	api.PUT("/versions/:id/measurements/:mid", h.updateMeasurement)
	api.DELETE("/versions/:id/measurements/:mid", h.deleteMeasurement)

	api.POST("/versions/:id/compute", h.compute)
	api.GET("/versions/:id/results", h.listResults)
}

func (h *Handler) health(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

func (h *Handler) createStudy(c *gin.Context) {
	var in StudyInput
	if !bind(c, &in) {
		return
	}
	st, v, err := h.svc.CreateStudy(c.Request.Context(), in)
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{"study": st, "version": v})
}

func (h *Handler) listStudies(c *gin.Context) {
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "100"))
	offset, _ := strconv.Atoi(c.DefaultQuery("offset", "0"))
	out, err := h.svc.ListStudies(c.Request.Context(), limit, offset)
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"studies": out})
}

func (h *Handler) getStudy(c *gin.Context) {
	st, err := h.svc.GetStudy(c.Request.Context(), c.Param("id"))
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"study": st})
}

func (h *Handler) updateStudy(c *gin.Context) {
	var in StudyInput
	if !bind(c, &in) {
		return
	}
	st, err := h.svc.UpdateStudy(c.Request.Context(), c.Param("id"), in)
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"study": st})
}

func (h *Handler) listVersions(c *gin.Context) {
	out, err := h.svc.ListVersions(c.Request.Context(), c.Param("id"))
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"versions": out})
}

func (h *Handler) currentDraft(c *gin.Context) {
	v, err := h.svc.CurrentDraft(c.Request.Context(), c.Param("id"))
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"version": v})
}

func (h *Handler) newVersion(c *gin.Context) {
	var req struct {
		BasedOnID string `json:"based_on_id"`
	}
	// Body optional.
	_ = c.ShouldBindJSON(&req)
	v, err := h.svc.NewVersion(c.Request.Context(), c.Param("id"), req.BasedOnID)
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{"version": v})
}

func (h *Handler) finalize(c *gin.Context) {
	v, err := h.svc.Finalize(c.Request.Context(), c.Param("id"))
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"version": v})
}

func (h *Handler) getVersion(c *gin.Context) {
	v, err := h.svc.GetVersion(c.Request.Context(), c.Param("id"))
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"version": v})
}

func (h *Handler) listMeasurements(c *gin.Context) {
	out, err := h.svc.ListMeasurements(c.Request.Context(), c.Param("id"))
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"measurements": out})
}

func (h *Handler) addMeasurement(c *gin.Context) {
	var in MeasurementInput
	if !bind(c, &in) {
		return
	}
	m, err := h.svc.AddMeasurement(c.Request.Context(), c.Param("id"), in)
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{"measurement": m})
}

func (h *Handler) importMeasurements(c *gin.Context) {
	var req ImportRequest
	if !bind(c, &req) {
		return
	}
	n, err := h.svc.Import(c.Request.Context(), c.Param("id"), req)
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{"imported": n})
}

func (h *Handler) updateMeasurement(c *gin.Context) {
	var req struct {
		MeasurementInput
		ExpectedRowVersion int `json:"expected_row_version"`
	}
	if !bind(c, &req) {
		return
	}
	expected := req.ExpectedRowVersion
	if v := c.GetHeader("If-Match"); v != "" {
		n, err := strconv.Atoi(unquote(v))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{
				"message": "If-Match must be the integer row_version",
				"field":   "If-Match",
			}})
			return
		}
		expected = n
	}
	if expected <= 0 {
		c.JSON(http.StatusConflict, gin.H{"error": gin.H{
			"message": "optimistic concurrency requires expected row_version (field expected_row_version or If-Match header)",
			"field":   "expected_row_version",
		}})
		return
	}
	m, err := h.svc.UpdateMeasurement(c.Request.Context(), c.Param("id"), c.Param("mid"), req.MeasurementInput, expected)
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"measurement": m})
}

func (h *Handler) deleteMeasurement(c *gin.Context) {
	if err := h.svc.DeleteMeasurement(c.Request.Context(), c.Param("id"), c.Param("mid")); err != nil {
		respondError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *Handler) compute(c *gin.Context) {
	var req ComputeRequest
	_ = c.ShouldBindJSON(&req)
	out, err := h.svc.Compute(c.Request.Context(), c.Param("id"), req.Method)
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{"result": out})
}

func (h *Handler) listResults(c *gin.Context) {
	out, err := h.svc.ListResults(c.Request.Context(), c.Param("id"))
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"results": out})
}

// ---- error mapping ----

func respondError(c *gin.Context, err error) {
	var ve *msa.ValidationError
	if errors.As(err, &ve) {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": gin.H{
			"message": "validation failed", "fields": ve.Errors,
		}})
		return
	}
	var be *store.BatchError
	if errors.As(err, &be) {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": gin.H{
			"message": be.Error(),
			"fields": []msa.FieldError{{
				Field:   "readings[" + strconv.Itoa(be.Index) + "]." + be.Field,
				Message: be.Message,
			}},
		}})
		return
	}
	switch {
	case errors.Is(err, store.ErrNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": gin.H{"message": err.Error()}})
	case errors.Is(err, store.ErrFinalized):
		c.JSON(http.StatusConflict, gin.H{"error": gin.H{
			"message": "this version is finalized; writes are rejected. Open a new version to re-measure.",
			"code":    "version_finalized",
		}})
	case errors.Is(err, store.ErrConflict):
		c.JSON(http.StatusConflict, gin.H{"error": gin.H{
			"message": "the reading was modified by another client; reload and retry",
			"code":    "row_version_conflict",
		}})
	case errors.Is(err, store.ErrDuplicate):
		c.JSON(http.StatusConflict, gin.H{"error": gin.H{
			"message": "a reading with this part/operator/trial already exists",
			"code":    "duplicate_reading",
		}})
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"message": "internal error"}})
	}
}

func bind(c *gin.Context, dst any) bool {
	if err := c.ShouldBindJSON(dst); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{
			"message": "invalid JSON body: " + err.Error(),
		}})
		return false
	}
	return true
}

func unquote(s string) string {
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		return s[1 : len(s)-1]
	}
	return s
}
