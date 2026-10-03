package httpapi

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// bindJSON first scans the body for non-finite numeric tokens so that
// values like NaN, Infinity or 1e999 can be rejected with a precise
// field path (standard encoding/json refuses them generically); it then
// unmarshals into dst as usual.
//
// scalarFields maps top-level numeric field names that represent a
// measurement/tolerance value (e.g. "value", "tolerance") and must be
// finite; batchFields maps array-level numeric fields (e.g.
// "readings[].value").
func bindJSON(c *gin.Context, dst any, scalarFields, batchFields map[string]bool) bool {
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{
			"message": "cannot read request body",
		}})
		return false
	}
	bad, scanErr := scanLooseJSON(body)
	if scanErr != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{
			"message": "invalid JSON request body",
			"detail":  scanErr.Error(),
		}})
		return false
	}
	var fieldErrs []gin.H
	for _, b := range bad {
		field := b.Path
		scalar := scalarFields != nil && scalarFields[b.Path]
		batch := false
		// batch path shape: readings[3].value
		if idx := strings.LastIndex(b.Path, "."); idx >= 0 {
			tail := b.Path[idx+1:]
			if batchFields != nil && batchFields[tail] && strings.Contains(b.Path, "[") {
				batch = true
			}
		}
		if scalar || batch {
			fieldErrs = append(fieldErrs, gin.H{
				"field":  field,
				"reason": "must be a finite number (not NaN/Infinity/overflow)",
				"raw":    b.Raw,
			})
		} else {
			fieldErrs = append(fieldErrs, gin.H{
				"field":  field,
				"reason": "invalid numeric value: " + b.Raw,
			})
		}
	}
	if len(fieldErrs) > 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{
			"message":      "validation failed",
			"field_errors": fieldErrs,
		}})
		return false
	}
	if err := json.Unmarshal(body, dst); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{
			"message": "invalid JSON request body",
			"detail":  err.Error(),
		}})
		return false
	}
	return true
}
