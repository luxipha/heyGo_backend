package handler

import "github.com/gin-gonic/gin"

// Only published market identity is exposed; finance staff do not need boundary access.
func (a *adminAPI) policyMarkets(c *gin.Context) {
	rows, err := a.pool.Query(c.Request.Context(), `SELECT DISTINCT ON (code) code,name FROM trip_geofences WHERE kind='market' AND status='approved' AND effective_from<=now() AND (effective_until IS NULL OR effective_until>now()) ORDER BY code,version DESC`)
	if err != nil {
		reviewFailure(c)
		return
	}
	defer rows.Close()
	items := []gin.H{}
	for rows.Next() {
		var code, name string
		if rows.Scan(&code, &name) != nil {
			reviewFailure(c)
			return
		}
		items = append(items, gin.H{"code": code, "name": name})
	}
	if rows.Err() != nil {
		reviewFailure(c)
		return
	}
	c.Header("Cache-Control", "no-store")
	heroData(c, items)
}
