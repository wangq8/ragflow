package handler

import (
	"ragflow/internal/common"
	"ragflow/internal/compliance"

	"github.com/gin-gonic/gin"
)

func filterSearchResponse(c *gin.Context, chunks, docAggs *[]map[string]interface{}) bool {
	filter, err := compliance.NewOutputFilter(c.Request.Context())
	if err != nil {
		common.RespondError(c, common.CodeServerError, err.Error())
		return false
	}
	*chunks = filter.FilterFields(*chunks).([]map[string]interface{})
	*docAggs = filter.FilterFields(*docAggs).([]map[string]interface{})
	return true
}
