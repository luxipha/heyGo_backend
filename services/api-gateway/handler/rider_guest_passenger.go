package handler

import (
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/luxipha/heyGo_backend/shared/contracts"
	"regexp"
	"strings"
	"unicode"
)

var guestPhonePattern = regexp.MustCompile(`^\+[1-9][0-9]{6,14}$`)

func normalizeGuestPassenger(name, phone string) (string, string, bool) {
	name, phone = strings.TrimSpace(name), strings.TrimSpace(phone)
	if name == "" && phone == "" {
		return name, phone, true
	}
	return name, phone, name != "" && len([]rune(name)) <= 100 && !strings.ContainsFunc(name, unicode.IsControl) && guestPhonePattern.MatchString(phone)
}
func setFareGuestPassenger(pool *pgxpool.Pool) gin.HandlerFunc {
	return func(c *gin.Context) {
		var b struct {
			Name  string `json:"name"`
			Phone string `json:"phone"`
		}
		id, e := uuid.Parse(c.Param("fareID"))
		if e != nil || c.ShouldBindJSON(&b) != nil {
			driverError(c, 400, "invalid_guest", "Guest name and international phone number are required")
			return
		}
		name, phone, ok := normalizeGuestPassenger(b.Name, b.Phone)
		if !ok {
			driverError(c, 400, "invalid_guest", "Enter a name of at most 100 characters and an international phone number, for example +2348012345678")
			return
		}
		if pool == nil {
			accountError(c)
			return
		}
		result, e := pool.Exec(c.Request.Context(), `UPDATE ride_fares SET guest_name=$3,guest_phone=$4 WHERE id=$1::uuid AND rider_id=$2::uuid AND consumed_at IS NULL AND expires_at>NOW()`, id, riderAccountOwner(c), name, phone)
		if e != nil {
			accountError(c)
			return
		}
		if result.RowsAffected() != 1 {
			driverError(c, 409, "fare_unavailable", "Refresh the quote before changing the passenger")
			return
		}
		c.Header("Cache-Control", "no-store")
		c.JSON(200, contracts.APIResponse{Data: gin.H{"fareId": id, "guestName": name, "guestPhone": phone}})
	}
}
