package api

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
)

const (
	maxAuthBodyBytes int64 = 64 * 1024
	maxJSONBodyBytes int64 = 4 * 1024 * 1024
)

func bindLimitedJSON(c *gin.Context, destination interface{}, limit int64) error {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, limit)
	return c.ShouldBindJSON(destination)
}

func bindJSON(c *gin.Context, destination interface{}) error {
	return bindLimitedJSON(c, destination, maxJSONBodyBytes)
}

func requestBodyErrorStatus(err error) int {
	var maxBytesError *http.MaxBytesError
	if errors.As(err, &maxBytesError) {
		return http.StatusRequestEntityTooLarge
	}
	return http.StatusBadRequest
}
