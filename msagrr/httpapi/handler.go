// Package httpapi exposes the Gauge R&R service over HTTP using Gin.
package httpapi

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"msagrr/domain"
	"msagrr/service"
	"msagrr/stat"
	"msagrr/store"
)

// Handler wires the service to HTTP routes.
type Handler struct {
	svc *service.Service
}

// New creates a Handler.
func New(svc *service.Service) *Handler { return &Handler{svc: svc} }

// Register mounts all routes on r.
func (h *Handler) Register(r *gin.Engine) {
	r.GET("/health", h.health)

	api := r.Group("/api/v1")
	{
		api.POST("/studies", h.createStudy)
		api.GET("/studies", h.listStudies)
		api.GET("/studies/:sid", h.getStudy)
		api.GET("/studies/:sid/versions", h.listVersions)
		api.POST("/studies/:sid/versions/:vid/return", h.startReturn)
		api.POST("/studies/:sid/versions/:vid/finalize", h.finalize)

		api.GET("/studies/:sid/versions/:vid/measurements", h.listMeasurements)
		api.POST("/studies/:sid/versions/:vid/measurements", h.addReading)
		api.POST("/studies/:sid/versions/:vid/measurements/batch", h.addBatch)

		api.GET("/measurements/:mid", h.getMeasurement)
		api.PUT("/measurements/:mid", h.updateMeasurement)
		api.DELETE("/measurements/:mid", h.deleteMeasurement)

		api.POST("/studies/:sid/versions/:vid/results", h.computeResult)
		api.GET("/studies/:sid/versions/:vid/results", h.listResults)
		api.GET("/results/:rid", h.getResult)
	}
}

func (h *Handler) health(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

func (h *Handler) createStudy(c *gin.Context) {
	var req service.CreateStudyRequest
	if !bindJSON(c, &req, map[string]bool{"tolerance": true}, nil) {
		return
	}
	st, v, err := h.svc.CreateStudy(req)
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{"study": st, "version": v})
}

func (h *Handler) listStudies(c *gin.Context) {
	sts, err := h.svc.ListStudies()
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"studies": sts})
}

func (h *Handler) getStudy(c *gin.Context) {
	st, err := h.svc.GetStudy(c.Param("sid"))
	if err != nil {
		writeError(c, err)
		return
	}
	vs, err := h.svc.ListVersions(st.ID)
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"study": st, "versions": vs})
}

func (h *Handler) listVersions(c *gin.Context) {
	vs, err := h.svc.ListVersions(c.Param("sid"))
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"versions": vs})
}

func (h *Handler) startReturn(c *gin.Context) {
	v, err := h.svc.StartReturnStudy(c.Param("sid"), c.Param("vid"))
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{"version": v, "cloned_from": c.Param("vid")})
}

func (h *Handler) finalize(c *gin.Context) {
	v, res, err := h.svc.Finalize(c.Param("sid"), c.Param("vid"))
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"version": v, "result": res})
}

func (h *Handler) listMeasurements(c *gin.Context) {
	if _, err := h.version(c); err != nil {
		writeError(c, err)
		return
	}
	ms, err := h.svc.ListMeasurements(c.Param("vid"))
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"measurements": ms})
}

func (h *Handler) addReading(c *gin.Context) {
	if _, err := h.version(c); err != nil {
		writeError(c, err)
		return
	}
	var req service.ReadingRequest
	if !bindJSON(c, &req, map[string]bool{"value": true}, nil) {
		return
	}
	m, err := h.svc.AddReading(c.Param("vid"), req)
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{"measurement": m})
}

func (h *Handler) addBatch(c *gin.Context) {
	if _, err := h.version(c); err != nil {
		writeError(c, err)
		return
	}
	var body struct {
		Readings []service.ReadingRequest `json:"readings"`
	}
	if !bindJSON(c, &body, nil, map[string]bool{"value": true}) {
		return
	}
	ms, err := h.svc.AddReadings(c.Param("vid"), body.Readings)
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{"created": len(ms), "measurements": ms})
}

func (h *Handler) getMeasurement(c *gin.Context) {
	m, err := h.svc.GetMeasurement(c.Param("mid"))
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"measurement": m})
}

func (h *Handler) updateMeasurement(c *gin.Context) {
	var req service.UpdateReadingRequest
	if !bindJSON(c, &req, map[string]bool{"value": true}, nil) {
		return
	}
	m, err := h.svc.UpdateReading(c.Param("mid"), req)
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"measurement": m})
}

func (h *Handler) deleteMeasurement(c *gin.Context) {
	m, err := h.svc.GetMeasurement(c.Param("mid"))
	if err != nil {
		writeError(c, err)
		return
	}
	rev := int64(0)
	if r := c.GetHeader("If-Match"); r != "" {
		n, err := strconv.ParseInt(r, 10, 64)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{
				"message": "If-Match must be an integer revision",
				"field":   "If-Match",
			}})
			return
		}
		rev = n
	}
	if err := h.svc.DeleteReadingFrom(m.VersionID, m.ID, rev); err != nil {
		writeError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *Handler) computeResult(c *gin.Context) {
	res, err := h.svc.Compute(c.Param("sid"), c.Param("vid"))
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{"result": res})
}

func (h *Handler) listResults(c *gin.Context) {
	rs, err := h.svc.ListResults(c.Param("vid"))
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"results": rs})
}

func (h *Handler) getResult(c *gin.Context) {
	rv, err := h.svc.GetResult(c.Param("rid"))
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"result": rv.Analysis, "stale": rv.Stale,
		"data_fingerprint": rv.Fingerprint, "version_id": rv.VersionID})
}

// version loads and verifies the version belongs to the study in path.
func (h *Handler) version(c *gin.Context) (*domain.Version, error) {
	return h.svc.GetVersion(c.Param("sid"), c.Param("vid"))
}

func writeError(c *gin.Context, err error) {
	var ve *service.ValidationError
	switch {
	case errors.As(err, &ve):
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{
			"message":      "validation failed",
			"field_errors": ve.Fields,
		}})
	case errors.Is(err, store.ErrNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": gin.H{"message": "not found"}})
	case errors.Is(err, store.ErrFinalized):
		c.JSON(http.StatusConflict, gin.H{"error": gin.H{
			"code":    "version_finalized",
			"message": "version is finalized and is read-only; start a return-study version to revise data",
		}})
	case errors.Is(err, stat.ErrNoUsableDesign), errors.Is(err, stat.ErrInsufficientLevels):
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": gin.H{
			"code":    "insufficient_design",
			"message": err.Error() + "; at least 2 parts, 2 operators and one complete repeated design are required",
		}})
	case errors.Is(err, store.ErrSlotDuplicate):
		c.JSON(http.StatusConflict, gin.H{"error": gin.H{
			"code":    "duplicate_slot",
			"message": "a reading with the same part, operator and trial number already exists",
			"fields":  []string{"part", "operator", "trial"},
		}})
	case errors.Is(err, store.ErrRevisionConflict):
		c.JSON(http.StatusConflict, gin.H{"error": gin.H{
			"code":    "revision_conflict",
			"message": "the reading was modified by someone else; refetch its revision and retry",
		}})
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"message": err.Error()}})
	}
}

func bindError(c *gin.Context, err error) {
	c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{
		"message": "invalid JSON request body",
		"detail":  err.Error(),
	}})
}
